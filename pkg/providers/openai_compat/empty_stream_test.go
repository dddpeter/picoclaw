// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package openai_compat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// TestProviderChatStream_EmptyStreamReturnsError pins the empty-stream guard
// (fork, 2026-10-04): a stream that ends with no content, no tool calls, no
// reasoning, and no finish_reason — the typical shape of an upstream
// silently dropping an over-limit prompt — must surface as
// common.EmptyCompletionError so the fallback chain rotates candidates,
// instead of becoming a silent empty answer.
func TestProviderChatStream_EmptyStreamReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Heartbeat-only stream: choices present but with empty deltas and
		// no finish_reason, then [DONE].
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	out, err := p.ChatStream(
		t.Context(),
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		"gpt-4o",
		nil,
		nil,
	)
	if err == nil {
		t.Fatalf("ChatStream() with empty stream must return an error, got response %+v", out)
	}
	var emptyErr *common.EmptyCompletionError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("error = %T (%v), want *common.EmptyCompletionError", err, err)
	}
}

// Superseded (fork, 2026-10-05): TestProviderChatStream_LegitimateEmpty
// ContentStillSucceeds used to pin "explicit finish_reason + empty payload =
// legitimate empty answer". That assumption was wrong in the field: the agent
// converts ANY empty final answer into the generic defaultResponse error
// message, so no consumer ever benefits from an empty non-error response,
// while aggregated gateways deliver exactly this shape (finish_reason
// "length"/"stop" + empty payload) when they silently drop an over-limit
// prompt during long heavy turns. All-empty payloads now surface as
// EmptyCompletionError regardless of finish_reason — see
// empty_completion_test.go for the pinned semantics.
