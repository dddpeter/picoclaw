//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

// Regression tests for B2 (docs/design/2026-10-04-feishu-streaming-card-
// bug-review.zh.md §3): streams-map turn identity. Stopgap fixes — stale
// streamer sealing on TTL-refused reuse (B2b) and CAS-scoped map delete
// (B2c) — plus the session-scoped begin (B2-2).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

// newFakeFeishuServer builds a channel whose lark client talks to an
// httptest fake of the five endpoints the streaming path touches, logging
// every cardkit/im request. Used by tests that need the real BeginStream.
func newFakeFeishuServer(t *testing.T, log *reqLog) *FeishuChannel {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t-token"})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards", func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Method + " " + r.URL.Path)
		ok(w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"card_id": "card-new"}})
	})
	mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Method + " " + r.URL.Path)
		ok(w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": "om_new"}})
	})
	mux.HandleFunc("/open-apis/cardkit/v1/cards/", func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Method + " " + r.URL.Path)
		ok(w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.record("UNEXPECTED " + r.Method + " " + r.URL.Path)
		ok(w, map[string]any{"code": 0, "msg": "success"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &FeishuChannel{
		BaseChannel: channels.NewBaseChannel("feishu", nil, bus.NewMessageBus(), []string{"*"}),
		tokenCache:  newTokenCache(),
		client:      lark.NewClient("app", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithTokenCache(newTokenCache())),
	}
}

type reqLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *reqLog) record(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *reqLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// TestBeginStreamSealsStaleStreamer: when the reuse TTL refuses an unfinished
// card, BeginStream must seal that card best-effort instead of orphaning it
// in streaming mode forever (B2b).
func TestBeginStreamSealsStaleStreamer(t *testing.T) {
	log := &reqLog{}
	ch := newFakeFeishuServer(t, log)

	sealed := make(chan struct{}, 4)
	closed := make(chan struct{}, 4)
	var sealMu sync.Mutex
	var sealedCard map[string]any
	old := newFeishuCardStreamer(ch, "chat-stale", "card-old", "")
	old.streamContent = func(context.Context, string, string, string, int) error { return nil }
	old.updateCard = func(_ context.Context, _ string, card map[string]any, _ int) error {
		sealMu.Lock()
		sealedCard = card
		sealMu.Unlock()
		sealed <- struct{}{}
		return nil
	}
	old.reopenStreaming = func(context.Context, string, int) error { return nil }
	old.closeStreaming = func(context.Context, string, map[string]any, int) error {
		closed <- struct{}{}
		return nil
	}
	old.msgID = "om_old"
	old.state.LLMCalls = 1
	_ = old.Update(context.Background(), "第一轮回答")
	ch.streams.Store("chat-stale", old)
	old.mu.Lock()
	old.lastAt = time.Now().Add(-3 * time.Minute) // TTL expired
	old.mu.Unlock()

	newS, err := ch.BeginStream(context.Background(), "chat-stale")
	if err != nil {
		t.Fatalf("BeginStream error: %v", err)
	}
	fs, ok := newS.(*feishuCardStreamer)
	if !ok || fs.cardID != "card-new" {
		t.Fatalf("expected a new card, got %#v", newS)
	}

	// The async seal must land: card update + streaming-mode close.
	select {
	case <-sealed:
	case <-time.After(3 * time.Second):
		t.Fatal("stale streamer was not sealed by BeginStream (ghost card)")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stale streamer's streaming mode was never closed")
	}
	old.mu.Lock()
	done := old.done
	old.mu.Unlock()
	if !done {
		t.Error("stale streamer not marked done after seal")
	}
	// The sealed card must explain why it stopped.
	sealMu.Lock()
	cardJSON := string(mustJSON(sealedCard))
	sealMu.Unlock()
	if !strings.Contains(cardJSON, "已被新任务取代") {
		t.Errorf("sealed stale card must carry the superseded reason, got %.200s", cardJSON)
	}
}

// TestFinalizeDeleteIsCASScoped: a streamer whose map slot was taken over by
// a newer streamer must not delete the newer entry on finalize (B2c).
func TestFinalizeDeleteIsCASScoped(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	fakeA := &degradeAPIFake{}
	a := newFeishuCardStreamer(ch, "chat-cas", "card-a", "")
	a.streamContent = fakeA.streamContent
	a.updateCard = fakeA.updateCard
	a.closeStreaming = fakeA.closeStreaming
	ch.streams.Store("chat-cas", a)

	// A newer streamer takes over the map slot.
	b := newFeishuCardStreamer(ch, "chat-cas", "card-b", "")
	ch.streams.Store("chat-cas", b)

	if err := a.Finalize(context.Background(), "done"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if v, ok := ch.streams.Load("chat-cas"); !ok || v.(*feishuCardStreamer) != b {
		t.Fatal("a's finalize must not remove b's map entry")
	}
}

// TestCancelDeleteIsCASScoped mirrors the finalize test for the cancel path.
func TestCancelDeleteIsCASScoped(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	fakeA := &degradeAPIFake{}
	a := newFeishuCardStreamer(ch, "chat-cas2", "card-a", "")
	a.streamContent = fakeA.streamContent
	a.updateCard = fakeA.updateCard
	a.closeStreaming = fakeA.closeStreaming
	ch.streams.Store("chat-cas2", a)

	b := newFeishuCardStreamer(ch, "chat-cas2", "card-b", "")
	ch.streams.Store("chat-cas2", b)

	a.CancelWithReason(context.Background(), "stop_command")
	if v, ok := ch.streams.Load("chat-cas2"); !ok || v.(*feishuCardStreamer) != b {
		t.Fatal("a's cancel must not remove b's map entry")
	}
}

// TestConcurrentTurnsGetDistinctCards: B2-2 reuse matrix — a live card from
// another session is neither reused nor sealed; the same session keeps its
// card; a stale card gets sealed superseded regardless of session.
func TestConcurrentTurnsGetDistinctCards(t *testing.T) {
	log := &reqLog{}
	ch := newFakeFeishuServer(t, log)

	sealed := make(chan string, 4) // cardIDs of sealed cards
	a := newFeishuCardStreamer(ch, "chat-matrix", "card-a", "")
	a.sessionKey = "s1"
	a.streamContent = func(context.Context, string, string, string, int) error { return nil }
	a.updateCard = func(_ context.Context, cardID string, _ map[string]any, _ int) error {
		sealed <- cardID
		return nil
	}
	a.closeStreaming = func(context.Context, string, map[string]any, int) error { return nil }
	a.state.LLMCalls = 1
	ch.streams.Store("chat-matrix", a)

	// Fresh card + other session → distinct card, old one untouched.
	b, err := ch.BeginStreamForSession(context.Background(), "chat-matrix", "s2")
	if err != nil {
		t.Fatalf("BeginStreamForSession(s2): %v", err)
	}
	fb, ok := b.(*feishuCardStreamer)
	if !ok || fb.cardID != "card-new" || fb == a {
		t.Fatalf("fresh foreign card must yield a new streamer, got %#v", b)
	}
	// Stub the new card's seams so the later staleness-seal is observable,
	// and give it streamed content so it seals instead of being deleted
	// as an empty card.
	fb.updateCard = func(_ context.Context, cardID string, _ map[string]any, _ int) error {
		sealed <- cardID
		return nil
	}
	fb.closeStreaming = func(context.Context, string, map[string]any, int) error { return nil }
	_ = fb.Update(context.Background(), "第二轮部分回答")
	select {
	case id := <-sealed:
		t.Fatalf("live card of another session must not be sealed, sealed %q", id)
	default:
	}
	a.mu.Lock()
	aDone := a.done
	a.mu.Unlock()
	if aDone {
		t.Fatal("live foreign card must stay unsealed")
	}

	// Same session → reuse (the new s2 card is fresh and owned by s2).
	again, err := ch.BeginStreamForSession(context.Background(), "chat-matrix", "s2")
	if err != nil {
		t.Fatalf("BeginStreamForSession(s2) reuse: %v", err)
	}
	if fs, ok := again.(*feishuCardStreamer); !ok || fs != fb {
		t.Fatal("same-session BeginStream must reuse the live card")
	}

	// Stale card (any session) → sealed superseded, new card returned.
	fb.mu.Lock()
	fb.lastAt = time.Now().Add(-3 * time.Minute)
	fb.mu.Unlock()
	c, err := ch.BeginStreamForSession(context.Background(), "chat-matrix", "s1")
	if err != nil {
		t.Fatalf("BeginStreamForSession(s1) after staleness: %v", err)
	}
	if fc, ok := c.(*feishuCardStreamer); !ok || fc == fb {
		t.Fatal("stale card must not be reused")
	}
	select {
	case id := <-sealed:
		if id != "card-new" {
			t.Fatalf("stale card card-new must be sealed superseded, got %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale card was never sealed")
	}
}

// TestProgressBeatDoesNotPinNarration: ToolStepKindProgress renders as one
// replaceable grey status line — never pinned into the narration trail nor
// archived in the tool timeline (B3).
func TestProgressBeatDoesNotPinNarration(t *testing.T) {
	fake := &degradeAPIFake{}
	s := newDegradeTestStreamer(t, fake)

	_ = s.AppendToolStep(context.Background(), progressStep("⏱ 进度：第 2 轮迭代"))
	_ = s.AppendToolStep(context.Background(), progressStep("⏱ 进度：第 3 轮迭代"))

	s.mu.Lock()
	narration := append([]string(nil), s.state.Narration...)
	tools := len(s.state.Tools)
	note := s.state.ProgressNote
	s.mu.Unlock()
	if len(narration) != 0 {
		t.Errorf("progress beats must not pin narration, got %q", narration)
	}
	if tools != 0 {
		t.Errorf("progress beats must not archive timeline steps, got %d", tools)
	}
	if note != "⏱ 进度：第 3 轮迭代" {
		t.Errorf("progress note must hold the latest beat only, got %q", note)
	}

	// The panel renders the note as a grey notation line.
	panel := buildFeishuPanelBudget(&s.state, true, feishuPanelTextBudget)
	if panelJSON := string(mustJSON(panel)); !strings.Contains(panelJSON, "⏱ 进度：第 3 轮迭代") {
		t.Errorf("panel must render the progress note, got %.300s", panelJSON)
	}
	// Beats keep the card alive (keep-alive): the panel was flushed.
	if fake.updateCardCalls < 1 {
		t.Error("progress beat must flush the panel (200850 keep-alive)")
	}
}

func progressStep(text string) bus.ToolStep {
	return bus.ToolStep{Kind: bus.ToolStepKindProgress, Result: text}
}
