package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newProjectDocsBuilder(t *testing.T, files map[string]string) (*ContextBuilder, string) {
	t.Helper()
	tmpDir := setupWorkspace(t, files)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	return NewContextBuilder(tmpDir).WithProjectDocs(config.DefaultProjectDocs()), tmpDir
}

func TestProjectDocsInjectedFromWorkspaceRoot(t *testing.T) {
	cb, _ := newProjectDocsBuilder(t, map[string]string{
		"AGENT.md":  "# Agent",
		"AGENTS.md": "Repo rule: run go test before pushing.",
		"README.md": "# Demo\n\nThis project demos project docs.",
	})

	prompt := cb.BuildSystemPrompt()
	for _, want := range []string{"# Project Docs", "## AGENTS.md", "run go test before pushing", "## README.md", "demos project docs"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt should contain %q:\n%s", want, prompt)
		}
	}
	// Ordering: project docs follow the workspace instructions (AGENT.md) and
	// precede the skill catalog / memory.
	if agent, docs := strings.Index(prompt, "## AGENT.md"), strings.Index(prompt, "# Project Docs"); agent < 0 || docs < agent {
		t.Errorf("project docs should render after the AGENT.md section (agent=%d docs=%d)", agent, docs)
	}
}

func TestProjectDocsSkipAgentsMarkdownWhenItIsTheDefinition(t *testing.T) {
	// No AGENT.md: AGENTS.md is the legacy agent definition and is already in
	// the bootstrap section — it must not be injected a second time.
	cb, _ := newProjectDocsBuilder(t, map[string]string{
		"AGENTS.md": "legacy definition body",
		"README.md": "readme body",
	})

	prompt := cb.BuildSystemPrompt()
	if got := strings.Count(prompt, "legacy definition body"); got != 1 {
		t.Errorf("AGENTS.md body should appear exactly once, got %d:\n%s", got, prompt)
	}
	if strings.Contains(prompt, "## AGENTS.md\n\nlegacy") && strings.Contains(prompt, "# Project Docs\n\n") &&
		strings.Index(prompt, "## AGENTS.md") > strings.Index(prompt, "# Project Docs") {
		t.Errorf("AGENTS.md must not render inside the project docs section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## README.md") {
		t.Errorf("README.md should still be injected:\n%s", prompt)
	}
}

func TestProjectDocsDisabledAndMissing(t *testing.T) {
	tmpDir := setupWorkspace(t, map[string]string{"README.md": "readme body"})
	defer os.RemoveAll(tmpDir)

	// Empty list disables the section entirely.
	if prompt := NewContextBuilder(tmpDir).WithProjectDocs(nil).BuildSystemPrompt(); strings.Contains(prompt, "Project Docs") || strings.Contains(prompt, "readme body") {
		t.Errorf("empty project_docs must not inject anything:\n%s", prompt)
	}
	// Configured but absent files render nothing.
	if prompt := NewContextBuilder(tmpDir).WithProjectDocs([]string{"CONTRIBUTING.md"}).BuildSystemPrompt(); strings.Contains(prompt, "Project Docs") {
		t.Errorf("missing docs must not render a section:\n%s", prompt)
	}
	// Bootstrap files and paths are refused as doc names.
	cb := NewContextBuilder(tmpDir).WithProjectDocs([]string{"AGENT.md", "USER.md", "sub/README.md", "  ", "README.md"})
	if got := cb.projectDocPaths(); len(got) != 1 || filepath.Base(got[0]) != "README.md" {
		t.Errorf("projectDocPaths should keep only bare non-bootstrap names, got %v", got)
	}
}

func TestProjectDocsTruncation(t *testing.T) {
	long := strings.Repeat("文档内容。", projectDocMaxBytes) // several times the per-file cap
	cb, _ := newProjectDocsBuilder(t, map[string]string{
		"AGENT.md":  "# Agent",
		"README.md": long,
		"CLAUDE.md": long,
	})

	section := cb.loadProjectDocs()
	if got := strings.Count(section, projectDocTruncatedMarker); got != 2 {
		t.Errorf("both over-long docs should carry the truncation marker, got %d", got)
	}
	if len(section) > projectDocsMaxBytes+2*len(projectDocTruncatedMarker)+400 {
		t.Errorf("section should respect the total budget, got %d bytes", len(section))
	}
	for _, r := range section {
		if r == '\uFFFD' {
			t.Fatal("truncation split a multi-byte rune")
		}
	}
}

func TestProjectDocsChangeInvalidatesPromptCache(t *testing.T) {
	cb, tmpDir := newProjectDocsBuilder(t, map[string]string{
		"AGENT.md":  "# Agent",
		"README.md": "version one",
	})

	sp1 := cb.BuildSystemPromptWithCache()
	if !strings.Contains(sp1, "version one") {
		t.Fatalf("initial prompt missing README:\n%s", sp1)
	}

	readme := filepath.Join(tmpDir, "README.md")
	os.WriteFile(readme, []byte("version two"), 0o644)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(readme, future, future)

	sp2 := cb.BuildSystemPromptWithCache()
	if !strings.Contains(sp2, "version two") {
		t.Errorf("README edit should rebuild the cached prompt:\n%s", sp2)
	}

	// A newly created doc (tracked even while absent) rebuilds as well.
	agents := filepath.Join(tmpDir, "AGENTS.md")
	os.WriteFile(agents, []byte("brand new rules"), 0o644)
	os.Chtimes(agents, future.Add(2*time.Second), future.Add(2*time.Second))
	if sp3 := cb.BuildSystemPromptWithCache(); !strings.Contains(sp3, "brand new rules") {
		t.Errorf("creating AGENTS.md should rebuild the cached prompt:\n%s", sp3)
	}
}
