// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MemoryStore manages persistent memory for the agent.
// - Long-term memory: memory/MEMORY.md
// - Daily notes: memory/YYYYMM/YYYYMMDD.md
type MemoryStore struct {
	workspace  string
	memoryDir  string
	memoryFile string
}

// NewMemoryStore creates a new MemoryStore with the given workspace path.
// It ensures the memory directory exists.
func NewMemoryStore(workspace string) *MemoryStore {
	memoryDir := filepath.Join(workspace, "memory")
	memoryFile := filepath.Join(memoryDir, "MEMORY.md")

	// Ensure memory directory exists
	os.MkdirAll(memoryDir, 0o755)

	return &MemoryStore{
		workspace:  workspace,
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
	}
}

// getTodayFile returns the path to today's daily note file (memory/YYYYMM/YYYYMMDD.md).
func (ms *MemoryStore) getTodayFile() string {
	today := time.Now().Format("20060102") // YYYYMMDD
	monthDir := today[:6]                  // YYYYMM
	filePath := filepath.Join(ms.memoryDir, monthDir, today+".md")
	return filePath
}

// ReadLongTerm reads the long-term memory (MEMORY.md).
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadLongTerm() string {
	if data, err := os.ReadFile(ms.memoryFile); err == nil {
		return string(data)
	}
	return ""
}

// GetRecentDailyNotes returns daily notes from the last N days.
// Contents are joined with "---" separator.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	var sb strings.Builder
	first := true

	for i := range days {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102") // YYYYMMDD
		monthDir := dateStr[:6]            // YYYYMM
		filePath := filepath.Join(ms.memoryDir, monthDir, dateStr+".md")

		if data, err := os.ReadFile(filePath); err == nil {
			if !first {
				sb.WriteString("\n\n---\n\n")
			}
			sb.Write(data)
			first = false
		}
	}

	return sb.String()
}

// GetMemoryContext returns formatted memory context for the agent prompt.
// Includes long-term memory and recent daily notes.
func (ms *MemoryStore) GetMemoryContext() string {
	longTerm := ms.ReadLongTerm()
	recentNotes := ms.GetRecentDailyNotes(3)

	if longTerm == "" && recentNotes == "" {
		return ""
	}

	var sb strings.Builder

	if longTerm != "" {
		sb.WriteString("## Long-term Memory\n\n")
		sb.WriteString(longTerm)
	}

	if recentNotes != "" {
		if longTerm != "" {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## Recent Daily Notes\n\n")
		sb.WriteString(recentNotes)
	}

	return sb.String()
}
