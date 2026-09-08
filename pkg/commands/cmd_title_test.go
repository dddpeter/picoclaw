package commands

import (
	"context"
	"strings"
	"testing"
)

func titleTestRuntime() *Runtime {
	var stored string
	return &Runtime{
		SetSessionTitle: func(title string) bool {
			stored = title
			return true
		},
		GetSessionTitle: func() (string, string, bool) {
			if stored == "" {
				return "", "", false
			}
			return stored, "user", true
		},
	}
}

func TestTitleCommandSets(t *testing.T) {
	def := titleCommand()
	rt := titleTestRuntime()
	if err := def.Handler(context.Background(), Request{
		Text:  "/title 部署巡检手册",
		Reply: func(string) error { return nil },
	}, rt); err != nil {
		t.Fatalf("handler: %v", err)
	}
	title, _, ok := rt.GetSessionTitle()
	if !ok || title != "部署巡检手册" {
		t.Fatalf("stored title = %q (ok=%v)", title, ok)
	}
}

func TestTitleCommandShowsCurrent(t *testing.T) {
	def := titleCommand()
	rt := titleTestRuntime()
	var replyText string
	show := Request{Text: "/title", Reply: func(text string) error { replyText = text; return nil }}

	if err := def.Handler(context.Background(), show, rt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replyText, "no title") {
		t.Fatalf("empty-state reply wrong: %q", replyText)
	}

	if err := def.Handler(context.Background(), Request{
		Text:  "/title 手册",
		Reply: func(string) error { return nil },
	}, rt); err != nil {
		t.Fatal(err)
	}
	if err := def.Handler(context.Background(), show, rt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replyText, "手册") || !strings.Contains(replyText, "user") {
		t.Fatalf("show reply wrong: %q", replyText)
	}
}

func TestTitleCommandUnavailableWithoutRuntimeCapability(t *testing.T) {
	def := titleCommand()
	reply := ""
	err := def.Handler(context.Background(), Request{
		Text:  "/title x",
		Reply: func(text string) error { reply = text; return nil },
	}, &Runtime{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(reply, "unavailable") {
		t.Fatalf("expected unavailable reply, got %q", reply)
	}
}
