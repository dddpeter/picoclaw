package plugin

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed plugins and their components",
		RunE: func(cmd *cobra.Command, _ []string) error {
			installRoot, err := agentplugins.DefaultInstallRoot()
			if err != nil {
				return err
			}
			dataRoot, err := agentplugins.DefaultDataRoot()
			if err != nil {
				return err
			}
			plugins, rep := agentplugins.LoadPluginsDir(installRoot, dataRoot)

			out := cmd.OutOrStdout()
			if lines := reportLines(rep); lines != "" {
				fmt.Fprint(out, lines)
			}
			if len(plugins) == 0 {
				fmt.Fprintln(out, "No plugins installed.")
				return nil
			}

			fmt.Fprintf(out, "%-24s %-10s %-8s %-8s %-8s %s\n", "NAME", "VERSION", "SKILLS", "MCP", "ENABLED", "ROOT")
			for _, p := range plugins {
				skills, mcp := "-", "-"
				if p.Enabled {
					skills = fmt.Sprintf("%d", len(p.Skills))
					mcp = fmt.Sprintf("%d", len(p.MCPServers))
				}
				fmt.Fprintf(out, "%-24s %-10s %-8s %-8s %-8t %s\n", p.Name, p.Version, skills, mcp, p.Enabled, p.Root)
			}
			return nil
		},
	}
}
