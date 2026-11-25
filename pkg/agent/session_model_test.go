package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func newSessionModelTestLoop(t *testing.T) *AgentLoop {
	t.Helper()
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:      t.TempDir(),
				Provider:       "openai",
				ModelName:      "primary",
				MaxTokens:      4096,
				ContextWindow:  100_000,
				ModelFallbacks: []string{"fallback"},
			},
		},
		ModelList: []*config.ModelConfig{
			{ModelName: "primary", Model: "openai/primary-model", APIKeys: config.SimpleSecureStrings("k1")},
			{
				ModelName:     "small",
				Model:         "openai/small-model",
				APIKeys:       config.SimpleSecureStrings("k2"),
				ContextWindow: 32_000,
			},
			{ModelName: "fallback", Model: "openai/fallback-model", APIKeys: config.SimpleSecureStrings("k3")},
		},
	}
	return NewAgentLoop(cfg, bus.NewMessageBus(), nil)
}

// A session-scoped model must NOT touch the config file, and must NOT
// mutate the agent's global model — the agent keeps running the configured
// model while the session runs the chosen one.
func TestSetSessionModel_DoesNotMutateAgentModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	originalModel := agent.Model
	originalWindow := agent.ContextWindow
	originalDeclared := agent.DeclaredContextWindow

	sessionKey := "sk_v1_test"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	if agent.Model != originalModel {
		t.Fatalf("agent model mutated: %q -> %q", originalModel, agent.Model)
	}
	if agent.ContextWindow != originalWindow {
		t.Fatalf("agent context window mutated: %d -> %d", originalWindow, agent.ContextWindow)
	}
	if agent.DeclaredContextWindow != originalDeclared {
		t.Fatalf("agent declared window mutated: %d -> %d", originalDeclared, agent.DeclaredContextWindow)
	}
	if got := al.GetSessionModel(sessionKey); got != "small" {
		t.Fatalf("GetSessionModel = %q, want %q", got, "small")
	}
}

// An unknown model must be rejected — a session-scoped model is a choice
// among CONFIGURED models, never an ad-hoc provider.
func TestSetSessionModel_RejectsUnknownModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	if err := al.SetSessionModel("sk_v1_test", "not-configured"); err == nil {
		t.Fatal("expected an error for an unconfigured model")
	}
	if got := al.GetSessionModel("sk_v1_test"); got != "" {
		t.Fatalf("rejected model was stored: %q", got)
	}
}

// The resolved session model carries the model's own (min-capped) context
// window, and the agent's window stays untouched.
func TestResolveSessionModel_WindowFollowsModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()

	sessionKey := "sk_v1_window"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	resolved := al.resolveSessionModel(agent, sessionKey)
	if resolved == nil {
		t.Fatal("session model did not resolve")
	}
	if resolved.ModelName != "small" {
		t.Fatalf("resolved model = %q, want %q", resolved.ModelName, "small")
	}
	// The per-model window (32k) min-caps the agent's window (100k).
	if resolved.ContextWindow != 32_000 {
		t.Fatalf("resolved window = %d, want 32000", resolved.ContextWindow)
	}
	if resolved.DeclaredContextWindow != 32_000 {
		t.Fatalf("resolved declared window = %d, want 32000", resolved.DeclaredContextWindow)
	}
	// The agent's own window is unchanged.
	if agent.ContextWindow != 100_000 {
		t.Fatalf("agent window mutated: %d", agent.ContextWindow)
	}
}

// A session WITHOUT an override resolves to nothing — the turn then uses
// the agent's configured model, exactly as before.
func TestResolveSessionModel_NoOverride(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	if resolved := al.resolveSessionModel(agent, "sk_v1_none"); resolved != nil {
		t.Fatalf("expected no session model, got %+v", resolved)
	}
}

