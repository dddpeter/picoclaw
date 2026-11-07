package agent

// CheckpointHook captures pre-write file contents for workspace
// checkpointing + turn-level undo (trae borrowing 增量清单 §2.1,
// tools.checkpoint). It is a fork in-process hook: BeforeTool snapshots the
// target of every file-writing tool before it runs, and the deferred
// agent.turn.end event commits the captured changes as one version.
//
// Failure policy: a capture must never break the tool it wraps — validation
// failures, IO errors and oversized files are recorded as unstored entries
// with a reason (surfaced at revert time) instead of erroring the hook.
// Likewise a commit failure only logs.

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/snapshot"
)

// checkpointWriteTools maps the file-writing tools to the argument that
// carries their target path (lsp_fix writes through an internal
// WriteFileTool with the same "path" argument).
var checkpointWriteTools = map[string]string{
	"write_file":  "path",
	"edit_file":   "path",
	"append_file": "path",
	"lsp_fix":     "path",
}

// CheckpointHook implements ToolInterceptor and RuntimeEventObserver.
type CheckpointHook struct {
	store *snapshot.Store
	cfg   config.CheckpointConfig
	// workspaceFor resolves the workspace root of the agent running the
	// turn (multi-agent: each agent owns its own workspace).
	workspaceFor func(agentID string) string
	restrict     bool
	allowWrite   []*regexp.Regexp

	mu     sync.Mutex
	active map[string]*snapshot.TurnCapture // sessionKey → open capture
}

func newCheckpointHook(
	store *snapshot.Store,
	cfg config.CheckpointConfig,
	workspaceFor func(agentID string) string,
	restrict bool,
	allowWrite []*regexp.Regexp,
) *CheckpointHook {
	return &CheckpointHook{
		store:        store,
		cfg:          cfg,
		workspaceFor: workspaceFor,
		restrict:     restrict,
		allowWrite:   allowWrite,
		active:       make(map[string]*snapshot.TurnCapture),
	}
}

// BeforeTool snapshots the pre-turn content of the tool's target file. Runs
// before validation-sensitive hooks are affected: a path the tool itself
// would reject simply produces no capture.
func (h *CheckpointHook) BeforeTool(ctx context.Context, call *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision, error) {
	if call.Tool == "exec" {
		// exec change detection (trae borrowing 增量清单 §2.1, TrackExec):
		// only the run action touches the filesystem; wrap it with a
		// pre-scan so whatever it changes is recoverable afterwards.
		if h.cfg.TrackExec {
			if action, _ := call.Arguments["action"].(string); action == "run" {
				workspace := h.workspaceFor(call.Meta.AgentID)
				if tc := h.captureFor(call.Meta.SessionKey, call.Meta.TurnID, workspace); tc != nil {
					h.store.ExecPreScan(tc)
				}
			}
		}
		return call, HookDecision{Action: HookActionContinue}, nil
	}

	argName, isWriteTool := checkpointWriteTools[call.Tool]
	if !isWriteTool {
		return call, HookDecision{Action: HookActionContinue}, nil
	}
	path, _ := call.Arguments[argName].(string)
	if strings.TrimSpace(path) == "" {
		return call, HookDecision{Action: HookActionContinue}, nil
	}

	workspace := h.workspaceFor(call.Meta.AgentID)
	if workspace == "" {
		return call, HookDecision{Action: HookActionContinue}, nil
	}
	// Same validation the write tools apply (workspace anchor, system-dir
	// protection, restrict/symlink rules): snapshots must not reach further
	// than the tools can.
	abs, err := fstools.ValidatePathWithAllowPaths(path, workspace, h.restrict, h.allowWrite)
	if err != nil {
		return call, HookDecision{Action: HookActionContinue}, nil
	}
	rel, err := filepath.Rel(workspace, abs)
	if err != nil {
		rel = abs
	}

	if tc := h.captureFor(call.Meta.SessionKey, call.Meta.TurnID, workspace); tc != nil {
		h.store.CaptureFile(tc, abs, filepath.ToSlash(rel))
	}
	return call, HookDecision{Action: HookActionContinue}, nil
}

