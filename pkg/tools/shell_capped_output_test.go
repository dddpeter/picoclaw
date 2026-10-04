package tools

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The synchronous exec path caps retained output at head+tail around a
// truncation marker (T3: unbounded runSync buffers could OOM the gateway).
// These tests pin the buffer's arithmetic: exact retention up to the limit,
// correct ring order after wraparound, an honest lost-byte count in the
// marker, and concurrency safety for the shared stdout/stderr instance.

func rep(s string, n int) string { return strings.Repeat(s, n) }

func TestCappedOutputBuffer_BelowLimitIsVerbatim(t *testing.T) {
	b := newCappedOutputBuffer(1024)
	content := "hello world"
	if _, err := b.Write([]byte(content)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := b.String(); got != content {
		t.Fatalf("String() = %q, want verbatim %q", got, content)
	}
	if b.Len() != len(content) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(content))
	}
}

// Exactly `limit` bytes fit in head+ring with nothing lost: the marker must
// not appear (dropped counts ring residents too, the marker reports the
// difference).
func TestCappedOutputBuffer_ExactLimitKeepsEverything(t *testing.T) {
	const limit = 1024
	b := newCappedOutputBuffer(limit)
	head := rep("H", limit/2)
	tail := rep("T", limit/2)
	if _, err := b.Write([]byte(head + tail)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := b.String()
	if got != head+tail {
		t.Fatalf("exact-limit output not fully retained (len %d vs %d):\n%q…", len(got), limit, got[:40])
	}
	if strings.Contains(got, "truncated") {
		t.Fatal("marker reported truncation although nothing was lost")
	}
}

func TestCappedOutputBuffer_HeadMarkerTailOrder(t *testing.T) {
	const limit = 1024
	b := newCappedOutputBuffer(limit)
	head := rep("H", limit/2)
	middle := rep("M", 2000)
	tail := rep("T", 300)
	if _, err := b.Write([]byte(head + middle + tail)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := b.String()
	if !strings.HasPrefix(got, head) {
		t.Fatal("head prefix not retained")
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatal("tail suffix not retained (ring order broken?)")
	}
	// middle 2000 + tail 300 routed past head; ring keeps 512, so 1788 lost.
	wantMarker := fmt.Sprintf("[%s of output truncated", formatBytes(2000+300-limit/2))
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("marker %q missing from output (got marker region: %q)", wantMarker,
			got[len(head):len(got)-len(tail)])
	}
	if b.Len() != len(head)+len(middle)+len(tail) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(head)+len(middle)+len(tail))
	}
}

// The tail ring must reassemble in stream order after wrapping multiple
// times, across differently-sized Write calls (the reader goroutines chunk
// at 4KB but Write must not assume any particular chunking).
func TestCappedOutputBuffer_RingWraparoundOrder(t *testing.T) {
	const limit = 1024
	b := newCappedOutputBuffer(limit)
	head := rep("0", limit/2)
	segA := rep("A", 700)
	segB := rep("B", 200)
	segC := rep("C", 300)
	for _, s := range []string{head, segA, segB, segC} {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	got := b.String()
	// Stream past head: A*700 B*200 C*300 (1200 bytes); ring keeps the last
	// 512: A[688:700] + B*200 + C*300.
	wantTail := rep("A", limit/2-len(segB)-len(segC)) + segB + segC
	if !strings.HasSuffix(got, wantTail) {
		t.Fatalf("wrapped tail out of order:\n got tail %q\nwant tail %q",
			got[len(got)-len(wantTail):], wantTail)
	}
}

func TestCappedOutputBuffer_TinyLimitFloor(t *testing.T) {
	b := newCappedOutputBuffer(1) // bumped to the 64-byte floor
	if len(b.tail) == 0 {
		t.Fatal("tail ring must never be empty (String divides by its length)")
	}
	if _, err := b.Write([]byte(rep("x", 1000))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := b.String(); !strings.Contains(got, "truncated") {
		t.Fatal("overflow past the floor limit must produce a marker")
	}
}

func TestCappedOutputBuffer_ConcurrentWrites(t *testing.T) {
	const limit = 4096
	b := newCappedOutputBuffer(limit)
	const writers = 8
	const perWriter = 1000
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter/100; j++ {
				if _, err := b.Write([]byte(rep("w", 100))); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if b.Len() != writers*perWriter {
		t.Fatalf("Len() = %d, want %d", b.Len(), writers*perWriter)
	}
	got := b.String()
	if !strings.HasPrefix(got, rep("w", limit/2)) {
		t.Fatal("head corrupted under concurrent writers")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{3 * 1024 * 1024, "3.0MB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
