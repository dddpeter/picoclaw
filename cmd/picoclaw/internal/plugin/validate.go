package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a plugin directory without installing it (CI friendly)",
		Long: `Validate a plugin directory without installing it.

Runs the full spec load path: closed plugin.json validation, flat skill
discovery, mcp.json validation and path containment. Exits non-zero with the
fatal diagnostics when the plugin is rejected.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := args[0]
			if st, err := os.Stat(root); err != nil || !st.IsDir() {
				return fmt.Errorf("plugin path %q is not an accessible directory", root)
			}

			// Provisional PLUGIN_DATA (validate never launches anything; the
			// data dir is only used for placeholder expansion containment).
			dataDir := filepath.Join(os.TempDir(), "picoclaw-plugin-validate", "data")
			p, err := agentplugins.LoadPlugin(root, dataDir, true)
			if err != nil {
				return fmt.Errorf("INVALID: %v", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "OK: %s (%d skills, %d mcp servers)\n", p.Name, len(p.Skills), len(p.MCPServers))
			for _, s := range p.Skills {
				fmt.Fprintf(out, "  skill: %s\n", s.Name)
			}
			for name, srv := range p.MCPServers {
				fmt.Fprintf(out, "  mcp: %s (%s)\n", name, srv.Type)
			}
			if lines := reportLines(p.Report); lines != "" {
				fmt.Fprint(out, strings.TrimRight(lines, "\n")+"\n")
			}
			return nil
		},
	}
}
