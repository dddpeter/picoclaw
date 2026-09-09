package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// checkState is the outcome of a single check.
type checkState int

const (
	stateOK checkState = iota
	stateWarn
	stateFail
)

func (s checkState) mark() string {
	switch s {
	case stateOK:
		return "✅"
	case stateWarn:
		return "⚠️ "
	default:
		return "❌"
	}
}

type checkResult struct {
	name    string
	state   checkState
	detail  string
	fixHint string
}

// doctorCmd runs all checks and writes the report. Returns an error when
// any critical check fails (so the cobra RunE pipeline sets exit code 1
// instead of a bare os.Exit that skips cleanup paths).
func doctorCmd(out io.Writer) error {
	var results []checkResult

	cfg, cfgErr := internal.LoadConfig()
	configPath := internal.GetConfigPath()

	// --- Check 1: config exists & parses ---------------------------------
	if cfgErr != nil {
		results = append(results, checkResult{
			name:    "config",
			state:   stateFail,
			detail:  fmt.Sprintf("%s: %v", configPath, cfgErr),
			fixHint: "run `picoclaw onboard` to generate a fresh config",
		})
		printReport(out, results)
		return errCriticalFailures
	}
	results = append(results, checkResult{
		name:   "config",
		state:  stateOK,
		detail: configPath,
	})

	// --- Check 2: workspace writable -------------------------------------
	workspace := cfg.WorkspacePath()
	if err := checkDirWritable(workspace); err != nil {
		results = append(results, checkResult{
			name:    "workspace",
			state:   stateFail,
			detail:  fmt.Sprintf("%s: %v", workspace, err),
			fixHint: "create the directory or fix permissions",
		})
	} else {
		results = append(results, checkResult{
			name:   "workspace",
			state:  stateOK,
			detail: workspace,
		})
	}

	// --- Check 3: default model configured & reachable --------------------
	defaultModel := cfg.Agents.Defaults.GetModelName()
	if defaultModel == "" {
		results = append(results, checkResult{
			name:    "default model",
			state:   stateFail,
			detail:  "no default model configured",
			fixHint: "add an API key to a model in config.json or via the WebUI Models page",
		})
	} else {
		mc := findModelConfig(cfg, defaultModel)
		if mc == nil {
			results = append(results, checkResult{
				name:    "default model",
				state:   stateFail,
				detail:  fmt.Sprintf("%s (entry missing from model_list)", defaultModel),
				fixHint: "re-add the model entry or fix model name",
			})
		} else if mc.APIKey() == "" && !providerAllowsEmptyKey(mc) {
			results = append(results, checkResult{
				name:    "default model",
				state:   stateWarn,
				detail:  fmt.Sprintf("%s has no API key", defaultModel),
				fixHint: "add the API key in the WebUI or config.json",
			})
		} else if !probeDefaultModel(mc) {
			results = append(results, checkResult{
				name:    "default model reachability",
				state:   stateWarn,
				detail:  fmt.Sprintf("%s endpoint unreachable", defaultModel),
				fixHint: "check network/proxy, API base, or key validity; the runtime fallback chain may still recover",
			})
		} else {
			results = append(results, checkResult{
				name:   "default model reachability",
				state:  stateOK,
				detail: fmt.Sprintf("%s reachable", defaultModel),
			})
		}
	}

	// --- Check 4: enabled channels have credentials ------------------------
	results = append(results, checkChannelCredentials(cfg)...)

	// --- Check 5: security profile summary ---------------------------------
	results = append(results, securityProfileCheck(cfg))

	return printReport(out, results)
}

// checkDirWritable verifies the dir exists and a probe file can be created.
func checkDirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create: %w", err)
	}
	probe := filepath.Join(dir, ".picoclaw-doctor-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	os.Remove(probe)
	return nil
}

// findModelConfig locates a model entry by display name.
func findModelConfig(cfg *config.Config, modelName string) *config.ModelConfig {
	for _, m := range cfg.ModelList {
		if m != nil && m.ModelName == modelName {
			return m
		}
	}
	return nil
}

// providerAllowsEmptyKey is true for providers that use ambient/local auth
// (ollama, lmstudio, vllm, claude-cli, codex-cli, github-copilot).
func providerAllowsEmptyKey(m *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(m)
	switch protocol {
	case "ollama", "lmstudio", "vllm", "gpt4free", "claude-cli", "codex-cli", "github-copilot":
		return true
	}
	return false
}