// Clearing the override returns the session to the configured model.
func TestSetSessionModel_Clear(t *testing.T) {
	al := newSessionModelTestLoop(t)
	sessionKey := "sk_v1_clear"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if err := al.SetSessionModel(sessionKey, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "" {
		t.Fatalf("override not cleared: %q", got)
	}
}

// The context usage of a session-scoped model reports the model's window
// as the total — the UI's context ring must match the model in use.
func TestSessionContextUsage_UsesSessionWindow(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	sessionKey := "sk_v1_usage"

	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	// Refresh the cached windows the way a turn would.
	if resolved := al.resolveSessionModel(agent, sessionKey); resolved == nil {
		t.Fatal("session model did not resolve")
	}

	usage := al.SessionContextUsage(agent, sessionKey)
	if usage == nil {
		t.Fatal("no usage computed")
	}
	if usage.TotalTokens != 32_000 {
		t.Fatalf("usage total = %d, want 32000", usage.TotalTokens)
	}
}

// The session model survives independently of the agent's model: setting it
// on one session never affects another session.
func TestSetSessionModel_IsolatedPerSession(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()

	if err := al.SetSessionModel("sk_v1_a", "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if resolved := al.resolveSessionModel(agent, "sk_v1_b"); resolved != nil {
		t.Fatalf("session B inherited session A's model: %+v", resolved)
	}
}

// =============================================================================
// Yellow-fix batch (fork §28 review): TOCTOU / busy / eviction / SetupTurn
// =============================================================================

// countedStatefulProvider is a StatefulProvider whose Close calls are
// counted, so tests can assert no provider leaks or is closed while a turn
// still holds it.
type countedStatefulProvider struct {
	closed atomic.Int32
}

func (p *countedStatefulProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok"}, nil
}

func (p *countedStatefulProvider) GetDefaultModel() string {
	return "counted-test-model"
}

func (p *countedStatefulProvider) Close() {
	p.closed.Add(1)
}

// TestSessionModelRegistry_ProviderForConcurrentCreate pins the atomic
// get-or-create: N concurrent resolvers must observe exactly ONE factory
// call and the SAME provider instance — the old
// Provider→create→SetProvider dance let the losers leak theirs.
func TestSessionModelRegistry_ProviderForConcurrentCreate(t *testing.T) {
	r := newSessionModelRegistry()
	r.Set("s", &SessionModel{ModelName: "m"})

	var creations atomic.Int32
	seen := make([]providers.LLMProvider, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			p, err := r.ProviderFor("s", "m", func() (providers.LLMProvider, error) {
				creations.Add(1)
				return &countedStatefulProvider{}, nil
			})
			if err != nil {
				t.Errorf("ProviderFor: %v", err)
				return
			}
			seen[slot] = p
		}(i)
	}
	wg.Wait()

	if got := creations.Load(); got != 1 {
		t.Fatalf("factory ran %d times, want exactly 1", got)
	}
	for slot, p := range seen {
		if p == nil || p != seen[0] {
			t.Fatalf("resolver %d observed a different provider instance", slot)
		}
	}
}

// A provider must never be stored for a model the session no longer runs:
// when the override moved, ProviderFor reports the sentinel and the create
// closure is not even invoked.
func TestSessionModelRegistry_ProviderForOverrideChanged(t *testing.T) {
	r := newSessionModelRegistry()
	r.Set("s", &SessionModel{ModelName: "m1"})

	creations := 0
	_, err := r.ProviderFor("s", "m2", func() (providers.LLMProvider, error) {
		creations++
		return &countedStatefulProvider{}, nil
	})
	if !errors.Is(err, errSessionModelOverrideChanged) {
		t.Fatalf("err = %v, want errSessionModelOverrideChanged", err)
	}
	if creations != 0 {
		t.Fatalf("factory ran %d times for a stale model, want 0", creations)
	}

	// The correct model still resolves.
	if _, err := r.ProviderFor("s", "m1", func() (providers.LLMProvider, error) {
		return &countedStatefulProvider{}, nil
	}); err != nil {
		t.Fatalf("ProviderFor with matching model: %v", err)
	}
}

// The registry is LRU-bounded: the least recently used entry is evicted
// (with its provider closed) and the recently used one survives.
func TestSessionModelRegistry_LRUEvictionClosesProvider(t *testing.T) {
	r := newSessionModelRegistry()
	r.maxEntries = 2

	testProviders := map[string]*countedStatefulProvider{
		"a": {},
		"b": {},
		"c": {},
	}
	attach := func(key, model string) {
		t.Helper()
		r.Set(key, &SessionModel{ModelName: model})
		p := testProviders[key]
		if _, err := r.ProviderFor(key, model, func() (providers.LLMProvider, error) {
			return p, nil
		}); err != nil {
			t.Fatalf("ProviderFor(%s): %v", key, err)
		}
	}

	attach("a", "m-a")
	attach("b", "m-b")
	_ = r.Get("a") // refresh a: b is now the LRU
	attach("c", "m-c")

	if got := r.Get("b"); got != nil {
		t.Fatalf("LRU entry b survived eviction: %+v", got)
	}
	if got := r.Get("a"); got == nil {
		t.Fatal("recently used entry a was evicted")
	}
	if got := r.Get("c"); got == nil {
		t.Fatal("newly inserted entry c was evicted")
	}
	if got := testProviders["b"].closed.Load(); got != 1 {
		t.Fatalf("evicted provider closed %d times, want 1", got)
	}
	if got := testProviders["a"].closed.Load(); got != 0 {
		t.Fatalf("retained provider closed %d times, want 0", got)
	}
}

