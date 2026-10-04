// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
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
