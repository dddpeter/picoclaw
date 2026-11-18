package gateway

import (
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
)

// picoSessionModelController adapts the agent loop to the pico channel's
// SessionModelController interface (fork: session-scoped model selection).
//
// The web chat addresses a conversation by its pico session UUID; the agent
// loop keys sessions by the opaque routing key. The controller translates
// between the two and keeps the choice scoped to THAT conversation —
// config.json is never touched and the agent's global model never changes.
type picoSessionModelController struct {
	agentLoop *agent.AgentLoop
}

// newPicoSessionModelController builds the controller for a gateway agent loop.
func newPicoSessionModelController(agentLoop *agent.AgentLoop) *picoSessionModelController {
	return &picoSessionModelController{agentLoop: agentLoop}
}

// sessionKey resolves the agent-side session key of a pico session ID.
func (c *picoSessionModelController) sessionKey(sessionID string) string {
	if c == nil || c.agentLoop == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return c.agentLoop.SessionKeyForChannelSession(config.ChannelPico, "pico:"+sessionID)
}

// SetModel assigns a model to the session (an empty name clears the
// override, returning the session to the configured model).
func (c *picoSessionModelController) SetModel(sessionID, modelName string) error {
	if c == nil || c.agentLoop == nil {
		return errSessionModelUnavailable
	}
	sessionKey := c.sessionKey(sessionID)
	if sessionKey == "" {
		return errSessionModelUnavailable
	}
	return c.agentLoop.SetSessionModel(sessionKey, modelName)
}

// GetModel returns the model currently assigned to the session ("" = the
// session follows the configured model).
func (c *picoSessionModelController) GetModel(sessionID string) string {
	if c == nil || c.agentLoop == nil {
		return ""
	}
	sessionKey := c.sessionKey(sessionID)
	if sessionKey == "" {
		return ""
	}
	return c.agentLoop.GetSessionModel(sessionKey)
}

// ModelWindows returns the effective context window of the session's model
// and the threshold at which the session starts compressing.
//
// Both are 0 when the session follows the configured model — the client then
// uses the config default's window, so the context ring keeps working for
// sessions that never switched model.
func (c *picoSessionModelController) ModelWindows(sessionID string) (int, int) {
	if c == nil || c.agentLoop == nil {
		return 0, 0
	}
	sessionKey := c.sessionKey(sessionID)
	if sessionKey == "" {
		return 0, 0
	}
	snapshot := c.agentLoop.SessionModelSnapshot(sessionKey)
	if snapshot == nil {
		return 0, 0
	}
	compressAt := snapshot.ContextWindow
	if agent := c.agentLoop.AgentForSessionKey(sessionKey); agent != nil {
		compressAt = snapshot.ContextWindow - agent.MaxTokens
		if compressAt <= 0 {
			compressAt = snapshot.ContextWindow
		}
	}
	return snapshot.ContextWindow, compressAt
}

// errSessionModelUnavailable is returned when the agent loop cannot resolve
// the session (no gateway loop wired, or an empty session id).
var errSessionModelUnavailable = errors.New("session model selection is not available")
