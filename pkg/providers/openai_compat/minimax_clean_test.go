package openai_compat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realWorldLeak reproduces the field sample: the model prints a pseudo
// tool-call as text (parroting picoclaw's seahorse history format), with the
// MiniMax encoded boundary glitch-decoded before each close tag.
func realWorldLeak() string {
	return "[tool_use: \"exec\", \"command\": \"python3 <<'PYEOF'\n" +
		"SK = \"sk-hqpk-TESTSECRETNOTAKEY\"\n" +
		"PYEOF" + minimaxBoundaryToken + "</command>" +
		minimaxBoundaryToken + "</invoke>\n" +
		minimaxBoundaryToken + "</tool_call>"
}

func TestSanitizeMinimaxToolLeak(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"real-world sample removed entirely", realWorldLeak(), ""},
		{"prefix answer kept, leak cut", "我先测一下网关连通性。\n" + realWorldLeak(), "我先测一下网关连通性。"},
		{"no boundary token left untouched", "PYEOF</command></tool_call>", "PYEOF</command></tool_call>"},
		{"plain text untouched", "hello world", "hello world"},
		{"boundary only, no tail tags, no cut", "hello " + minimaxBoundaryToken + " world", "hello  world"},
		{
			"tail tags without start marker trim tags only",
			"answer tail" + minimaxBoundaryToken + "</tool_call>",
			"answer tail",
		},
		{
			"native xml block cut from open tag",
			"好的。\n<tool_call>\n<invoke name=\"exec\">do it</invoke>" + minimaxBoundaryToken + "</tool_call>",
			"好的。",
		},
		{
			"trailing whitespace after leak trimmed",
			"answer\n" + minimaxBoundaryToken + "</tool_call>\n",
			"answer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeMinimaxToolLeak(tt.in); got != tt.want {
				t.Fatalf("sanitizeMinimaxToolLeak() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeMinimaxToolLeak_RemovesSecretPayload(t *testing.T) {
	got := sanitizeMinimaxToolLeak("前言。" + realWorldLeak())
	if strings.Contains(got, "sk-hqpk-TESTSECRETNOTAKEY") {
		t.Fatalf("secret payload leaked into result: %q", got)
	}
	if strings.Contains(got, minimaxBoundaryToken) {
		t.Fatalf("boundary token leaked into result: %q", got)
	}
}

func TestProviderChatStream_StripsMinimaxToolLeak(t *testing.T) {
	// The leak is split across deltas so the boundary token itself is cut in
	// half mid-stream; the sanitizer works on accumulated text, so both the
	// intermediate frames and the final content must come out clean.
	answer := "我先测一下网关连通性。"
	leak := realWorldLeak()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"我先测一下网关连通性。\\n\"}}]}\n\n"))
		mid := len(leak) / 2
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + jsonString(leak[:mid]) + "}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + jsonString(leak[mid:]) + "}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	var frames []string
	out, err := p.ChatStreamEvents(
		t.Context(),
		[]Message{{Role: "user", Content: "test"}},
		nil,
		"MiniMax-M2.7",
		nil,
		func(chunk StreamChunk) {
			if chunk.Content != "" {
				frames = append(frames, chunk.Content)
			}
		},
	)
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if out.Content != answer {
		t.Fatalf("Content = %q, want %q", out.Content, answer)
	}
	for i, f := range frames {
		if strings.Contains(f, minimaxBoundaryToken) || strings.Contains(f, "sk-hqpk-TESTSECRETNOTAKEY") {
			t.Fatalf("frame %d leaks tool-call markup: %q", i, f)
		}
	}
}

func TestProviderChat_StripsMinimaxToolLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":` + jsonString(
				"我先测一下网关连通性。\n"+realWorldLeak(),
			) + `},"finish_reason":"stop"}]}`,
		))
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "")
	out, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, nil, "MiniMax-M2.7", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if out.Content != "我先测一下网关连通性。" {
		t.Fatalf("Content = %q, want %q", out.Content, "我先测一下网关连通性。")
	}
}

