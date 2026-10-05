// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func storeTestImage(t *testing.T, store *media.FileMediaStore, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("payload-"+name), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename:    name,
		ContentType: "image/png",
		Source:      "test:cap_images",
	}, "test:cap_images")
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func decodePNGPayload(t *testing.T, dataURL string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("decode data URL payload: %v", err)
	}
	return string(raw)
}

// encodedPayloads collects the decoded payloads of every data URL across the
// message slice, so tests can assert exactly which images were encoded.
func encodedPayloads(t *testing.T, msgs []providers.Message) []string {
	t.Helper()
	var payloads []string
	for _, m := range msgs {
		for _, dataURL := range m.Media {
			if strings.HasPrefix(dataURL, "data:") {
				payloads = append(payloads, decodePNGPayload(t, dataURL))
			}
		}
	}
	return payloads
}

// TestMediaRefs_CapsCurrentTurnImages pins the fork's context-image cap
// (agentscope-go borrowing §三): beyond maxImages, the OLDEST current-turn
// tool images stay as [image:/path] tags (load_image can bring them back)
// while only the newest maxImages ride into the context as base64.
func TestMediaRefs_CapsCurrentTurnImages(t *testing.T) {
	store := media.NewFileMediaStore()
	refs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		refs = append(refs, storeTestImage(t, store, fmt.Sprintf("img%d.png", i)))
	}

	messages := []providers.Message{
		{Role: "user", Content: "screenshots please"},
		{Role: "tool", Content: "result", ToolCallID: "c1", Media: []string{refs[0], refs[1]}},
		{Role: "tool", Content: "result", ToolCallID: "c2", Media: []string{refs[2]}},
		{Role: "tool", Content: "result", ToolCallID: "c3", Media: []string{refs[3], refs[4]}},
	}

	// Cap at 3: the oldest two (img0, img1) degrade to path tags.
	got := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize, 3, 1)
	payloads := encodedPayloads(t, got)
	if len(payloads) != 3 {
		t.Fatalf("expected cap of 3 base64 images, got %d (%v)", len(payloads), payloads)
	}
	joined := strings.Join(payloads, "\n")
	for _, want := range []string{"payload-img2.png", "payload-img3.png", "payload-img4.png"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("newest image %s must be encoded; encoded set: %v", want, payloads)
		}
	}
	for _, stale := range []string{"payload-img0.png", "payload-img1.png"} {
		if strings.Contains(joined, stale) {
			t.Fatalf("oldest image %s must not be base64-encoded", stale)
		}
	}

	// Dropped images keep their path tags so load_image can still reach them.
	droppedLocal0, _, err := store.ResolveWithMeta(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[1].Content, "[image:"+droppedLocal0+"]") {
		t.Fatalf("dropped image must keep its path tag in the tool message, got %q", got[1].Content)
	}

	// Exactly one synthetic follow-up message carries the encoded images.
	followUps := 0
	for _, m := range got {
		if m.Role == "user" && strings.HasPrefix(m.Content, "[Loaded image from tool result above]") {
			followUps++
		}
	}
	if followUps != 1 {
		t.Fatalf("expected exactly one synthetic image follow-up message, got %d", followUps)
	}

	// Unlimited keeps all five.
	gotAll := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize, -1, 1)
	if all := encodedPayloads(t, gotAll); len(all) != 5 {
		t.Fatalf("unlimited must encode all 5 images, got %d", len(all))
	}
}

// TestMediaRefs_CapIgnoresOversizedImages pins L3: files the encoder would
// skip anyway (over maxSize) must not consume cap slots — otherwise they
// evict encodable older images while contributing nothing to the context.
func TestMediaRefs_CapIgnoresOversizedImages(t *testing.T) {
	store := media.NewFileMediaStore()

	smallA := storeTestImage(t, store, "a.png")
	bigPath := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(bigPath, make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	bigRef, err := store.Store(bigPath, media.MediaMeta{
		Filename:    "big.png",
		ContentType: "image/png",
		Source:      "test:cap_images",
	}, "test:cap_images")
	if err != nil {
		t.Fatal(err)
	}
	smallC := storeTestImage(t, store, "c.png")
	smallD := storeTestImage(t, store, "d.png")

	messages := []providers.Message{
		{Role: "user", Content: "screenshots"},
		{Role: "tool", Content: "result", ToolCallID: "c1", Media: []string{smallA, bigRef, smallC, smallD}},
	}

	const maxSize = 100 // big.png (500B) can never encode; smalls (~25B) can

	// Encodable candidates are [a, c, d]; cap 2 keeps the newest two (c, d)
	// — the oversized ref must not push a out of the candidate list.
	got := resolveMediaRefs(messages, store, maxSize, 2, 1)
	payloads := encodedPayloads(t, got)
	joined := strings.Join(payloads, "\n")
	if len(payloads) != 2 {
		t.Fatalf("expected exactly 2 encoded images, got %d (%v)", len(payloads), payloads)
	}
	for _, want := range []string{"payload-a.png", "payload-c.png", "payload-d.png"} {
		if want == "payload-a.png" {
			if strings.Contains(joined, want) {
				t.Fatal("oldest encodable image must be dropped by the cap")
			}
			continue
		}
		if !strings.Contains(joined, want) {
			t.Fatalf("%s must be encoded (oversized image must not steal its slot)", want)
		}
	}
	// The oversized image keeps its path tag regardless.
	bigLocal, _, err := store.ResolveWithMeta(bigRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[1].Content, "[image:"+bigLocal+"]") {
		t.Fatalf("oversized image must keep its path tag, got %q", got[1].Content)
	}
}
