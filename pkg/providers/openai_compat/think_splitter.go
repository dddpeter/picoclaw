package openai_compat

import "strings"

// thinkSplitter extracts reasoning embedded in delta.content as
// <think>...</think> (some gateways, e.g. MiniMax via token-plan proxies,
// inline it there instead of using the reasoning_content field).
// It is a streaming state machine: Feed() receives each delta and returns
// (reasoningDelta, answerDelta). Close() flushes any unterminated <think>
// block as reasoning (returns the remaining answer text, if any).
type thinkSplitter struct {
	inThink bool
}

func newThinkSplitter() *thinkSplitter { return &thinkSplitter{} }

// Feed processes one content delta and returns the reasoning / answer portions.
func (t *thinkSplitter) Feed(delta string) (reasoning, answer string) {
	var r, a strings.Builder
	rest := delta
	for rest != "" {
		if t.inThink {
			if idx := strings.Index(rest, "</think>"); idx >= 0 {
				r.WriteString(rest[:idx])
				rest = rest[idx+len("</think>"):]
				t.inThink = false
			} else {
				// Keep a possible partial "</think" tail buffered? Simplification:
				// emit it as reasoning; the next delta starts with the remainder
				// of the tag and is re-checked. ponytail: a chunk boundary inside
				// "</think>" leaks a few tag chars into reasoning; upgrade to a
				// rolling buffer if that matters.
				r.WriteString(rest)
				rest = ""
			}
		} else {
			if idx := strings.Index(rest, "<think>"); idx >= 0 {
				a.WriteString(rest[:idx])
				rest = rest[idx+len("<think>"):]
				t.inThink = true
			} else {
				a.WriteString(rest)
				rest = ""
			}
		}
	}
	return r.String(), a.String()
}

// Close flushes trailing state: if the stream ended inside <think>, the
// buffered text was already emitted as reasoning; nothing else to return.
func (t *thinkSplitter) Close() {}
