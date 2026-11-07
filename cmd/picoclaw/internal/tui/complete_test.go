package tui

import (
	"strings"
	"testing"
)

func TestCompletion_ServerCommandTrigger(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, "/sw")
	if cs.kind != '/' {
		t.Fatalf("kind = %c, want /", cs.kind)
	}
	if !cs.active() {
		t.Fatalf("popup should be active for /sw")
	}
	repl := cs.accept()
	if !strings.HasPrefix(repl, "/sw") {
		t.Fatalf("top suggestion %q should extend /sw", repl)
	}
}

func TestCompletion_LocalCommandTrigger(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, ":co")
	if cs.kind != ':' || !cs.active() {
		t.Fatalf("local completion should activate for :co")
	}
	if repl := cs.accept(); repl != ":copy" && repl != ":context" && repl != ":clear" {
		t.Fatalf("unexpected top suggestion %q", repl)
	}
}

func TestCompletion_NotTriggeredInProse(t *testing.T) {
	cases := []string{
		"",
		"看下 /path/to/file",
		"hello",
		"/status extra args", // past the command token
		":stop now",
	}
	for _, text := range cases {
		var cs completionState
		updateCompletion(&cs, text)
		if cs.active() {
			t.Fatalf("popup should stay closed for %q", text)
		}
	}
}

func TestCompletion_LeadingSpaceStillTriggers(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, "  /sw")
	if cs.kind != '/' {
		t.Fatalf("leading-space text should still trigger, kind=%d", cs.kind)
	}
}

func TestCompletion_MoveAndAccept(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, ":")
	first := cs.accept()
	if !cs.move(1) {
		t.Fatalf("move should change selection")
	}
	second := cs.accept()
	if first == second {
		t.Fatalf("selection should have moved: %q", first)
	}
	if !cs.move(-1) {
		t.Fatalf("move back should change selection")
	}
	if cs.accept() != first {
		t.Fatalf("should be back at first: %q vs %q", cs.accept(), first)
	}
}

func TestCompletion_EscDismissSticky(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, "/sw")
	if !cs.active() {
		t.Fatal("active")
	}
	cs.dismissed = true
	if cs.active() {
		t.Fatal("dismissed popup must not render")
	}
	// Typing more keeps it dismissed.
	updateCompletion(&cs, "/swi")
	if cs.active() {
		t.Fatal("still dismissed within the same prefix mode")
	}
	// Leaving the prefix mode re-arms.
	updateCompletion(&cs, "hello /sw")
	if cs.active() {
		t.Fatal("prose must not trigger")
	}
	updateCompletion(&cs, "/sw")
	if !cs.active() {
		t.Fatal("re-entering the prefix mode re-arms the popup")
	}
}

func TestCompletion_SubCommandCompletion(t *testing.T) {
	var cs completionState
	updateCompletion(&cs, "/switch")
	found := false
	for _, row := range cs.visibleRows() {
		if row.Replace == "/switch model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/switch should complete its 'model' subcommand, got %v", cs.visibleRows())
	}
}
