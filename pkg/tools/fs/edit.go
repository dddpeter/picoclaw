package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
)

// EditFileTool edits a file by replacing old_text with new_text.
// The old_text must exist exactly in the file.
type EditFileTool struct {
	fs fileSystem
}

// NewEditFileTool creates a new EditFileTool with optional directory restriction.
func NewEditFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *EditFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &EditFileTool{fs: buildFs(workspace, restrict, patterns)}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Description() string {
	return "Edit a file by replacing old_text with new_text. The old_text must exist in the file; line-ending differences are tolerated (\\n matches a CRLF file) and the file's line-ending style and text encoding (UTF-8/GBK) are preserved. Standard JSON escaping applies: \\n for newline and \\\\n for literal backslash-n."
}

func (t *EditFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to edit",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "The exact text to find and replace. Standard JSON escaping applies: \\n for newline and \\\\n for literal backslash-n.",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "The text to replace with. Standard JSON escaping applies: \\n for newline and \\\\n for literal backslash-n.",
			},
		},
		"required": []string{"path", "old_text", "new_text"},
	}
}

func (t *EditFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	oldText, ok := args["old_text"].(string)
	if !ok {
		return ErrorResult("old_text is required")
	}

	newText, ok := args["new_text"].(string)
	if !ok {
		return ErrorResult("new_text is required")
	}

	beforeContent, afterContent, err := editFile(t.fs, path, oldText, newText)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return DiffResult(path, beforeContent, afterContent)
}

type AppendFileTool struct {
	fs fileSystem
}

func NewAppendFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *AppendFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &AppendFileTool{fs: buildFs(workspace, restrict, patterns)}
}

func (t *AppendFileTool) Name() string {
	return "append_file"
}

func (t *AppendFileTool) Description() string {
	return "Append content to the end of a file. If the file uses CRLF or CR line endings, the appended content's line endings are adapted to match. Standard JSON escaping applies: \\n for newline and \\\\n for literal backslash-n."
}

func (t *AppendFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to append to",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to append. Standard JSON escaping applies: \\n for newline and \\\\n for literal backslash-n.",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *AppendFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	content, ok := args["content"].(string)
	if !ok {
		return ErrorResult("content is required")
	}

	if err := appendFile(t.fs, path, content); err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(fmt.Sprintf("Appended to %s", path))
}

// editFile reads the file via sysFs, performs the replacement, and writes back.
// It uses a fileSystem interface, allowing the same logic for both restricted and unrestricted modes.
//
// The file is decoded first (UTF-8 with/without BOM, or GB18030 for Chinese
// Windows files), edited as text, and re-encoded in the original encoding so
// the on-disk form never silently changes. The returned before/after pair is
// decoded text, so diffs render correctly regardless of encoding.
func editFile(sysFs fileSystem, path, oldText, newText string) ([]byte, []byte, error) {
	raw, err := sysFs.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	text, enc := decodeText(raw)

	newContent, err := replaceEditContent([]byte(text), oldText, newText)
	if err != nil {
		return nil, nil, err
	}

	encoded, err := encodeText(string(newContent), enc)
	if err != nil {
		return nil, nil, err
	}

	if err := sysFs.WriteFile(path, encoded); err != nil {
		return nil, nil, err
	}

	return []byte(text), newContent, nil
}

// appendFile reads the existing content (if any) via sysFs, appends new content, and writes back.
// Appended text is re-terminated in the file's dominant line-ending style
// (and written back in the file's encoding) so a single file never ends up
// with mixed line endings or mixed encodings.
func appendFile(sysFs fileSystem, path, appendContent string) error {
	raw, err := sysFs.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	text, enc := decodeText(raw)
	if len(text) > 0 {
		if style := detectEOLStyle(text); style != "\n" {
			appendContent = adaptEOLToStyle(appendContent, style)
		}
	}

	encoded, err := encodeText(text+appendContent, enc)
	if err != nil {
		return err
	}
	return sysFs.WriteFile(path, encoded)
}
