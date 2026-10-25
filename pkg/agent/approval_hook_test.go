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

	// Matching matrix (matchedAskPattern returns the hit pattern text).
	if got := h.matchedAskPattern("exec", map[string]any{"command": "git push origin main"}); got != "git push" {
		t.Fatalf("command pattern must match exec command, got %q", got)
	}
	if got := h.matchedAskPattern("exec", map[string]any{"command": "ls -la"}); got != "" {
		t.Fatalf("command pattern must not match unrelated command, got %q", got)
	}
	if got := h.matchedAskPattern("read_file", map[string]any{"path": "/x"}); got != "" {
		t.Fatalf("command pattern must not match non-exec tools, got %q", got)
	}
	if got := h.matchedAskPattern("write_file", map[string]any{"path": "/x"}); got != "tool:write_file" {
		t.Fatalf("tool: prefix must match the tool name, got %q", got)
	}
	if got := h.matchedAskPattern("exec", map[string]any{"command": "write_file"}); got != "" {
		t.Fatalf("command text that merely names a tool must not trip the tool rule, got %q", got)
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

	// Reply-shaped message with no waiter is an ORPHAN: dropped with a
	// receipt so the protocol word never reaches steering or a fresh turn
	// (stale button clicks land here after the question expired).
	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve"}, "no-such-session") {
		t.Fatal("orphan approval token must be consumed (dropped with receipt)")
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

// TestApproval_SessionAlwaysAllow pins the "always" flow (二期): /approve
// always (or 总是允许) approves the pending call AND promotes the matched
// pattern to session scope, so later matches auto-approve without asking —
// until the TTL expires.
func TestApproval_SessionAlwaysAllow(t *testing.T) {
	// Parse matrix for the always forms.
	cases := []struct {
		content  string
		approved bool
		always   bool
		ok       bool
	}{
		{"/approve always", true, true, true},
		{"/APPROVE ALWAYS", true, true, true},
		{"总是允许", true, true, true},
		{"/approve", true, false, true},
		{"/approve always extra", false, false, false},
		{"/approve always吧", false, false, false},
		// "/deny always" is a plain deny without reason (deny has no always
		// form; a bare trailing "always" must not become a weird reason).
		{"/deny always", false, false, true},
	}
	for _, tc := range cases {
		ans := parseApprovalReply(tc.content)
		if ans.approved != tc.approved || ans.always != tc.always || ans.ok != tc.ok {
			t.Fatalf("parseApprovalReply(%q) = %+v, want (%v,%v,%v)",
				tc.content, ans, tc.approved, tc.always, tc.ok)
		}
	}

	// Full chain: answer with always → rule added → next call auto-approves
	// without registering a waiter.
	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)

	// Direct rule-API semantics: active after add, inactive for other
	// patterns/sessions, and expired entries are pruned.
	h.addSessionRule("s-alw", "git push")
	if !h.ruleActive("s-alw", "git push") {
		t.Fatal("rule must be active after add")
	}
	if h.ruleActive("s-alw", "systemctl restart") {
		t.Fatal("unrelated pattern must not ride the session rule")
	}
	if h.ruleActive("other-session", "git push") {
		t.Fatal("session rules must not leak across sessions")
	}
	loaded, _ := h.sessionRules.Load("s-alw")
	set := loaded.(*sessionRuleSet)
	set.mu.Lock()
	set.rules["git push"] = time.Now().Add(-time.Second)
	set.mu.Unlock()
	if h.ruleActive("s-alw", "git push") {
		t.Fatal("expired rule must be pruned")
	}

	// Auto-approve path: an active rule approves immediately, no waiter.
	h.addSessionRule("s-alw", "git push")
	start := time.Now()
	d, err := h.ApproveTool(context.Background(), approvalTestRequest("s-alw", "feishu"))
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if !d.Approved {
		t.Fatalf("session rule must auto-approve, got %q", d.Reason)
	}
	if time.Since(start) > time.Second {
		t.Fatal("auto-approve must be immediate")
	}
	if _, exists := h.pending.Load("s-alw"); exists {
		t.Fatal("auto-approve must not register a waiter")
	}

	// Routing side: "/approve always" resolves the waiter AND registers the
	// pending call's pattern.
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.approvalHook = h
	ch := make(chan approvalReply, 1)
	h.pending.Store("s-alw2", &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", pattern: "git push"})
	if !al.tryHandleApprovalReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve always"}, "s-alw2") {
		t.Fatal("/approve always must be consumed")
	}
	if reply := <-ch; !reply.approved || !reply.always {
		t.Fatal("waiter must receive approved+always")
	}
	if !h.ruleActive("s-alw2", "git push") {
		t.Fatal("/approve always must promote the matched pattern to session scope")
	}
}

