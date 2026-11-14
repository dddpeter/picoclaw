package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
)

func envColorDisabled() bool {
	return os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
}

func tuiColorDisabled(cmd *cobra.Command) bool {
	noColor, _ := cmd.Root().PersistentFlags().GetBool("no-color")
	return noColor || envColorDisabled()
}

// outputText applies the same color policy to live views and unmanaged
// tea.Println output. The latter bypasses Bubble Tea's color-profile conversion.
// ASCII retains bold/italic and terminal controls, but removes all colors,
// including colors embedded by Glamour or nested Lip Gloss v2 styles.
func (m *appModel) outputText(text string) string {
	if !m.noColor {
		return text
	}
	var out strings.Builder
	writer := colorprofile.Writer{Forward: &out, Profile: colorprofile.ASCII}
	_, _ = writer.WriteString(text) // strings.Builder writes cannot fail.
	return out.String()
}

func (m *appModel) printLine(text string) tea.Cmd {
	return tea.Println(m.outputText(text))
}
