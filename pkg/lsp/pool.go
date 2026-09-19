package lsp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Pool keeps idle-TTL language-server sessions keyed by root+server
// (design §3.6): the first matching call pays the cold start, subsequent
// calls reuse the session; sessions idle beyond the TTL are reaped;
// (root,server) pairs that failed to start are circuit-broken for the
// process lifetime (opencode's broken-set, prevents re-crashing a broken
// server on every tool call).
type Pool struct {
	mu       sync.Mutex
	sessions map[string]*session
	broken   map[string]string // key -> failure reason
	starting map[string]chan struct{}
	idleTTL  time.Duration
	timeout  time.Duration

	stopOnce sync.Once
	stop     chan struct{}
}

type session struct {
	client   *Client
	lastUsed time.Time
}

// NewPool creates a session pool with the given idle TTL and per-request
// timeout, and starts the background reaper.
func NewPool(idleTTL, requestTimeout time.Duration) *Pool {
	if idleTTL <= 0 {
		idleTTL = 5 * time.Minute
	}
	p := &Pool{
		sessions: make(map[string]*session),
		broken:   make(map[string]string),
		starting: make(map[string]chan struct{}),
		idleTTL:  idleTTL,
		timeout:  requestTimeout,
		stop:     make(chan struct{}),
	}
	go p.reaper()
	return p
}

func poolKey(root, serverID string) string { return root + "\x00" + serverID }

// Acquire returns a live client for (root, cfg), starting one if needed.
func (p *Pool) Acquire(ctx context.Context, cfg ServerConfig, root string) (*Client, error) {
	key := poolKey(root, cfg.Name)
	for {
		p.mu.Lock()
		if s, ok := p.sessions[key]; ok {
			s.lastUsed = time.Now()
			p.mu.Unlock()
			return s.client, nil
		}
		if reason, ok := p.broken[key]; ok {
			p.mu.Unlock()
			return nil, fmt.Errorf("LSP server %s is disabled for this session (failed earlier: %s)", cfg.Name, reason)
		}
		if ch, ok := p.starting[key]; ok {
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		started := make(chan struct{})
		p.starting[key] = started
		p.mu.Unlock()

		client, err := p.startSession(ctx, cfg, root, key, started)
		if err != nil {
			return nil, err
		}
		return client, nil
	}
}

func (p *Pool) startSession(ctx context.Context, cfg ServerConfig, root, key string, started chan struct{}) (*Client, error) {
	client, err := Start(ctx, cfg, root, p.timeout)

	p.mu.Lock()
	delete(p.starting, key)
	if err != nil {
		if _, exists := p.broken[key]; !exists {
			p.broken[key] = err.Error()
		}
	}
	close(started)
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("LSP server %s failed to start: %w", cfg.Name, err)
	}
	p.sessions[key] = &session{client: client, lastUsed: time.Now()}
	p.mu.Unlock()
	return client, nil
}

// Touch refreshes a session's idle timestamp (called after each use).
func (p *Pool) Touch(root, serverID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[poolKey(root, serverID)]; ok {
		s.lastUsed = time.Now()
	}
}

// Broken reports the failure reason recorded for (root, server), if any.
func (p *Pool) Broken(root, serverID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reason, ok := p.broken[poolKey(root, serverID)]
	return reason, ok
}

func (p *Pool) reaper() {
	interval := p.idleTTL / 4
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.reapIdle()
		}
	}
}

func (p *Pool) reapIdle() {
	now := time.Now()
	var stale []*Client
	p.mu.Lock()
	for key, s := range p.sessions {
		if now.Sub(s.lastUsed) > p.idleTTL {
			stale = append(stale, s.client)
			delete(p.sessions, key)
		}
	}
	p.mu.Unlock()
	for _, c := range stale {
		c.Shutdown(500 * time.Millisecond)
	}
}

// Close shuts down every pooled session and stops the reaper.
func (p *Pool) Close() {
	p.stopOnce.Do(func() { close(p.stop) })

	p.mu.Lock()
	sessions := make([]*session, 0, len(p.sessions))
	for key, s := range p.sessions {
		sessions = append(sessions, s)
		delete(p.sessions, key)
	}
	p.mu.Unlock()
	for _, s := range sessions {
		s.client.Shutdown(500 * time.Millisecond)
	}
}

// SessionCount reports live sessions (diagnostics/tests).
func (p *Pool) SessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}
