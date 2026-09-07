package commands

import (
	"context"
	"fmt"
)

// newCommand starts a fresh conversation: it clears the current session's
// history so the next message begins with a clean context, and restores the
// model configured as default (re-reading the config file, so a newly edited
// default applies even when hot reload is off). It is the conversation-level
// reset users expect from chat UX ("/new chat"), while /clear stays the
// history-only wording.
func newCommand() Definition {
	return Definition{
		Name:        "new",
		Description: "Start a new conversation (clears history, resets model to config default)",
		Usage:       "/new",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ClearHistory == nil {
				return req.Reply(unavailableMsg)
			}
			if err := rt.ClearHistory(); err != nil {
				return req.Reply("Failed to start a new conversation: " + err.Error())
			}
			reply := "🆕 New conversation started! Chat history cleared."
			if rt.ResetModel != nil {
				if model, err := rt.ResetModel(); err != nil {
					reply += fmt.Sprintf(" ⚠ Model reset failed: %v", err)
				} else if model != "" {
					reply += fmt.Sprintf(" Model: %s", model)
				}
			}
			return req.Reply(reply)
		},
	}
}
