package tui

import (
	"testing"

	pico "github.com/sipeed/picoclaw/pkg/channels/pico"
	"github.com/sipeed/picoclaw/pkg/picoclient"
)

func msg(t string, payload map[string]any) picoclient.Event {
	return picoclient.Decode(pico.PicoMessage{Type: t, Payload: payload})
}

func TestState_TurnLifecycle(t *testing.T) {
	s := NewState()

	if !s.Apply(msg(pico.TypeTypingStart, nil)) || !s.Generating {
		t.Fatal("typing.start must enter generating")
	}
	if !s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "t1", "content": "思考", "kind": "thought",
	})) {
		t.Fatal("thought create must change state")
	}
	if !s.Apply(msg(pico.TypeMessageUpdate, map[string]any{
		"message_id": "t1", "content": "思考更多",
	})) {
		t.Fatal("thought update must change state")
	}
	if len(s.Items) != 1 || s.Items[0].Kind != ItemThought || s.Items[0].Content != "思考更多" || !s.Items[0].Streaming {
		t.Fatalf("thought item wrong: %+v", s.Items)
	}

	// Answer create closes the thought stream and appends the answer.
	if !s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "a1", "content": "答案", "model_name": "glm-4.7",
	})) {
		t.Fatal("answer create must change state")
	}
	if s.Items[0].Streaming {
		t.Fatal("thought must stop streaming once the answer starts")
	}
	if len(s.Items) != 2 || s.Items[1].Kind != ItemAnswer || s.ModelName != "glm-4.7" {
		t.Fatalf("answer item wrong: %+v", s.Items)
	}
	if s.Items[1].Content != "答案" {
		t.Fatalf("answer content = %q", s.Items[1].Content)
	}

	// Streaming updates replace the whole content on the same item.
	if !s.Apply(msg(pico.TypeMessageUpdate, map[string]any{
		"message_id": "a1", "content": "答案全文",
	})) {
		t.Fatal("answer update must change state")
	}
	if len(s.Items) != 2 || s.Items[1].Content != "答案全文" {
		t.Fatalf("answer update landed wrong: %+v", s.Items)
	}

	if !s.Apply(msg(pico.TypeTypingStop, nil)) || s.Generating {
		t.Fatal("typing.stop must leave generating")
	}
	if s.Items[1].Streaming {
		t.Fatal("answer must stop streaming at typing.stop")
	}
}

func TestState_ProgressNoteNeverEntersTimeline(t *testing.T) {
	s := NewState()
	if !s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "p1", "content": "⏱ 3m 无新进展", "kind": "progress_note",
	})) {
		t.Fatal("progress note should refresh the progress line")
	}
	if len(s.Items) != 0 || s.Progress == "" {
		t.Fatalf("progress note leaked into timeline: items=%d progress=%q", len(s.Items), s.Progress)
	}
	// And it must not clear generating state (web 假完成 bug 的终端侧对应项)。
	s.Apply(msg(pico.TypeTypingStart, nil))
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "p2", "content": "⏱ 6m", "kind": "progress_note",
	}))
	if !s.Generating {
		t.Fatal("progress note must not end the turn")
	}
}

func TestState_ToolCallsAndFeedback(t *testing.T) {
	s := NewState()
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "tc1", "kind": "tool_calls", "content": "计划",
		"tool_calls": []any{
			map[string]any{"id": "c1", "function": map[string]any{"name": "exec", "arguments": `{"command":"ls"}`}},
		},
	}))
	if len(s.Items) != 1 || s.Items[0].Kind != ItemToolCalls || s.Items[0].ToolName != "exec" {
		t.Fatalf("tool call item wrong: %+v", s.Items)
	}

	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "f1", "content": "🔧 `exec` 运行中…",
	}))
	s.Apply(msg(pico.TypeMessageUpdate, map[string]any{
		"message_id": "f1", "content": "🔧 `exec` 完成",
	}))
	if len(s.Items) != 2 || s.Items[1].Kind != ItemToolFeedback || s.Items[1].Content != "🔧 `exec` 完成" {
		t.Fatalf("tool feedback items wrong: %+v", s.Items)
	}
}

