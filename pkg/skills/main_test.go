package skills

import (
	"os"
	"testing"
)

// TestMain points the home directory at a scratch dir so NewSkillsLoader's
// derived ~/.agents/skills root never picks up the developer's real cross-tool
// skills: those would leak into every ListSkills assertion in this package.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "picoclaw-skills-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
