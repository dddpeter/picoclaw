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