// AfterTool closes the exec wrap: the post-scan turns everything the run
// changed into pending turn entries. It never modifies results.
func (h *CheckpointHook) AfterTool(ctx context.Context, result *ToolResultHookResponse) (*ToolResultHookResponse, HookDecision, error) {
	if result != nil && result.Tool == "exec" && h.cfg.TrackExec {
		if action, _ := result.Arguments["action"].(string); action == "run" {
			workspace := h.workspaceFor(h.agentIDFor(result.Meta))
			if tc := h.captureFor(result.Meta.SessionKey, result.Meta.TurnID, workspace); tc != nil {
				h.store.ExecPostScan(tc)
			}
		}
	}
	return result, HookDecision{Action: HookActionContinue}, nil
}

// agentIDFor extracts the agent id from hook metadata (empty-safe).
func (h *CheckpointHook) agentIDFor(meta HookMeta) string {
	return meta.AgentID
}

// OnRuntimeEvent opens captures on turn start and commits them on turn end
// (deferred — every return path, including aborts and errors, commits what
// was captured).
func (h *CheckpointHook) OnRuntimeEvent(ctx context.Context, evt runtimeevents.Event) error {
	switch evt.Kind {
	case runtimeevents.KindAgentTurnStart:
		workspace := h.workspaceFor(evt.Scope.AgentID)
		h.captureFor(evt.Scope.SessionKey, evt.Scope.TurnID, workspace)

	case runtimeevents.KindAgentTurnEnd:
		h.commitTurn(evt.Scope.SessionKey, evt.Scope.TurnID)
	}
	return nil
}

// captureFor returns the open capture for the session, committing a stale
// one first (a turn that wedged past its turn_end — the hard-abort watchdog
// releases such sessions — keeps its already-captured observations).
func (h *CheckpointHook) captureFor(sessionKey, turnID, workspace string) *snapshot.TurnCapture {
	if sessionKey == "" || workspace == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if tc, ok := h.active[sessionKey]; ok {
		if tc.TurnID == turnID || turnID == "" {
			return tc
		}
		// Stale capture from a previous turn: commit what it observed, then
		// start fresh for this turn.
		delete(h.active, sessionKey)
		h.commit(tc)
	}
	tc := h.store.BeginTurn(sessionKey, turnID, workspace)
	h.active[sessionKey] = tc
	return tc
}

// commitTurn commits the session's open capture if it belongs to the given
// turn. A mismatching turnID means a stale turn_end from a turn whose
// capture was already superseded — it must not commit the newer turn's
// partial capture.
func (h *CheckpointHook) commitTurn(sessionKey, turnID string) {
	if sessionKey == "" {
		return
	}
	h.mu.Lock()
	tc, ok := h.active[sessionKey]
	// Only remove the capture when it actually belongs to this turn — a
	// stale turn_end from a wedged predecessor must not drop the newer
	// turn's in-flight capture.
	match := ok && (turnID == "" || tc.TurnID == "" || tc.TurnID == turnID)
	if match {
		delete(h.active, sessionKey)
	}
	h.mu.Unlock()
	if !match {
		return
	}
	h.commit(tc)
}

func (h *CheckpointHook) commit(tc *snapshot.TurnCapture) {
	version, err := h.store.CommitTurn(tc)
	if err != nil {
		logger.WarnCF("checkpoint", "Failed to commit turn snapshot", map[string]any{
			"session_key": tc.SessionKey,
			"turn_id":     tc.TurnID,
			"error":       err.Error(),
		})
		return
	}
	if version == nil {
		return // workspace never modified: nothing to record
	}
	logger.InfoCF("checkpoint", "Turn snapshot committed", map[string]any{
		"session_key": tc.SessionKey,
		"turn_id":     tc.TurnID,
		"version":     version.ID,
		"files":       len(version.Files),
	})
}
