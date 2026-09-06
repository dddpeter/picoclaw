package agent

import (
	"strings"
	"testing"
)

// TestIdentityPromptIncludesUnpromptedContentRule locks in the behavior rule
// for bare input: content sent without instructions must get a substantive
// read-out plus a follow-up question, not a bare acknowledgement.
func TestIdentityPromptIncludesUnpromptedContentRule(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	identity := cb.getIdentity(true)
	if !strings.Contains(identity, "Unprompted content") {
		t.Fatalf("identity prompt should contain the unprompted-content rule:\n%s", identity)
	}
	if !strings.Contains(identity, "never reply with a bare acknowledgement") {
		t.Fatalf("identity prompt should forbid bare acknowledgements:\n%s", identity)
	}

	identityNoTools := cb.getIdentity(false)
	if !strings.Contains(identityNoTools, "Unprompted content") {
		t.Fatal("unprompted-content rule should be present regardless of tool-use rule")
	}
}
