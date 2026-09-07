package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Low-progress loop detection, borrowed from MiMoCode's Try-Best design
// (see docs/design/mimo-code-borrowing-analysis.zh.md, item 1, minimal
// version). Two signals are tracked:
//
//   - bash_retry: the same normalized command failing N times in a row with
//     unchanged normalized output — repeating it will not make progress.
//   - edit_streak: N consecutive edit-class tool calls with no other action
//     in between — a strong "editing without verifying" shape.
//
// On a hit the detector resets and returns a warning that the caller injects
// in front of the tool result sent to the model. The turn is never killed:
// the model gets an explicit chance to replan, which keeps the cost of a
// false positive low.

var (
	healthTmpPathPattern  = regexp.MustCompile(`(?:/tmp|/var/tmp|/private/tmp)/\S+`)
	healthSeedPattern     = regexp.MustCompile(`--seed[= ]\S+`)
	healthNumberRun       = regexp.MustCompile(`\d{6,}`)
	healthDurationPattern = regexp.MustCompile(`\b\d+(?:\.\d+)?\s?[nm]?s\b`)
)

// normalizeCommandForHealth masks the volatile parts of a command so that
// timestamps, temp paths and seeds do not defeat equality comparison.
func normalizeCommandForHealth(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	cmd = healthTmpPathPattern.ReplaceAllString(cmd, "<TMP>")
	cmd = healthSeedPattern.ReplaceAllString(cmd, "--seed=<SEED>")
	cmd = healthNumberRun.ReplaceAllString(cmd, "<NUM>")
	return cmd
}

// normalizeErrorForHealth masks durations and long numbers in failure output
// and bounds its length before comparing consecutive failures.
func normalizeErrorForHealth(s string) string {
	s = healthDurationPattern.ReplaceAllString(s, "<DUR>")
	s = healthNumberRun.ReplaceAllString(s, "<NUM>")
	if len(s) > 2000 {
		s = s[:1000] + "\n<...>\n" + s[len(s)-1000:]
	}
	return s
}

type turnHealth struct {
	mu  sync.Mutex
	cfg config.LoopDetectionConfig

	lastCmd       string
	lastErrDigest string
	bashRetryRuns int
	editStreak    int
}

func newTurnHealth(cfg config.LoopDetectionConfig) *turnHealth {
	return &turnHealth{cfg: cfg}
}

// note feeds one tool execution into the detector and returns a non-empty
// warning when a loop signal fires (the signal resets on fire, so repeated
// triggering requires re-accumulating the streak).
func (h *turnHealth) note(toolName, command string, errMsg string, isError bool) string {
	if h == nil || !h.cfg.IsEnabled() {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case toolName == "exec":
		h.editStreak = 0
		norm := normalizeCommandForHealth(command)
		if !isError || norm == "" {
			h.bashRetryRuns = 0
			h.lastCmd, h.lastErrDigest = "", ""
			return ""
		}
		digest := normalizeErrorForHealth(errMsg)
		if norm == h.lastCmd && digest == h.lastErrDigest {
			h.bashRetryRuns++
		} else {
			h.bashRetryRuns = 1
			h.lastCmd, h.lastErrDigest = norm, digest
		}
		if h.bashRetryRuns >= h.cfg.BashRetryThreshold {
			h.bashRetryRuns = 0
			return fmt.Sprintf(
				"⚠ Loop signal (bash_retry): command %q has now failed %d times in a row with unchanged output. Repeating it is unlikely to make progress — stop, inspect the actual error, check preconditions, and use a different approach.",
				norm, h.cfg.BashRetryThreshold,
			)
		}
	case isEditClassTool(toolName):
		h.bashRetryRuns = 0
		h.lastCmd, h.lastErrDigest = "", ""
		h.editStreak++
		if h.editStreak >= h.cfg.EditStreakThreshold {
			h.editStreak = 0
			return fmt.Sprintf(
				"⚠ Loop signal (edit_streak): %d consecutive %s calls with no other action in between. Step back before editing again — read the current file state or run a verification command, and reconsider whether the approach itself is working.",
				h.cfg.EditStreakThreshold, toolName,
			)
		}
	default:
		// Any other action breaks both streaks: interleaved reads or checks
		// are exactly the observable progress these signals look for.
		h.bashRetryRuns = 0
		h.editStreak = 0
		h.lastCmd, h.lastErrDigest = "", ""
	}
	return ""
}

func isEditClassTool(name string) bool {
	switch name {
	case "edit_file", "write_file", "append_file":
		return true
	}
	return false
}

// noteToolHealth adapts a raw tool execution for the detector. The returned
// warning (if any) is meant to be prefixed onto the tool result content that
// goes back to the model.
func (ts *turnState) noteToolHealth(toolName string, args map[string]any, errMsg string, isError bool) string {
	if ts == nil || ts.health == nil {
		return ""
	}
	command, _ := args["command"].(string)
	return ts.health.note(toolName, command, errMsg, isError)
}
