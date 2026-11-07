package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveEditorCommand picks the external editor: $EDITOR → $VISUAL →
// platform default (notepad on Windows, vi elsewhere). The value may carry
// arguments ("code --wait"); returns the command line as-is (empty when
// nothing configured and platform default is used, caller sees fallback).
func resolveEditorCommand(env func(string) string) (cmdline string) {
	if e := strings.TrimSpace(env("EDITOR")); e != "" {
		return e
	}
	if e := strings.TrimSpace(env("VISUAL")); e != "" {
		return e
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

// buildEditorCmd assembles the exec.Cmd for editing filePath. On Windows the
// command runs through `cmd /c` so .cmd shims (code.cmd …) resolve like a
// shell (pi external-editor.ts 的 shell:true 同款处理).
func buildEditorCmd(cmdline, filePath string) *exec.Cmd {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		fields = []string{"vi"}
	}
	if runtime.GOOS == "windows" {
		args := append([]string{"/c"}, fields...)
		args = append(args, filePath)
		return exec.Command("cmd", args...)
	}
	return exec.Command(fields[0], append(fields[1:], filePath)...)
}

// newEditorTempFile creates the prompt scratch file pre-filled with the
// composer content; the caller owns removal of the directory.
func newEditorTempFile(content string) (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "picoclaw-tui-")
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, path, nil
}

// readEditorResult reads the edited file back: strips a UTF-8 BOM and a
// single trailing newline (pi external-editor.ts 同款清理).
func readEditorResult(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimPrefix(string(data), "\ufeff")
	return strings.TrimSuffix(s, "\n"), nil
}
