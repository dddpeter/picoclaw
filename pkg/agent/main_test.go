package agent

import (
	"os"
	"testing"
)

// TestMain points the home directory at a scratch dir so tests never pick up
// the developer's real ~/.picoclaw or ~/.agents state. This matters now that
// ~/.agents/skills is a skill root: a real-world skills library (100+
// entries) inflates the system prompt enough to flip context-budget
// assertions (TestAgentLoop_ContextExhaustionRetry failed with the real home
// because the prompt alone exceeded the retry compaction budget). Individual
// tests can still override HOME via t.Setenv for their own scenarios.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "picoclaw-agent-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
