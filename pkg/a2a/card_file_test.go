package a2a

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeWorkspacePersona builds a fake workspace with the given persona files.
func writeWorkspacePersona(t *testing.T, agentMD, soulMD string) string {
	t.Helper()
	dir := t.TempDir()
	if agentMD != "" {
		if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(agentMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if soulMD != "" {
		if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte(soulMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDeriveDefaultCardSpecFromFrontmatter(t *testing.T) {
	ws := writeWorkspacePersona(t,
		"---\nname: 虾酱\ndescription: 部署增强版鲎，专注 picoclaw 开发\n---\n\n正文不影响 card。\n",
		"# Soul\n正文兜底用，不应被读到。\n")

	spec := DeriveDefaultCardSpec(ws)
	if spec.Name != "虾酱" {
		t.Errorf("name = %q, want 虾酱", spec.Name)
	}
	if spec.Description != "部署增强版鲎，专注 picoclaw 开发" {
		t.Errorf("description = %q", spec.Description)
	}
}

func TestDeriveDefaultCardSpecSoulFallback(t *testing.T) {
	// AGENT.md without description → first meaningful SOUL.md line wins
	// (headings and blanks are structure, not identity).
	ws := writeWorkspacePersona(t,
		"---\nname: 小爱马\n---\n\nbody\n",
		"# 人格\n\n第一段是身份描述。\n第二段不是。\n")

	spec := DeriveDefaultCardSpec(ws)
	if spec.Name != "小爱马" {
		t.Errorf("name = %q, want 小爱马", spec.Name)
	}
	if spec.Description != "第一段是身份描述。" {
		t.Errorf("description = %q, want SOUL.md first line", spec.Description)
	}
}

func TestDeriveDefaultCardSpecGenericFallback(t *testing.T) {
	// No workspace / no persona files at all → generic identity.
	for _, ws := range []string{"", t.TempDir()} {
		spec := DeriveDefaultCardSpec(ws)
		if spec.Name != "PicoClaw" || spec.Description != "PicoClaw personal AI agent" {
			t.Errorf("ws=%q → %+v, want generic identity", ws, spec)
		}
	}
}

func TestEnsureCardFileGeneratesOnce(t *testing.T) {
	ws := writeWorkspacePersona(t,
		"---\nname: 虾酱\ndescription: 人格描述\n---\n", "")
	path := filepath.Join(t.TempDir(), CardFileName)

	// First call: generate from persona.
	if err := EnsureCardFile(path, ws); err != nil {
		t.Fatalf("EnsureCardFile: %v", err)
	}
	spec, ok, err := LoadCardSpec(path)
	if err != nil || !ok {
		t.Fatalf("spec not written: ok=%v err=%v", ok, err)
	}
	if spec.Name != "虾酱" || spec.Description != "人格描述" {
		t.Errorf("generated spec = %+v", spec)
	}

	// Second call with a DIFFERENT persona: existing file must win
	// (user edits are never clobbered by regeneration).
	if err := EnsureCardFile(path, t.TempDir()); err != nil {
		t.Fatalf("EnsureCardFile (existing): %v", err)
	}
	spec2, ok, err := LoadCardSpec(path)
	if err != nil || !ok || spec2.Name != "虾酱" {
		t.Errorf("existing spec overwritten: ok=%v spec=%+v err=%v", ok, spec2, err)
	}
}

func TestCardSpecRoundtripWithURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), CardFileName)
	in := CardSpec{Name: "爱老师", Description: "远端网关", URL: "https://a2a.example.com"}
	if err := SaveCardSpec(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, ok, err := LoadCardSpec(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if *out != in {
		t.Errorf("roundtrip = %+v, want %+v", *out, in)
	}

	// URL override must flow into the served card (fixes the undialable
	// 0.0.0.0 listen-addr fallback for NAT/proxy deployments).
	card := NewAgentCard(*out, "http://0.0.0.0:18790", true)
	if card.URL != "https://a2a.example.com" {
		t.Errorf("card url = %q, want the spec override", card.URL)
	}
}

func TestLoadCardSpecMissing(t *testing.T) {
	spec, ok, err := LoadCardSpec(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || ok || spec != nil {
		t.Errorf("missing file → spec=%v ok=%v err=%v, want nil/false/nil", spec, ok, err)
	}
}

func TestReloadableCardHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CardFileName)
	if err := os.WriteFile(path, []byte(`{"name":"旧名","description":"d"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := NewReloadableCard(path, "http://127.0.0.1:1", false)

	if got := rc.Card().Name; got != "旧名" {
		t.Fatalf("initial card name = %q, want 旧名", got)
	}

	// Rewrite the file and force a distinct mtime (some filesystems have
	// coarse mtime granularity — same-second writes would false-negative).
	if err := os.WriteFile(path, []byte(`{"name":"新名","description":"d"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if got := rc.Card().Name; got != "新名" {
		t.Errorf("after edit card name = %q, want 新名 (mtime hot reload)", got)
	}

	// Deleting the file must NOT regress to the generic default.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := rc.Card().Name; got != "新名" {
		t.Errorf("after delete card name = %q, want 新名 (last known wins)", got)
	}
}

func TestReloadableCardURLAndAuthFlowThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CardFileName)
	if err := os.WriteFile(path, []byte(`{"name":"x","description":"y","url":"https://pub.example"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	card := NewReloadableCard(path, "http://0.0.0.0:18790", true).Card()
	if card.URL != "https://pub.example" {
		t.Errorf("url = %q, want spec override", card.URL)
	}
	if len(card.SecuritySchemes) == 0 {
		t.Error("withAuth=true must advertise bearer")
	}

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	NewReloadableCard(path, "http://0.0.0.0:18790", true).Handler()(rec, req)
	var back AgentCard
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil || back.Name != "x" {
		t.Errorf("handler body decode: err=%v card=%+v", err, back)
	}
}
