// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Default caps for tool output fed back into the LLM context. Lines is the
// primary cap; bytes is a safety net for pathological outputs.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
)

// TruncationOptions configures output truncation. Zero values fall back to
// DefaultMaxLines / DefaultMaxBytes.
type TruncationOptions struct {
	MaxLines int
	MaxBytes int
}

// TruncatedBy describes which limit triggered truncation.
type TruncatedBy string

const (
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// TruncationResult reports the kept content plus accounting about what was
// dropped, so callers can render an informative notice for the model.
type TruncationResult struct {
	Content               string
	Truncated             bool
	TruncatedBy           TruncatedBy
	TotalLines            int
	TotalBytes            int
	OutputLines           int
	OutputBytes           int
	LastLinePartial       bool
	FirstLineExceedsLimit bool
	MaxLines              int
	MaxBytes              int
}

// TruncateHead keeps the first lines/bytes of content. Suitable for outputs
// where the beginning matters (listings, file dumps). Never returns partial
// lines; if the first line alone exceeds the byte limit the result is empty
// with FirstLineExceedsLimit set.
func TruncateHead(content string, opts TruncationOptions) TruncationResult {
	maxLines, maxBytes := opts.resolve()
	lines := splitLinesForCounting(content)
	totalBytes := len(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:    content,
			Truncated:  false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	if len(lines[0]) > maxBytes {
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           TruncatedByBytes,
			FirstLineExceedsLimit: true,
			TotalLines:            totalLines, TotalBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	kept := make([]string, 0, maxLines)
	outputBytes := 0
	truncatedBy := TruncatedByLines
	for i := 0; i < len(lines) && i < maxLines; i++ {
		lineBytes := len(lines[i])
		if i > 0 {
			lineBytes++ // newline
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			break
		}
		kept = append(kept, lines[i])
		outputBytes += lineBytes
	}

	output := strings.Join(kept, "\n")
	return TruncationResult{
		Content:     output,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines, TotalBytes: totalBytes,
		OutputLines: len(kept), OutputBytes: len(output),
		MaxLines: maxLines, MaxBytes: maxBytes,
	}
}

// TruncateTail keeps the last lines/bytes of content. Suitable for command
// output where the end matters (errors, final results). When a single line
// exceeds the byte limit on its own, its last maxBytes bytes are kept
// (UTF-8 safe) and LastLinePartial is set.
func TruncateTail(content string, opts TruncationOptions) TruncationResult {
	maxLines, maxBytes := opts.resolve()
	lines := splitLinesForCounting(content)
	totalBytes := len(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:    content,
			Truncated:  false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	kept := make([]string, 0, maxLines)
	outputBytes := 0
	truncatedBy := TruncatedByLines
	lastLinePartial := false

	for i := len(lines) - 1; i >= 0 && len(kept) < maxLines; i-- {
		lineBytes := len(lines[i])
		if len(kept) > 0 {
			lineBytes++ // newline
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			if len(kept) == 0 {
				kept = append(kept, truncateStringToBytesFromEnd(lines[i], maxBytes))
				outputBytes = len(kept[0])
				lastLinePartial = true
			}
			break
		}
		kept = append([]string{lines[i]}, kept...)
		outputBytes += lineBytes
	}

	output := strings.Join(kept, "\n")
	return TruncationResult{
		Content:     output,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines, TotalBytes: totalBytes,
		OutputLines: len(kept), OutputBytes: len(output),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines, MaxBytes: maxBytes,
	}
}

// Notice renders a human/model-readable note about what was dropped, so the
// model knows the output was cut and where the shown portion sits.
func (r TruncationResult) Notice() string {
	if !r.Truncated {
		return ""
	}
	startLine := r.TotalLines - r.OutputLines + 1
	endLine := r.TotalLines
	switch {
	case r.LastLinePartial:
		return fmt.Sprintf("[Showing last %s of line %d (line is %d bytes).]",
			formatSize(r.OutputBytes), endLine, r.TotalBytes)
	case r.TruncatedBy == TruncatedByBytes:
		return fmt.Sprintf("[Showing lines %d-%d of %d (%s limit).]",
			startLine, endLine, r.TotalLines, formatSize(r.MaxBytes))
	default:
		return fmt.Sprintf("[Showing lines %d-%d of %d.]",
			startLine, endLine, r.TotalLines)
	}
}

func (o TruncationOptions) resolve() (int, int) {
	maxLines := o.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := o.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return maxLines, maxBytes
}

// splitLinesForCounting splits on \n and drops a single trailing empty element
// so a trailing newline does not count as a line.
func splitLinesForCounting(content string) []string {
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// truncateStringToBytesFromEnd keeps the last maxBytes bytes of s, snapped to
// a UTF-8 character boundary so multi-byte runes are not torn apart.
func truncateStringToBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func formatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}
