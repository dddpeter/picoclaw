// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"strings"
	"testing"
)

// TestStripToolCallParrot pins the display filter for seahorse tool-call
// parrot lines (fork, 2026-10-05): models that see "[tool_use: name,
// args: ...]" lines in their seahorse history replay them as visible answer
// content; streaming surfaces must render prose only.
func TestStripToolCallParrot(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		same  bool // want output identical to input (fast path or no-op)
		fence bool // content legitimately quoting the format in a fence
	}{
		{
			// Mirrors the observed parrot shape: args carry literal \n
			// sequences (JSON-escaped on the wire), so each block is one line.
			name: "pure parrot blocks vanish",
			in: "[tool_use: exec, args: {\"action\":\"run\",\"command\":\"echo \\\"=== 1. ===\\\"\\nstat -c '%n mtime=%y size=%s' /usr/bin/picoclaw 2\\u003e/dev/null\"}]\n" +
				"\n" +
				"[tool_use: exec, args: {\"action\":\"run\",\"command\":\"strings /usr/bin/picoclaw | head -10\"}]",
			want: "",
		},
		{
			name: "parrot removed, prose kept",
			in:   "[tool_use: exec, args: {\"command\":\"ls\"}]\n\n我先看一下文件，再给你结论。\n\n[tool_result for call_1: total 0]\n\n结论：目录是空的。",
			want: "我先看一下文件，再给你结论。\n\n结论：目录是空的。",
		},
		{
			name:  "code fence quote protected",
			in:    "历史格式长这样：\n```\n[tool_use: exec, args: {\"command\":\"ls\"}]\n```\n以上。",
			same:  true,
			fence: true,
		},
		{
			name: "no markers fast path returns verbatim",
			in:   "普通回答，没有任何标记。\n第二行。",
			same: true,
		},
		{
			name: "prose mentioning marker inline survives",
			in:   "它输出了一行 [tool_use: exec, args: {\"command\":\"ls\"}] 作为前缀，然后继续。",
			same: true,
		},
		{
			name:  "media marker dropped",
			in:    "[media: pic.png (image/png)]\n看这张图。",
			want:  "看这张图。",
			fence: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripToolCallParrot(tc.in)
			if tc.same {
				if got != tc.in {
					t.Fatalf("stripToolCallParrot() changed input:\n got  %q\n want %q", got, tc.in)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("stripToolCallParrot() =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// TestStripToolCallParrot_BlankRunCollapse pins the whitespace repair: after
// dropping interleaved parrot blocks the remaining prose keeps at most one
// blank line between paragraphs.
func TestStripToolCallParrot_BlankRunCollapse(t *testing.T) {
	in := "[tool_use: a, args: {}]\n\n\n\n第一段。\n\n[tool_use: b, args: {}]\n\n\n第二段。"
	got := stripToolCallParrot(in)
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("blank runs not collapsed: %q", got)
	}
	want := "第一段。\n\n第二段。"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