// TestApprovalHook_HardDenyOverridesApproval pins the hard floor: matches
// reject outright — no ask, no session-rule bypass, interactive or not.
func TestApprovalHook_HardDenyOverridesApproval(t *testing.T) {
	h := approvalTestHook(t, `{"ask_patterns":["dd if="],"hard_deny_patterns":["dd if="],"timeout_ms":600000}`)
	h.addSessionRule("s-hd", "dd if=") // even a session rule must not bypass

	start := time.Now()
	hardReq := approvalTestRequest("s-hd", "feishu")
	hardReq.Arguments = map[string]any{"command": "dd if=/dev/zero of=/dev/sda"}
	d, err := h.ApproveTool(context.Background(), hardReq)
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if d.Approved {
		t.Fatal("hard-deny match must reject even with an active session rule")
	}
	if !strings.Contains(d.Reason, "hard-deny rule") {
		t.Fatalf("expected hard-deny reason, got %q", d.Reason)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatal("hard-deny must be immediate, no waiting")
	}
	if _, exists := h.pending.Load("s-hd"); exists {
		t.Fatal("hard-deny must not register a waiter")
	}

	// Non-hard commands still follow the ask path (existing behavior).
	benign := approvalTestRequest("s-hd-benign", "")
	benign.Arguments = map[string]any{"command": "ls -la"}
	h2 := approvalTestHook(t, `{"hard_deny_patterns":["dd if="]}`)
	if d, _ := h2.ApproveTool(context.Background(), benign); !d.Approved {
		t.Fatalf("unmatched call must approve: %s", d.Reason)
	}
}

// TestApprovalHook_InheritExecDenyPatterns pins the single-list recipe: the
// hook pulls tools.exec.custom_deny_patterns at mount, dedupes, skips
// invalid regexes with a warning, and needs no explicit ask_patterns.
func TestApprovalHook_InheritExecDenyPatterns(t *testing.T) {
	h := approvalTestHook(t, `{"inherit_exec_deny_patterns":true,"timeout_ms":300000}`)

	cfg := &config.Config{Tools: config.ToolsConfig{Exec: config.ExecConfig{
		CustomDenyPatterns: []string{
			"git push",        // command rule
			"tool:write_file", // tool rule
			"git push",        // duplicate — deduped
			"[",               // invalid — skipped with warning
			"",                // blank — ignored
		},
		EnableCustomDenyPatterns: false, // single-list recipe keeps exec enforcement off
	}}}

	added := h.inheritExecDenyPatterns(cfg)
	if added != 2 {
		t.Fatalf("expected 2 inherited rules (deduped, invalid skipped), got %d", added)
	}
	if got := h.matchedAskPattern("exec", map[string]any{"command": "git push origin main"}); got != "git push" {
		t.Fatalf("inherited command rule must match, got %q", got)
	}
	if got := h.matchedAskPattern("write_file", map[string]any{"path": "/x"}); got != "tool:write_file" {
		t.Fatalf("inherited tool rule must match, got %q", got)
	}
	if got := h.matchedAskPattern("read_file", map[string]any{"path": "/x"}); got != "" {
		t.Fatalf("unmatched tool must not ask, got %q", got)
	}

	// Re-inherit is idempotent (dedupe by pattern text).
	if again := h.inheritExecDenyPatterns(cfg); again != 0 {
		t.Fatalf("re-inherit must dedupe to 0, got %d", again)
	}

	// Nil/empty config is a no-op.
	if n := h.inheritExecDenyPatterns(nil); n != 0 {
		t.Fatal("nil config must inherit nothing")
	}
}

