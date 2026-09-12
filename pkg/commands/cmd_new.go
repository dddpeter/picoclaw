package commands

import (
	"context"
	"fmt"
)

// newCommand starts a fresh conversation: the previous chat is archived into
// its own session (preserved for history views) and the live session starts
// empty, and the model is restored to the configured default (re-reading the
// config file, so a newly edited default applies even when hot reload is
// off). It is the conversation-level reset users expect from chat UX
// ("/new chat"), while /clear stays the history-only wording.
func newCommand() Definition {
	return Definition{
		Name:        "new",
		Description: "Start a new conversation (archives the previous chat, resets model to config default)",
		Usage:       "/new",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil {
				return req.Reply(unavailableMsg)
			}
			archived := false
			switch {
			case rt.NewSession != nil:
				archivedResult, err := rt.NewSession()
				if err != nil {
					return req.Reply("Failed to start a new conversation: " + err.Error())
				}
				archived = archivedResult
			case rt.ClearHistory != nil:
				if err := rt.ClearHistory(); err != nil {
					return req.Reply("Failed to start a new conversation: " + err.Error())
				}
			default:
				return req.Reply(unavailableMsg)
			}
			reply := "🆕 New conversation started!"
			if archived {
				reply += " Previous chat archived."
			}
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
