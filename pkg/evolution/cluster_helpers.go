package evolution

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// normalizePath lowercases and trims skill path segments, dropping empties.
func normalizePath(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectRecordIDs(cluster []LearningRecord) []string {
	ids := make([]string, 0, len(cluster))
	for _, record := range cluster {
		ids = append(ids, record.ID)
	}
	return ids
}

func stableRuleID(workspaceID, key string) string {
	sum := sha1.Sum([]byte(workspaceID + "\x00" + key))
	return "rule-" + hex.EncodeToString(sum[:6])
}