// TestApprovalWireFormatPinned freezes the cross-language text contract:
// the web frontend (web/frontend/src/features/chat/approval.ts) matches
// these exact strings to render approval cards and pair receipts. If a
// change here is intentional, update approval.ts in the same commit.
func TestApprovalWireFormatPinned(t *testing.T) {
	pins := []struct{ name, got, want string }{
		{"ask prefix", ApprovalAskPrefix, "⚠️ 需要批准：即将执行工具 "},
		{"reply hint prefix", ApprovalReplyHintPrefix, "回复 /approve"},
		{"receipt always prefix", ApprovalReceiptAlwaysPrefix, "✅ 已批准；本会话内命中规则"},
		{"receipt allow-all prefix", ApprovalReceiptAllowAllPrefix, "✅ 已批准；本会话内所有审批将自动放行"},
		{"receipt approved", ApprovalReceiptApprovedExact, "✅ 已批准，继续执行。"},
		{"receipt denied", ApprovalReceiptDeniedExact, "⛔ 已拒绝本次工具执行。"},
		{"receipt timeout prefix", ApprovalReceiptTimeoutPrefix, "⛔ 审批超时"},
		{"receipt no-pending prefix", ApprovalReceiptNoPendingPrefix, "当前没有待批准的操作"},
	}
	for _, pin := range pins {
		if pin.got != pin.want {
			t.Errorf("%s drifted: got %q want %q (frontend approval.ts must be updated in lockstep)", pin.name, pin.got, pin.want)
		}
	}
}

