package plugin

import (
	"github.com/spf13/cobra"
)

// NewPluginCommand builds the `picoclaw plugin` command tree: install /
// remove / list / enable / disable / validate.
func NewPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage Agent Plugins Spec 1.0 packages",
		Long: `Manage Agent Plugins Spec 1.0 packages.

Plugins live under ~/.agents/plugins (spec example layout): each plugin is a
directory with plugin.json, optional skills/ and optional mcp.json. Their
skills and MCP servers are bridged into the agent on the next load.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newInstallCommand(),
		newRemoveCommand(),
		newListCommand(),
		newEnableCommand(true),
		newEnableCommand(false),
		newValidateCommand(),
	)
	return cmd
}
