package tools

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Inline command-output cleaning, borrowed from MiMoCode's token-efficient
// mode (see docs/design/mimo-code-borrowing-analysis.zh.md, item 2, slim
// version): only the text sent back to the model is cleaned. Raw output is
// untouched — persistence (persistFullOutput) and truncation notice wording
// behave exactly as before. Every stage is best-effort; if the pipeline does
// not shrink the output, the original is returned (never-worse guard).

const (
	cleanLongLineThreshold = 500 // runes; longer lines get compressed
	cleanLongLineHead      = 160 // runes kept at the head of a compressed line
)

var (
	// CSI sequences (colors, cursor movement): ESC [ params final-byte.
	ansiCSIPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	// OSC sequences (window title, hyperlinks): ESC ] ... (BEL or ST).
	ansiOSCPattern = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	// DCS/SOS/PM/APC sequences: ESC P/^/_ ... ST.
	ansiDCSPattern = regexp.MustCompile("\x1b[PUX^_][^\x1b]*\x1b\\\\")
	// Remaining two-byte ESC sequences and stray control bytes (backspace
	// overstrike included). Tab, LF are kept; CR never survives
	// collapseProgressLines in practice but is kept for split CRLF pairs.
	ansiStrayPattern   = regexp.MustCompile("\x1b[@-Z\\-_]")
	controlBytePattern = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
)

// CleanCommandOutput applies the inline cleaning pipeline to a command's
// output. The command participates in the decision: invocations that produce
// structured or teed output are passed through untouched so nothing the user
// (or a downstream parser) relies on gets reshaped.
func CleanCommandOutput(command, output string) string {
	if outputCleaningBypassed(command) {
		return output
	}
	cleaned := collapseProgressLines(output)
	cleaned = stripANSI(cleaned)
	cleaned = compressLongLines(cleaned)
	if len(cleaned) >= len(output) {
		return output
	}
	return cleaned
}

func outputCleaningBypassed(command string) bool {
	c := strings.ToLower(command)
	return strings.Contains(c, "--json") ||
		strings.Contains(c, "-o json") ||
		strings.Contains(c, "| tee") ||
		strings.Contains(c, "# nofilter")
}

// collapseProgressLines keeps only the final state of carriage-return redraws
// (progress bars, spinners). It must run before ANSI stripping: the redraw
// frames are exactly what carries the escape noise.
func collapseProgressLines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// A \r at the very end of the segment is the CR of a CRLF line
		// ending (every Windows shell), not a redraw — keep the content.
		// Only a \r with content after it inside the segment moves the
		// cursor back for a redraw.
		if strings.HasSuffix(line, "\r") {
			line = line[:len(line)-1]
		}
		if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
			line = line[idx+1:]
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		s = controlBytePattern.ReplaceAllString(s, "")
		return s
	}
	s = ansiCSIPattern.ReplaceAllString(s, "")
	s = ansiOSCPattern.ReplaceAllString(s, "")
	s = ansiDCSPattern.ReplaceAllString(s, "")
	s = ansiStrayPattern.ReplaceAllString(s, "")
	return controlBytePattern.ReplaceAllString(s, "")
}

func compressLongLines(s string) string {
	if !strings.ContainsRune(s, '\n') {
		return compressSingleLongLine(s)
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i, line := range lines {
		compressed := compressSingleLongLine(line)
		if compressed != line {
			lines[i] = compressed
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

func compressSingleLongLine(line string) string {
	runes := []rune(line)
	if len(runes) <= cleanLongLineThreshold {
		return line
	}
	head := string(runes[:cleanLongLineHead])
	if !utf8.ValidString(head) {
		return line
	}
	return head + fmt.Sprintf("…[%d chars omitted]", len(runes)-cleanLongLineHead)
}
