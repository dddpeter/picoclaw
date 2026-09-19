package plugin

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

func newRemoveCommand() *cobra.Command {
	var purgeData bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin (keeps its data dir unless --purge-data)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			installRoot, err := agentplugins.DefaultInstallRoot()
			if err != nil {
				return err
			}
			if err := agentplugins.Remove(name, installRoot, purgeData); err != nil {
				return err
			}

			// Drop the registry entry (remove the file if it becomes empty).
			regPath := filepath.Join(installRoot, "registry.json")
			if reg, err := agentplugins.LoadRegistry(regPath); err == nil {
				if _, ok := reg.Entries[name]; ok {
					delete(reg.Entries, name)
					if err := reg.Save(); err != nil {
						return err
					}
				}
			}

			if purgeData {
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s (data purged)\n", name)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s (data kept under %s)\n", name, filepath.Join(installRoot, "data", name))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purgeData, "purge-data", false, "Also delete the plugin's PLUGIN_DATA directory")
	return cmd
}
