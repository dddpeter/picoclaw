package snapshot

// Version diffs: at commit time each retained entry gets a unified diff of
// pre-turn content vs post-turn content, concatenated into the version's
// diffs.txt. The text always passes tools.MaskAPIKeys — diffs are a
// display/query surface, while the stored pre-images stay raw for
// byte-exact restore (see docs/design fork borrowing 增量清单 §2.1).

import (
	"fmt"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/sipeed/picoclaw/pkg/tools"
)

// Diff caps mirror pkg/tools/shared: keep per-file diffs bounded so a huge
// regenerated file cannot blow up the version directory. Files beyond the
// caps get an explicit note instead of a silent elision.
const (
	maxDiffInputBytes = 64 * 1024
	maxDiffInputLines = 2000
)

// buildVersionDiff renders the masked diffs.txt for the given entries and
// returns the text plus the number of per-file diff sections. Called from
// CommitTurn before the store lock (it reads current file contents).
func (s *Store) buildVersionDiff(sessionKey string, entries []FileEntry) (string, int) {
	var b strings.Builder
	count := 0
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("=== %s (%s)\n", e.Rel, e.Op))
		if e.Op == OpCreated && !e.Stored {
			// created: no pre-image by definition
		} else if !e.Stored {
			b.WriteString(fmt.Sprintf("[pre-image not retained: %s]\n", e.Reason))
			b.WriteString("\n")
			continue
		}

		before, err := s.preImageBytes(sessionKey, e.SHA256)
		if err != nil && e.Op != OpCreated {
			b.WriteString(fmt.Sprintf("[pre-image unavailable: %v]\n\n", err))
			continue
		}

		after, err := os.ReadFile(e.Path)
		if err != nil {
			if e.Op == OpDeleted {
				after = nil // deleted: after-state is empty by definition
			} else {
				b.WriteString(fmt.Sprintf("[post-state unavailable: %v]\n\n", err))
				continue
			}
		}

		if exceedsDiffLimits(before, after) {
			b.WriteString(fmt.Sprintf("[diff skipped: content exceeds %d bytes / %d lines]\n\n",
				maxDiffInputBytes, maxDiffInputLines))
			continue
		}

		diff, err := unifiedDiff(e.Rel, before, after)
		if err != nil || diff == "" {
			b.WriteString("[no content change]\n\n")
			continue
		}
		b.WriteString(diff)
		b.WriteString("\n")
		count++
	}
	return tools.MaskAPIKeys(b.String()), count
}

// preImageBytes loads a stored pre-image.
func (s *Store) preImageBytes(sessionKey, hash string) ([]byte, error) {
	if hash == "" {
		return nil, fmt.Errorf("no pre-image hash")
	}
	return os.ReadFile(preImagePath(s.preDir(sessionKey), hash))
}

func preImagePath(preDir, hash string) string {
	return preDir + string(os.PathSeparator) + hash + ".bin"
}

func exceedsDiffLimits(before, after []byte) bool {
	return len(before) > maxDiffInputBytes || len(after) > maxDiffInputBytes ||
		countLines(before) > maxDiffInputLines || countLines(after) > maxDiffInputLines
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := strings.Count(string(content), "\n")
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

// unifiedDiff renders a unified diff with EOF-newline handling matching
// pkg/tools/shared (context 3, \ No newline marker).
func unifiedDiff(relPath string, before, after []byte) (string, error) {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitLinesPreservingEOF(before),
		B:        splitLinesPreservingEOF(after),
		FromFile: "a/" + relPath,
		ToFile:   "b/" + relPath,
		Context:  3,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(diff, "\n"), nil
}

func splitLinesPreservingEOF(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	lines := make([]string, 0, strings.Count(string(content), "\n")+1)
	start := 0
	for i, b := range content {
		if b != '\n' {
			continue
		}
		lines = append(lines, string(content[start:i+1]))
		start = i + 1
	}
	if start < len(content) {
		lines = append(lines, string(content[start:]))
		if content[len(content)-1] != '\n' {
			lines[len(lines)-1] += "\n"
			lines = append(lines, "\\ No newline at end of file\n")
		}
	}
	return lines
}
