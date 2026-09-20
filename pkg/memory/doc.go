// Package memory provides the low-level persistent session storage:
// a Store interface plus an append-only JSONL implementation
// (MemoryStore) for conversation messages, titles and metadata.
//
// It sits at the bottom of the session stack — session (pkg/session)
// wraps it with scope/allocator semantics, and agent / web read it
// directly for history and titles. Not to be confused with
// pkg/session (session management layer) or pkg/agent/sessions/
// (runtime data directory where the JSONL files land).
package memory
