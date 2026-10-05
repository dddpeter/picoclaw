package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// ─── Idle compaction scanner tests (design: idle-compaction-design.zh.md) ───

// idleTestStore wraps a real JSONL backend in a temp dir so LastModified,
// GetHistory and ListSessions all behave like production.
type idleTestStore struct {
	*session.JSONLBackend
	dir string
}

func newIdleTestStore(t *testing.T) *idleTestStore {
	t.Helper()
	dir := t.TempDir()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &idleTestStore{JSONLBackend: session.NewJSONLBackend(store), dir: dir}
}

// backdateLastModified rewrites the session jsonl mtime into the past so the
// idle gate passes without real waiting.
func (s *idleTestStore) backdateLastModified(t *testing.T, key string, age time.Duration) {
	t.Helper()
	past := time.Now().Add(-age)
	meta := filepath.Join(s.dir, sanitizedIdleKey(key)+".meta.json")
	jsonl := filepath.Join(s.dir, sanitizedIdleKey(key)+".jsonl")
	for _, p := range []string{meta, jsonl} {
		if _, err := os.Stat(p); err == nil {
			if err := os.Chtimes(p, past, past); err != nil {
				t.Fatalf("Chtimes(%s): %v", p, err)
			}
		}
	}
}

// sanitizedIdleKey mirrors memory's on-disk key sanitization.
func sanitizedIdleKey(key string) string {
	out := make([]rune, 0, len(key))
	for _, r := range key {
		switch r {
		case ':', '/', '\\', '*', '?', '"', '<', '>', '|':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// countingContextManager counts Compact calls per session.
type countingContextManager struct {
	mu     sync.Mutex
	calls  []CompactRequest
	fail   bool
	fireCh chan struct{} // closed-signaled per call when non-nil
}

func (c *countingContextManager) Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	return &AssembleResponse{}, nil
}

func (c *countingContextManager) Compact(ctx context.Context, req *CompactRequest) error {
	c.mu.Lock()
	c.calls = append(c.calls, *req)
	fail := c.fail
	c.mu.Unlock()
	if c.fireCh != nil {
		c.fireCh <- struct{}{}
	}
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *countingContextManager) Ingest(ctx context.Context, req *IngestRequest) error { return nil }
func (c *countingContextManager) Clear(ctx context.Context, key string) error          { return nil }
func (c *countingContextManager) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// newIdleTestLoop builds an agent loop whose default agent uses the given
// store and context manager, sized so the usage gate passes.
func newIdleTestLoop(t *testing.T, store *idleTestStore, cm ContextManager, msgs int) (*AgentLoop, *AgentInstance) {
	t.Helper()
	prv := &failFirstNLLMProvider{response: "ok"}
	al, agent, cleanup := newTurnCoordTestLoop(t, prv)
	t.Cleanup(cleanup)
	agent.Sessions = store
	al.contextManager = cm
	// Small window + enough big messages ⇒ usage gate passes.
	agent.ContextWindow = 500
	agent.MaxTokens = 64
	for i := 0; i < msgs; i++ {
		store.AddFullMessage("s1", providers.Message{Role: "user", Content: "消息内容" + string(rune('a'+i%26)) + "PaddingPaddingPaddingPadding"})
	}
	if err := store.Save("s1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return al, agent
}

func newIdleScanner(al *AgentLoop, cfg *config.IdleCompactConfig) *idleCompactScanner {
	return &idleCompactScanner{
		al:       al,
		cfg:      cfg,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		records:  make(map[string]*idleSessionRecord),
		resultCh: make(chan idleCompactResult, 64),
	}
}

func TestIdleCompact_GateNotIdle(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	sc := newIdleScanner(al, nil)

	// Fresh mtime (just written) — not idle yet.
	sc.scanOnce()
	if cm.callCount() != 0 {
		t.Fatalf("fresh session must not compact, calls=%d", cm.callCount())
	}
}

func TestIdleCompact_GateTooFewMessages(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, _ := newIdleTestLoop(t, store, cm, 5) // below default min 12
	sc := newIdleScanner(al, nil)
	store.backdateLastModified(t, "s1", time.Hour)

	sc.scanOnce()
	if cm.callCount() != 0 {
		t.Fatalf("small session must not compact, calls=%d", cm.callCount())
	}
}

func TestIdleCompact_FiresWhenEligible(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	sc := newIdleScanner(al, nil)
	store.backdateLastModified(t, "s1", time.Hour)

	sc.scanOnce()

	select {
	case <-cm.fireCh:
	case <-time.After(3 * time.Second):
		t.Fatal("eligible session was not compacted")
	}
	// Drain result and verify bookkeeping recorded success.
	sc.drainResults()
	rec := sc.records["s1"]
	if rec == nil || rec.successAt.IsZero() {
		t.Fatalf("success bookkeeping missing: %+v", rec)
	}
	req := func() CompactRequest {
		cm.mu.Lock()
		defer cm.mu.Unlock()
		return cm.calls[0]
	}()
	if req.Reason != ContextCompressReasonSummarize {
		t.Fatalf("reason = %q, want summarize", req.Reason)
	}
	if req.SessionKey != "s1" {
		t.Fatalf("session = %q", req.SessionKey)
	}
}

// TestIdleCompact_SuccessBookkeepingBlocksRecompaction pins the review fix:
// after a successful idle compaction with no new activity, subsequent ticks
// skip — even when the usage gate still passes (the seahorse shape, where
// compaction never lowers the JSONL-derived waterline).
func TestIdleCompact_SuccessBookkeepingBlocksRecompaction(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	sc := newIdleScanner(al, nil)
	store.backdateLastModified(t, "s1", time.Hour)

	sc.scanOnce()
	<-cm.fireCh
	sc.drainResults()

	// Second and third ticks: still over threshold (nothing changed), still
	// idle — but the success record must block recompaction.
	sc.scanOnce()
	sc.scanOnce()
	if got := cm.callCount(); got != 1 {
		t.Fatalf("recompacted despite no activity: calls=%d, want 1", got)
	}
}

// TestIdleCompact_NewActivityRestoresEligibility pins the reset rule: the
// record (including the success block) clears once UpdatedAt moves forward.
func TestIdleCompact_NewActivityRestoresEligibility(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	sc := newIdleScanner(al, nil)
	store.backdateLastModified(t, "s1", time.Hour)

	sc.scanOnce()
	<-cm.fireCh
	sc.drainResults()

	// New activity: append + backdate again (idle once more).
	store.AddFullMessage("s1", providers.Message{Role: "user", Content: "新消息PaddingPaddingPaddingPadding"})
	_ = store.Save("s1")
	store.backdateLastModified(t, "s1", time.Hour)

	sc.scanOnce()
	select {
	case <-cm.fireCh:
	case <-time.After(3 * time.Second):
		t.Fatal("session with new activity must be eligible again")
	}
}

// TestIdleCompact_FailureBackoff pins the retry policy: three consecutive
// failures put the session into long backoff until activity resets it.
func TestIdleCompact_FailureBackoff(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fail: true, fireCh: make(chan struct{}, 16)}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	sc := newIdleScanner(al, nil)
	store.backdateLastModified(t, "s1", time.Hour)

	// Three failing scans — each records a failure.
	for i := 0; i < 3; i++ {
		sc.scanOnce()
		<-cm.fireCh
		sc.drainResults()
	}
	if got := cm.callCount(); got != 3 {
		t.Fatalf("setup calls=%d, want 3", got)
	}

	// Fourth tick: backoff, no call.
	sc.scanOnce()
	sc.drainResults()
	if got := cm.callCount(); got != 3 {
		t.Fatalf("backoff not honored: calls=%d", got)
	}
}

// TestIdleCompact_DisabledByConfig pins the config gate.
func TestIdleCompact_DisabledByConfig(t *testing.T) {
	off := false
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	if err := al.StartIdleCompactScanner(&config.IdleCompactConfig{Enabled: &off}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, ok := al.idleScanner.Load().(*idleCompactScanner); ok {
		t.Fatal("disabled scanner must not register")
	}
}

// TestIdleCompactConfig_Clamps pins defaults and floors.
func TestIdleCompactConfig_Clamps(t *testing.T) {
	var nilCfg *config.IdleCompactConfig
	if !nilCfg.EffectiveEnabled() ||
		nilCfg.EffectiveIdleAfter() != 30*time.Minute ||
		nilCfg.EffectiveScanInterval() != 300*time.Second ||
		nilCfg.EffectiveMinHistoryMessages() != 12 {
		t.Fatal("nil config must default to on/30m/300s/12")
	}
	off := false
	tiny := &config.IdleCompactConfig{
		Enabled:             &off,
		IdleAfterMinutes:    1,
		ScanIntervalSeconds: 5,
		MinHistoryMessages:  0,
	}
	if tiny.EffectiveEnabled() {
		t.Fatal("explicit off must be off")
	}
	if tiny.EffectiveIdleAfter() != 5*time.Minute {
		t.Fatalf("idle floor = %v, want 5m", tiny.EffectiveIdleAfter())
	}
	if tiny.EffectiveScanInterval() != 60*time.Second {
		t.Fatalf("interval floor = %v, want 60s", tiny.EffectiveScanInterval())
	}
	if tiny.EffectiveMinHistoryMessages() != 12 {
		t.Fatalf("min messages = %d, want 12", tiny.EffectiveMinHistoryMessages())
	}
}

// TestIdleCompact_StartStopRoundtrip covers the public surface: start, tick
// stop, wait for done.
func TestIdleCompact_StartStopRoundtrip(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, _ := newIdleTestLoop(t, store, cm, 20)
	if err := al.StartIdleCompactScanner(&config.IdleCompactConfig{ScanIntervalSeconds: 60}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s, ok := al.idleScanner.Load().(*idleCompactScanner); !ok || s == nil {
		t.Fatal("scanner not registered")
	}
	al.StopIdleCompactScanner()
	if s, ok := al.idleScanner.Load().(*idleCompactScanner); ok && s != nil {
		t.Fatal("scanner still registered after stop")
	}
}
