package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewCommand_ClearsHistory(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "new")
	called := false
	rt := &Runtime{
		ClearHistory: func() error {
			called = true
			return nil
		},
	}
	var reply string
	if err := def.Handler(context.Background(), Request{Text: "/new", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt); err != nil {
		t.Fatalf("/new handler error: %v", err)
	}
	if !called {
		t.Fatal("/new should call ClearHistory")
	}
	if !strings.Contains(reply, "New conversation started") {
		t.Fatalf("/new reply = %q", reply)
	}
}

func TestNewCommand_ReportsResetModel(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "new")
	cleared := false
	reset := false
	rt := &Runtime{
		ClearHistory: func() error {
			cleared = true
			return nil
		},
		ResetModel: func() (string, error) {
			reset = true
			return "glm-4.7", nil
		},
	}
	var reply string
	if err := def.Handler(context.Background(), Request{Text: "/new", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt); err != nil {
		t.Fatalf("/new handler error: %v", err)
	}
	if !cleared || !reset {
		t.Fatalf("/new should call ClearHistory (cleared=%v) and ResetModel (reset=%v)", cleared, reset)
	}
	if !strings.Contains(reply, "New conversation started") || !strings.Contains(reply, "Model: glm-4.7") {
		t.Fatalf("/new reply = %q, want history-cleared notice plus active model", reply)
	}
}

func TestNewCommand_ModelResetFailureStillClearsHistory(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "new")
	rt := &Runtime{
		ClearHistory: func() error { return nil },
		ResetModel:   func() (string, error) { return "", errors.New("model gone") },
	}
	var reply string
	if err := def.Handler(context.Background(), Request{Text: "/new", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt); err != nil {
		t.Fatalf("/new handler error: %v", err)
	}
	if !strings.Contains(reply, "New conversation started") {
		t.Fatalf("/new reply should still confirm the reset: %q", reply)
	}
	if !strings.Contains(reply, "model gone") {
		t.Fatalf("/new reply should surface the model reset failure: %q", reply)
	}
}

func TestNewCommand_ClearFailureReported(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "new")
	rt := &Runtime{
		ClearHistory: func() error { return errors.New("boom") },
	}
	var reply string
	_ = def.Handler(context.Background(), Request{Text: "/new", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt)
	if !strings.Contains(reply, "boom") {
		t.Fatalf("/new failure reply = %q, want error detail", reply)
	}
}

func TestNewCommand_UnavailableWithoutRuntime(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "new")
	var reply string
	_ = def.Handler(context.Background(), Request{Text: "/new", Reply: func(s string) error {
		reply = s
		return nil
	}}, nil)
	if reply != unavailableMsg {
		t.Fatalf("/new without runtime = %q, want unavailable message", reply)
	}
}

func TestStatusCommand_FormatsOverview(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "status")
	rt := &Runtime{
		GetStatusOverview: func() *StatusOverview {
			return &StatusOverview{
				Version:  "v0.9.8 (git: abc12345)",
				Model:    "glm-4.7",
				Provider: "openai_compat",
				Channels: []string{"feishu", "telegram"},
				Uptime:   90 * time.Minute,
				ActiveTurns: []ActiveTurnStatus{{
					SessionKey: "feishu:oc_1",
					Channel:    "feishu",
					Task:       "fix the login bug and add tests for it please",
					Phase:      "running",
					Iteration:  2,
					RunningFor: 34 * time.Second,
				}},
			}
		},
		GetContextStats: func() *ContextStats {
			return &ContextStats{UsedTokens: 14500, TotalTokens: 32000, UsedPercent: 45}
		},
	}
	var reply string
	if err := def.Handler(context.Background(), Request{Text: "/status", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt); err != nil {
		t.Fatalf("/status handler error: %v", err)
	}
	for _, want := range []string{
		"Version: v0.9.8",
		"Model: glm-4.7 (openai_compat)",
		"Channels: feishu, telegram",
		"Uptime: 1h30m",
		"Context: 45% used (~14.5K/32.0K",
		"Active tasks (1)",
		"[feishu] fix the login bug and add tests for it please — 34s, iteration 2 (running)",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("/status reply missing %q\ngot:\n%s", want, reply)
		}
	}
}

func TestStatusCommand_Idle(t *testing.T) {
	def := findDefinitionByName(t, BuiltinDefinitions(), "status")
	rt := &Runtime{
		GetStatusOverview: func() *StatusOverview {
			return &StatusOverview{Version: "dev"}
		},
	}
	var reply string
	_ = def.Handler(context.Background(), Request{Text: "/status", Reply: func(s string) error {
		reply = s
		return nil
	}}, rt)
	if !strings.Contains(reply, "Active tasks: none") {
		t.Fatalf("/status idle reply = %q", reply)
	}
}

func TestHelpListsNewAndStatus(t *testing.T) {
	defs := BuiltinDefinitions()
	findDefinitionByName(t, defs, "new")
	findDefinitionByName(t, defs, "status")
}
