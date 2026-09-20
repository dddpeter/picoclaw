// Package session is the session management layer on top of pkg/memory:
// SessionScope resolution, session-key parsing and the multi-agent
// allocator that routes conversations to agents, plus SessionManager.
//
// Its JSONL backend adapts the pkg/memory store (metaAwareStore) into
// the agent-facing per-scope session view. It holds no storage of its
// own — durability lives in pkg/memory.
package session
