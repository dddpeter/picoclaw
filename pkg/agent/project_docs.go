package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Project docs are workspace-root markdown files (AGENTS.md, README.md, ...)
// injected into the system prompt so the agent knows the project's own
// conventions without being told to read them. READMEs can be huge, so unlike
// the bootstrap files they are capped.
const (
	projectDocMaxBytes  = 6000  // per file
	projectDocsMaxBytes = 12000 // whole section
)

const projectDocTruncatedMarker = "\n\n… (truncated; use read_file for the rest)"

// bootstrapDocNames are workspace files the definition loader already places
// in the prompt; listing them under project_docs must not inject them twice.
var bootstrapDocNames = map[string]bool{
	string(AgentDefinitionSourceAgent): true,
	"SOUL.md":                          true,
	"USER.md":                          true,
	"IDENTITY.md":                      true,
}

// projectDocPaths lists the configured doc files as workspace paths, in config
// order. It feeds both loading and cache-invalidation tracking, so missing
// files are included: creating one must rebuild the prompt.
func (cb *ContextBuilder) projectDocPaths() []string {
	paths := make([]string, 0, len(cb.projectDocs))
	for _, name := range cb.projectDocs {
		name = strings.TrimSpace(name)
		// Bare file names only: project_docs is a doc list at the workspace
		// root, not a path allowlist.
		if name == "" || name != filepath.Base(name) || bootstrapDocNames[name] {
			continue
		}
		paths = append(paths, filepath.Join(cb.workspace, name))
	}
	return uniquePaths(paths)
}

// loadProjectDocs renders the project docs section, or "" when none of the
// configured docs exist. AGENTS.md doubles as the legacy agent definition
// while AGENT.md is absent (see loadAgentDefinition); it is already in the
// prompt then and is skipped here.
func (cb *ContextBuilder) loadProjectDocs() string {
	agentsIsDefinition := !fileExists(filepath.Join(cb.workspace, string(AgentDefinitionSourceAgent)))
	budget := projectDocsMaxBytes
	var sections []string
	for _, path := range cb.projectDocPaths() {
		name := filepath.Base(path)
		if agentsIsDefinition && name == string(AgentDefinitionSourceAgents) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		if budget <= 0 {
			break
		}
		body, truncated := truncateUTF8(body, min(projectDocMaxBytes, budget))
		budget -= len(body)
		if truncated {
			body += projectDocTruncatedMarker
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", name, body))
	}
	if len(sections) == 0 {
		return ""
	}
	return "# Project Docs\n\nThese files live at the workspace root and describe the project's own conventions. Follow them.\n\n" +
		strings.Join(sections, "\n\n")
}

// truncateUTF8 cuts s to at most maxBytes without splitting a rune.
func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]), true
}
