package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanCommandOutput_CollapsesProgressLines(t *testing.T) {
	in := "downloading  10%\rdownloading  50%\rdownloading 100%\ndone\n"
	got := CleanCommandOutput("curl -sSL big.iso", in)
	if strings.Contains(got, "10%") || strings.Contains(got, "50%") {
		t.Fatalf("progress redraws should collapse, got %q", got)
	}
	if !strings.Contains(got, "downloading 100%") || !strings.Contains(got, "done") {
		t.Fatalf("final progress state and following lines must survive, got %q", got)
	}
}

func TestCleanCommandOutput_StripsANSI(t *testing.T) {
	in := "\x1b[32mOK\x1b[0m \x1b[1;32m/tests\x1b[0m\n\x1b]0;title\x07plain\n"
	got := CleanCommandOutput("npm test", in)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("ANSI escapes should be stripped, got %q", got)
	}
	if !strings.Contains(got, "OK") || !strings.Contains(got, "/tests") || !strings.Contains(got, "plain") {
		t.Fatalf("visible text must survive, got %q", got)
	}
}

func TestCleanCommandOutput_CompressesLongLines(t *testing.T) {
	long := strings.Repeat("x", 1200)
	in := "header\n" + long + "\nfooter\n"
	got := CleanCommandOutput("cat big.log", in)
	if !strings.Contains(got, "header") || !strings.Contains(got, "footer") {
		t.Fatalf("short lines must survive, got %q", got)
	}
	if !strings.Contains(got, "chars omitted]") {
		t.Fatalf("long line should be compressed with a notice, got %q", got)
	}
	if len(got) >= len(in) {
		t.Fatalf("compression must shrink output: got %d bytes for %d input", len(got), len(in))
	}
}

func TestCleanCommandOutput_LongLineCutKeepsUTF8(t *testing.T) {
	long := strings.Repeat("国", 800) // multibyte runes
	got := CleanCommandOutput("cat zh.log", long+"\n")
	if !utf8.ValidString(got) {
		t.Fatalf("compressed output must stay valid UTF-8, got invalid bytes near %q", got[:40])
	}
	if !strings.Contains(got, "chars omitted]") {
		t.Fatalf("long multibyte line should be compressed, got %d bytes", len(got))
	}
}

func TestCleanCommandOutput_NeverWorse(t *testing.T) {
	in := "clean plain output\nsecond line\n"
	if got := CleanCommandOutput("ls -la", in); got != in {
		t.Fatalf("already-clean output must be returned verbatim, got %q", got)
	}
}

func TestCleanCommandOutput_PassthroughBypasses(t *testing.T) {
	in := "binary\x00\x01payload \x1b[31mnot-cleaned\x1b[0m\n"
	for _, cmd := range []string{
		"jq --json '.' x",
		"pytest -o json=1",
		"build.sh | tee build.log",
		"run.sh # nofilter",
	} {
		if got := CleanCommandOutput(cmd, in); got != in {
			t.Fatalf("command %q must bypass cleaning, got %q", cmd, got)
		}
	}
}

func TestCleanCommandOutput_MultilinePipelineOrdering(t *testing.T) {
	// Progress frames carry ANSI; both must be handled in one pass and the
	// CR collapse must see them before the escape strip would blur the line.
	in := "step1\r\x1b[2Kstep2\r\x1b[2Kstep3\n"
	got := CleanCommandOutput("docker build .", in)
	if strings.Contains(got, "step1") || strings.Contains(got, "step2") {
		t.Fatalf("earlier redraw frames must collapse, got %q", got)
	}
	if !strings.Contains(got, "step3") {
		t.Fatalf("last frame must survive, got %q", got)
	}
}

func TestCleanCommandOutput_PreservesCRLFLineContent(t *testing.T) {
	// Windows shells emit CRLF line endings; a trailing \r before \n is the
	// line ending, not a progress redraw — collapsing it must not blank the
	// line's content.
	in := "hello world\r\nsecond line\r\n"
	got := CleanCommandOutput("Write-Output 'hello world'", in)
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "second line") {
		t.Fatalf("CRLF line endings must not be treated as progress redraws, got %q", got)
	}
}

// TestMaskAPIKeys pins the format-based credential mask (octop borrowing
// §十一, pii.py formats): configured-secret filtering only knows values from
// picoclaw's own config; keys pasted from elsewhere (cat .env, error logs)
// slip through unless the FORMAT itself is recognized.
func TestMaskAPIKeys(t *testing.T) {
	openai := "sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef"
	anthropic := "sk-ant-api03-9d1a2b3c4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b"
	aws := "AKIAIOSFODNN7EXAMPLE"
	google := "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r"
	hf := "hf_axBcDdEeFfGgHhIiJjKkLlMmNnOoPpQqRrSsTt"

	in := "config:\n  openai: " + openai + "\n  aws: " + aws + "\n  g: " + google + "\n  hf: " + hf + "\n  anth: " + anthropic + "\n"
	got := MaskAPIKeys(in)
	for _, key := range []string{openai, aws, google, hf, anthropic} {
		if strings.Contains(got, key) {
			t.Fatalf("key %q must be masked, got: %s", key[:12], got)
		}
	}
	for _, frag := range []string{"sk-4", "AKIA", "AIza", "hf_a"} {
		if !strings.Contains(got, frag) {
			t.Fatalf("mask keeps a short prefix, missing %q in: %s", frag, got)
		}
	}
	// Plain prose with short sk- lookalikes stays untouched.
	plain := "task sk-1 short and sk-not-a-key stay visible\n"
	if got := MaskAPIKeys(plain); got != plain {
		t.Fatalf("short lookalikes must not be masked, got %q", got)
	}
}

