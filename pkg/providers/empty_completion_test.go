// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// TestClassifyError_EmptyCompletion pins the empty-completion classification
// (fork, 2026-10-04): a 200 response with zero choices (or an empty stream)
// must be classified as FailoverOverloaded — retriable and fallback-eligible
// — so the fallback chain rotates candidates instead of the turn ending in a
// silent empty answer.
func TestClassifyError_EmptyCompletion(t *testing.T) {
	err := &common.EmptyCompletionError{}
	failErr := ClassifyError(err, "test-provider", "test-model")
	if failErr == nil {
		t.Fatal("ClassifyError() = nil, want FailoverError")
	}
	if failErr.Reason != FailoverOverloaded {
		t.Fatalf("Reason = %q, want %q", failErr.Reason, FailoverOverloaded)
	}
	if !failErr.IsRetriable() {
		t.Fatal("EmptyCompletionError must be retriable (fallback-eligible)")
	}
	if failErr.Provider != "test-provider" || failErr.Model != "test-model" {
		t.Fatalf("Provider/Model = %q/%q, want test-provider/test-model",
			failErr.Provider, failErr.Model)
	}
}

// TestFallbackExhaustedError_UnwrapEmptyCompletion pins the aggregate error's
// chain exposure (fork, 2026-10-04): when every fallback candidate returned
// an EmptyCompletionError, errors.As must see through FallbackExhaustedError
// (via Unwrap -> errors.Join of the per-attempt errors) so CallLLM's
// compression recovery can route it — previously the chain stopped at the
// aggregate and classification fell back to string-matching its message.
func TestFallbackExhaustedError_UnwrapEmptyCompletion(t *testing.T) {
	inner := &common.EmptyCompletionError{}
	exhausted := &FallbackExhaustedError{
		Attempts: []FallbackAttempt{
			{Provider: "p1", Model: "m1", Error: ClassifyError(inner, "p1", "m1"), Reason: FailoverOverloaded},
			{Provider: "p2", Model: "m2", Error: ClassifyError(inner, "p2", "m2"), Reason: FailoverOverloaded},
		},
	}

	var emptyErr *common.EmptyCompletionError
	if !errors.As(exhausted, &emptyErr) {
		t.Fatal("errors.As must see EmptyCompletionError through FallbackExhaustedError")
	}

	// The structured chain replaces message string-matching: the first
	// attempt's classified FailoverError is returned directly.
	failErr := ClassifyError(exhausted, "", "")
	if failErr == nil {
		t.Fatal("ClassifyError() = nil for aggregated empty completions")
	}
	if failErr.Reason != FailoverOverloaded {
		t.Fatalf("Reason = %q, want %q from the structured chain", failErr.Reason, FailoverOverloaded)
	}
	if !failErr.IsRetriable() {
		t.Fatal("aggregated empty completions must stay retriable")
	}
}
