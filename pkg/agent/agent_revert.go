package agent

// /revert — turn-level workspace undo (trae borrowing 增量清单 §2.1).
// Idle sessions reach this through the command executor (Runtime.RevertTurn);
// busy sessions are intercepted in the message pump like /stop, where actual
// restores are refused (files are changing under the live turn) while the
// read-only `list` stays available. After a successful restore the revert is
// broadcast to the session's pico clients (web chat included) via the
// gateway-injected notifier, and an agent.revert runtime event is emitted.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/snapshot"
)

// SetCheckpointNotifier wires a callback invoked after every successful
// /revert so the gateway can fan the event out to session clients. Nil
// (default) = no fan-out. The callback must not block: it runs on the
// command path.
func (al *AgentLoop) SetCheckpointNotifier(fn func(channel, chatID, sessionKey string, result *snapshot.RevertResult)) {
	al.checkpointNotifier = fn
}

// tryHandleRevertCommand intercepts /revert in the busy branch (before it
// would be enqueued as steering and leak into the model context). Restores
// are refused while a turn is live; the read-only `list` form and the
// mid-teardown window (turn state just cleared) execute right here — the
// command must NEVER fall through to the steering queue.
func (al *AgentLoop) tryHandleRevertCommand(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	cmdName, ok := commands.CommandName(msg.Content)
	if !ok || cmdName != "revert" {
		return false
	}
	reply := al.executeRevertCommand(sessionKey, "", msg.Channel, msg.ChatID, revertArg(msg.Content))
	al.PublishResponseIfNeeded(ctx, msg.Channel, msg.ChatID, sessionKey, reply)
	return true
}

// executeRevertCommand runs /revert for a session and returns the reply
// text. agentID may be empty (busy path) — it only feeds the runtime event.
func (al *AgentLoop) executeRevertCommand(sessionKey, agentID, channel, chatID, arg string) string {
	store := al.CheckpointStore()
	if store == nil {
		return "Workspace snapshots are disabled (set tools.checkpoint.enabled=true)."
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return "Workspace snapshots: no session context for this command."
	}

	if strings.TrimSpace(arg) == "list" {
		return formatRevertList(store, sessionKey)
	}

	if al.SessionTurnActiveByKey(sessionKey) {
		return "⏳ A task is running for this session — /revert is skipped while a turn is active. Try again after it finishes."
	}

	result, err := store.Revert(sessionKey, strings.TrimSpace(arg))
	if err != nil {
		if errors.Is(err, snapshot.ErrNoVersions) {
			return "No workspace versions recorded for this session — nothing to revert."
		}
		return "Revert failed: " + err.Error()
	}

	al.emitEvent(runtimeevents.KindAgentRevert, HookMeta{
		AgentID:    agentID,
		SessionKey: sessionKey,
		Source:     "revert",
	}, result)

	if al.checkpointNotifier != nil {
		al.checkpointNotifier(channel, chatID, sessionKey, result)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "✅ Reverted %d turn(s) (%s → %s): %d file(s) restored, %d created file(s) removed.",
		result.Versions, result.FromID, result.ToID, result.Restored, result.Deleted)
	for _, entry := range result.Skipped {
		fmt.Fprintf(&b, "\n⚠ %s not restored (content not retained: %s)", entry.Rel, entry.Reason)
	}
	for _, note := range result.Notes {
		b.WriteString("\n" + note)
	}
	return b.String()
}

// formatRevertList renders the session's versions, oldest first, with the
// undo semantics spelled out.
func formatRevertList(store *snapshot.Store, sessionKey string) string {
	versions, err := store.Versions(sessionKey)
	if err != nil {
		return "Failed to list workspace versions: " + err.Error()
	}
	if len(versions) == 0 {
		return "No workspace versions recorded for this session — nothing to revert."
	}
	var b strings.Builder
	b.WriteString("🗂 Workspace versions (oldest first):")
	for _, v := range versions {
		fmt.Fprintf(&b, "\n  %s — %s — %d file(s)", v.ID, v.CommittedAt.Format("01-02 15:04:05"), len(v.Files))
		if v.ExecUsed {
			b.WriteString(" (exec)")
		}
	}
	b.WriteString("\n/revert undoes the newest turn; /revert <id> rolls the workspace back to before that turn.")
	return b.String()
}

// revertArg extracts the argument after "/revert" (empty for the bare form).
func revertArg(content string) string {
	_, ok := commands.CommandName(content)
	if !ok {
		return ""
	}
	tokens := strings.Fields(content)
	if len(tokens) < 2 {
		return ""
	}
	return strings.TrimSpace(tokens[1])
}
