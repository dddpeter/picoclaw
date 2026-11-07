package commands

import (
	"context"
	"strings"
)

// revertCommand undoes workspace checkpoint versions recorded by
// tools.checkpoint (trae borrowing 增量清单 §2.1). Bare /revert undoes the
// newest turn; /revert <version> cascades back to before that turn;
// /revert list shows the versions. The actual undo runs on the agent side
// (Runtime.RevertTurn) — the store lives with the gateway process.
func revertCommand() Definition {
	return Definition{
		Name:        "revert",
		Description: "Undo the workspace file changes made by the last turn(s) (workspace snapshots)",
		Usage:       "/revert [list|<version>]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.RevertTurn == nil {
				return req.Reply("Workspace snapshots are unavailable (tools.checkpoint is disabled).")
			}
			arg := ""
			if tokens := strings.Fields(req.Text); len(tokens) > 1 {
				arg = tokens[1]
			}
			reply, err := rt.RevertTurn(req.Channel, req.ChatID, arg)
			if err != nil {
				return req.Reply("Revert failed: " + err.Error())
			}
			return req.Reply(reply)
		},
	}
}