// Switching models while a resolver races must not leak providers: every
// provider the factory ever built is either still cached or has been
// closed — none are dropped on the floor.
//
// Note: interleaving coverage here is scheduler-dependent; -race (on the
// Linux machine, where TSan works) plus the atomic ProviderFor make the
// invariant hold deterministically.
func TestResolveSessionModel_NoProviderLeakUnderSwitchStorm(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()

	var mu sync.Mutex
	var created []*countedStatefulProvider
	al.providerFactory = func(*config.ModelConfig) (providers.LLMProvider, string, error) {
		p := &countedStatefulProvider{}
		mu.Lock()
		created = append(created, p)
		mu.Unlock()
		return p, "", nil
	}

	sessionKey := "sk_v1_storm"
	models := []string{"small", "fallback"}
	stop := make(chan struct{})

	var switcher sync.WaitGroup
	switcher.Add(1)
	go func() {
		defer switcher.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = al.SetSessionModel(sessionKey, models[i%len(models)])
			if i%5 == 4 {
				_ = al.SetSessionModel(sessionKey, "") // clear
			}
		}
	}()

	var resolvers sync.WaitGroup
	for g := 0; g < 2; g++ {
		resolvers.Add(1)
		go func() {
			defer resolvers.Done()
			for i := 0; i < 300; i++ {
				al.resolveSessionModel(agent, sessionKey)
			}
		}()
	}
	resolvers.Wait()
	close(stop)
	switcher.Wait()

	mu.Lock()
	defer mu.Unlock()
	cached := 0
	al.sessionModels.mu.Lock()
	for _, entry := range al.sessionModels.entries {
		if entry != nil && entry.provider != nil {
			cached++
		}
	}
	al.sessionModels.mu.Unlock()

	closed := int32(0)
	for _, p := range created {
		closed += p.closed.Load()
	}
	if got, want := len(created), cached+int(closed); got != want {
		t.Fatalf("provider leak: created %d, cached %d + closed %d = %d", got, cached, closed, want)
	}
}

