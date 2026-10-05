package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

var (
	styleUser         = lipgloss.NewStyle().Bold(true)
	styleSteering     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleThought      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleTool         = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	styleToolFeedback = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleAnswerStream = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	styleAnswer       = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	styleError        = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleAction       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleProgress     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleHint         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleStatusbar    = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
)

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// connIndicator renders the statusbar connection dot.
func connIndicator(s picoclient.ConnState) (string, string) {
	switch s {
	case picoclient.StateConnected:
		return "●", "3" // green
	case picoclient.StateConnecting:
		return "◌", "179" // amber
	default:
		return "○", "203" // red
	}
}

// renderTimeline renders every state item; width is the wrap width.
func renderTimeline(s *State, width int) string {
	if width < 10 {
		width = 10
	}
	var b strings.Builder
	for i := range s.Items {
		renderItem(&b, &s.Items[i], width)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func renderItem(b *strings.Builder, it *Item, width int) {
	switch it.Kind {
	case ItemUser:
		line := "▊ " + it.Content
		if it.Steering {
			line += " " + styleSteering.Render("↩ 并入当前回合")
		}
		b.WriteString(styleUser.Width(width).Render(line))

	case ItemThought:
		b.WriteString(renderThought(it, width))

	case ItemToolCalls:
		line := "⚙ " + it.ToolName
		if args := firstLine(it.ToolArgs); args != "" {
			line += " " + truncateRunes(args, max(8, width-len(line)-4))
		}
		b.WriteString(styleTool.Width(width).Render(line))

	case ItemToolFeedback:
		b.WriteString(styleToolFeedback.Width(width).Render(firstLine(it.Content)))

	case ItemAnswer:
		st := styleAnswer
		if it.Streaming {
			st = styleAnswerStream
		}
		if it.Content == "" {
			b.WriteString(st.Width(width).Render("…"))
		} else {
			b.WriteString(st.Width(width).Render(it.Content))
		}

	case ItemError:
		line := "⚠ "
		if it.Code != "" {
			line += it.Code + ": "
		}
		line += it.ErrMessage
		b.WriteString(styleError.Width(width).Render(line))

	case ItemAction:
		b.WriteString(styleAction.Width(width).Render("· " + it.Content))

	case ItemMedia:
		names := make([]string, 0, len(it.Attachments))
		for _, att := range it.Attachments {
			name := att.Filename
			if name == "" {
				name = att.URL
			}
			names = append(names, name)
		}
		line := "📎 " + strings.Join(names, ", ")
		b.WriteString(styleAnswer.Width(width).Render(line))
	}
}

// renderThought renders the folding reasoning block: streaming shows the tail
// lines, collapsed shows a one-line summary (Ctrl+O toggles).
func renderThought(it *Item, width int) string {
	n := utf8.RuneCountInString(it.Content)
	header := "💭 "
	switch {
	case it.Streaming:
		header += fmt.Sprintf("思考中…（%d 字，Ctrl+O 展开/收起）", n)
	case it.Expanded:
		header += fmt.Sprintf("已思考 %d 字（Ctrl+O 收起）", n)
	default:
		header += fmt.Sprintf("已思考 %d 字（Ctrl+O 展开）", n)
	}

	if it.Expanded || it.Streaming {
		lines := strings.Split(strings.TrimRight(it.Content, "\n"), "\n")
		if !it.Expanded && len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		body := styleThought.Render(strings.Join(lines, "\n"))
		return styleThought.Width(width).Render(header) + "\n" + body
	}
	return styleThought.Width(width).Render(header)
}

// renderStatusbar renders the top line: identity, connection, model, usage.
func renderStatusbar(m *appModel) string {
	dot, color := connIndicator(m.connState)
	dotStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))

	var b strings.Builder
	b.WriteString("picoclaw tui ")
	b.WriteString(dotStyle.Render(dot + " " + m.connState.String()))
	if m.connState != picoclient.StateConnected {
		if dialErr := tail(m.client.LastDialError(), 72); dialErr != "" {
			b.WriteString(styleError.Render(" · " + dialErr))
		}
	}
	if session := m.client.SessionID(); len(session) > 8 {
		b.WriteString(styleHint.Render(" · " + session[:8]))
	}
	if m.state.ModelName != "" {
		b.WriteString(styleStatusbar.Render(" · " + m.state.ModelName))
	}
	if u := m.state.CtxUsage; u != nil && u.TotalTokens > 0 {
		b.WriteString(styleHint.Render(fmt.Sprintf(" · ctx %.0f%% (%dk/%dk)",
			u.UsedPercent, u.UsedTokens/1000, u.TotalTokens/1000)))
	}
	if m.state.Generating {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		b.WriteString(styleStatusbar.Render(" · " + string(frame) + " 生成中 " +
			elapsed(m.state.GeneratingSince)))
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(b.String())
}

// tail keeps the last n runes of s (dial errors put the OS reason at the end).
func tail(s string, n int) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func elapsed(since time.Time) string {
	d := time.Since(since).Truncate(time.Second)
	return d.String()
}

func renderHint(width int) string {
	hint := "Enter 发送 · Shift+Enter 换行 · Esc 停止回合 · Ctrl+N 新会话 · Ctrl+O 展开思考 · Ctrl+C 退出"
	return styleHint.MaxWidth(width).Render(hint)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