// TestApproval_DenyWithReason pins the deny-with-reason flow (octop
// borrowing §二): "/deny <text>" is a valid answer carrying the reason to
// the waiter, the reason reaches ApprovalDecision.Reason (and thus the
// denied tool result the model sees), and it is bounded. Approve keeps its
// strict whole-message rule — trailing content never approves.
func TestApproval_DenyWithReason(t *testing.T) {
	// Parse matrix: deny accepts trailing text as the reason; approve does not.
	cases := []struct {
		content string
		ok      bool
		reason  string
	}{
		{"/deny", true, ""},
		{"/deny 别动生产分支", true, "别动生产分支"},
		{"/deny   先看 diff 再说  ", true, "先看 diff 再说"},
		{"/DENY prod only", true, "prod only"},
		{"/deny always", true, ""}, // bare "always" is not a reason
		{"/approve 别执行", false, ""},
		{"/deny " + strings.Repeat("长", approvalDenyReasonMaxRunes+50), true,
			strings.Repeat("长", approvalDenyReasonMaxRunes) + "…"},
	}
	for _, tc := range cases {
		ans := parseApprovalReply(tc.content)
		if ans.ok != tc.ok || ans.reason != tc.reason {
			t.Fatalf("parseApprovalReply(%q) reason=%q ok=%v, want reason=%q ok=%v",
				tc.content, ans.reason, ans.ok, tc.reason, tc.ok)
		}
	}

	// Routing side: the waiter receives the reason typed by the user.
	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.approvalHook = h
	ch := make(chan approvalReply, 1)
	h.pending.Store("s-r", &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", pattern: "git push"})
	if !al.tryHandleApprovalReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/deny 别动生产分支"}, "s-r") {
		t.Fatal("/deny with reason must be consumed")
	}
	reply := <-ch
	if reply.approved || reply.always {
		t.Fatal("deny-with-reason must not approve")
	}
	if reply.reason != "别动生产分支" {
		t.Fatalf("waiter reason = %q, want the typed reason", reply.reason)
	}

	// Full chain: the reason surfaces in ApprovalDecision.Reason (this is
	// what appendDeniedToolResult renders into the tool result for the model).
	decided := make(chan ApprovalDecision, 1)
	go func() {
		d, err := h.ApproveTool(context.Background(), approvalTestRequest("s-chain", "feishu"))
		if err != nil {
			t.Errorf("ApproveTool: %v", err)
		}
		decided <- d
	}()
	deadline := time.Now().Add(5 * time.Second)
	var waiter *pendingApproval
	for time.Now().Before(deadline) {
		if loaded, ok := h.pending.Load("s-chain"); ok {
			waiter = loaded.(*pendingApproval)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if waiter == nil {
		t.Fatal("waiter not registered")
	}
	waiter.resolve <- approvalReply{approved: false, reason: "别动生产分支"}
	d := <-decided
	if d.Approved {
		t.Fatal("deny reply must not approve")
	}
	if !strings.Contains(d.Reason, "别动生产分支") {
		t.Fatalf("typed reason must reach ApprovalDecision.Reason, got %q", d.Reason)
	}
}

// TestApproval_SessionAllowAll pins the allow_all tier (octop borrowing §一):
// "/approve all" (or 全部允许) approves the pending call AND switches the
// session to allow_all — every later ask pattern auto-approves without a
// waiter, TTL-bounded and session-isolated. Hard-deny stays above every
// tier; deny never forms policy.
func TestApproval_SessionAllowAll(t *testing.T) {
	// Parse matrix for the allow_all forms.
	cases := []struct {
		content  string
		ok       bool
		allowAll bool
	}{
		{"/approve all", true, true},
		{"/APPROVE ALL", true, true},
		{"全部允许", true, true},
		{"/approve all extra", false, false},
		{"/approve always", true, false}, // pattern promote, not allow_all
		{"/deny all", true, false},       // trailing text is a deny reason
	}
	for _, tc := range cases {
		ans := parseApprovalReply(tc.content)
		if ans.ok != tc.ok || ans.allowAll != tc.allowAll {
			t.Fatalf("parseApprovalReply(%q) = %+v, want ok=%v allowAll=%v", tc.content, ans, tc.ok, tc.allowAll)
		}
	}

	h := approvalTestHook(t, `{"ask_patterns":["git push","rm -rf"],"hard_deny_patterns":["cat /etc/shadow"],"timeout_ms":600000}`)

	// Routing side: "/approve all" resolves the waiter with approved+allowAll.
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.approvalHook = h
	ch := make(chan approvalReply, 1)
	h.pending.Store("s-aa", &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", pattern: "git push"})
	if !al.tryHandleApprovalReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve all"}, "s-aa") {
		t.Fatal("/approve all must be consumed")
	}
	reply := <-ch
	if !reply.approved || !reply.allowAll || reply.always {
		t.Fatal("waiter must receive approved+allowAll (not pattern-always)")
	}
	if !h.allowAllActive("s-aa") {
		t.Fatal("/approve all must switch the session to allow_all")
	}
	h.pending.Delete("s-aa") // drop the routing fixture before tier assertions

	// allow_all tier: a DIFFERENT ask pattern auto-approves without asking.
	other := approvalTestRequest("s-aa", "feishu")
	other.Arguments = map[string]any{"command": "rm -rf /tmp/x"}
	start := time.Now()
	d, err := h.ApproveTool(context.Background(), other)
	if err != nil {
		t.Fatalf("ApproveTool under allow_all: %v", err)
	}
	if !d.Approved {
		t.Fatalf("allow_all must auto-approve other patterns, got %q", d.Reason)
	}
	if time.Since(start) > time.Second {
		t.Fatal("allow_all auto-approve must be immediate")
	}
	if _, exists := h.pending.Load("s-aa"); exists {
		t.Fatal("allow_all auto-approve must not register a waiter")
	}

	// Hard deny stays above allow_all.
	hardReq := approvalTestRequest("s-aa", "feishu")
	hardReq.Arguments = map[string]any{"command": "cat /etc/shadow"}
	if d, _ := h.ApproveTool(context.Background(), hardReq); d.Approved {
		t.Fatal("hard-deny must reject even under allow_all")
	}

	// Session isolation: another session still asks.
	if h.allowAllActive("s-other") {
		t.Fatal("allow_all must not leak across sessions")
	}

	// TTL re-arm: an expired allow_all re-arms asking.
	loaded, _ := h.sessionRules.Load("s-aa")
	set := loaded.(*sessionRuleSet)
	set.mu.Lock()
	set.allowAllUntil = time.Now().Add(-time.Second)
	set.mu.Unlock()
	if h.allowAllActive("s-aa") {
		t.Fatal("expired allow_all must re-arm asking")
	}
}

// TestApprovalReply_EmptySurfaceNotConsumed pins the pump-side half of the
// "empty ask_patterns = identical to disabled" contract (review P1-11): with
// no ask/hard-deny surface the hook can never have a pending question, so an
// orphan protocol word ("同意" typed in ordinary chat) must keep flowing
// instead of being swallowed with a receipt.
func TestApprovalReply_EmptySurfaceNotConsumed(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	al.approvalHook = approvalTestHook(t, `{}`)
	for _, content := range []string{"/approve", "同意", "批准", "拒绝"} {
		if al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: content}, "sess-empty") {
			t.Fatalf("empty ask surface must not consume %q (identical to disabled)", content)
		}
	}

	// Contrast: a real ask surface still consumes orphans.
	al.approvalHook = approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve"}, "sess-empty") {
		t.Fatal("hook with ask patterns keeps the orphan consumption")
	}
}

