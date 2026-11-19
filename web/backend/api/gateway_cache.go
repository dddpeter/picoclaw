package api

import (
	"os"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Status-path caches. GET /api/gateway/status is polled every 2s by the
// launcher UI, and every /pico/* proxy request re-validates the gateway pid;
// without these caches each poll pays two full config.json loads plus a fork
// of ps(1) for the pid command-line classification.

// configCache memoizes the last config load keyed by (mtime, size):
// SaveConfig writes via WriteFileAtomic (temp + fsync + rename), so every
// write produces a fresh mtime and the cache self-invalidates — handlers
// that write the config (reset, bindings, PATCH) see their own write on the
// next read. The cached *config.Config is shared across callers and must be
// treated as read-only; model-name normalization happens once at fill time.
type configCacheEntry struct {
	modTime time.Time
	size    int64
	cfg     *config.Config
}

var (
	configCacheMu    sync.Mutex
	configCachePath  string
	configCacheValue *configCacheEntry
)

// cachedConfigFor returns the parsed config for path, re-reading only when
// the file's identity changed since the last load. On stat failure the
// entry keys on zero values (matches LoadConfig's missing-file default
// config) so a file appearing later invalidates naturally.
func cachedConfigFor(path string) (*config.Config, error) {
	var modTime time.Time
	var size int64
	if info, err := os.Stat(path); err == nil {
		modTime = info.ModTime()
		size = info.Size()
	}

	configCacheMu.Lock()
	defer configCacheMu.Unlock()
	if configCacheValue != nil && configCachePath == path &&
		configCacheValue.modTime.Equal(modTime) &&
		configCacheValue.size == size {
		return configCacheValue.cfg, nil
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		// Do not cache failures: a transient parse error must not pin a
		// poisoned entry (the next poll re-reads and self-heals).
		return nil, err
	}
	normalizeStoredModelNames(cfg)
	configCachePath = path
	configCacheValue = &configCacheEntry{modTime: modTime, size: size, cfg: cfg}
	return cfg, nil
}

// pidClassifyTTL bounds how long a pid command-line classification
// (matcher = one ps fork) is reused. The pid-file read path re-confirms
// liveness via Signal(0) on every call, so the cache only memoizes the
// "is this live pid a picoclaw gateway" verdict — PID-reuse inside the TTL
// window is vanishingly rare and non-security-relevant (the verdict gates
// attach/telemetry, not access).
const pidClassifyTTL = 30 * time.Second

type pidClassifyEntry struct {
	ok       bool
	decisive bool
	at       time.Time
}

var (
	pidClassifyMu    sync.Mutex
	pidClassifyCache = map[int]pidClassifyEntry{}
)

// classifyGatewayProcessCached memoizes gatewayProcessMatcher per pid.
func classifyGatewayProcessCached(pid int) (ok, decisive bool) {
	pidClassifyMu.Lock()
	if e, hit := pidClassifyCache[pid]; hit && time.Since(e.at) < pidClassifyTTL {
		pidClassifyMu.Unlock()
		return e.ok, e.decisive
	}
	pidClassifyMu.Unlock()

	ok, decisive = gatewayProcessMatcher(pid)

	pidClassifyMu.Lock()
	if len(pidClassifyCache) > 128 {
		pidClassifyCache = map[int]pidClassifyEntry{}
	}
	pidClassifyCache[pid] = pidClassifyEntry{ok: ok, decisive: decisive, at: time.Now()}
	pidClassifyMu.Unlock()
	return ok, decisive
}

// resetGatewayCachesForTest drops both caches so tests observe fresh loads
// and canary matcher calls (resetGatewayTestState calls this).
func resetGatewayCachesForTest() {
	configCacheMu.Lock()
	configCachePath = ""
	configCacheValue = nil
	configCacheMu.Unlock()

	pidClassifyMu.Lock()
	pidClassifyCache = map[int]pidClassifyEntry{}
	pidClassifyMu.Unlock()
}
