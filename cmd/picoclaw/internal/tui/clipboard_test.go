package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRunner routes commands by name through a script table.
type fakeRunner struct {
	scripts map[string]func(args []string) ([]byte, error)
	calls   []string
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if fn, ok := f.scripts[name]; ok {
		return fn(args)
	}
	return nil, fmt.Errorf("%s: not found", name)
}

func pngBytes() []byte {
	// Minimal PNG header (magic + IHDR chunk start) — enough for byte checks.
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
}

func TestClipboard_DataURLAndSizeCap(t *testing.T) {
	img := &clipboardImage{data: pngBytes(), mimeType: "image/png"}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes())
	if got := img.dataURL(); got != want {
		t.Fatalf("dataURL wrong: %s", got)
	}
	if !img.sizeOK() {
		t.Fatal("small image must pass")
	}
	img.data = make([]byte, maxClipboardImageBytes+1)
	if img.sizeOK() {
		t.Fatal("oversized image must fail")
	}
}

func TestClipboardDarwinHex(t *testing.T) {
	png := pngBytes()
	hexed := make([]byte, 0, len(png)*2)
	const hexdigits = "0123456789ABCDEF"
	for _, b := range png {
		hexed = append(hexed, hexdigits[b>>4], hexdigits[b&0xF])
	}
	fr := &fakeRunner{scripts: map[string]func([]string) ([]byte, error){
		"osascript": func([]string) ([]byte, error) {
			return []byte("«data PNGf" + string(hexed) + ";»"), nil
		},
	}}
	r := &clipboardReader{run: fr.run}
	img, err := r.readDarwin()
	if err != nil || img == nil {
		t.Fatalf("darwin read failed: %v", err)
	}
	if string(img.data) != string(png) || img.mimeType != "image/png" {
		t.Fatalf("decoded wrong: %q %s", img.data, img.mimeType)
	}

	// 非 PNG 剪贴板（如 TIFF）→ 明确的“无图片”。
	fr.scripts["osascript"] = func([]string) ([]byte, error) {
		return []byte("«class TIFFf»"), nil
	}
	if _, err := r.readDarwin(); err != ErrNoClipboardImage {
		t.Fatalf("want ErrNoClipboardImage, got %v", err)
	}
}

func TestClipboardLinuxTypeSelection(t *testing.T) {
	png := pngBytes()
	fr := &fakeRunner{scripts: map[string]func([]string) ([]byte, error){
		"wl-paste": func(args []string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--list-types" {
				return []byte("text/plain\nimage/jpeg\nimage/png\n"), nil
			}
			return png, nil
		},
	}}
	r := &clipboardReader{run: fr.run}
	img, err := r.readLinux()
	if err != nil || img == nil || string(img.data) != string(png) {
		t.Fatalf("wayland read failed: %v", err)
	}
	// 首选 png（列表顺序无关）。
	last := fr.calls[len(fr.calls)-1]
	if !strings.Contains(last, "--type image/png") {
		t.Fatalf("should request image/png: %s", last)
	}

	// 无剪贴板工具 → ErrNoClipboardTool。
	fr2 := &fakeRunner{scripts: map[string]func([]string) ([]byte, error){}}
	r2 := &clipboardReader{run: fr2.run}
	if _, err := r2.readLinux(); err != ErrNoClipboardTool {
		t.Fatalf("want ErrNoClipboardTool, got %v", err)
	}

	// 有工具但无图片类型 → ErrNoClipboardImage。
	fr3 := &fakeRunner{scripts: map[string]func([]string) ([]byte, error){
		"wl-paste": func([]string) ([]byte, error) { return []byte("text/plain\n"), nil },
	}}
	r3 := &clipboardReader{run: fr3.run}
	if _, err := r3.readLinux(); err != ErrNoClipboardImage {
		t.Fatalf("want ErrNoClipboardImage, got %v", err)
	}
}

