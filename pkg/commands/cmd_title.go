package commands

import (
	"context"
	"fmt"
	"strings"
)

// maxTitleRunes caps manual titles (derived ones are capped tighter in the
// agent package).
const maxTitleRunes = 80

// titleCommand names the current session manually. Manual titles outrank
// derived and light-model ones and are never overwritten by them.
func titleCommand() Definition {
	return Definition{
		Name:        "title",
		Description: "Show or set a title for the current conversation",
		Usage:       "/title [new title]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.SetSessionTitle == nil {
				return req.Reply(unavailableMsg)
			}
			// Parse: /title [rest] — first token is the command itself.
			arg := ""
			if text := strings.TrimSpace(req.Text); text != "" {
				if idx := strings.IndexAny(text, " \t"); idx >= 0 {
					arg = strings.TrimSpace(text[idx+1:])
				}
			}
			if arg == "" {
				if rt.GetSessionTitle == nil {
					return req.Reply(unavailableMsg)
				}
				current, source, ok := rt.GetSessionTitle()
				if !ok || current == "" {
					return req.Reply("This conversation has no title yet. Use /title <text> to set one.")
				}
				return req.Reply(fmt.Sprintf("Current title: %s (source: %s)", current, source))
			}
			if runes := []rune(arg); len(runes) > maxTitleRunes {
				arg = string(runes[:maxTitleRunes])
			}
			if !rt.SetSessionTitle(arg) {
				return req.Reply("Failed to set title: session titles are unavailable in this context.")
			}
			return req.Reply(fmt.Sprintf("🏷 Title set: %s", arg))
		},
	}
}
