package api

import (
	"os"
	_exec "os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

// TestBindingSleeperHelperProcess blocks until killed. It gives tests a live
// "gateway" process to point the PID file at without spawning a shell
// (cross-platform): the parent test kills it via the restart path, which on
// Windows means Process.Kill — so the PID must never be the test process
// itself.
func TestBindingSleeperHelperProcess(t *testing.T) {
	isHelper := false
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == "binding-sleeper" {
			isHelper = true
			break
		}
	}
	if !isHelper {
		t.Skip("helper process")
	}
	<-time.After(10 * time.Minute)
}

// startBindingSleeper starts a helper process that stays alive until killed
// and returns its PID.
func startBindingSleeper(t *testing.T) int {
	t.Helper()

	cmd := _exec.Command(os.Args[0], "-test.run=TestBindingSleeperHelperProcess", "--", "binding-sleeper")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binding sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// startBindingHelperWriter replaces gatewayExecCommand with a canary that (a)
// records whether configWriteMu is FREE at the moment the restart path spawns
// the gateway process, and (b) re-execs the test binary as the harmless
// TestGatewayStartHelperProcess child so startGatewayLocked completes without
// launching a real gateway.
func startBindingHelperWriter(t *testing.T, lockFree *bool, canaryCalls *int) {
	t.Helper()

	envPath := filepath.Join(t.TempDir(), "gateway-child-env.json")
	gatewayExecCommand = func(_ string, _ ...string) *_exec.Cmd {
		*canaryCalls++
		stillHeld := true
		if configWriteMu.TryLock() {
			*lockFree = true
			configWriteMu.Unlock()
			stillHeld = false
		}
		t.Logf("canary call %d: configWriteMu still held = %v", *canaryCalls, stillHeld)
		return _exec.Command(
			os.Args[0],
			"-test.run=TestGatewayStartHelperProcess",
			"--",
			"gateway-env-helper",
			envPath,
		)
	}
}

// assertBindingSaveRestartsOutsideConfigLock runs fn (a save*Binding wrapper)
// with a fake "running" gateway and fails when the restart path is entered
// while configWriteMu is still held by the caller — the exact self-deadlock
// shape of review C-1 (sync.Mutex is non-reentrant, so a nested Lock blocks
// forever; the watchdog turns that hang into a test failure).
func assertBindingSaveRestartsOutsideConfigLock(t *testing.T, fn func(h *Handler) error) {
	t.Helper()

	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	// gatewayStartReady requires a fully configured default model.
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].SetAPIKey("test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	// Live helper process plays the "running gateway": pre-attached as
	// gateway.cmd (with a background Wait so the exit is detectable) and
	// referenced by the PID file, so gatewayStatusData() reports "running"
	// and the binding save takes the restart path.
	sleeper := _exec.Command(os.Args[0], "-test.run=TestBindingSleeperHelperProcess", "--", "binding-sleeper")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start binding sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	})
	go func() { _ = sleeper.Wait() }() // so ProcessState flips after Kill

	gateway.mu.Lock()
	gateway.cmd = sleeper
	gateway.owned = false
	gateway.pidData = &ppid.PidFileData{PID: sleeper.Process.Pid}
	gateway.mu.Unlock()

	writeTestPidFile(t, ppid.PidFileData{
		PID:     sleeper.Process.Pid,
		Version: "test",
		Port:    1,
		Host:    "127.0.0.1",
	})
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }

	canaryCalls := new(int)
	lockFreeAtRestart := false
	startBindingHelperWriter(t, &lockFreeAtRestart, canaryCalls)

	h := NewHandler(configPath)

	done := make(chan error, 1)
	go func() { done <- fn(h) }()

	select {
	case err := <-done:
		t.Logf("save binding returned: err=%v, canaryCalls=%d", err, *canaryCalls)
		if err != nil {
			t.Fatalf("save binding returned error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Logf("canaryCalls at watchdog: %d", *canaryCalls)
		t.Fatal("save binding did not return within 15s — RestartGateway was " +
			"entered while configWriteMu was still held (C-1 self-deadlock)")
	}

	if !lockFreeAtRestart {
		t.Fatal("RestartGateway spawned the gateway process while configWriteMu " +
			"was still held by the binding save (C-1 self-deadlock)")
	}
}

func TestSaveWeixinBindingRestartDoesNotHoldConfigLock(t *testing.T) {
	assertBindingSaveRestartsOutsideConfigLock(t, func(h *Handler) error {
		return h.saveWeixinBinding("bot-token", "bot-account")
	})
}

func TestSaveWecomBindingRestartDoesNotHoldConfigLock(t *testing.T) {
	assertBindingSaveRestartsOutsideConfigLock(t, func(h *Handler) error {
		return h.saveWecomBinding("bot-id", "bot-secret")
	})
}