// TestApprovalReply_LateReplyAfterRetirement closes the timeout-race window
// (review P1-1): once the waiter's owner has retired it (resolved set —
// timeout/abort decided), a racing reply must NOT be delivered, must NOT
// register allow_all/session rules, and must NOT draw a "已批准" receipt.
func TestApprovalReply_LateReplyAfterRetirement(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al.approvalHook = h

	ch := make(chan approvalReply, 1)
	waiter := &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", pattern: "git push"}
	waiter.mu.Lock()
	waiter.resolved = true // finish() ran: the timer already decided
	waiter.mu.Unlock()
	h.pending.Store("sess-late", waiter)

	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/approve all"}, "sess-late") {
		t.Fatal("retired waiter still consumes the late reply as an orphan (with receipt)")
	}
	select {
	case reply := <-ch:
		t.Fatalf("late reply must not be delivered after retirement, got %+v", reply)
	default:
	}
	if hook := al.approvalHook; hook != nil {
		if set, ok := hook.sessionRules.Load("sess-late"); ok {
			if rs, ok := set.(*sessionRuleSet); ok {
				rs.mu.Lock()
				active := !rs.allowAllUntil.IsZero()
				rs.mu.Unlock()
				if active {
					t.Fatal("allow_all must NOT be registered for a retired waiter")
				}
			}
		}
	}
}

