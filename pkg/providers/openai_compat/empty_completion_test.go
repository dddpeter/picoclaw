package openai_compat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// 空响应检测盲区回归（fork 2026-10-05）：网关截断超限 prompt 时返回
// finish_reason="length"/"stop" + 空内容，是「合法的 200」，此前流式只在
// finish_reason=="" 时判空、非流式完全不判，长重任务里直接漏成静默空答复
// （defaultResponse 报错）。

func newEmptyStreamServer(t *testing.T, finishReason string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"" + finishReason + "\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
}

func assertEmptyCompletionError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected EmptyCompletionError, got nil")
	}
	var empty *common.EmptyCompletionError
	if !errors.As(err, &empty) {
		t.Fatalf("error = %T, want *common.EmptyCompletionError: %v", err, err)
	}
}

func TestProviderChatStream_EmptyWithFinishReasonLength(t *testing.T) {
	server := newEmptyStreamServer(t, "length")
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	_, err := p.ChatStream(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil, nil)
	assertEmptyCompletionError(t, err)
}

func TestProviderChatStream_EmptyWithFinishReasonStop(t *testing.T) {
	// finish_reason="stop" + 空 payload：旧代码在此处直接放行成合法空答复。
	server := newEmptyStreamServer(t, "stop")
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	_, err := p.ChatStream(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil, nil)
	assertEmptyCompletionError(t, err)
}

func TestProviderChatStream_WhitespaceOnlyContentIsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\n\\n\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	_, err := p.ChatStream(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil, nil)
	assertEmptyCompletionError(t, err)
}

func TestProviderChat_NonStreamEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil)
	assertEmptyCompletionError(t, err)
}

func TestProviderChat_NonStreamWhitespaceOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  \n"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil)
	assertEmptyCompletionError(t, err)
}

func TestProviderChatStream_ContentfulResponseNotAffected(t *testing.T) {
	// 正向对照：有内容的正常流不受新判定影响。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"正常回答\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	out, err := p.ChatStream(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil, nil)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if out.Content != "正常回答" {
		t.Fatalf("Content = %q", out.Content)
	}
}

func TestProviderChat_NonStreamToolCallsOnlyNotAffected(t *testing.T) {
	// 只有 tool_calls 没有正文是合法形态（模型请求执行工具），不得判空。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"exec","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	out, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "test-model", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "exec" {
		t.Fatalf("tool calls lost: %+v", out.ToolCalls)
	}
}
