package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/logger"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)

const (
	MaxNameLength        = 64
	MaxDescriptionLength = 1024
)

type SkillMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// DisableModelInvocation mirrors the Agent Skills spec frontmatter flag
	// (agentskills.io): such skills stay out of the model-facing catalog and
	// are only invocable explicitly (e.g. /use).
	DisableModelInvocation bool `json:"disable_model_invocation"`
}

type SkillInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Source      string `json:"source"`
	Description string `json:"description"`
	// DisableModelInvocation hides the skill from the prompt catalog; explicit
	// invocation (/use, agents[].skills, turn profiles) keeps working.
	DisableModelInvocation bool `json:"disable_model_invocation"`
}

func (info SkillInfo) validate() error {
	var errs error
	if info.Name == "" {
		errs = errors.Join(errs, errors.New("name is required"))
	} else {
		if err := ValidateSkillName(info.Name); err != nil {
			errs = errors.Join(errs, err)
		}
	}

	if info.Description == "" {
		errs = errors.Join(errs, errors.New("description is required"))
	} else if len(info.Description) > MaxDescriptionLength {
		errs = errors.Join(errs, fmt.Errorf("description exceeds %d character", MaxDescriptionLength))
	}
	return errs
}

// Skill provenance labels, surfaced in the prompt catalog and the web/CLI
// listings.
const (
	SourceWorkspace = "workspace" // <workspace>/skills (installer target)
	SourceProject   = "project"   // <workspace>/.skills (hand-managed, project-level)
	SourceGlobal    = "global"    // ~/.picoclaw/skills and ~/.agents/skills
	SourceBuiltin   = "builtin"
)

// SkillRoot is one skill directory together with its provenance label.
type SkillRoot struct {
	Dir    string
	Source string
}

type SkillsLoader struct {
	workspace string
	roots     []SkillRoot // resolution priority order, no empties or duplicates
}

// ResolveSkillRoots lists the skill directories in resolution priority order:
// <workspace>/skills > <workspace>/.skills > globalSkills > <home>/.agents/skills
// > builtinSkills. Empty and duplicate directories are dropped. home is the
// user's home directory, not PICOCLAW_HOME: ~/.agents/skills is the
// cross-tool skills convention shared with other coding agents, so it must not
// move with picoclaw's own config root.
func ResolveSkillRoots(workspace, globalSkills, builtinSkills, home string) []SkillRoot {
	candidates := []SkillRoot{
		{Dir: filepath.Join(workspace, "skills"), Source: SourceWorkspace},
		{Dir: filepath.Join(workspace, ".skills"), Source: SourceProject},
		{Dir: globalSkills, Source: SourceGlobal},
	}
	if home = strings.TrimSpace(home); home != "" {
		candidates = append(candidates, SkillRoot{Dir: filepath.Join(home, ".agents", "skills"), Source: SourceGlobal})
	}
	candidates = append(candidates, SkillRoot{Dir: builtinSkills, Source: SourceBuiltin})

	seen := make(map[string]struct{}, len(candidates))
	roots := make([]SkillRoot, 0, len(candidates))
	for _, root := range candidates {
		trimmed := strings.TrimSpace(root.Dir)
		if trimmed == "" {
			continue
		}
		clean := filepath.Clean(trimmed)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, SkillRoot{Dir: clean, Source: root.Source})
	}
	return roots
}

// SkillRoots returns all unique skill root directories used by this loader,
// in resolution priority order.
func (sl *SkillsLoader) SkillRoots() []string {
	out := make([]string, 0, len(sl.roots))
	for _, root := range sl.roots {
		out = append(out, root.Dir)
	}
	return out
}

// NewSkillsLoader builds a loader over the standard root set (see
// ResolveSkillRoots), deriving the cross-tool ~/.agents/skills root from the
// user's home directory.
func NewSkillsLoader(workspace string, globalSkills string, builtinSkills string) *SkillsLoader {
	home, _ := os.UserHomeDir()
	return NewSkillsLoaderFromRoots(workspace, ResolveSkillRoots(workspace, globalSkills, builtinSkills, home))
}

// NewSkillsLoaderFromRoots builds a loader over an explicit root list (tests,
// or callers with a non-standard layout). Roots are searched in order.
func NewSkillsLoaderFromRoots(workspace string, roots []SkillRoot) *SkillsLoader {
	return &SkillsLoader{
		workspace: workspace,
		roots:     roots,
	}
}

func (sl *SkillsLoader) ListSkills() []SkillInfo {
	skills := make([]SkillInfo, 0)
	seen := make(map[string]bool)

	for _, root := range sl.roots {
		dirs, err := os.ReadDir(root.Dir)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			skillFile := filepath.Join(root.Dir, d.Name(), "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			info := SkillInfo{
				Name:   d.Name(),
				Path:   skillFile,
				Source: root.Source,
			}
			metadata := sl.getSkillMetadata(skillFile)
			if metadata != nil {
				info.Description = clampDescription(metadata.Description)
				info.Name = metadata.Name
				info.DisableModelInvocation = metadata.DisableModelInvocation
			}
			if err := info.validate(); err != nil {
				slog.Warn("invalid skill from "+root.Source, "name", info.Name, "error", err)
				continue
			}
			if seen[info.Name] {
				continue
			}
			seen[info.Name] = true
			skills = append(skills, info)
		}
	}

	return skills
}

