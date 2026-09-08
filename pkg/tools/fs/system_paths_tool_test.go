package fstools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tool-level pin for the system-directory floor: under the fork's default
// (restrict_to_workspace=false) the everyday file tools go through hostFs,
// which must still refuse protected OS system directories. This guards the
// wiring (the shared validator alone is NOT on that path).
func TestFileToolsRefuseSystemPathsWhenUnrestricted(t *testing.T) {
	t.Cleanup(func() { SetSystemPathProtection(true) })

	var systemFile, systemDir string
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			t.Skip("SystemRoot not set")
		}
		systemFile = filepath.Join(root, "System32", "drivers", "etc", "hosts")
		systemDir = filepath.Join(root, "System32")
	} else {
		systemFile = "/etc/passwd"
		systemDir = "/etc"
	}

	ws := t.TempDir()
	readTool := NewReadFileBytesTool(ws, false, 1<<16)
	result := readTool.Execute(context.Background(), map[string]any{"path": systemFile})
	if !result.IsError || !strings.Contains(result.ForLLM, "protected system directory") {
		t.Fatalf("read_file on %s must hit the system guard, got: %+v", systemFile, result)
	}

	writeTool := NewWriteFileTool(ws, false)
	result = writeTool.Execute(context.Background(), map[string]any{"path": systemFile, "content": "x"})
	if !result.IsError || !strings.Contains(result.ForLLM, "protected system directory") {
		t.Fatalf("write_file on %s must hit the system guard, got: %+v", systemFile, result)
	}

	listTool := NewListDirTool(ws, false)
	result = listTool.Execute(context.Background(), map[string]any{"path": systemDir})
	if !result.IsError || !strings.Contains(result.ForLLM, "protected system directory") {
		t.Fatalf("list_dir on %s must hit the system guard, got: %+v", systemDir, result)
	}

	// A normal path under the unrestricted default keeps working.
	normal := filepath.Join(ws, "notes.txt")
	result = writeTool.Execute(context.Background(), map[string]any{"path": normal, "content": "ok"})
	if result.IsError {
		t.Fatalf("write_file on workspace path should pass, got: %+v", result)
	}

	// Explicit opt-out removes the floor (read-only check — writing into a
	// real system dir in tests would be destructive on some CI runners).
	SetSystemPathProtection(false)
	readResult := readTool.Execute(context.Background(), map[string]any{"path": systemFile})
	if readResult.IsError && strings.Contains(readResult.ForLLM, "protected system directory") {
		t.Fatalf("guard disabled: read should not be refused, got: %+v", readResult)
	}
}

func TestSystemPathPrefixCaseFolding(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("case-sensitive platform: folding intentionally not applied")
	}
	var mixed string
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			t.Skip("SystemRoot not set")
		}
		// Upper-case spelling of the whole path (env root may be C:\WINDOWS).
		mixed = strings.ToUpper(root) + "\\SYSTEM32"
	} else {
		mixed = "/ETC/passwd"
	}
	if !IsProtectedSystemPath(mixed) {
		t.Fatalf("%s must be protected on case-insensitive platforms", mixed)
	}
}