func TestState_ErrorRemovesPendingAndEndsTurn(t *testing.T) {
	s := NewState()
	s.AddUser("你好")
	if len(s.Items) != 1 || s.Items[0].Kind != ItemUser {
		t.Fatalf("user item wrong: %+v", s.Items)
	}
	pendingID := s.Items[0].ID

	s.Apply(msg(pico.TypeTypingStart, nil))
	s.Apply(msg(pico.TypeError, map[string]any{
		"code": "llm_failed", "message": "boom", "request_id": pendingID,
	}))
	if len(s.Items) != 1 || s.Items[0].Kind != ItemError {
		t.Fatalf("error handling wrong: %+v", s.Items)
	}
	if s.Items[0].Code != "llm_failed" || s.Items[0].ErrMessage != "boom" {
		t.Fatalf("error fields wrong: %+v", s.Items[0])
	}
	if s.Generating {
		t.Fatal("error must end generating")
	}
}

func TestState_DeleteRemovesMessage(t *testing.T) {
	s := NewState()
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "a1", "content": "将被取消的流",
	}))
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "a2", "content": "保留",
	}))
	if !s.Apply(msg(pico.TypeMessageDelete, map[string]any{"message_id": "a1"})) {
		t.Fatal("delete must change state")
	}
	if len(s.Items) != 1 || s.Items[0].ID != "a2" {
		t.Fatalf("delete landed wrong: %+v", s.Items)
	}
}

func TestState_UserSteeringBadge(t *testing.T) {
	s := NewState()
	s.AddUser("先说这个")
	s.Apply(msg(pico.TypeTypingStart, nil))
	s.AddUser("补充一点")
	if s.Items[0].Steering {
		t.Fatal("first message is not steering")
	}
	if !s.Items[1].Steering {
		t.Fatal("message sent during an active turn must be marked steering")
	}
}

func TestState_ToggleLastThought(t *testing.T) {
	s := NewState()
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "t1", "content": "想", "kind": "thought",
	}))
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "t2", "content": "再想", "kind": "thought",
	}))
	if !s.ToggleLastThought() || s.Items[0].Expanded || !s.Items[1].Expanded {
		t.Fatal("toggle must flip only the latest thought")
	}
	if !s.ToggleLastThought() || s.Items[1].Expanded {
		t.Fatal("second toggle must collapse it back")
	}
}

func TestState_UsageAndContextCapture(t *testing.T) {
	s := NewState()
	s.Apply(msg(pico.TypeMessageUpdate, map[string]any{
		"message_id": "a1", "content": "终帧",
		"model_name": "glm-4.7",
		"context_usage": map[string]any{
			"used_tokens": 1000, "total_tokens": 8000, "used_percent": 12.5,
		},
		"usage": map[string]any{"input_tokens": 900, "output_tokens": 100},
	}))
	if s.ModelName != "glm-4.7" || s.CtxUsage == nil || s.CtxUsage.UsedTokens != 1000 || s.Usage == nil || s.Usage.OutputTokens != 100 {
		t.Fatalf("usage capture wrong: model=%q ctx=%+v usage=%+v", s.ModelName, s.CtxUsage, s.Usage)
	}
}

func TestState_ModelFromCommandReplyLine(t *testing.T) {
	// 命令回复（如 /status）不带 model_name payload，footer 的模型 id 只能
	// 从 "Model: <id>" 行提取。
	s := NewState()
	s.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "st1",
		"content":    "📊 Status\nVersion: v0.4.0\nModel: cbcn/deepseek-v4.1-flash (openai)\nChannels: pico, feishu",
	}))
	if s.ModelName != "cbcn/deepseek-v4.1-flash" {
		t.Fatalf("model from /status reply = %q", s.ModelName)
	}

	// payload 的 model_name 优先于文本行（真实回合答复不受影响）。
	s2 := NewState()
	s2.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "a1", "content": "Model: stale-model", "model_name": "live-model",
	}))
	if s2.ModelName != "live-model" {
		t.Fatalf("payload model_name must win: %q", s2.ModelName)
	}

	// 普通正文里没有 Model: 行时不得改动模型。
	s3 := NewState()
	s3.ModelName = "keep-me"
	s3.Apply(msg(pico.TypeMessageCreate, map[string]any{
		"message_id": "a2", "content": "正常回答内容，不含模型信息",
	}))
	if s3.ModelName != "keep-me" {
		t.Fatalf("plain content must not touch model: %q", s3.ModelName)
	}
}
