package api

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

// TestStatusPidClassifyCached pins the pid-classification memo: repeated
// status reads within the TTL must call the (ps-forking) process matcher
// once per pid, not once per request.
func TestStatusPidClassifyCached(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	// The pid file's liveness check (Signal(0)) needs a real live process;
	// the matcher itself is mocked, so any live pid works.
	live := startLongRunningProcess(t)
	writeTestPidFile(t, ppid.PidFileData{PID: live.Process.Pid, Version: "test", Port: 1, Host: "127.0.0.1"})

	matcherCalls := 0
	gatewayProcessMatcher = func(int) (bool, bool) {
		matcherCalls++
		return true, true
	}

	h := NewHandler(configPath)
	h.gatewayStatusData()
	h.gatewayStatusData()
	h.gatewayStatusData()

	if matcherCalls != 1 {
		t.Fatalf("process matcher called %d times across 3 status reads, want 1 (TTL cache)", matcherCalls)
	}
}

// TestCachedConfigForIdentity pins the config cache: same file identity
// returns the shared read-only instance; a SaveConfig (new mtime via atomic
// rename) invalidates and reloads.
func TestCachedConfigForIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	resetGatewayCachesForTest()
	t.Cleanup(resetGatewayCachesForTest)

	first, err := cachedConfigFor(configPath)
	if err != nil {
		t.Fatalf("cachedConfigFor() error = %v", err)
	}
	second, err := cachedConfigFor(configPath)
	if err != nil {
		t.Fatalf("cachedConfigFor() error = %v", err)
	}
	if first != second {
		t.Fatal("same file identity must return the cached instance")
	}

	// Rewrite with a different default model: next read must observe it.
	updated := config.DefaultConfig()
	updated.Agents.Defaults.ModelName = updated.ModelList[0].ModelName
	if err := config.SaveConfig(configPath, updated); err != nil {
		t.Fatalf("SaveConfig() updated error = %v", err)
	}
	third, err := cachedConfigFor(configPath)
	if err != nil {
		t.Fatalf("cachedConfigFor() after rewrite error = %v", err)
	}
	if third == first {
		t.Fatal("rewritten config must invalidate the cache")
	}
	if got := third.Agents.Defaults.GetModelName(); got != updated.ModelList[0].ModelName {
		t.Fatalf("cached config default model = %q, want %q", got, updated.ModelList[0].ModelName)
	}
}
