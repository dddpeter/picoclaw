package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func approvalTestHook(t *testing.T, cfgJSON string) *approvalHook {
	t.Helper()
	h, err := newApprovalHookFromConfig(json.RawMessage(cfgJSON))
	if err != nil {
		t.Fatalf("newApprovalHookFromConfig: %v", err)
	}
	return h
}

func approvalTestRequest(sessionKey, channel string) *ToolApprovalRequest {
	req := &ToolApprovalRequest{
		Meta:      HookMeta{SessionKey: sessionKey, AgentID: "main"},
		Tool:      "exec",
		Arguments: map[string]any{"command": "git push origin main"},
	}
	if channel != "" {
		req.Context = &TurnContext{Inbound: &bus.InboundContext{Channel: channel, ChatID: "chat-1"}}
	}
	return req
}

// TestApproval_EmptyPatternsNoBehaviorChange pins the open-default guard:
// with no ask patterns the hook approves everything, exactly as if it were
// not mounted at all (fork red line: do not harden the open defaults).
func TestApproval_EmptyPatternsNoBehaviorChange(t *testing.T) {
	h := approvalTestHook(t, `{}`)
	d, err := h.ApproveTool(context.Background(), approvalTestRequest("s1", "feishu"))
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if !d.Approved {
		t.Fatalf("empty patterns must approve, got denial: %s", d.Reason)
	}
	// Even the no-channel path never fires without patterns.
	if d, _ := h.ApproveTool(context.Background(), approvalTestRequest("s1", "")); !d.Approved {
		t.Fatalf("empty patterns must not engage the channel check: %s", d.Reason)
	}
}

// TestApprovalHook_AskPatternMatches covers the matching matrix (command
// regex vs "tool:" prefix) plus one full approve and one deny round trip.
func TestApprovalHook_AskPatternMatches(t *testing.T) {
	h := approvalTestHook(t, `{"ask_patterns":["git push","tool:write_file"],"timeout_ms":600000}`)

	// Matching matrix.
	if !h.needsApproval("exec", map[string]any{"command": "git push origin main"}) {
		t.Fatal("command pattern must match exec command")
	}
	if h.needsApproval("exec", map[string]any{"command": "ls -la"}) {
		t.Fatal("command pattern must not match unrelated command")
	}
	if h.needsApproval("read_file", map[string]any{"path": "/x"}) {
		t.Fatal("command pattern must not match non-exec tools")
	}
	if !h.needsApproval("write_file", map[string]any{"path": "/x"}) {
		t.Fatal("tool: prefix must match the tool name")
	}
	if h.needsApproval("exec", map[string]any{"command": "write_file"}) {
		t.Fatal("command text that merely names a tool must not trip the tool rule")
	}

	// Non-matching call approves immediately (no pending registered).
	benign := approvalTestRequest("s2", "feishu")
	benign.Arguments = map[string]any{"command": "ls -la"}
	if d, _ := h.ApproveTool(context.Background(), benign); !d.Approved {
		t.Fatalf("non-matching call must approve: %s", d.Reason)
	}
	if _, exists := h.pending.Load("s2"); exists {
		t.Fatal("approved call must not leave a pending waiter")
	}

	roundTrip := func(reply approvalReply, wantApproved bool) {
		t.Helper()
		done := make(chan ApprovalDecision, 1)
		go func() {
			d, err := h.ApproveTool(context.Background(), approvalTestRequest("s-rt", "feishu"))
			if err != nil {
				t.Errorf("ApproveTool: %v", err)
			}
			done <- d
		}()
		var loaded any
		deadline := time.Now().Add(2 * time.Second)
		for {
			loaded, _ = h.pending.Load("s-rt")
			if loaded != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("pending waiter never registered")
			}
			time.Sleep(5 * time.Millisecond)
		}
		loaded.(*pendingApproval).resolve <- reply
		select {
		case d := <-done:
			if d.Approved != wantApproved {
				t.Fatalf("round trip: approved=%v want %v (reason %s)", d.Approved, wantApproved, d.Reason)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("ApproveTool did not return after reply")
		}
		if _, exists := h.pending.Load("s-rt"); exists {
			t.Fatal("waiter must be cleaned up after resolution")
		}
	}
	roundTrip(approvalReply{approved: true}, true)
	roundTrip(approvalReply{approved: false}, false)
}

// TestApprovalHook_TimeoutFailsClosed: no user reply within the hook timeout
// denies (fail-closed), never silently approves.
func TestApprovalHook_TimeoutFailsClosed(t *testing.T) {
	h := approvalTestHook(t, `{"ask_patterns":["git push"],"timeout_ms":80}`)
	start := time.Now()
	d, err := h.ApproveTool(context.Background(), approvalTestRequest("s3", "feishu"))
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if d.Approved {
		t.Fatal("timeout must deny, not approve")
	}
	if !strings.Contains(d.Reason, "timed out") {
		t.Fatalf("expected timeout reason, got %q", d.Reason)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout denial too slow: %s", elapsed)
	}
	if _, exists := h.pending.Load("s3"); exists {
		t.Fatal("timed-out waiter must be cleaned up")
	}
}

