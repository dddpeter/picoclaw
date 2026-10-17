// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// buildAskTurnState forges a minimal turnState for the ask_user paths.
func buildAskTurnState(t *testing.T, al *AgentLoop, channel, chatID, sessionKey string) *turnState {
	t.Helper()
	agent := al.GetRegistry().GetDefaultAgent()
	ts := newTurnState(agent, processOptions{
		Dispatch: DispatchRequest{SessionKey: sessionKey, UserMessage: "hi"},
	}, al.newTurnEventScope(agent.ID, sessionKey, newTurnContext(nil, nil, nil)))
	ts.channel = channel
	ts.chatID = chatID
	return ts
}

func askUserArgs(questions ...bus.AskQuestion) map[string]any {
	raws := make([]any, 0, len(questions))
	for _, q := range questions {
		opts := make([]any, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, map[string]any{"label": o.Label, "description": o.Description})
		}
		raws = append(raws, map[string]any{
			"question":     q.Question,
			"header":       q.Header,
			"options":      opts,
			"multi_select": q.MultiSelect,
		})
	}
	return map[string]any{"questions": raws}
}

// TestParseAskQuestions_CapsAndValidation pins the input contract: 1-5
// questions, ≤6 options each, texts trimmed and bounded; violations become
// tool errors (the model retries with a sane payload).
func TestParseAskQuestions_CapsAndValidation(t *testing.T) {
	valid, err := parseAskQuestions(askUserArgs(bus.AskQuestion{
		Question: "  部署到哪个环境？ ",
		Options: []bus.AskOption{
			{Label: " staging "}, {Label: "production"}, {Label: "本地"},
		},
	}))
	if err != nil || len(valid) != 1 || valid[0].Question != "部署到哪个环境？" || valid[0].Options[0].Label != "staging" {
		t.Fatalf("parse+trim failed: err=%v valid=%+v", err, valid)
	}

	if _, err := parseAskQuestions(map[string]any{"questions": nil}); err == nil {
		t.Fatal("zero questions must be rejected")
	}
	tooMany := askUserArgs(make([]bus.AskQuestion, askUserMaxQuestions+1)...)
	if _, err := parseAskQuestions(tooMany); err == nil {
		t.Fatal(">5 questions must be rejected")
	}
	sevenOpts := askUserArgs(bus.AskQuestion{Question: "q", Options: make([]bus.AskOption, askUserMaxOptions+1)})
	if _, err := parseAskQuestions(sevenOpts); err == nil {
		t.Fatal(">6 options must be rejected")
	}
	if _, err := parseAskQuestions(map[string]any{"questions": "not-a-list"}); err == nil {
		t.Fatal("malformed questions must be rejected")
	}
}

// TestRunAskUser_BlocksAndReceivesAnswer pins the happy path: the tool
// blocks on the pending wait, an inbound reply from the same channel/chat
// resolves it, option tokens normalize to canonical labels, and the result
// is the deterministic "header: answer" list for the model.
func TestRunAskUser_BlocksAndReceivesAnswer(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.askTimeout = 5 * time.Second
	ts := buildAskTurnState(t, al, "feishu", "c1", "agent_default:ask:s1")

	questions := []bus.AskQuestion{
		{Question: "部署到哪个环境？", Header: "环境", Options: []bus.AskOption{{Label: "staging"}, {Label: "production"}}},
		{Question: "需要回滚吗？", Header: "回滚"},
	}
	done := make(chan *tools.ToolResult, 1)
	go func() {
		done <- al.runAskUser(context.Background(), ts, askUserArgs(questions...))
	}()

	// Wait for the pending ask to be registered, then answer from the same
	// chat: "A" normalizes to the first option's label; the second line is
	// free text.
	deadline := time.Now().Add(5 * time.Second)
	var pending *pendingAsk
	for time.Now().Before(deadline) {
		if loaded, ok := al.pendingAsks.Load("agent_default:ask:s1"); ok {
			pending = loaded.(*pendingAsk)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pending == nil {
		t.Fatal("pending ask not registered")
	}
	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "A\n不用回滚"}, "agent_default:ask:s1") {
		t.Fatal("ask reply must be consumed")
	}

	res := <-done
	if res == nil || res.IsError {
		t.Fatalf("ask result: %+v", res)
	}
	content := res.ContentForLLM()
	if !strings.Contains(content, "环境: staging") {
		t.Fatalf("option token must normalize to the canonical label, got: %s", content)
	}
	if !strings.Contains(content, "回滚: 不用回滚") {
		t.Fatalf("free-text answer must ride through, got: %s", content)
	}
}

