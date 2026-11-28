package a2a

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// errBoom is the sentinel error used by tests to simulate agent failures.
var errBoom = errors.New("boom")

// IdempotencyStore deduplicates message/send by client messageId (fix plan
// task 1; mirrors the Pico channel's inbound idempotency pattern — map +
// TTL + bounded memory, in-memory only).
//
// Semantics (documented contract):
//   - Only successful results are cached; a failed call must re-run on retry.
//   - A repeated Put on the same key overwrites (cache of the latest result).
//   - Restart clears the store — a downgrade to "no dedup", never to a
//     wrongly delivered duplicate.
//
// Concurrency: two simultaneous requests sharing one messageId must run the
// agent loop once. Put/Get alone cannot give that (both would miss before
// either Put lands), so the store also single-flights in-progress keys:
// followers wait on the leader's done channel and share its outcome.
type IdempotencyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]idempotencyEntry
	flights map[string]*idempotencyFlight
}

type idempotencyEntry struct {
	result    any
	contextID string
	storedAt  time.Time
}

type idempotencyFlight struct {
	done chan struct{}
	// leader writes these before closing done; followers read after.
	result any
	err    error
}

// NewIdempotencyStore builds the store with the given result TTL. A janitor
// goroutine prunes expired entries every ten minutes; stop it via the
// returned Stop func (nil when ttl <= 0 — dedup disabled).
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	s := &IdempotencyStore{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]idempotencyEntry),
		flights: make(map[string]*idempotencyFlight),
	}
	if ttl <= 0 {
		return s
	}
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.prune()
		}
	}()
	return s
}

// prune drops expired entries; bounded memory follows the Pico channel's
// cleanup rhythm (periodic sweep rather than per-op eviction).
func (s *IdempotencyStore) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-s.ttl)
	for k, e := range s.entries {
		if e.storedAt.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}

// Get returns the cached result for messageId (ok=false on miss/expiry).
func (s *IdempotencyStore) Get(messageID string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[messageID]
	if !ok {
		return nil, false
	}
	if s.now().Sub(e.storedAt) >= s.ttl {
		delete(s.entries, messageID)
		return nil, false
	}
	return e.result, true
}

// ContextOf returns the contextId the cached answer belongs to ("" on miss).
func (s *IdempotencyStore) ContextOf(messageID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[messageID].contextID
}

// Put caches a successful result. Callers must only invoke this after the
// agent loop succeeded (failed calls stay uncached so retries re-run).
func (s *IdempotencyStore) Put(messageID string, result any, contextID string) {
	if messageID == "" {
		return
	}
	s.mu.Lock()
	s.entries[messageID] = idempotencyEntry{result: result, contextID: contextID, storedAt: s.now()}
	s.mu.Unlock()
}

// beginSingleFlight registers the caller for messageId. It returns
// (leaderMode, result, err): leaders run the agent loop and MUST call
// finishSingleFlight; followers get the leader's outcome directly.
func (s *IdempotencyStore) beginSingleFlight(messageID string) (bool, any, error) {
	if messageID == "" {
		return true, nil, nil // empty id: no dedup, behave as leader
	}
	s.mu.Lock()
	if f, running := s.flights[messageID]; running {
		s.mu.Unlock()
		<-f.done
		return false, f.result, f.err
	}
	f := &idempotencyFlight{done: make(chan struct{})}
	s.flights[messageID] = f
	s.mu.Unlock()
	return true, nil, nil
}

// beginSingleFlightCtx is beginSingleFlight with a ctx-bounded follower
// wait: a caller whose request dies while queued returns (false, nil,
// ctx.Err()) without disturbing the leader — unlike the unbounded
// message/send variant, the stream path serves real HTTP connections
// that can go away mid-wait. The flight entry itself stays until the
// leader settles it.
func (s *IdempotencyStore) beginSingleFlightCtx(ctx context.Context, messageID string) (bool, any, error) {
	if messageID == "" {
		return true, nil, nil
	}
	s.mu.Lock()
	if f, running := s.flights[messageID]; running {
		s.mu.Unlock()
		select {
		case <-f.done:
			return false, f.result, f.err
		case <-ctx.Done():
			return false, nil, ctx.Err()
		}
	}
	f := &idempotencyFlight{done: make(chan struct{})}
	s.flights[messageID] = f
	s.mu.Unlock()
	return true, nil, nil
}

// finishSingleFlight publishes the leader's outcome and wakes followers.
func (s *IdempotencyStore) finishSingleFlight(messageID string, result any, err error) {
	if messageID == "" {
		return
	}
	s.mu.Lock()
	f, ok := s.flights[messageID]
	if ok {
		f.result, f.err = result, err
		close(f.done)
		delete(s.flights, messageID)
	}
	s.mu.Unlock()
}

// dedupResult wraps the sync message/send execution with cache + single
// flight: cache hit answers without touching the agent loop; concurrent
// duplicates share one loop run; only successes are cached.
func (s *Server) dedupResult(messageID string, run func() (any, string, error)) (any, error) {
	if s.idem == nil || messageID == "" {
		res, _, err := run()
		return res, err
	}
	if cached, ok := s.idem.Get(messageID); ok {
		logger.InfoCF("a2a", "idempotency cache hit", map[string]any{"message_id": messageID})
		return cached, nil
	}
	leader, res, err := s.idem.beginSingleFlight(messageID)
	if !leader {
		return res, err
	}
	result, contextID, runErr := run()
	if runErr != nil {
		s.idem.finishSingleFlight(messageID, nil, runErr)
		return nil, runErr
	}
	s.idem.Put(messageID, result, contextID)
	s.idem.finishSingleFlight(messageID, result, nil)
	return result, nil
}
