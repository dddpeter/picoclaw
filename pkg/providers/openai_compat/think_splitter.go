package openai_compat

import "strings"

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// thinkSplitter extracts reasoning embedded in delta.content as
// <think>...</think> (some gateways, e.g. MiniMax via token-plan proxies,
// inline it there instead of using the reasoning_content field).
//
// It is a streaming state machine: Feed() receives each delta and returns
// (reasoningDelta, answerDelta). Tag boundaries can fall between deltas, so a
// tail that could be the beginning of a tag is held back until the next Feed
// or Close disambiguates it.
type thinkSplitter struct {
	inThink bool
	buf     string // held-back bytes: a candidate partial tag prefix
}

func newThinkSplitter() *thinkSplitter { return &thinkSplitter{} }

// Feed processes one content delta and returns the reasoning / answer portions.
func (t *thinkSplitter) Feed(delta string) (reasoning, answer string) {
	var r, a strings.Builder
	data := t.buf + delta
	t.buf = ""
	for data != "" {
		if t.inThink {
			idx := strings.Index(data, thinkCloseTag)
			if idx >= 0 {
				r.WriteString(data[:idx])
				data = data[idx+len(thinkCloseTag):]
				t.inThink = false
				continue
			}
			held, emit := splitPartialTagSuffix(data, thinkCloseTag)
			r.WriteString(emit)
			t.buf = held
			return r.String(), a.String()
		}
		idx := strings.Index(data, thinkOpenTag)
		if idx >= 0 {
			a.WriteString(data[:idx])
			data = data[idx+len(thinkOpenTag):]
			t.inThink = true
			continue
		}
		held, emit := splitPartialTagSuffix(data, thinkOpenTag)
		a.WriteString(emit)
		t.buf = held
		return r.String(), a.String()
	}
	return r.String(), a.String()
}

// Close flushes any held-back partial-tag bytes to the stream they belong to
// and returns the trailing (reasoning, answer) remainder. If the stream ended
// inside an unterminated <think> block, everything seen since the tag counts
// as reasoning.
func (t *thinkSplitter) Close() (reasoning, answer string) {
	held := t.buf
	t.buf = ""
	if held == "" {
		return "", ""
	}
	if t.inThink {
		return held, ""
	}
	return "", held
}

// splitPartialTagSuffix splits data into (heldBack, emit) where heldBack is
// the longest proper suffix of data that is also a proper prefix of tag. A
// chunk boundary inside the tag then stays buffered instead of leaking tag
// bytes into the output.
func splitPartialTagSuffix(data, tag string) (heldBack, emit string) {
	max := len(tag) - 1
	if max > len(data) {
		max = len(data)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(tag, data[len(data)-n:]) {
			return data[len(data)-n:], data[:len(data)-n]
		}
	}
	return "", data
}
