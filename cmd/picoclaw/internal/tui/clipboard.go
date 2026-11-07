package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// clipboardImage is one image read from the system clipboard.
type clipboardImage struct {
	data     []byte
	mimeType string
}

// clipboardReader abstracts the platform clipboard probe so tests can inject
// fake command runners.
type clipboardReader struct {
	// run executes a command and returns stdout; err != nil means the tool
	// is unavailable.
	run func(name string, args ...string) ([]byte, error)
}

func newClipboardReader() *clipboardReader {
	return &clipboardReader{run: runCommandOutput}
}

func runCommandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// ErrNoClipboardImage: the clipboard is reachable but holds no image.
var ErrNoClipboardImage = errors.New("剪贴板中没有图片")

// ErrNoClipboardTool: no platform clipboard tool available (ssh/远端会话).
var ErrNoClipboardTool = errors.New("无法访问本地剪贴板（ssh/远程会话或缺少剪贴板工具）")

// maxClipboardImageBytes mirrors the gateway media cap (20MB decoded,
// pkg/config DefaultMaxMediaSize) so oversized images fail client-side.
const maxClipboardImageBytes = 20 * 1024 * 1024

// readImage probes the platform clipboard and returns the image bytes.
func (r *clipboardReader) readImage() (*clipboardImage, error) {
	switch runtime.GOOS {
	case "windows":
		return r.readWindows()
	case "darwin":
		return r.readDarwin()
	default:
		return r.readLinux()
	}
}

// readWindows saves the clipboard image to a temp PNG via PowerShell
// (System.Windows.Forms; powershell.exe v5.1 console host 默认 STA).
func (r *clipboardReader) readWindows() (*clipboardImage, error) {
	tmp, err := os.CreateTemp("", "picoclaw-clip-*.png")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)

	ps := strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms",
		"Add-Type -AssemblyName System.Drawing",
		fmt.Sprintf("$path = '%s'", strings.ReplaceAll(name, "'", "''")),
		"$img = [System.Windows.Forms.Clipboard]::GetImage()",
		"if ($img) { $img.Save($path, [System.Drawing.Imaging.ImageFormat]::Png); Write-Output 'ok' } else { Write-Output 'empty' }",
	}, "; ")
	out, err := r.run("powershell", "-NoProfile", "-Command", ps)
	if err != nil {
		return nil, ErrNoClipboardTool
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return nil, ErrNoClipboardImage
	}
	data, err := os.ReadFile(name)
	if err != nil || len(data) == 0 {
		return nil, ErrNoClipboardImage
	}
	return &clipboardImage{data: data, mimeType: "image/png"}, nil
}

// osascriptHexRe matches the hex payload of `osascript -e 'the clipboard as
// «class PNGf»'` output: «data PNGf<hex>;».
var osascriptHexRe = regexp.MustCompile(`«data PNGf([0-9A-Fa-f]+);»`)

// readDarwin reads a PNG from the macOS pasteboard.
func (r *clipboardReader) readDarwin() (*clipboardImage, error) {
	out, err := r.run("osascript", "-e", "the clipboard as «class PNGf»")
	if err != nil {
		return nil, ErrNoClipboardTool
	}
	m := osascriptHexRe.FindSubmatch(out)
	if m == nil {
		return nil, ErrNoClipboardImage
	}
	data, hexErr := decodeHex(m[1])
	if hexErr != nil || len(data) == 0 {
		return nil, ErrNoClipboardImage
	}
	return &clipboardImage{data: data, mimeType: "image/png"}, nil
}

func decodeHex(s []byte) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("odd hex length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			return nil, errors.New("bad hex digit")
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, nil
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// readLinux prefers Wayland (wl-paste) when present, then X11 (xclip).
func (r *clipboardReader) readLinux() (*clipboardImage, error) {
	if out, err := r.run("wl-paste", "--list-types"); err == nil {
		if t := preferredImageType(strings.Split(string(out), "\n")); t != "" {
			if data, err := r.run("wl-paste", "--type", t, "--no-newline"); err == nil && len(data) > 0 {
				return &clipboardImage{data: data, mimeType: strings.SplitN(t, ";", 2)[0]}, nil
			}
			return nil, ErrNoClipboardImage
		}
		return nil, ErrNoClipboardImage
	}
	if out, err := r.run("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"); err == nil {
		if t := preferredImageType(strings.Split(string(out), "\n")); t != "" {
			if data, err := r.run("xclip", "-selection", "clipboard", "-t", t, "-o"); err == nil && len(data) > 0 {
				return &clipboardImage{data: data, mimeType: strings.SplitN(t, ";", 2)[0]}, nil
			}
		}
		return nil, ErrNoClipboardImage
	}
	return nil, ErrNoClipboardTool
}

// preferredImageType picks png > jpeg > webp > gif from TARGETS output.
func preferredImageType(types []string) string {
	preferred := []string{"image/png", "image/jpeg", "image/webp", "image/gif"}
	normalize := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if i := strings.IndexByte(s, ';'); i >= 0 {
			s = s[:i]
		}
		return s
	}
	for _, want := range preferred {
		for _, t := range types {
			if normalize(t) == want {
				return strings.TrimSpace(t)
			}
		}
	}
	return ""
}

// dataURL encodes the image as a data: URL for message.send media.
func (img *clipboardImage) dataURL() string {
	return "data:" + img.mimeType + ";base64," + base64.StdEncoding.EncodeToString(img.data)
}

// sizeOK reports whether the image fits the gateway media cap.
func (img *clipboardImage) sizeOK() bool { return len(img.data) <= maxClipboardImageBytes }
