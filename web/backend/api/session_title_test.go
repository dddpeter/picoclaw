package api

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestBuildSessionListItemPrefersStoredTitle(t *testing.T) {
	sess := sessionFile{
		Key: "k",
		Messages: []providers.Message{
			{Role: "user", Content: "很长的第一条用户消息，会被截断作为预览"},
			{Role: "assistant", Content: "回复"},
		},
		Title:   "存储的标题",
		Created: time.Now(),
		Updated: time.Now(),
	}
	item := buildSessionListItem("id", sess, 200)
	if item.Title != "存储的标题" {
		t.Fatalf("title = %q, want stored title", item.Title)
	}
	if item.Preview == "存储的标题" || item.Preview == "" {
		t.Fatalf("preview should stay independent of title: %q", item.Preview)
	}
}

func TestBuildSessionListItemFallsBackToPreview(t *testing.T) {
	sess := sessionFile{
		Key: "k",
		Messages: []providers.Message{
			{Role: "user", Content: "没有标题时的预览来源"},
		},
		Created: time.Now(),
		Updated: time.Now(),
	}
	item := buildSessionListItem("id", sess, 200)
	if item.Title != "没有标题时的预览来源" {
		t.Fatalf("title fallback = %q", item.Title)
	}
}