func TestClipboardWindowsFlow(t *testing.T) {
	png := pngBytes()
	r := &clipboardReader{run: func(name string, args ...string) ([]byte, error) {
		if name != "powershell" {
			return nil, fmt.Errorf("unexpected %s", name)
		}
		// 从脚本里提取 $path = '<tmp>'，模拟 GetImage 保存成功。
		script := strings.Join(args, " ")
		i := strings.Index(script, "$path = '")
		if i < 0 {
			return nil, fmt.Errorf("no $path in script")
		}
		rest := script[i+len("$path = '"):]
		j := strings.Index(rest, "'")
		if j < 0 {
			return nil, fmt.Errorf("unterminated $path")
		}
		if err := os.WriteFile(rest[:j], png, 0o644); err != nil {
			return nil, err
		}
		return []byte("ok"), nil
	}}
	img, err := r.readWindows()
	if err != nil || img == nil || string(img.data) != string(png) || img.mimeType != "image/png" {
		t.Fatalf("windows read failed: %v %+v", err, img)
	}

	// ok 但文件没写成 → 无图片；empty → 无图片；命令失败 → 无工具。
	base := []byte("empty")
	r2 := &clipboardReader{run: func(string, ...string) ([]byte, error) { return base, nil }}
	if _, err := r2.readWindows(); err != ErrNoClipboardImage {
		t.Fatalf("empty clipboard: %v", err)
	}
	r3 := &clipboardReader{run: func(string, ...string) ([]byte, error) { return nil, fmt.Errorf("no ps") }}
	if _, err := r3.readWindows(); err != ErrNoClipboardTool {
		t.Fatalf("no tool: %v", err)
	}
}

func TestPreferredImageType(t *testing.T) {
	if got := preferredImageType([]string{"text/plain", "image/jpeg", "image/png"}); got != "image/png" {
		t.Fatalf("png preferred: %q", got)
	}
	if got := preferredImageType([]string{"image/webp;format=alpha", "image/gif"}); got != "image/webp;format=alpha" {
		t.Fatalf("raw type preserved: %q", got)
	}
	if got := preferredImageType([]string{"text/plain"}); got != "" {
		t.Fatalf("no image: %q", got)
	}
}

func TestEditorCommandResolution(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "EDITOR":
			return "code --wait"
		case "VISUAL":
			return ""
		}
		return ""
	}
	if got := resolveEditorCommand(env); got != "code --wait" {
		t.Fatalf("EDITOR wins with args: %q", got)
	}
	env2 := func(k string) string {
		if k == "VISUAL" {
			return "vim"
		}
		return ""
	}
	if got := resolveEditorCommand(env2); got != "vim" {
		t.Fatalf("VISUAL fallback: %q", got)
	}
	def := resolveEditorCommand(func(string) string { return "" })
	want := "notepad"
	if runtime.GOOS != "windows" {
		want = "vi"
	}
	if def != want {
		t.Fatalf("platform default: %q want %q", def, want)
	}
}

func TestEditorBuildCmd(t *testing.T) {
	cmd := buildEditorCmd("code --wait", "f.md")
	if runtime.GOOS == "windows" {
		if len(cmd.Args) < 4 || cmd.Args[0] != "cmd" || cmd.Args[1] != "/c" {
			t.Fatalf("windows must wrap via cmd /c: %v", cmd.Args)
		}
		if cmd.Args[len(cmd.Args)-1] != "f.md" {
			t.Fatalf("file must be last: %v", cmd.Args)
		}
	} else {
		if len(cmd.Args) != 3 || cmd.Args[0] != "code" || cmd.Args[2] != "f.md" {
			t.Fatalf("unix cmd wrong: %v", cmd.Args)
		}
	}
}

func TestEditorTempFileRoundTrip(t *testing.T) {
	dir, path, err := newEditorTempFile("初始内容")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if filepath.Base(path) != "prompt.md" {
		t.Fatalf("file name: %s", path)
	}
	got, err := readEditorResult(path)
	if err != nil || got != "初始内容" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	// BOM + 尾换行清理。
	bom := "\ufeff新内容\n"
	if err := os.WriteFile(path, []byte(bom), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := readEditorResult(path); got != "新内容" {
		t.Fatalf("BOM/newline strip failed: %q", got)
	}
}