// Session-model writes are rejected while the session has an active turn —
// the same busy contract /switch and /new already have. Clearing is a write
// too: closing the provider under a live turn is exactly the hazard.
func TestSetSessionModel_BusyDuringActiveTurn(t *testing.T) {
	al := newSessionModelTestLoop(t)
	sessionKey := "sk_v1_busy"

	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("initial SetSessionModel: %v", err)
	}

	// Register a turn the way runAgentLoop does (the placeholder is a real
	// *turnState — getActiveTurnState ignores other types).
	al.sessionModelGate.Lock()
	al.activeTurnStates.Store(sessionKey, &turnState{sessionKey: sessionKey})
	al.sessionModelGate.Unlock()

	if err := al.SetSessionModel(sessionKey, "fallback"); !errors.Is(err, errSessionBusy) {
		t.Fatalf("Set during turn: err = %v, want errSessionBusy", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "small" {
		t.Fatalf("override changed during busy: %q", got)
	}
	if err := al.SetSessionModel(sessionKey, ""); !errors.Is(err, errSessionBusy) {
		t.Fatalf("clear during turn: err = %v, want errSessionBusy", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "small" {
		t.Fatalf("override cleared during busy: %q", got)
	}

	// Turn done: writes go through again.
	al.sessionModelGate.Lock()
	al.activeTurnStates.Delete(sessionKey)
	al.sessionModelGate.Unlock()
	if err := al.SetSessionModel(sessionKey, "fallback"); err != nil {
		t.Fatalf("Set after turn: %v", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "fallback" {
		t.Fatalf("GetSessionModel = %q, want fallback", got)
	}
}

// /new keeps the session key across rotation, so the override must be
// dropped with the old conversation — the fresh one runs on the configured
// default (/new's contract). The claim held by the pump for the /new
// message itself must NOT cause the clear to be skipped (review P1-1: the
// first version of this fix checked turn activity and was a no-op on the
// real path, because /new always runs under its own placeholder claim).
func TestRotateSession_ClearsSessionModelOverride(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	sessionKey := "sk_v1_rotate"

	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	// Mirror the pump: the /new message runs under its own placeholder
	// claim (agent.go LoadOrStore) while handleCommand → rotateSession
	// executes.
	al.sessionModelGate.Lock()
	al.activeTurnStates.Store(sessionKey, &turnState{
		sessionKey: sessionKey,
		turnID:     makePendingTurnID(sessionKey, 1),
	})
	al.sessionModelGate.Unlock()

	opts := &processOptions{
		SessionKey: sessionKey,
		Dispatch:   DispatchRequest{SessionKey: sessionKey},
	}
	if _, err := al.rotateSession(context.Background(), agent, opts); err != nil {
		t.Fatalf("rotateSession: %v", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "" {
		t.Fatalf("override survived /new rotation: %q", got)
	}
}

// LRU eviction must never close the provider of a session whose turn is
// still running (review P2-1): a long turn only touches its entry at
// resolve time, so other sessions' writes can push it to the eviction
// order. The registry tolerates staying over capacity while every
// candidate is in use.
func TestSessionModelRegistry_EvictionSkipsInUse(t *testing.T) {
	r := newSessionModelRegistry()
	r.maxEntries = 1
	inUseSet := map[string]bool{"a": true}
	r.inUse = func(key string) bool { return inUseSet[key] }

	pa := &countedStatefulProvider{}
	r.Set("a", &SessionModel{ModelName: "m-a"})
	if _, err := r.ProviderFor("a", "m-a", func() (providers.LLMProvider, error) {
		return pa, nil
	}); err != nil {
		t.Fatalf("ProviderFor(a): %v", err)
	}

	// Insert b while a is in use: over capacity, but a must survive.
	r.Set("b", &SessionModel{ModelName: "m-b"})
	if got := r.Get("a"); got == nil {
		t.Fatal("in-use entry a was evicted")
	}
	if got := pa.closed.Load(); got != 0 {
		t.Fatalf("in-use provider closed %d times, want 0", got)
	}

	// a idle again: the next write evicts it and closes its provider.
	delete(inUseSet, "a")
	r.Set("c", &SessionModel{ModelName: "m-c"})
	if got := r.Get("a"); got != nil {
		t.Fatal("idle entry a survived eviction")
	}
	if got := pa.closed.Load(); got != 1 {
		t.Fatalf("evicted provider closed %d times, want 1", got)
	}
}

// UpdateWindows is conditional on the override's model: a stale resolver
// must not stamp the old model's windows onto an entry that concurrently
// moved to another model (review P2-2).
func TestSessionModelRegistry_UpdateWindowsModelGuard(t *testing.T) {
	r := newSessionModelRegistry()
	r.Set("s", &SessionModel{ModelName: "m1", ContextWindow: 1000, DeclaredContextWindow: 1000})

	r.UpdateWindows("s", "m2", 2000, 2000)
	if got := r.Get("s"); got.ContextWindow != 1000 || got.DeclaredContextWindow != 1000 {
		t.Fatalf("stale model's windows were applied: %+v", got)
	}

	r.UpdateWindows("s", "m1", 3000, 3000)
	if got := r.Get("s"); got.ContextWindow != 3000 || got.DeclaredContextWindow != 3000 {
		t.Fatalf("matching model's windows were not applied: %+v", got)
	}
}

// SetupTurn-level wiring: the turn runs on the session's provider, window
// and candidates while the agent's globals stay untouched.
func TestSetupTurn_AppliesSessionModel(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "primary",
				MaxTokens:         4096,
				ContextWindow:     100_000,
				MaxToolIterations: 10,
				ModelFallbacks:    []string{"fallback"},
			},
		},
		ModelList: []*config.ModelConfig{
			{ModelName: "primary", Model: "openai/primary-model", APIKeys: config.SimpleSecureStrings("k1")},
			{
				ModelName:     "small",
				Model:         "openai/small-model",
				APIKeys:       config.SimpleSecureStrings("k2"),
				ContextWindow: 32_000,
			},
			{ModelName: "fallback", Model: "openai/fallback-model", APIKeys: config.SimpleSecureStrings("k3")},
		},
	}
	sessionProvider := &countedStatefulProvider{}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleConvProvider{})
	al.providerFactory = func(*config.ModelConfig) (providers.LLMProvider, string, error) {
		return sessionProvider, "", nil
	}
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	originalModel := agent.Model
	originalWindow := agent.ContextWindow

	sessionKey := "sk_v1_setupturn"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts(sessionKey)
	// newTurnState reads the key from opts.Dispatch (makeTestProcessOpts only
	// fills the top-level SessionKey field).
	opts.Dispatch.SessionKey = sessionKey
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	if exec.activeProvider != providers.LLMProvider(sessionProvider) {
		t.Fatalf("turn activeProvider is not the session model provider (%T)", exec.activeProvider)
	}
	if got := ts.effectiveContextWindow(); got != 32_000 {
		t.Fatalf("turn window = %d, want 32000 (session model window)", got)
	}
	if len(exec.activeCandidates) == 0 || exec.activeCandidates[0].Model != "small-model" ||
		exec.activeCandidates[0].DisplayName != "small" {
		t.Fatalf("turn candidates = %+v, want the session model's chain", exec.activeCandidates)
	}
	if agent.Model != originalModel {
		t.Fatalf("agent model mutated by the turn: %q -> %q", originalModel, agent.Model)
	}
	if agent.ContextWindow != originalWindow {
		t.Fatalf("agent window mutated by the turn: %d", agent.ContextWindow)
	}
}