func (sl *SkillsLoader) LoadSkill(name string) (string, bool) {
	if err := ValidateSkillName(name); err != nil {
		return "", false
	}

	for _, root := range sl.roots {
		skillFile := filepath.Join(root.Dir, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	return "", false
}

func (sl *SkillsLoader) LoadSkillsForContext(skillNames []string) string {
	if len(skillNames) == 0 {
		return ""
	}

	var parts []string
	for _, name := range skillNames {
		content, ok := sl.LoadSkill(name)
		if ok {
			parts = append(parts, fmt.Sprintf("### Skill: %s\n\n%s", name, content))
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func (sl *SkillsLoader) BuildSkillsSummary() string {
	allSkills := sl.ListSkills()
	if len(allSkills) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "<skills>")
	for _, s := range allSkills {
		// Skills flagged disable-model-invocation stay out of the model-facing
		// catalog; only explicit invocation (e.g. /use) can load them.
		if s.DisableModelInvocation {
			continue
		}
		escapedName := escapeXML(s.Name)
		escapedDesc := escapeXML(s.Description)
		escapedPath := escapeXML(s.Path)

		lines = append(lines, fmt.Sprintf("  <skill>"))
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapedName))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapedDesc))
		lines = append(lines, fmt.Sprintf("    <location>%s</location>", escapedPath))
		lines = append(lines, fmt.Sprintf("    <source>%s</source>", s.Source))
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</skills>")

	return strings.Join(lines, "\n")
}

// clampDescription cuts an over-long description to MaxDescriptionLength bytes
// on a rune boundary, ending in an ellipsis. Cross-tool skills (~/.agents/skills)
// routinely carry paragraph-length descriptions; dropping the whole skill for
// that would silently hide it from the catalog.
func clampDescription(description string) string {
	if len(description) <= MaxDescriptionLength {
		return description
	}
	const ellipsis = "…"
	cut := MaxDescriptionLength - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(description[cut]) {
		cut--
	}
	return strings.TrimSpace(description[:cut]) + ellipsis
}

func (sl *SkillsLoader) getSkillMetadata(skillPath string) *SkillMetadata {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		logger.WarnCF("skills", "Failed to read skill metadata",
			map[string]any{
				"skill_path": skillPath,
				"error":      err.Error(),
			})
		return nil
	}

	frontmatter, bodyContent := splitFrontmatter(string(content))
	dirName := filepath.Base(filepath.Dir(skillPath))
	title, bodyDescription := extractMarkdownMetadata(bodyContent)

	metadata := &SkillMetadata{
		Name:        dirName,
		Description: bodyDescription,
	}
	if title != "" && namePattern.MatchString(title) && len(title) <= MaxNameLength {
		metadata.Name = title
	}

	if frontmatter == "" {
		return metadata
	}

	// Try JSON first (for backward compatibility)
	var jsonMeta struct {
		Name                   string `json:"name"`
		Description            string `json:"description"`
		DisableModelInvocation bool   `json:"disable-model-invocation"`
	}
	if err := json.Unmarshal([]byte(frontmatter), &jsonMeta); err == nil {
		if jsonMeta.Name != "" {
			metadata.Name = jsonMeta.Name
		}
		if jsonMeta.Description != "" {
			metadata.Description = jsonMeta.Description
		}
		metadata.DisableModelInvocation = jsonMeta.DisableModelInvocation
		return metadata
	}

	// Fall back to simple YAML parsing
	yamlMeta := sl.parseSimpleYAML(frontmatter)
	if name := yamlMeta["name"]; name != "" {
		metadata.Name = name
	}
	if description := yamlMeta["description"]; description != "" {
		metadata.Description = description
	}
	if yamlMeta["disable-model-invocation"] == "true" {
		metadata.DisableModelInvocation = true
	}
	return metadata
}

func extractMarkdownMetadata(content string) (title, description string) {
	p := parser.NewWithExtensions(parser.CommonExtensions)
	doc := markdown.Parse([]byte(content), p)
	if doc == nil {
		return "", ""
	}

	ast.WalkFunc(doc, func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}

		switch n := node.(type) {
		case *ast.Heading:
			if title == "" && n.Level == 1 {
				title = nodeText(n)
				if title != "" && description != "" {
					return ast.Terminate
				}
			}
		case *ast.Paragraph:
			if description == "" {
				description = nodeText(n)
				if title != "" && description != "" {
					return ast.Terminate
				}
			}
		}
		return ast.GoToNext
	})

	return title, description
}

func nodeText(n ast.Node) string {
	var b strings.Builder
	ast.WalkFunc(n, func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}

		switch t := node.(type) {
		case *ast.Text:
			b.Write(t.Literal)
		case *ast.Code:
			b.Write(t.Literal)
		case *ast.Softbreak, *ast.Hardbreak, *ast.NonBlockingSpace:
			b.WriteByte(' ')
		}
		return ast.GoToNext
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// parseSimpleYAML parses YAML frontmatter and extracts known metadata fields.
func (sl *SkillsLoader) parseSimpleYAML(content string) map[string]string {
	result := make(map[string]string)

	var meta struct {
		Name                   string `yaml:"name"`
		Description            string `yaml:"description"`
		DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	}
	if err := yaml.Unmarshal([]byte(content), &meta); err != nil {
		return result
	}
	if meta.Name != "" {
		result["name"] = meta.Name
	}
	if meta.Description != "" {
		result["description"] = meta.Description
	}
	if meta.DisableModelInvocation {
		result["disable-model-invocation"] = "true"
	}

	return result
}

func (sl *SkillsLoader) extractFrontmatter(content string) string {
	frontmatter, _ := splitFrontmatter(content)
	return frontmatter
}

func (sl *SkillsLoader) stripFrontmatter(content string) string {
	_, body := splitFrontmatter(content)
	return body
}

func splitFrontmatter(content string) (frontmatter, body string) {
	normalized := string(parser.NormalizeNewlines([]byte(content)))
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", content
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", content
	}

	frontmatter = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n")
	return frontmatter, body
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
