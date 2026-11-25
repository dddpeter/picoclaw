package pico

import "sync"

// a2uiSurfaceCap bounds the live A2UI surface registry (LRU) so a
// long-running gateway does not leak per-surface state.
const a2uiSurfaceCap = 64

// a2uiSurfaceKey scopes a live surface to its session: chatID is the pico
// session routing key ("pico:<sessionID>", the same key broadcastToSession
// routes on), surfaceID is the batch's single surface id (UIMeta.SurfaceID,
// enforced non-empty by the render_ui gates). Two sessions reusing the same
// surfaceId string never collide.
type a2uiSurfaceKey struct {
	chatID    string
	surfaceID string
}

// a2uiSurfaceState is one registered live surface: the message id the
// create batch was broadcast under (message.update targets it) plus the
// accumulated frames broadcast so far (create frames + merged update
// frames) so the next update merges against the full live state.
type a2uiSurfaceState struct {
	messageID string
	content   string
	frames    []any
}

// a2uiSurfaceRegistry is a bounded LRU map of live A2UI surfaces. Entries
// are in-memory only: a gateway restart clears the registry and later
// update-only batches degrade to fresh message.create cards (accepted
// boundary — the frontend shows a new card instead of a lost orphan).
//
// Concurrency contract: Send's caller (the manager worker) drains the
// channel queue with a single goroutine, so Sends — and hence the
// lookup→broadcast→record sequence around this registry — are serialized
// per channel today. That sequence is deliberately NOT atomic (merge and
// record run outside the registry lock): concurrentizing Send requires
// moving the merge into the lock as an atomic commit, or interleaved
// create/update batches can lose deltas.
type a2uiSurfaceRegistry struct {
	mu      sync.Mutex
	entries map[a2uiSurfaceKey]*a2uiSurfaceState
	// order holds the registry keys oldest-first (LRU eviction order).
	// Touch (lookup or record) moves a key to the back.
	order []a2uiSurfaceKey
}

func newA2uiSurfaceRegistry() *a2uiSurfaceRegistry {
	return &a2uiSurfaceRegistry{entries: make(map[a2uiSurfaceKey]*a2uiSurfaceState)}
}

// lookup returns a snapshot of the registered state for (chatID, surfaceID)
// and marks it most recently used. Nil registry (hand-constructed channel
// in tests) reports a miss so updates degrade to creates.
func (r *a2uiSurfaceRegistry) lookup(chatID, surfaceID string) (a2uiSurfaceState, bool) {
	if r == nil || surfaceID == "" {
		return a2uiSurfaceState{}, false
	}
	key := a2uiSurfaceKey{chatID, surfaceID}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.entries[key]
	if !ok {
		return a2uiSurfaceState{}, false
	}
	r.touchLocked(key)
	return *state, true
}

// record inserts or replaces the entry for (chatID, surfaceID) and marks it
// most recently used, evicting the least recently used entry when the cap
// is exceeded. A repeated create for the same surface replaces the entry
// (the last card takes over subsequent merges).
func (r *a2uiSurfaceRegistry) record(chatID, surfaceID, messageID, content string, frames []any) {
	if r == nil || surfaceID == "" {
		return
	}
	key := a2uiSurfaceKey{chatID, surfaceID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; !exists && len(r.entries) >= a2uiSurfaceCap {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	r.entries[key] = &a2uiSurfaceState{messageID: messageID, content: content, frames: frames}
	r.touchLocked(key)
}

// touchLocked moves key to the back of the LRU order. Caller holds r.mu.
func (r *a2uiSurfaceRegistry) touchLocked(key a2uiSurfaceKey) {
	for i, k := range r.order {
		if k == key {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.order = append(r.order, key)
}

// batchHasCreateSurface reports whether a decoded A2UI batch carries a
// createSurface frame, i.e. it establishes a new card rather than updating
// a registered one.
func batchHasCreateSurface(frames []any) bool {
	for _, frame := range frames {
		if msg, ok := frame.(map[string]any); ok {
			if _, ok := msg["createSurface"]; ok {
				return true
			}
		}
	}
	return false
}

// mergeA2UIFrames merges an update-only batch's frames into a live surface's
// accumulated frames: components/delete frames append; updateDataModel
// frames overwrite the frame targeting the same path in place (last value
// wins). This mirrors the history-replay fold semantics (pkg/tools
// RenderUIHistoryFold) so live cards and replayed cards converge on the
// same final state. Reimplemented locally instead of exporting the tools
// helper: the fold pipeline is batch/history-oriented (decodes persisted
// call args, derives fallbacks, emits at create positions) while the live
// path accumulates already-broadcast frames — the shared rule is these few
// lines, pinned by tests on both sides. The accumulated slice is copied
// first so the registry's stored frames are never mutated through the
// caller's copy (a failed broadcast must not corrupt the registry state).
func mergeA2UIFrames(acc []any, update []any) []any {
	merged := make([]any, len(acc), len(acc)+len(update))
	copy(merged, acc)
	for _, frame := range update {
		if !overwriteDataModelFrame(merged, frame) {
			merged = append(merged, frame)
		}
	}
	return merged
}

// overwriteDataModelFrame replaces an existing updateDataModel frame with
// the same path in frames (last value wins) and reports whether it did.
// Non-updateDataModel frames never overwrite.
func overwriteDataModelFrame(frames []any, frame any) bool {
	msg, ok := frame.(map[string]any)
	if !ok {
		return false
	}
	dm, ok := msg["updateDataModel"].(map[string]any)
	if !ok {
		return false
	}
	path, _ := dm["path"].(string)
	for i, existing := range frames {
		exMsg, ok := existing.(map[string]any)
		if !ok {
			continue
		}
		exDM, ok := exMsg["updateDataModel"].(map[string]any)
		if !ok {
			continue
		}
		if exPath, _ := exDM["path"].(string); exPath == path {
			frames[i] = frame
			return true
		}
	}
	return false
}