// TestApproveOff_RevokesSessionTiers pins "/approve off" (撤销放行): the
// session's auto-pass tiers (allow_all + always rules) must be revocable on
// demand, and the revoke form never answers a pending question.
func TestApproveOff_RevokesSessionTiers(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al.approvalHook = h

	// Arm both tiers.
	h.setAllowAll("sess-rev")
	h.addSessionRule("sess-rev", "git push")

	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{
		Channel: "feishu", ChatID: "c", Content: "/approve off",
	}, "sess-rev") {
		t.Fatal("/approve off must be consumed")
	}
	if h.ruleActive("sess-rev", "git push") {
		t.Fatal("always rule must be cleared by /approve off")
	}
	if h.ruleActive("sess-rev", "anything-else") {
		t.Fatal("allow_all tier must be cleared by /approve off")
	}

	// Chinese alias works too; with no active tiers it still consumes and
	// reports honestly instead of leaking the protocol word into steering.
	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{
		Channel: "feishu", ChatID: "c", Content: "撤销放行",
	}, "sess-rev") {
		t.Fatal("撤销放行 must be consumed even with nothing to revoke")
	}

	// The revoke form must NOT answer a pending waiter.
	ch := make(chan approvalReply, 1)
	h.pending.Store("sess-rev2", &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", pattern: "git push"})
	if !al.tryHandleApprovalReply(context.Background(), bus.InboundMessage{
		Channel: "feishu", ChatID: "c", Content: "/approve off",
	}, "sess-rev2") {
		t.Fatal("revoke on a session with a pending question is still consumed")
	}
	select {
	case reply := <-ch:
		t.Fatalf("/approve off must not resolve the pending waiter, got %+v", reply)
	default:
	}
}

// TestApprovalReply_GroupSenderPinning (review P2, ask_user's §三-4 policy):
// in a group chat the approval must be answered by the user whose gated call
// triggered it — another member cannot approve it. Direct chats and
// identity-less replies stay permissive; the feishu card-click path carries
// operatorOpenID as SenderID, so buttons are covered by the same check.
func TestApprovalReply_GroupSenderPinning(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al.approvalHook = h

	register := func(key, sender string) chan approvalReply {
		ch := make(chan approvalReply, 1)
		h.pending.Store(key, &pendingApproval{resolve: ch, channel: "feishu", chatID: "c", senderID: sender, pattern: "git push"})
		return ch
	}

	ch := register("sess-apin", "ou_asked")
	other := bus.InboundMessage{Channel: "feishu", ChatID: "c", SenderID: "ou_other",
		Content: "/approve", Context: bus.InboundContext{ChatType: "group"}}
	if al.tryHandleApprovalReply(context.Background(), other, "sess-apin") {
		t.Fatal("another group member must not answer the approval")
	}
	select {
	case reply := <-ch:
		t.Fatalf("no reply may be delivered for another member's approval, got %+v", reply)
	default:
	}

	asked := bus.InboundMessage{Channel: "feishu", ChatID: "c", SenderID: "ou_asked",
		Content: "/approve", Context: bus.InboundContext{ChatType: "group"}}
	if !al.tryHandleApprovalReply(context.Background(), asked, "sess-apin") {
		t.Fatal("the asked user's approval must be consumed")
	}
	if reply := <-ch; !reply.approved {
		t.Fatal("asked user's reply must resolve as approved")
	}

	// Direct chats stay permissive on sender mismatch (the peer is
	// unambiguous); replies without sender identity stay permissive.
	ch2 := register("sess-apin-d", "ou_asked")
	direct := bus.InboundMessage{Channel: "feishu", ChatID: "c", SenderID: "ou_elsewhere",
		Content: "/approve", Context: bus.InboundContext{ChatType: "direct"}}
	if !al.tryHandleApprovalReply(context.Background(), direct, "sess-apin-d") {
		t.Fatal("direct chat reply must be consumed regardless of sender pin")
	}
	if reply := <-ch2; !reply.approved {
		t.Fatal("direct reply must resolve")
	}

	ch3 := register("sess-apin-anon", "ou_asked")
	anon := bus.InboundMessage{Channel: "feishu", ChatID: "c",
		Content: "/approve", Context: bus.InboundContext{ChatType: "group"}}
	if !al.tryHandleApprovalReply(context.Background(), anon, "sess-apin-anon") {
		t.Fatal("identity-less reply stays permissive")
	}
	if reply := <-ch3; !reply.approved {
		t.Fatal("identity-less reply must resolve")
	}
}