// TestRunAskUser_SoftFailures pins the opposite-of-approval semantics: no
// interactive channel and timeout are SOFT failures ("user did not
// answer") — never hard denials; the model decides how to continue.
func TestRunAskUser_SoftFailures(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	// No interactive channel (cron/heartbeat shape).
	ts := buildAskTurnState(t, al, "", "", "agent_default:ask:s2")
	res := al.runAskUser(context.Background(), ts, askUserArgs(bus.AskQuestion{Question: "q"}))
	if !strings.Contains(res.ContentForLLM(), "no interactive channel") {
		t.Fatalf("no-channel result must say so, got: %s", res.ContentForLLM())
	}

	// Timeout (soft).
	al.askTimeout = 30 * time.Millisecond
	ts2 := buildAskTurnState(t, al, "feishu", "c2", "agent_default:ask:s3")
	res2 := al.runAskUser(context.Background(), ts2, askUserArgs(bus.AskQuestion{Question: "q"}))
	if !strings.Contains(res2.ContentForLLM(), "did not answer") {
		t.Fatalf("timeout result must say the user did not answer, got: %s", res2.ContentForLLM())
	}
	if res2.IsError {
		t.Fatal("ask timeout is a soft failure, not a tool error")
	}
}

// TestRunAskUser_SkipEscape pins the escape hatch: /skip answers with
// skipped=true and the result tells the model the user declined to answer.
func TestRunAskUser_SkipEscape(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.askTimeout = 5 * time.Second
	ts := buildAskTurnState(t, al, "feishu", "c3", "agent_default:ask:s4")

	done := make(chan *tools.ToolResult, 1)
	go func() {
		done <- al.runAskUser(context.Background(), ts, askUserArgs(bus.AskQuestion{Question: "q"}))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := al.pendingAsks.Load("agent_default:ask:s4"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c3", Content: "/skip"}, "agent_default:ask:s4") {
		t.Fatal("/skip must be consumed by the ask interceptor")
	}
	res := <-done
	if !strings.Contains(res.ContentForLLM(), "skipped") {
		t.Fatalf("skip result must say the user skipped, got: %s", res.ContentForLLM())
	}
}

// TestTryHandleAskReply_Fallthroughs pins the routing discipline: a
// different chat never answers; slash commands other than /skip fall
// through to the command path; with no pending ask nothing is consumed.
func TestTryHandleAskReply_Fallthroughs(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	ch := make(chan askReply, 1)
	al.pendingAsks.Store("agent_default:ask:s5", &pendingAsk{
		resolve:   ch,
		channel:   "feishu",
		chatID:    "c1",
		questions: []bus.AskQuestion{{Question: "q"}},
	})

	if al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "OTHER", Content: "答案"}, "agent_default:ask:s5") {
		t.Fatal("a different chat must not answer the ask")
	}
	if al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "/status"}, "agent_default:ask:s5") {
		t.Fatal("slash commands must fall through to the command path")
	}
	if al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "答案"}, "agent_default:ask:none") {
		t.Fatal("no pending ask: nothing to consume")
	}
	// Consume it properly so later assertions see a resolved state.
	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "答案"}, "agent_default:ask:s5") {
		t.Fatal("matching reply must be consumed")
	}
}

// TestNormalizeAskAnswer pins deterministic answer normalization: letter/
// number tokens map to canonical option labels (case-insensitive),
// multi-select joins with "; ".
func TestNormalizeAskAnswer(t *testing.T) {
	q := bus.AskQuestion{
		Question: "q",
		Options:  []bus.AskOption{{Label: "staging"}, {Label: "production"}},
	}
	cases := map[string]string{
		"A":       "staging",
		"a":       "staging",
		"B":       "production",
		"2":       "production",
		"staging": "staging",
		"STAGING": "staging",
		"随便都行":    "随便都行",
	}
	for in, want := range cases {
		if got := normalizeAskAnswer(q, in); got != want {
			t.Fatalf("normalizeAskAnswer(%q) = %q, want %q", in, got, want)
		}
	}
	multi := bus.AskQuestion{
		Question:    "q",
		MultiSelect: true,
		Options:     []bus.AskOption{{Label: "staging"}, {Label: "production"}, {Label: "本地"}},
	}
	if got := normalizeAskAnswer(multi, "A;C"); got != "staging; 本地" {
		t.Fatalf("multi-select join = %q", got)
	}
}