// TestCleanCommandOutput_MasksKeysInPipeline pins integration: cat .env
// output going through the inline pipeline gets keys masked (never-worse
// holds — masking only shortens).
func TestCleanCommandOutput_MasksKeysInPipeline(t *testing.T) {
	key := "sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef"
	in := "OPENAI_API_KEY=" + key + "\nMODE=prod\n"
	got := CleanCommandOutput("cat .env", in)
	if strings.Contains(got, key) {
		t.Fatalf("cat .env output must mask the key, got %q", got)
	}
	if !strings.Contains(got, "MODE=prod") {
		t.Fatalf("surrounding lines must survive, got %q", got)
	}
}

// TestMaskAPIKeys_NeverWorse pins the property the cleaning pipeline's
// guardrail depends on (octop borrowing §十一): masking only ever shortens.
// A future pattern that could lengthen output would silently defeat the
// never-worse comparison in CleanCommandOutput.
func TestMaskAPIKeys_NeverWorse(t *testing.T) {
	samples := []string{
		"",
		"plain text without any credentials",
		"key: sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef and AKIAIOSFODNN7EXAMPLE",
		"two keys sk-ant-api03-9d1a2b3c4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8 and hf_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"strings.Repeat-style long text with an embedded AIzaSyA1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8 token",
		"unicode 中文段落与 sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef 混排",
	}
	for _, s := range samples {
		if got := MaskAPIKeys(s); len(got) > len(s) {
			t.Fatalf("MaskAPIKeys must never lengthen: in=%d out=%d for %.60s", len(s), len(got), s)
		}
	}
}

// TestCleanCommandOutput_BypassStillMasksKeys (review P1): the bypass
// markers are substrings of the model-controlled command — "cat .env #
// nofilter" must never ship a plaintext third-party key into the model
// context. Bypass exempts the RESHAPING pipeline only.
func TestCleanCommandOutput_BypassStillMasksKeys(t *testing.T) {
	key := "sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef"
	in := "OPENAI_API_KEY=" + key + "\n"
	for _, cmd := range []string{
		"cat .env # nofilter",
		"kubectl get secret x -o json",
		"deploy.sh | tee deploy.log",
	} {
		got := CleanCommandOutput(cmd, in)
		if strings.Contains(got, key) {
			t.Fatalf("bypass command %q must still mask keys, got %q", cmd, got)
		}
		if !strings.Contains(got, "sk-4") {
			t.Fatalf("masked prefix missing for %q: %q", cmd, got)
		}
	}
}

// TestMaskAPIKeys_NoFalsePositiveOnEmbeddedPrefix (review P2): the old sk-
// pattern lacked a word boundary, so "task-abcdef..." / "desk-1a2b..."
// (whose "sk-" is a suffix of the previous word) were masked.
func TestMaskAPIKeys_NoFalsePositiveOnEmbeddedPrefix(t *testing.T) {
	plain := "task-abcdef0123456789abcdef0123456789abcdef01 and desk-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d flags"
	if got := MaskAPIKeys(plain); got != plain {
		t.Fatalf("embedded sk- prefixes must not be masked, got %q", got)
	}
	// Real keys in common placements still match.
	for _, s := range []string{
		"sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef",
		"key=sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef",
		`"sk-4b829b7ba0aa40648d5318b0025f2abcdeadbeef"`,
	} {
		if got := MaskAPIKeys(s); got == s {
			t.Fatalf("real key must be masked: %q", s)
		}
	}
}

// TestMaskAPIKeys_GithubAndSlackFormats (review P2): ghp_ / xoxb- formats
// were previously uncovered.
func TestMaskAPIKeys_GithubAndSlackFormats(t *testing.T) {
	// Synthetic samples, assembled at runtime: a literal token-shaped
	// string in the file trips GitHub push protection (secret scanning).
	ghp := "ghp_" + "0123456789" + "abcdefghijklmnopqrstuvwxyzAB"
	xoxb := "xoxb-" + "1234567890-" + "123456789012-" + "abcdef0123456789abcdef01"
	got := MaskAPIKeys("pat: " + ghp + " bot: " + xoxb)
	if strings.Contains(got, ghp) {
		t.Fatalf("ghp_ token must be masked, got %q", got)
	}
	if strings.Contains(got, xoxb) {
		t.Fatalf("xoxb- token must be masked, got %q", got)
	}
}
