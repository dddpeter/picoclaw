package agent

import (
	"strings"
	"testing"
)

// /learn interception tests pin the fork behavior: "/learn <source>" is
// rewritten into a full skill-authoring turn (borrowed from hermes-agent,
// docs/design/hermes-borrowing-analysis.zh.md §三) instead of being answered
// inline. Behavior mirrors the /use interception pattern.

func TestApplyLearnCommand_MatchesAndRewritesMessage(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	opts := &processOptions{
		SessionKey:  "agent:main:test",
		UserMessage: "/learn how we deployed the gateway",
	}
	matched, handled, reply := al.applyLearnCommand(opts.UserMessage, agent, opts)
	if !matched {
		t.Fatal("expected /learn to match")
	}
	if handled {
		t.Fatal("expected /learn to fall through into normal agent execution")
	}
	if reply != "" {
		t.Fatalf("passthrough must not carry a reply, got %q", reply)
	}
	if opts.UserMessage == "/learn how we deployed the gateway" {
		t.Fatal("user message must be rewritten into the authoring prompt")
	}
	if opts.Dispatch.UserMessage != opts.UserMessage {
		t.Fatal("dispatch user message must match the rewritten prompt")
	}
	for _, want := range []string{"SKILL.md", "## Verification", "how we deployed the gateway"} {
		if !strings.Contains(opts.UserMessage, want) {
			t.Fatalf("rewritten prompt missing %q", want)
		}
	}
}

func TestApplyLearnCommand_WorkspaceSubstituted(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	opts := &processOptions{SessionKey: "agent:main:test", UserMessage: "/learn anything"}
	matched, handled, _ := al.applyLearnCommand(opts.UserMessage, agent, opts)
	if !matched || handled {
		t.Fatalf("expected passthrough match, matched=%v handled=%v", matched, handled)
	}
	if strings.Contains(opts.UserMessage, "<workspace>") {
		t.Fatal("workspace placeholder must be substituted with the real workspace")
	}
	if !strings.Contains(opts.UserMessage, agent.Workspace) {
		t.Fatalf("prompt should reference the agent workspace %q", agent.Workspace)
	}
}

func TestApplyLearnCommand_BareCommandShowsUsage(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.GetRegistry().GetDefaultAgent()
	opts := &processOptions{SessionKey: "agent:main:test", UserMessage: "/learn"}
	matched, handled, reply := al.applyLearnCommand(opts.UserMessage, agent, opts)
	if !matched || !handled {
		t.Fatalf("bare /learn must be handled inline, matched=%v handled=%v", matched, handled)
	}
	if !strings.Contains(reply, "Usage: /learn") {
		t.Fatalf("expected usage reply, got %q", reply)
	}
	if opts.UserMessage == "" && opts.Dispatch.UserMessage != "" {
		t.Fatal("usage path must not mutate the pending message")
	}
}

func TestApplyLearnCommand_NonLearnUntouched(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.GetRegistry().GetDefaultAgent()
	opts := &processOptions{SessionKey: "agent:main:test", UserMessage: "what is /learning curve"}
	matched, _, _ := al.applyLearnCommand(opts.UserMessage, agent, opts)
	if matched {
		t.Fatal("plain messages mentioning learn must not match")
	}
	if opts.UserMessage != "what is /learning curve" {
		t.Fatal("non-learn message must stay untouched")
	}
}