// TestAskUser_NeverFormsApprovalPolicy pins the isolation rule: answering an
// ask never touches the approval session rules (asks are UX, not gates).
func TestAskUser_NeverFormsApprovalPolicy(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	ch := make(chan askReply, 1)
	al.pendingAsks.Store("agent_default:ask:s6", &pendingAsk{
		resolve:   ch,
		channel:   "feishu",
		chatID:    "c1",
		questions: []bus.AskQuestion{{Question: "q"}},
	})
	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "总是允许"}, "agent_default:ask:s6") {
		t.Fatal("plain text (even approval-looking words) must be consumed as an ask answer")
	}
	// Isolation: answering an ask never forms approval policy — mount a
	// real approval hook and prove no rule/tier got created.
	h := approvalTestHook(t, `{"ask_patterns":["git push"]}`)
	al.approvalHook = h
	if h.ruleActive("agent_default:ask:s6", "git push") || h.allowAllActive("agent_default:ask:s6") {
		t.Fatal("ask answers must never form approval session rules or allow_all")
	}
}

// TestRunAskUser_KeepAliveCalmsWatchdog pins §三-5: the wait itself counts
// as progress — the keepalive branch keeps touching the turn's activity so
// the stall watchdog never mistakes the blocked ask for a dead turn.
func TestRunAskUser_KeepAliveCalmsWatchdog(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	orig := askKeepAliveInterval
	askKeepAliveInterval = 10 * time.Millisecond
	t.Cleanup(func() { askKeepAliveInterval = orig })

	al.askTimeout = 150 * time.Millisecond
	ts := buildAskTurnState(t, al, "feishu", "ck", "agent_default:ask:ka")
	res := al.runAskUser(context.Background(), ts, askUserArgs(bus.AskQuestion{Question: "q"}))
	if res.IsError {
		t.Fatalf("timeout must stay a soft failure, got: %+v", res)
	}
	// The wait lasted ~150ms with 10ms keepalive beats: the last touch must
	// be within a couple of intervals of "now", not the turn-start stamp.
	if idle := ts.activityIdleFor(time.Now()); idle > 3*askKeepAliveInterval {
		t.Fatalf("keepalive must keep touching activity during the wait, idle=%v", idle)
	}
}

// TestRunAskUser_CardAndReceiptsAreNonFinal pins the wire contract (review
// P1-3): receipts carry the PICO-SCOPED non-final marker (Raw["pico_kind"])
// — never the generic message_kind, which the channel manager treats as
// auxiliary and drops inside the finalized-stream suppression window — and
// the card needs no marker at all (the pico channel marks ask/approval
// cards by payload presence). The timeout receipt itself is the
// anti-ghost-card fix (review P2-5).
func TestRunAskUser_CardAndReceiptsAreNonFinal(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	al.askTimeout = 20 * time.Millisecond
	ts := buildAskTurnState(t, al, "feishu", "ct", "agent_default:ask:to")

	res := al.runAskUser(context.Background(), ts, askUserArgs(bus.AskQuestion{Question: "q"}))
	if res.IsError {
		t.Fatalf("timeout must stay a soft failure, got: %+v", res)
	}

	sawCard, sawTimeoutReceipt := false, false
	for {
		var out bus.OutboundMessage
		var ok bool
		select {
		case out, ok = <-al.bus.(*bus.MessageBus).OutboundChan():
			if !ok {
				break
			}
		default:
			ok = false
		}
		if !ok {
			break
		}
		if kind := out.Context.Raw["message_kind"]; kind != "" {
			t.Fatalf("ask outbounds must NOT use the generic message_kind (droppable as auxiliary), got %q", kind)
		}
		if out.Ask != nil {
			sawCard = true
		}
		if strings.Contains(out.Content, "问询超过") {
			sawTimeoutReceipt = true
			if out.Context.Raw["pico_kind"] != "progress_note" {
				t.Fatal("the timeout receipt must carry the pico-scoped progress marker")
			}
		}
	}
	if !sawCard {
		t.Fatal("the question card must be published")
	}
	if !sawTimeoutReceipt {
		t.Fatal("the timeout receipt must be published so cards seal instead of hanging")
	}
}

