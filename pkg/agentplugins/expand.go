package agentplugins

import (
	"fmt"
	"strings"
)

// Vars carries the values substituted for the two spec placeholders (§9.2).
type Vars struct {
	Root string // substituted for ${PLUGIN_ROOT}
	Data string // substituted for ${PLUGIN_DATA}
}

// Placeholder spellings (§9.2) — exact occurrences only.
const (
	placeholderRoot = "${PLUGIN_ROOT}"
	placeholderData = "${PLUGIN_DATA}"
)

// Expand performs a single, non-recursive textual replacement of every exact
// occurrence of ${PLUGIN_ROOT} / ${PLUGIN_DATA} in s. Text introduced by a
// replacement is never rescanned; unrecognized placeholder-like text stays
// literal (spec §9.2). Implemented with a single-pass scanner, not chained
// ReplaceAll, so replacement output cannot be re-expanded.
func Expand(s string, v Vars) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, placeholderRoot):
			b.WriteString(v.Root)
			s = rest[len(placeholderRoot):]
		case strings.HasPrefix(rest, placeholderData):
			b.WriteString(v.Data)
			s = rest[len(placeholderData):]
		default:
			// Not one of ours: copy the "${" and rescan after it so an
			// embedded later placeholder still expands.
			b.WriteString(s[i : i+2])
			s = rest[2:]
		}
	}
}

// ExpandArgs expands every string element of args (spec §9.2).
func ExpandArgs(args []string, v Vars) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = Expand(a, v)
	}
	return out
}

// ExpandEnvValues expands env values. Env KEYS are not expanded; a key named
// PLUGIN_ROOT or PLUGIN_DATA (case-insensitive) makes the server
// configuration invalid (spec §9.1).
func ExpandEnvValues(env map[string]string, v Vars) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, val := range env {
		if strings.EqualFold(k, "PLUGIN_ROOT") || strings.EqualFold(k, "PLUGIN_DATA") {
			return nil, fmt.Errorf("env key %q is reserved (PLUGIN_ROOT/PLUGIN_DATA are client-supplied)", k)
		}
		out[k] = Expand(val, v)
	}
	return out, nil
}
