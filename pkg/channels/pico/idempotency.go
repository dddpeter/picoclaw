package pico

import (
	"sync"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Inbound request idempotency (trae borrowing 增量清单 §2.2).
//
// Clients already assign a fresh ID to every message.send/media.send; this
// store remembers recently accepted (sessionID, ID) pairs so a reconnecting
// or retrying client that re-sends the same ID is answered with a
// message.ack instead of silently starting a second turn.
//
// The window is in-memory and deliberately not durable: a gateway restart
// clears it, and eviction (below) forgets the oldest IDs first. Both are
// downgrades to "no idempotency guarantee", never to a wrongly delivered
// duplicate — the ack merely stops being sent for forgotten IDs.
//
// Locking discipline (AGENTS.md): the store owns its own mutexes and never
// touches connsMu or any model-state lock. Seen() is a single atomic
// check-and-record call on the caller's readLoop goroutine; nothing here
// blocks on the bus or the network.

const (
	// defaultIdempotencyWindow is the per-session window size, deliberately
	// aligned with DefaultSeqBufferCapacity so both bounded memories share
	// one mental constant.
	defaultIdempotencyWindow = DefaultSeqBufferCapacity
	// maxTrackedIdempotencySessions bounds the session map, mirroring the
	// seq store: sessions beyond this floor evict the least-recently-seen
	// window. Connections are already capped by MaxConnections, so this
	// only guards against long-lived servers accumulating dead session
	// state.
	maxTrackedIdempotencySessions = maxTrackedSeqSessions
)

// sessionDedupWindow tracks recently accepted inbound message IDs for one
// session. Insertion order is kept in a FIFO slice; once full, the oldest
// ID is evicted and the window downgrades to "no idempotency guarantee"
// (logged once, never silently).
type sessionDedupWindow struct {
	mu       sync.Mutex
	capacity int
	ids      map[string]struct{}
	order    []string // FIFO: order[0] is the oldest retained ID
	evicting bool     // set once eviction has started (log-once guard)
	lastSeen uint64   // store-level monotonic counter, eviction hint
}

func newSessionDedupWindow(capacity int) *sessionDedupWindow {
	if capacity <= 0 {
		capacity = defaultIdempotencyWindow
	}
	return &sessionDedupWindow{
		capacity: capacity,
		ids:      make(map[string]struct{}),
	}
}

// seen records msgID and reports whether it was already present. Check and
// record happen atomically so two connections racing the same ID produce
// exactly one delivery.
func (w *sessionDedupWindow) seen(tick uint64, msgID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastSeen = tick
	if _, ok := w.ids[msgID]; ok {
		return true
	}
	w.ids[msgID] = struct{}{}
	w.order = append(w.order, msgID)
	if len(w.order) > w.capacity {
		oldest := w.order[0]
		w.order = w.order[1:]
		delete(w.ids, oldest)
		if !w.evicting {
			w.evicting = true
			logger.DebugCF("pico", "Idempotency window full, oldest IDs evicted — duplicate detection degraded", map[string]any{
				"capacity": w.capacity,
			})
		}
	}
	return false
}

// unseen removes a previously recorded ID, used when delivery failed after
// Seen() so a client retry with the same ID can still be delivered. A no-op
// for unknown IDs.
func (w *sessionDedupWindow) unseen(msgID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.ids[msgID]; !ok {
		return
	}
	delete(w.ids, msgID)
	for i, id := range w.order {
		if id == msgID {
			w.order = append(w.order[:i], w.order[i+1:]...)
			break
		}
	}
}

// idempotencyStore tracks per-session dedup windows for the channel.
type idempotencyStore struct {
	mu       sync.Mutex
	capacity int
	windows  map[string]*sessionDedupWindow
	assigns  uint64 // monotonic seen counter, backs lastSeen
}

func newIdempotencyStore(capacity int) *idempotencyStore {
	if capacity <= 0 {
		capacity = defaultIdempotencyWindow
	}
	return &idempotencyStore{capacity: capacity, windows: make(map[string]*sessionDedupWindow)}
}

func (s *idempotencyStore) windowFor(sessionID string) *sessionDedupWindow {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.windows[sessionID]
	if ok {
		return w
	}
	if len(s.windows) >= maxTrackedIdempotencySessions {
		s.evictOldestLocked()
	}
	w = newSessionDedupWindow(s.capacity)
	s.windows[sessionID] = w
	return w
}

// evictOldestLocked drops the least-recently-seen window. Caller holds mu.
func (s *idempotencyStore) evictOldestLocked() {
	var oldestID string
	var oldestTick uint64
	for id, w := range s.windows {
		w.mu.Lock()
		tick := w.lastSeen
		w.mu.Unlock()
		if oldestID == "" || tick < oldestTick {
			oldestID, oldestTick = id, tick
		}
	}
	if oldestID != "" {
		delete(s.windows, oldestID)
	}
}

// Seen records msgID for the session and reports whether it was already
// accepted. Empty session or message IDs are never tracked (seq-less
// clients keep the legacy at-most-once behavior).
func (s *idempotencyStore) Seen(sessionID, msgID string) bool {
	if sessionID == "" || msgID == "" {
		return false
	}
	w := s.windowFor(sessionID)
	s.mu.Lock()
	s.assigns++
	tick := s.assigns
	s.mu.Unlock()
	return w.seen(tick, msgID)
}

// Unseen rolls back a Seen record after delivery failure.
func (s *idempotencyStore) Unseen(sessionID, msgID string) {
	if sessionID == "" || msgID == "" {
		return
	}
	s.mu.Lock()
	w, ok := s.windows[sessionID]
	s.mu.Unlock()
	if ok {
		w.unseen(msgID)
	}
}
