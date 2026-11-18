package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// Reap the process once the restart path kills it, so Signal(0) probing
	// does not see an unreaped zombie as alive. Use os.Process.Wait — not
	// exec.Cmd.Wait — so exec.Cmd.ProcessState is never written and stays
	// race-free against isCmdProcessAliveLocked's reads.
	go func() { _, _ = sleeper.Process.Wait() }()

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

// TestResetConfigStatusCheckRunsOutsideConfigLock pins the C-1 lock order for
// the config-reset path. gatewayStatusData is the reset handler's first
// gateway touch (it calls the process matcher before taking gateway.mu), so a
// canary on gatewayProcessMatcher observes whether configWriteMu is still
// held there. Before the fix the deferred Unlock kept the mutex held through
// the status check and the RestartGateway call — the latter re-enters
// configWriteMu via startGatewayLocked → EnsurePicoChannel (sync.Mutex is not
// reentrant), so POST /api/config/reset self-deadlocked whenever a gateway
// was running. RestartGateway itself is not asserted to spawn: after a
// factory reset the default model name is empty, so readiness legitimately
// fails — the unlock-before-gateway-work ordering is what this test pins.
func TestResetConfigStatusCheckRunsOutsideConfigLock(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	// Fake "running" gateway: PID file + preset gateway.cmd, so
	// gatewayStatusData takes the running path without an attach, and the
	// reset handler proceeds to the (readiness-rejected) restart attempt.
	sleeper := _exec.Command(os.Args[0], "-test.run=TestBindingSleeperHelperProcess", "--", "binding-sleeper")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start binding sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	})
	go func() { _ = sleeper.Wait() }()

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

	var matcherHeldConfigLock bool
	gatewayProcessMatcher = func(int) (bool, bool) {
		if !configWriteMu.TryLock() {
			matcherHeldConfigLock = true
		} else {
			configWriteMu.Unlock()
		}
		return true, true
	}

	h := NewHandler(configPath)

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/config/reset", nil)
		rec := httptest.NewRecorder()
		h.handleResetConfig(rec, req)
		done <- rec.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("reset returned %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("handleResetConfig did not return within 15s — it self-deadlocked " +
			"on configWriteMu (C-1 shape: restart re-entered the held mutex)")
	}

	if matcherHeldConfigLock {
		t.Fatal("gatewayStatusData ran while configWriteMu was still held by " +
			"handleResetConfig (C-1 lock order violation)")
	}
}

// TestRegenPicoTokenUpdatesGatewayCacheOutsideConfigLock pins the C-1 lock
// order for the token rotation path: by the time the rotated token has
// reached config.json (the last configWriteMu-guarded step), the mutex must
// already be released — the gateway token-cache update waits on gateway.mu
// (held by this test) without holding configWriteMu. Before the fix the
// deferred Unlock kept configWriteMu held across the gateway.mu section,
// pairing with startGatewayLocked's gateway.mu → configWriteMu nesting into
// an ABBA deadlock.
func TestRegenPicoTokenUpdatesGatewayCacheOutsideConfigLock(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	if _, err := h.EnsurePicoChannel(); err != nil {
		t.Fatalf("EnsurePicoChannel() error = %v", err)
	}
	initial, err := loadPicoToken(configPath)
	if err != nil {
		t.Fatalf("load initial pico token: %v", err)
	}

	// Hold gateway.mu so the handler blocks at its token-cache update.
	gateway.mu.Lock()
	gatewayMuHeld := true
	t.Log("test: gateway.mu held")
	t.Cleanup(func() {
		if gatewayMuHeld {
			gatewayMuHeld = false
			gateway.mu.Unlock()
		}
	})

	type regenResult struct {
		code int
		body string
	}
	done := make(chan regenResult, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/pico/token", nil)
		rec := httptest.NewRecorder()
		h.handleRegenPicoToken(rec, req)
		done <- regenResult{code: rec.Code, body: rec.Body.String()}
	}()

	// Wait until the rotated token reached config.json — SaveConfig is the
	// last configWriteMu-guarded step in the handler.
	rotated := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := loadPicoToken(configPath)
		if err == nil && current != "" && current != initial {
			rotated = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !rotated {
		t.Fatal("rotated token never reached config.json within 5s")
	}
	t.Log("test: rotated token detected")

	if configWriteMu.TryLock() {
		configWriteMu.Unlock()
		t.Log("test: configWriteMu free")
	} else {
		t.Fatal("configWriteMu still held after the rotated token reached " +
			"config.json — handleRegenPicoToken takes gateway.mu under it (ABBA shape)")
	}

	gateway.mu.Unlock()
	gatewayMuHeld = false
	t.Log("test: gateway.mu released")

	select {
	case res := <-done:
		if res.code != http.StatusOK {
			t.Fatalf("regen returned %d: %s", res.code, res.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handleRegenPicoToken did not finish within 10s")
	}
}

// loadPicoToken reads the current raw pico token from the config file.
func loadPicoToken(configPath string) (string, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return "", err
	}
	bc := cfg.Channels.GetByType(config.ChannelPico)
	if bc == nil {
		return "", fmt.Errorf("no pico channel in config")
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		return "", err
	}
	settings, ok := decoded.(*config.PicoSettings)
	if !ok {
		return "", fmt.Errorf("pico settings decode type mismatch")
	}
	return settings.Token.String(), nil
}
