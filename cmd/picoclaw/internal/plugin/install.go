package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

// reportLines renders a load report for CLI output.
func reportLines(rep *agentplugins.Report) string {
	var b strings.Builder
	for _, w := range rep.Warnings {
		b.WriteString("  warning: " + w + "\n")
	}
	for _, f := range rep.Fatals {
		b.WriteString("  fatal: " + f + "\n")
	}
	return b.String()
}

// loadInstalled loads a plugin whose data dir is <dataRoot>/<manifest name>.
func loadInstalled(target, dataRoot string) (*agentplugins.Plugin, error) {
	var rep agentplugins.Report
	m, err := agentplugins.LoadManifest(target, &rep)
	if err != nil {
		return nil, err
	}
	return agentplugins.LoadPlugin(target, filepath.Join(dataRoot, m.Name), true)
}

func newInstallCommand() *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "install <path|git-url>",
		Short: "Install a plugin from a local directory or a git URL",
		Long: `Install a plugin from a local directory or a git URL.

The source is validated before anything is copied; an already-installed
plugin is never overwritten (remove it first). For git sources, --ref pins a
branch or tag (commit SHAs are not supported); without --ref the default
branch is cloned.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			installRoot, err := agentplugins.DefaultInstallRoot()
			if err != nil {
				return err
			}

			var target string
			if st, statErr := os.Stat(source); statErr == nil && st.IsDir() {
				target, err = agentplugins.InstallFromLocal(source, installRoot)
			} else if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "git@") {
				target, err = agentplugins.InstallFromGit(source, ref, installRoot)
			} else {
				return fmt.Errorf("source %q is neither an existing directory nor a git URL", source)
			}
			if err != nil {
				return err
			}

			dataRoot, err := agentplugins.DefaultDataRoot()
			if err != nil {
				return err
			}
			p, err := loadInstalled(target, dataRoot)
			if err != nil {
				return fmt.Errorf("installed, but the plugin failed to load: %w", err)
			}

			// Register in the registry (install ⇒ enabled).
			reg, err := agentplugins.LoadRegistry(filepath.Join(installRoot, "registry.json"))
			if err != nil {
				return err
			}
			agentplugins.RegisterIn(reg, p.Name, p.Version, source, ref)
			if err := reg.Save(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Installed %s v%s -> %s\n", p.Name, p.Version, target)
			fmt.Fprintf(out, "OK: %s (%d skills, %d mcp servers)\n", p.Name, len(p.Skills), len(p.MCPServers))
			if lines := reportLines(p.Report); lines != "" {
				fmt.Fprint(out, lines)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Branch or tag to check out for git sources")
	return cmd
}