// TestTryHandleAskReply_OrphanSkip pins the /stop-precedent orphan handling:
// a bare /skip with no pending ask is consumed with a receipt (never leaks
// into the model context); trailing content and other commands still fall
// through.
func TestTryHandleAskReply_OrphanSkip(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "/skip"}, "agent_default:ask:orphan") {
		t.Fatal("a bare orphan /skip must be consumed with a receipt")
	}
	if al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "/skip please"}, "agent_default:ask:orphan") {
		t.Fatal("/skip with trailing content is not a bare skip — must fall through")
	}

	sawReceipt := false
	for {
		var out bus.OutboundMessage
		select {
		case out = <-al.bus.(*bus.MessageBus).OutboundChan():
		default:
			out = bus.OutboundMessage{}
		}
		if out.Content == "" {
			break
		}
		if strings.Contains(out.Content, "当前没有待回答的问题") {
			sawReceipt = true
		}
	}
	if !sawReceipt {
		t.Fatal("the orphan /skip must be answered with a receipt")
	}
}

// TestTryHandleAskReply_SkipReceiptDistinct pins the two receipt texts
// (review P2-1): answering and skipping seal the web card with different
// verdicts, so the skip receipt must be distinguishable from the answer one.
func TestTryHandleAskReply_SkipReceiptDistinct(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	ch := make(chan askReply, 1)
	al.pendingAsks.Store("agent_default:ask:sk", &pendingAsk{
		resolve:   ch,
		channel:   "feishu",
		chatID:    "c1",
		questions: []bus.AskQuestion{{Question: "q"}},
	})
	if !al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "/skip"}, "agent_default:ask:sk") {
		t.Fatal("/skip must be consumed")
	}
	for {
		var out bus.OutboundMessage
		select {
		case out = <-al.bus.(*bus.MessageBus).OutboundChan():
		default:
			out = bus.OutboundMessage{}
		}
		if out.Content == "" {
			break
		}
		if strings.Contains(out.Content, "已收到回答") {
			t.Fatalf("skip receipt must not read as an answer receipt, got: %s", out.Content)
		}
		if strings.Contains(out.Content, "已跳过问询") {
			return
		}
	}
	t.Fatal("the skip receipt was not published")
}

// TestCollectAskReply_CapsAnswerRunes pins the free-text cap (review P2-2):
// a pasted log line rides into the model context truncated, not whole.
func TestCollectAskReply_CapsAnswerRunes(t *testing.T) {
	qs := []bus.AskQuestion{{Question: "q"}}
	long := strings.Repeat("字", askMaxAnswerRunes+100)
	reply := collectAskReply(qs, long)
	got := []rune(reply.answers[0])
	if len(got) > askMaxAnswerRunes+1 { // +1 for the ellipsis rune
		t.Fatalf("answer must be capped to %d runes (+ellipsis), got %d", askMaxAnswerRunes, len(got))
	}
	if got[len(got)-1] != '…' {
		t.Fatalf("capped answer must end with the ellipsis marker, got: ...%s", string(got[len(got)-3:]))
	}
}

// TestTryHandleAskReply_RetiredWaiterFlowsOn pins the late-reply window fix
// (review P3-2): once the waiter's owner decided (timeout/abort set resolved
// before deleting the map entry), a racing reply from the asking chat is
// neither consumed nor acknowledged — it flows on like any normal message
// instead of drawing a receipt for a question the model already abandoned.
func TestTryHandleAskReply_RetiredWaiterFlowsOn(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()
	ch := make(chan askReply, 1)
	p := &pendingAsk{
		resolve:   ch,
		channel:   "feishu",
		chatID:    "c1",
		questions: []bus.AskQuestion{{Question: "q"}},
	}
	p.mu.Lock()
	p.resolved = true
	p.mu.Unlock()
	al.pendingAsks.Store("agent_default:ask:retired", p)

	if al.tryHandleAskReply(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c1", Content: "答案"}, "agent_default:ask:retired") {
		t.Fatal("a retired waiter must not consume or ack the reply")
	}
	select {
	case <-ch:
		t.Fatal("no reply may be delivered to a retired waiter")
	default:
	}
}
