package doctor

import (
	"github.com/spf13/cobra"
)

func NewDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run health checks and print a diagnostic report",
		Long: `Run a battery of health checks and print a ✅/⚠️/❌ report:

  1. config.json exists and parses
  2. workspace directory exists and is writable
  3. default model configured and reachable (network probe)
  4. enabled channels have credentials present
  5. security profile summary (deny_profile, restrict_to_workspace)

Exit code 0 when all critical checks pass; 1 otherwise. The report is
plain text so it can be pasted into an issue.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return doctorCmd(cmd.OutOrStdout())
		},
	}

	return cmd
}