// jsonString marshals s as a JSON string literal.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		default:
			if r < 0x20 {
				b.WriteByte(byte(r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestMinimaxStreamState_MatchesFullSanitizePerFrame pins the incremental
// scan's equivalence contract: for an accumulated-frame sequence, the state
// helper must return exactly what a full sanitizeMinimaxToolLeak of each
// frame returns — while clean it only skips work it can prove redundant.
func TestMinimaxStreamState_MatchesFullSanitizePerFrame(t *testing.T) {
	scenarios := [][]string{
		// clean prose, no leak anywhere
		{"你好", "你好，我来", "你好，我来处理这个问题。"},
		// leak completes across the incremental window boundary
		{"前文正常。]<]min", "前文正常。]<]minimax[>[", "前文正常。]<]minimax[>[</invoke>"},
		{"前文正常。]<]minimax[>[\nPYEOF", "前文正常。]<]minimax[>[\nPYEOF]<]minimax[>[</command>]<]minimax[>[</invoke>"},
		// leak appears very late in an otherwise long clean answer
		{strings.Repeat("长", 5000), strings.Repeat("长", 5000) + "然后]<]minimax[>[</invoke>"},
	}
	for si, frames := range scenarios {
		var st minimaxStreamState
		accumulated := ""
		for fi, delta := range frames {
			accumulated += delta
			got := st.sanitize(accumulated)
			want := sanitizeMinimaxToolLeak(accumulated)
			if got != want {
				t.Fatalf("scenario %d frame %d: incremental = %q, full sanitize = %q", si, fi, got, want)
			}
		}
	}
}

// TestMinimaxStreamState_StickyFullSanitizeAfterLeak pins the sticky-dirty
// behavior: once a boundary token is seen, every later frame takes the full
// sanitize path (the builder keeps the leak, so per-frame cleaning must
// continue for as long as the stream emits it).
func TestMinimaxStreamState_StickyFullSanitizeAfterLeak(t *testing.T) {
	var st minimaxStreamState
	leakFrame := "答案开头]<]minimax[>[</invoke>"
	if got := st.sanitize(leakFrame); got != "答案开头" {
		t.Fatalf("first leak frame = %q, want %q", got, "答案开头")
	}
	// Later frames keep carrying the token (the accumulator is untouched):
	// the sticky path must keep sanitizing them. The close tag is no longer
	// at the very end here, so the tail-trim does not fire — the assertion
	// pins equality with the full sanitize, not a specific string.
	laterFrame := leakFrame + " 更多内容"
	if got := st.sanitize(laterFrame); got != sanitizeMinimaxToolLeak(laterFrame) {
		t.Fatalf("sticky frame = %q, want %q", got, sanitizeMinimaxToolLeak(laterFrame))
	}
}

// TestMinimaxStreamState_TokenStraddlingScannedBoundary pins the straddle
// window: a token whose bytes start inside the already-scanned prefix but
// complete only in the newest delta must still be caught — the rescan starts
// one token-length back from the proven-clean boundary.
func TestMinimaxStreamState_TokenStraddlingScannedBoundary(t *testing.T) {
	var st minimaxStreamState
	prefix := strings.Repeat("x", 20)
	if got := st.sanitize(prefix); got != prefix {
		t.Fatalf("clean frame = %q", got)
	}
	// append exactly the token, split so its head sits at the boundary
	split := len(prefix) - len(minimaxBoundaryToken) + 1
	if split < 0 || split > len(prefix) {
		t.Fatalf("test setup: invalid split %d", split)
	}
	first := prefix[:split] + minimaxBoundaryToken[:len(minimaxBoundaryToken)-1]
	second := minimaxBoundaryToken[len(minimaxBoundaryToken)-1:]
	// rebuild accumulated texts the way parseStreamResponse would
	accum1 := first
	if got, want := st.sanitize(accum1), sanitizeMinimaxToolLeak(accum1); got != want {
		t.Fatalf("straddle frame 1 = %q, want %q", got, want)
	}
	accum2 := first + second
	if got, want := st.sanitize(accum2), sanitizeMinimaxToolLeak(accum2); got != want {
		t.Fatalf("straddle frame 2 = %q, want %q", got, want)
	}
}
