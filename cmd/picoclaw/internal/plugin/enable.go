package plugin

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

// newEnableCommand builds both `enable` and `disable` (they differ only in
// the flag value they write to registry.json).
func newEnableCommand(enable bool) *cobra.Command {
	use, short := "enable", "Enable"
	if !enable {
		use, short = "disable", "Disable"
	}
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short + " an installed plugin (takes effect on next load)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			installRoot, err := agentplugins.DefaultInstallRoot()
			if err != nil {
				return err
			}
			reg, err := agentplugins.LoadRegistry(filepath.Join(installRoot, "registry.json"))
			if err != nil {
				return err
			}
			if err := reg.SetEnabled(name, enable); err != nil {
				return err
			}
			if err := reg.Save(); err != nil {
				return err
			}
			state := "enabled"
			if !enable {
				state = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", name, state)
			return nil
		},
	}
}
