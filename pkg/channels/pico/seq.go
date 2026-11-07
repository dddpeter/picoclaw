package pico

import (
	"sync"
)

// Session outbound sequence numbers and catch-up replay (trae borrowing §一,
// docs/design/trae-harness-borrowing-analysis.zh.md).
//
// Every session-level broadcast gets a monotonic Seq before it hits the wire.
// Frames live in a fixed-capacity ring buffer, so a reconnecting client can
// replay exactly the frames it missed by presenting its last seen seq in the
// handshake (?last_seq=N). Frames whose broadcast failed (no active
// connections) stay in the ring and become replay material — the exact case
// that previously dropped stream deltas forever. Consecutive message.update
// frames for the same message coalesce into the latest snapshot (edit
// semantics make intermediate snapshots useless), keeping the ring's memory
// flat during long streams instead of growing with content length.
//
// Locking discipline (AGENTS.md): this buffer owns its own mutex per session
// and never touches connsMu or any model-state lock. Assignment is fire-and-
// forget; no broadcast path blocks on replay bookkeeping beyond the ring push.

const (
	// DefaultSeqBufferCapacity is the default per-session ring capacity.
	// Frames are cheap struct copies; 512 covers several minutes of dense
	// streaming while keeping the memory footprint bounded and small.
	DefaultSeqBufferCapacity = 512
	// maxTrackedSeqSessions bounds the session map: sessions beyond this
	// floor evict the least-recently-assigned buffer. Connections are
	// already capped by MaxConnections, so this only guards against
	// long-lived servers accumulating dead session state.
	maxTrackedSeqSessions = 256
)

// seqReplay is the result of a catch-up request.
type seqReplay struct {
	frames []PicoMessage
	// complete is false when the client's last_seq predates the ring's
	// oldest retained frame — the dropped gap is reported instead of
	// replayed (never silently elided).
	complete bool
	// dropped is the number of frames between last_seq and the oldest
	// retained frame when complete is false.
	dropped int
}

// sessionSeqBuffer holds the seq counter and ring buffer for one session.
type sessionSeqBuffer struct {
	mu         sync.Mutex
	next       uint64 // highest assigned seq; 0 means nothing assigned yet
	ring       []PicoMessage
	head       int    // index of the oldest retained frame
	count      int    // frames currently retained
	lastAssign uint64 // store-level monotonic counter, eviction hint
}

func newSessionSeqBuffer(capacity int) *sessionSeqBuffer {
	if capacity <= 0 {
		capacity = DefaultSeqBufferCapacity
	}
	return &sessionSeqBuffer{ring: make([]PicoMessage, capacity)}
}

// assign stamps msg with the next monotonic seq and retains a copy in the
// ring. Seq numbering starts at 1; 0 stays reserved for seq-less frames.
// Consecutive message.update frames for the same message coalesce into the
// tail slot (latest snapshot only): coalescing consumes a seq (next
// advances, so live clients still see every frame with its own seq) but
// leaves the retained count alone.
func (b *sessionSeqBuffer) assign(tick uint64, msg *PicoMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.next++
	msg.Seq = b.next
	b.lastAssign = tick

	if b.count > 0 {
		tail := &b.ring[(b.head+b.count-1)%len(b.ring)]
		if coalescableUpdate(tail, msg) {
			*tail = *msg
			return
		}
	}
	b.ring[(b.head+b.count)%len(b.ring)] = *msg
	if b.count == len(b.ring) {
		b.head = (b.head + 1) % len(b.ring) // overwrote the oldest slot
	} else {
		b.count++
	}
}

// coalescableUpdate reports whether newMsg supersedes oldMsg as a cumulative
// edit of the same message: both are message.update frames carrying the
// same non-empty message_id.
func coalescableUpdate(oldMsg, newMsg *PicoMessage) bool {
	if oldMsg.Type != TypeMessageUpdate || newMsg.Type != TypeMessageUpdate {
		return false
	}
	oldID, _ := oldMsg.Payload["message_id"].(string)
	newID, _ := newMsg.Payload["message_id"].(string)
	return oldID != "" && oldID == newID
}

// replaySince returns frames with seq in (lastSeq, newest], plus the catch-up
// boundary when part of the gap was evicted. A client ahead of the counter
// (e.g. after a server restart) gets an empty complete replay. Coalescing
// can leave internal seq holes (superseded updates); every hole is
// superseded by a retained newer frame, so holes don't break completeness —
// though dropped may over-count them after head eviction (safe: the client
// falls back to history and the latest snapshot wins anyway).
func (b *sessionSeqBuffer) replaySince(lastSeq uint64) seqReplay {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.next == 0 || lastSeq >= b.next {
		return seqReplay{complete: true}
	}

	oldest := b.ring[b.head].Seq // oldest retained seq (count > 0 here)
	complete := lastSeq+1 >= oldest
	dropped := 0
	if !complete {
		dropped = int(oldest - lastSeq - 1)
	}

	// Copy the retained frames newer than lastSeq, in ring order.
	frames := make([]PicoMessage, 0, b.count)
	for i := 0; i < b.count; i++ {
		msg := b.ring[(b.head+i)%len(b.ring)]
		if msg.Seq > lastSeq {
			frames = append(frames, msg)
		}
	}
	return seqReplay{frames: frames, complete: complete, dropped: dropped}
}

// seqStore tracks per-session buffers for the channel.
type seqStore struct {
	mu       sync.Mutex
	capacity int
	bufs     map[string]*sessionSeqBuffer
	assigns  uint64 // monotonic assign counter, backs lastAssign
}

func newSeqStore(capacity int) *seqStore {
	if capacity <= 0 {
		capacity = DefaultSeqBufferCapacity
	}
	return &seqStore{capacity: capacity, bufs: make(map[string]*sessionSeqBuffer)}
}

func (s *seqStore) bufferFor(sessionID string) *sessionSeqBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, ok := s.bufs[sessionID]
	if ok {
		return buf
	}
	if len(s.bufs) >= maxTrackedSeqSessions {
		s.evictOldestLocked()
	}
	buf = newSessionSeqBuffer(s.capacity)
	s.bufs[sessionID] = buf
	return buf
}

// evictOldestLocked drops the least-recently-assigned buffer. Caller holds mu.
func (s *seqStore) evictOldestLocked() {
	var oldestID string
	var oldestTick uint64
	for id, buf := range s.bufs {
		buf.mu.Lock()
		tick := buf.lastAssign
		buf.mu.Unlock()
		if oldestID == "" || tick < oldestTick {
			oldestID, oldestTick = id, tick
		}
	}
	if oldestID != "" {
		delete(s.bufs, oldestID)
	}
}

// assign stamps the message with the session's next seq and retains it.
func (s *seqStore) assign(sessionID string, msg *PicoMessage) {
	if sessionID == "" {
		return
	}
	buf := s.bufferFor(sessionID)
	s.mu.Lock()
	s.assigns++
	tick := s.assigns
	s.mu.Unlock()
	buf.assign(tick, msg)
}

// replaySince computes the catch-up payload for a reconnecting client.
func (s *seqStore) replaySince(sessionID string, lastSeq uint64) seqReplay {
	if sessionID == "" {
		return seqReplay{complete: true}
	}
	s.mu.Lock()
	buf, ok := s.bufs[sessionID]
	s.mu.Unlock()
	if !ok {
		return seqReplay{complete: true}
	}
	return buf.replaySince(lastSeq)
}
