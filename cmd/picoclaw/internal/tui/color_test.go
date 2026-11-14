package tui

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

var testColorSGR = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// Tests use actual styled strings rather than matching every possible SGR
// spelling. ASCII-profile conversion may keep bold/italic and reset sequences.
func assertNoColor(t *testing.T, text string) {
	t.Helper()
	for _, seq := range testColorSGR.FindAllString(text, -1) {
		params := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
		for p := range strings.SplitSeq(strings.ReplaceAll(params, ":", ";"), ";") {
			if p == "38" || p == "48" || p == "58" ||
				(len(p) == 2 && (p[0] == '3' || p[0] == '4' || p[0] == '9')) ||
				(len(p) == 3 && strings.HasPrefix(p, "10")) {
				t.Fatalf("color sequence remains in no-color output: %q", seq)
			}
		}
	}
}

func TestNoColorViewAndScrollback(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "TERM"} {
		for _, full := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fullscreen_%v", env, full), func(t *testing.T) {
				t.Setenv("NO_COLOR", "")
				t.Setenv("TERM", "xterm-256color")
				if env == "NO_COLOR" {
					t.Setenv(env, "1")
				} else {
					t.Setenv(env, "dumb")
				}
				m := newTestApp(t)
				m.setFullscreen(full)
				m.state.AddUser("USERCOLORCHECK")
				m.state.Items = append(m.state.Items, Item{Kind: ItemAnswer, Content: "**MARKDOWNCOLORCHECK** `code`"})
				m.syncViewport()
				assertNoColor(t, m.View().Content)
				if !full {
					cmd := m.commitAll()
					if cmd == nil {
						t.Fatal("missing scrollback output")
					}
					out := fmt.Sprint(cmd())
					assertNoColor(t, out)
					if !strings.Contains(out, "MARKDOWNCOLORCHECK") {
						t.Fatal("decolorizing lost markdown text")
					}
				}
			})
		}
	}
}

func TestNoColorStartupHeader(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newTestApp(t)
	cmds, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(cmds) != 2 {
		t.Fatal("expected startup command and header")
	}
	assertNoColor(t, fmt.Sprint(cmds[1]()))
}

func TestColorOutputEnabledByDefault(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	m := newTestApp(t)
	m.state.AddUser("colorful")
	out := m.View().Content
	if out == ansi.Strip(out) {
		t.Fatal("normal color mode should retain styled rendering")
	}
}

func TestNoColorFlagAndEnvironmentPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, noColor, term string
		flag, want          bool
	}{
		{"normal", "", "xterm-256color", false, false},
		{"flag", "", "xterm-256color", true, true},
		{"env", "1", "xterm-256color", false, true},
		{"dumb", "", "dumb", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			root := &cobra.Command{Use: "picoclaw"}
			root.PersistentFlags().Bool("no-color", tc.flag, "")
			cmd := NewTUICommand()
			root.AddCommand(cmd)
			if got := tuiColorDisabled(cmd); got != tc.want {
				t.Fatalf("color disabled = %v, want %v", got, tc.want)
			}
			m := newTestApp(t)
			m.noColor = tuiColorDisabled(cmd)
			if tc.want {
				assertNoColor(t, m.View().Content)
			}
		})
	}
}

func TestNoColorPreservesTextDecorationsAndControls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newTestApp(t)
	input := "\x1b[1;3;38;2;12;34;56;48;5;24mstyled text\x1b[0m\x1b[2J"
	out := m.outputText(input)
	assertNoColor(t, out)
	if !strings.Contains(out, "\x1b[1;3mstyled text") || !strings.HasSuffix(out, "\x1b[2J") {
		t.Fatalf("non-color styling or terminal controls lost: %q", out)
	}
	if ansi.Strip(input) != ansi.Strip(out) {
		t.Fatal("no-color conversion altered the visible text")
	}
}

func TestNoColorResetHeader(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newTestApp(t)
	// The reset command is a sequence of ClearScreen and the startup header.
	seq := reflect.ValueOf(m.resetMainScreen()())
	if seq.Kind() != reflect.Slice || seq.Len() != 2 {
		t.Fatal("expected ordered clear-screen/header sequence")
	}
	printHeader, ok := seq.Index(1).Interface().(tea.Cmd)
	if !ok {
		t.Fatal("missing startup-header print command")
	}
	assertNoColor(t, fmt.Sprint(printHeader()))
}
