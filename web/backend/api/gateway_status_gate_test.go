package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

// TestGatewayStatusDoesNotResurrectStoppedGateway pins the B16 gate: after
// an operator stop, a lingering pid file (the dying gateway deletes it only
// on its deferred cleanup) must NOT flip the status back to running or
// re-attach — the poller used to resurrect the stopped state every 2s.
func TestGatewayStatusDoesNotResurrectStoppedGateway(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	// A live process plays the not-yet-reaped gateway: pid file still on
	// disk, but the launcher already stopped tracking it (stopGatewayLocked
	// cleared cmd/pidData and set userStopped).
	sleeper := startLongRunningProcess(t)
	writeTestPidFile(t, ppid.PidFileData{
		PID:     sleeper.Process.Pid,
		Version: "test",
		Port:    1,
		Host:    "127.0.0.1",
	})
	// The real ps classifier would reject the sleeper's command line; the
	// probe semantics under test are the matcher-confirmed path (same setup
	// as TestGatewayStatusReportsRunningFromPidProbe).
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	gateway.mu.Lock()
	gateway.userStopped = true
	gateway.runtimeStatus = "stopped"
	gateway.mu.Unlock()

	h := NewHandler(configPath)
	rec := httptest.NewRecorder()
	h.handleGatewayStatus(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil))

	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status, _ := data["gateway_status"].(string); status == "running" {
		t.Fatal("status = running after operator stop with lingering pid file (resurrection)")
	}

	// attachToGatewayProcessLocked would have set gateway.cmd to the live
	// process — it must stay nil while userStopped is set.
	gateway.mu.Lock()
	cmd := gateway.cmd
	gateway.mu.Unlock()
	if cmd != nil {
		t.Fatal("stopped gateway was re-attached by the status poll")
	}

	// Clearing the operator stop restores the probe behavior.
	gateway.mu.Lock()
	gateway.userStopped = false
	gateway.mu.Unlock()
	rec2 := httptest.NewRecorder()
	h.handleGatewayStatus(rec2, httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil))
	var data2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &data2); err != nil {
		t.Fatalf("decode status (cleared): %v", err)
	}
	if status, _ := data2["gateway_status"].(string); status != "running" {
		t.Fatalf("status = %v after userStopped cleared, want running from pid probe", data2["gateway_status"])
	}
}
