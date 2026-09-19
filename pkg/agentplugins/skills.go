package agentplugins

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginSkill is one skill discovered inside a plugin package.
type PluginSkill struct {
	Name string // skill directory name
	Dir  string // absolute path of the skill directory
}

// skillDirNameRe mirrors picoclaw's skill name rule (pkg/skills
// ValidateSkillName): alphanumeric groups joined by single hyphens. Plugin
// skill directory names must pass it so the bridge into SkillsLoader is not
// rejected downstream.
var skillDirNameRe = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)

// DiscoverSkills performs strict flat skill discovery per spec §7.1: each
// immediate child directory of <root>/skills containing a SKILL.md regular
// file is one skill; deeper descendants are never searched.
//
// Missing skills/ is silent (§6.2); present but not a directory is a warning
// (§6.2). Individual invalid skills are skipped with a warning (§7.1).
//
// Host hardening (NOT a spec requirement): the SKILL.md frontmatter name must
// equal the directory name and the directory name must pass picoclaw's skill
// name rule — otherwise the skill is skipped. This is PicoClaw's D2
// addressing policy.
func DiscoverSkills(root string, r *Report) []PluginSkill {
	skillsDir := filepath.Join(root, "skills")
	st, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // missing fixed location is not an error (§6.2)
		}
		r.Warnf("skills/ is not accessible: %v", err)
		return nil
	}
	if !st.IsDir() {
		r.Warnf("skills/ does not resolve to a directory; skills component type invalid (§6.2)")
		return nil
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		r.Warnf("skills/ cannot be read: %v", err)
		return nil
	}

	var out []PluginSkill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(skillsDir, name)

		skillFile := filepath.Join(dir, "SKILL.md")
		if !isRegularFile(skillFile) {
			r.Warnf("skill %q skipped: SKILL.md missing or not a regular file", name)
			continue
		}

		// Containment (§4.1.3): a discovered SKILL.md resolving outside the
		// plugin root must be skipped.
		if !Contains(root, skillFile) {
			r.Warnf("skill %q skipped: SKILL.md resolves outside the plugin root", name)
			continue
		}

		// Host hardening (D2 policy, not spec): dir name must be a valid
		// picoclaw skill name and frontmatter name must match it.
		if !skillDirNameRe.MatchString(name) {
			r.Warnf("skill %q skipped: directory name is not a valid skill name (host policy)", name)
			continue
		}
		fmName, ok := readFrontmatterName(skillFile)
		if !ok {
			r.Warnf("skill %q skipped: SKILL.md frontmatter unreadable", name)
			continue
		}
		if fmName != name {
			r.Warnf("skill %q skipped: frontmatter name %q does not match directory name (host policy)", name, fmName)
			continue
		}

		out = append(out, PluginSkill{Name: name, Dir: dir})
	}
	return out
}

// isRegularFile reports whether path resolves to a regular file (following
// symlinks is intentional: a symlinked SKILL.md within the root is fine as
// long as containment holds).
func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Mode().IsRegular()
}

// readFrontmatterName extracts the `name:` field from the leading YAML
// frontmatter block of a SKILL.md. ok=false means no frontmatter/name found.
func readFrontmatterName(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			break // end of frontmatter
		}
		if strings.HasPrefix(line, "name:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = strings.Trim(name, `"'`)
			if name != "" {
				return name, true
			}
		}
	}
	return "", false
}
