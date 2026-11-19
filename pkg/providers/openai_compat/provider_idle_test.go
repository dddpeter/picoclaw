package openai_compat

import (
	"io"
	"strings"
	"testing"
	"time"
)

// manyReadReader yields one byte-chunk per Read with a small pause, so a
// long stream exercises many wrapper Reads over a wall clock longer than
// the idle timeout — while every single read gap stays under it.
type manyReadReader struct {
	data     []string
	pos      int
	gapPause time.Duration
}

func (r *manyReadReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	chunk := r.data[r.pos]
	r.pos++
	copy(p, chunk)
	if r.gapPause > 0 {
		time.Sleep(r.gapPause)
	}
	return len(chunk), nil
}

func (r *manyReadReader) Close() error { return nil }

// TestStreamingReadIdleTimeoutManySlowReadsNoMisfire pins the single-timer
// reuse contract: a stream whose total duration exceeds the idle timeout
// but whose per-read gaps stay under it must read to EOF without a spurious
// "stream idle timeout" (the old per-Read AfterFunc design could misfire
// when the timer fired as a read returned).
func TestStreamingReadIdleTimeoutManySlowReadsNoMisfire(t *testing.T) {
	var chunks []string
	for i := 0; i < 40; i++ {
		chunks = append(chunks, "data")
	}
	body := &streamingReadIdleTimeoutBody{
		body:    io.NopCloser(&manyReadReader{data: chunks, gapPause: 2 * time.Millisecond}),
		timeout: 50 * time.Millisecond,
	}
	defer body.Close()

	buf := make([]byte, 16)
	var out strings.Builder
	for {
		n, err := body.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Read() error = %v, want clean EOF (no idle-timeout misfire)", err)
		}
	}
	if out.String() != strings.Repeat("data", 40) {
		t.Fatalf("content mismatch: got %d bytes, want %d", out.Len(), 40*4)
	}
}
