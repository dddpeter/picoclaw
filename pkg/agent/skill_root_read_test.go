package agent

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestAppendSkillRootReadPatternsAllowsOutsideRoots: under restrict mode the
// model must still be able to read_file the SKILL.md paths the catalog lists,
// so every skill root outside the workspace gets a read-allow pattern; roots
// inside the workspace need none, and sibling directories stay closed.
func TestAppendSkillRootReadPatternsAllowsOutsideRoots(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	home := filepath.Join(tmp, "home")
	picoHome := filepath.Join(tmp, "picohome")
	builtin := filepath.Join(tmp, "builtin")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(config.EnvHome, picoHome)
	t.Setenv(config.EnvBuiltinSkills, builtin)

	patterns := appendSkillRootReadPatterns(nil, ws)
	matches := func(path string) bool {
		for _, re := range patterns {
			if re.MatchString(path) {
				return true
			}
		}
		return false
	}

	for _, allowed := range []string{
		filepath.Join(picoHome, "skills", "x", "SKILL.md"),
		filepath.Join(home, ".agents", "skills", "x", "references", "a.md"),
		filepath.Join(builtin, "y", "SKILL.md"),
		builtin,
	} {
		if !matches(allowed) {
			t.Errorf("%s should be readable via a skill-root pattern", allowed)
		}
	}
	for _, closed := range []string{
		filepath.Join(ws, "skills", "x", "SKILL.md"), // inside workspace: no pattern needed
		filepath.Join(ws, ".skills", "x", "SKILL.md"),
		filepath.Join(home, ".agents", "skillsX", "SKILL.md"), // prefix must stop at a separator
		filepath.Join(tmp, "elsewhere", "SKILL.md"),
	} {
		if matches(closed) {
			t.Errorf("%s must not match any skill-root pattern", closed)
		}
	}

	// Configured patterns are preserved ahead of the appended ones.
	custom := regexp.MustCompile(`^/srv/data`)
	got := appendSkillRootReadPatterns([]*regexp.Regexp{custom}, ws)
	if len(got) == 0 || got[0] != custom {
		t.Errorf("configured patterns should lead the list, got %v", got)
	}
}