// probeDefaultModel performs a bounded network probe against the default
// model's endpoint. Kept intentionally simple: one GET /models with a short
// timeout, mirroring the WebUI's probe semantics.
func probeDefaultModel(m *config.ModelConfig) bool {
	apiBase := m.APIBase
	if apiBase == "" {
		return false
	}
	client := &http.Client{Timeout: 8 * time.Second}
	url := strings.TrimSuffix(apiBase, "/") + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	if key := m.APIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Any HTTP response (even 401/403) proves the endpoint is reachable;
	// credential validity is the user's to fix, reachability is ours to report.
	return true
}

// checkChannelCredentials reports enabled channels whose settings exist but
// look incomplete. Channels vary widely; we only flag the common big ones'
// required secret fields when the channel is enabled with non-empty settings.
func checkChannelCredentials(cfg *config.Config) []checkResult {
	var out []checkResult
	// Required secret fields per well-known channel.
	required := map[string][]string{
		"telegram": {"bot_token"},
		"discord":  {"bot_token"},
		"feishu":   {"app_secret"},
		"dingtalk": {"client_secret"},
		"slack":    {"bot_token"},
	}
	for name, ch := range cfg.Channels {
		if ch == nil || !ch.Enabled {
			continue
		}
		fields, known := required[name]
		if !known {
			continue
		}
		settings := decodeChannelSettings(ch)
		if len(settings) == 0 {
			continue // enabled but unconfigured — the channel registry skips these anyway
		}
		for _, f := range fields {
			if v, _ := settings[f].(string); v == "" {
				out = append(out, checkResult{
					name:    "channel " + name,
					state:   stateWarn,
					detail:  fmt.Sprintf("enabled with settings but %s missing", f),
					fixHint: "fill the credential in the WebUI Channels page or config",
				})
			}
		}
	}
	return out
}

// decodeChannelSettings best-effort decodes a channel's Settings RawNode
// into a plain map. Secure-field values may be masked/absent from the
// in-memory view; absence then just means "not reported", never a false OK.
func decodeChannelSettings(ch *config.Channel) map[string]any {
	if len(ch.Settings) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(ch.Settings, &m); err != nil {
		return nil
	}
	return m
}

// securityProfileCheck summarizes the effective security posture.
func securityProfileCheck(cfg *config.Config) checkResult {
	profile := cfg.Tools.Exec.DenyProfile
	if profile == "" {
		profile = configDefaultDenyProfile()
	}
	detail := fmt.Sprintf("deny_profile=%s, enable_deny_patterns=%v, restrict_to_workspace=%v, max_parallel_turns=%d",
		profile, cfg.Tools.Exec.EnableDenyPatterns, cfg.Agents.Defaults.RestrictToWorkspace, maxParallelTurnsOr1(cfg))
	return checkResult{
		name:   "security profile",
		state:  stateOK,
		detail: detail,
	}
}

func configDefaultDenyProfile() string {
	return "open"
}

func maxParallelTurnsOr1(cfg *config.Config) int {
	if n := cfg.Agents.Defaults.MaxParallelTurns; n > 0 {
		return n
	}
	return 1
}

func printReport(out io.Writer, results []checkResult) error {
	criticalFails := 0
	fmt.Fprintf(out, "%s picoclaw doctor\n\n", internal.Logo)
	for _, r := range results {
		fmt.Fprintf(out, "  %s %-28s %s\n", r.state.mark(), r.name, r.detail)
		if r.fixHint != "" && r.state != stateOK {
			fmt.Fprintf(out, "      ↳ fix: %s\n", r.fixHint)
		}
		if r.state == stateFail {
			criticalFails++
		}
	}
	fmt.Fprintln(out)
	if criticalFails > 0 {
		fmt.Fprintf(out, "Result: ❌ %d critical issue(s) found\n", criticalFails)
		return errCriticalFailures
	}
	fmt.Fprintln(out, "Result: ✅ all critical checks passed (warnings may need attention)")
	return nil
}

// errCriticalFailures signals a non-zero process exit via the standard
// RunE error path.
var errCriticalFailures = errors.New("critical checks failed")
