package commands

import "context"

// newCommand starts a fresh conversation: it clears the current session's
// history so the next message begins with a clean context. It is the
// conversation-level reset users expect from chat UX ("/new chat"), while
// /clear stays the history-only wording.
func newCommand() Definition {
	return Definition{
		Name:        "new",
		Description: "Start a new conversation (clears history)",
		Usage:       "/new",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ClearHistory == nil {
				return req.Reply(unavailableMsg)
			}
			if err := rt.ClearHistory(); err != nil {
				return req.Reply("Failed to start a new conversation: " + err.Error())
			}
			return req.Reply("🆕 New conversation started! Chat history cleared.")
		},
	}
}
