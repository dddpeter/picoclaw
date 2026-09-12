package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/session"
)

// sessionArchiveCapableStore is the optional metadata capability used by /new
// session rotation. The core data move only needs the SessionStore interface;
// scope and title carry-over is best-effort so stores without metadata
// support still rotate safely.
type sessionArchiveCapableStore interface {
	ArchiveSessionMetadata(liveKey, archiveKey string)
	ClearSessionTitle(sessionKey string)
}

// rotateSession archives the current conversation into its own session and
// clears the live one, so /new starts a fresh session entry while the
// previous chat stays browsable in history views (web UI session list).
//
// The live session key is deterministic per chat (derived from the session
// scope), so rotation keeps the key and moves the old history to a fresh
// archive key instead — the next message in the chat continues into the
// cleared live session.
func (al *AgentLoop) rotateSession(ctx context.Context, agent *AgentInstance, opts *processOptions) (bool, error) {
	sessionKey := opts.SessionKey
	if sessionKey == "" {
		sessionKey = opts.Dispatch.SessionKey
	}
	if sessionKey == "" {
		return false, fmt.Errorf("session key not available")
	}

	// Routed (non-default) agents keep history in their own session store,
	// so resolve the owning agent instead of assuming the default one.
	sessions := agent.Sessions
	if resolved := al.agentForSession(sessionKey); resolved != nil && resolved.Sessions != nil {
		sessions = resolved.Sessions
	}
	if sessions == nil {
		return false, fmt.Errorf("sessions not initialized")
	}

	history := sessions.GetHistory(sessionKey)
	summary := strings.TrimSpace(sessions.GetSummary(sessionKey))

	if len(history) == 0 && summary == "" {
		// Nothing to preserve; the session is already fresh.
		clearSessionTitleMetadata(sessions, sessionKey)
		return false, al.contextManager.Clear(ctx, sessionKey)
	}

	archiveKey := session.BuildOpaqueSessionKey(
		"archive:" + sessionKey + ":" + strconv.FormatInt(time.Now().UnixNano(), 36),
	)
	sessions.SetHistory(archiveKey, history)
	sessions.SetSummary(archiveKey, summary)
	if err := sessions.Save(archiveKey); err != nil {
		return false, fmt.Errorf("persist archived session: %w", err)
	}
	if metaStore, ok := sessions.(sessionArchiveCapableStore); ok {
		metaStore.ArchiveSessionMetadata(sessionKey, archiveKey)
	}

	if err := al.contextManager.Clear(ctx, sessionKey); err != nil {
		return false, err
	}
	// Drop the live session's stored title so the new conversation is
	// re-titled from its own first message instead of inheriting the
	// archived one.
	clearSessionTitleMetadata(sessions, sessionKey)
	return true, nil
}

func clearSessionTitleMetadata(store session.SessionStore, sessionKey string) {
	if metaStore, ok := store.(sessionArchiveCapableStore); ok {
		metaStore.ClearSessionTitle(sessionKey)
	}
}