// TestApproval_NoInteractiveChannelDeniesImmediately pins the DontAsk
// special case (agentscope-go borrowing §一 第 4 点): cron/heartbeat turns
// have no one to ask — Ask hits deny at once instead of burning the timeout.
func TestApproval_NoInteractiveChannelDeniesImmediately(t *testing.T) {
	h := approvalTestHook(t, `{"ask_patterns":["git push"],"timeout_ms":600000}`) // would hang for 10min if it waited
	for _, channel := range []string{"", "cli", "system", "subagent"} {
		start := time.Now()
		d, err := h.ApproveTool(context.Background(), approvalTestRequest("s4", channel))
		if err != nil {
			t.Fatalf("ApproveTool(%q): %v", channel, err)
		}
		if d.Approved {
			t.Fatalf("channel %q must deny, not approve", channel)
		}
		if !strings.Contains(d.Reason, "no interactive channel") {
			t.Fatalf("channel %q: expected no-channel reason, got %q", channel, d.Reason)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("channel %q: denial must be immediate, took %s", channel, elapsed)
		}
		if _, exists := h.pending.Load("s4"); exists {
			t.Fatalf("channel %q must not register a waiter", channel)
		}
	}
}

// TestApproval_HardAbortReturnsImmediately: cancelling the turn context
// (hard abort path) unblocks the waiter at once with a fail-closed denial.
func TestApproval_HardAbortReturnsImmediately(t *testing.T) {
	h := approvalTestHook(t, `{"ask_patterns":["git push"],"timeout_ms":600000}`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	d, err := h.ApproveTool(ctx, approvalTestRequest("s5", "feishu"))
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if d.Approved {
		t.Fatal("cancelled wait must deny, not approve")
	}
	if !strings.Contains(d.Reason, "cancelled") {
		t.Fatalf("expected cancellation reason, got %q", d.Reason)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation must return promptly, took %s", elapsed)
	}
}

// TestApprovalReply_RoutedBeforeSteering pins the inbound-pump routing:
// explicit /approve /deny (and Chinese aliases) resolve the pending waiter
// and are consumed; anything else falls through to steering.
func TestApprovalReply_RoutedBeforeSteering(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Content: "/approve"}, "none") {
		t.Fatal("no hook mounted must not consume")
	}

	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al.approvalHook = h

	register := func(key, channel, chatID string) chan approvalReply {
		ch := make(chan approvalReply, 1)
		h.pending.Store(key, &pendingApproval{resolve: ch, channel: channel, chatID: chatID})
		return ch
	}

	ch := register("sess", "feishu", "c")
	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve"}, "sess") {
		t.Fatal("/approve must be consumed while a waiter is pending")
	}
	if reply := <-ch; !reply.approved {
		t.Fatal("/approve must resolve as approved")
	}

	// M1: a reply from a DIFFERENT channel/chat must not resolve the waiter.
	ch = register("sess-x", "feishu", "c")
	if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "telegram", ChatID: "other", Content: "/approve"}, "sess-x") {
		t.Fatal("cross-chat reply must not be consumed as an approval answer")
	}
	if len(ch) != 0 {
		t.Fatal("cross-chat reply must not resolve the waiter")
	}

	// L1: a command with trailing content is not an approval answer.
	ch = register("sess-tail", "feishu", "c")
	if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve 请继续删库"}, "sess-tail") {
		t.Fatal("command with trailing content must fall through to steering")
	}
	if len(ch) != 0 {
		t.Fatal("trailing-content command must not resolve the waiter")
	}

	ch = register("sess-deny", "feishu", "c")
	al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/deny"}, "sess-deny")
	if reply := <-ch; reply.approved {
		t.Fatal("/deny must resolve as denied")
	}

	ch = register("sess-zh", "feishu", "c")
	al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "拒绝"}, "sess-zh")
	if reply := <-ch; reply.approved {
		t.Fatal("拒绝 must resolve as denied")
	}

	// Non-reply content falls through to steering even with a pending waiter.
	register("sess-other", "feishu", "c")
	if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "顺便看下进度"}, "sess-other") {
		t.Fatal("free-form text must fall through to steering")
	}

	// Reply-shaped message with no waiter falls through too.
	if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Content: "/approve"}, "no-such-session") {
		t.Fatal("no waiter must not consume")
	}
}

// TestApprovalHook_BareToolPrefixRejected pins N2: a bare "tool:" pattern is
// a config error, not a silent fallthrough to command matching.
func TestApprovalHook_BareToolPrefixRejected(t *testing.T) {
	if _, err := newApprovalHookFromConfig(json.RawMessage(`{"ask_patterns":["tool:"]}`)); err == nil {
		t.Fatal(`bare "tool:" pattern must be rejected at construction`)
	}
}

// TestApproval_MountWiringAndTimeoutBump pins the config-driven mount path:
// hooks.builtins.approval wires al.approvalHook (used by the inbound pump)
// and raises the manager-wide approval timeout to cover the hook's own
// deadline; unmounting clears the reference.
func TestApproval_MountWiringAndTimeoutBump(t *testing.T) {
	rawCfg := json.RawMessage(`{"ask_patterns":["git push"],"timeout_ms":300000}`)
	al := newConfiguredHookLoop(t, &llmHookTestProvider{}, config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			approvalHookName: {Enabled: true, Config: rawCfg},
		},
	})
	defer al.Close()

	if err := al.ensureHooksInitialized(context.Background()); err != nil {
		t.Fatalf("ensureHooksInitialized: %v", err)
	}
	if al.approvalHook == nil {
		t.Fatal("approval builtin must wire al.approvalHook")
	}
	if al.hooks.approvalTimeout < al.approvalHook.timeout {
		t.Fatalf("manager approval timeout %s must cover hook timeout %s",
			al.hooks.approvalTimeout, al.approvalHook.timeout)
	}

	al.UnmountHook(approvalHookName)
	if al.approvalHook != nil {
		t.Fatal("unmount must clear the loop-side reference")
	}
}
