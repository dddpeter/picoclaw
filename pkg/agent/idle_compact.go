package agent

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// 空闲压缩扫描器（fork, 2026-10-05，docs/design/idle-compaction-design.zh.md）。
//
// 会话闲置 idle_after 后由网关后台做一次真正的摘要式压缩，使下次提问
// 从压缩后稳态开始（延迟优化 + 回合尾压缩失败会话的补票；overflow 防
// 线已由迭代边界压缩承担）。扫描器只挑时机：压缩动作 100% 复用
// contextManager.Compact（Reason=summarize），与 compactScheduler 共享
// inFlight 单飞与 drain 关机覆盖。
//
// 成功记账（评审新增，防 seahorse 空转）：computeContextUsage 的水位基于
// JSONL 原始历史，seahorse 压缩只动 SQLite 不回落 → 不能靠水位判断「已
// 压过」。记账以活动为唯一依据：压缩成功后 UpdatedAt 无前进则跳过。
const (
	// idleCompactMaxFails 是失败退避阈值：连续失败到此次数后长退避，
	// 直到会话有新活动（UpdatedAt 前进）才重置。
	idleCompactMaxFails = 3
)

// idleSessionRecord is the scanner's per-session bookkeeping. Owned by the
// scan goroutine only; compaction results flow back through resultCh.
type idleSessionRecord struct {
	successAt   time.Time // last successful idle compaction
	seenUpdated time.Time // session UpdatedAt at that moment
	fails       int
	lastFail    time.Time
}

type idleCompactResult struct {
	sessionKey string
	updated    time.Time // UpdatedAt snapshot taken when the compaction launched
	err        error
}

type idleCompactScanner struct {
	al  *AgentLoop
	cfg *config.IdleCompactConfig

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	records  map[string]*idleSessionRecord
	resultCh chan idleCompactResult
}

// StartIdleCompactScanner launches the idle-compaction scanner (idempotent;
// no-op when disabled or without a context manager).
func (al *AgentLoop) StartIdleCompactScanner(cfg *config.IdleCompactConfig) error {
	// Stop any previous instance first (review P3-4): a blind Store would
	// orphan the old goroutine (its stop channel belongs to the new
	// instance) and its independent records map would break bookkeeping.
	al.StopIdleCompactScanner()
	if cfg != nil && !cfg.EffectiveEnabled() {
		return nil
	}
	if al.contextManager == nil {
		return nil
	}
	scanner := &idleCompactScanner{
		al:       al,
		cfg:      cfg,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		records:  make(map[string]*idleSessionRecord),
		resultCh: make(chan idleCompactResult, 64),
	}
	al.idleScanner.Store(scanner)
	go scanner.run()
	return nil
}

// StopIdleCompactScanner stops the tick loop and waits for in-flight
// compactions to report back (bounded; the shared drainCompact covers the
// actual Compact calls at shutdown).
func (al *AgentLoop) StopIdleCompactScanner() {
	if s, ok := al.idleScanner.Load().(*idleCompactScanner); ok && s != nil {
		s.stopOnce.Do(func() { close(s.stop) })
		<-s.done
		al.idleScanner.Store((*idleCompactScanner)(nil))
	}
}

func (s *idleCompactScanner) run() {
	defer close(s.done)
	interval := s.cfg.EffectiveScanInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			s.drainResults()
			return
		case <-ticker.C:
			s.scanOnce()
			s.drainResults()
		case r := <-s.resultCh:
			s.applyResult(r)
		}
	}
}

// drainResults non-blockingly absorbs finished compactions between ticks.
func (s *idleCompactScanner) drainResults() {
	for {
		select {
		case r := <-s.resultCh:
			s.applyResult(r)
		default:
			return
		}
	}
}

func (s *idleCompactScanner) applyResult(r idleCompactResult) {
	rec := s.records[r.sessionKey]
	if rec == nil {
		rec = &idleSessionRecord{}
		s.records[r.sessionKey] = rec
	}
	if r.err != nil {
		rec.fails++
		rec.lastFail = time.Now()
		// Anchor the failure to the same UpdatedAt snapshot: without it the
		// record stays zero-valued and every later scan mistakes "still
		// idle" for "new activity" and resets the backoff.
		rec.seenUpdated = r.updated
		logger.WarnCF("agent", "Idle compaction failed", map[string]any{
			"session_key": r.sessionKey,
			"fails":       rec.fails,
			"error":       r.err.Error(),
		})
		return
	}
	rec.successAt = time.Now()
	rec.seenUpdated = r.updated
	rec.fails = 0
	logger.InfoCF("agent", "Idle compaction done", map[string]any{
		"session_key": r.sessionKey,
	})
}

// scanOnce walks every agent's sessions through the gate matrix (order is
// cost order: cheap mtime checks first, the history pull last).
func (s *idleCompactScanner) scanOnce() {
	idleAfter := s.cfg.EffectiveIdleAfter()
	minMessages := s.cfg.EffectiveMinHistoryMessages()

	for _, agentID := range s.al.registry.ListAgentIDs() {
		agent, ok := s.al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Sessions == nil {
			continue
		}
		for _, key := range agent.Sessions.ListSessions() {
			select {
			case <-s.stop:
				return
			default:
			}
			s.maybeCompactSession(agent, key, idleAfter, minMessages)
		}
	}
}

func (s *idleCompactScanner) skip(key, reason string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["session_key"] = key
	fields["skipped_reason"] = reason
	logger.DebugCF("agent", "Idle compact skipped", fields)
}

func (s *idleCompactScanner) maybeCompactSession(agent *AgentInstance, key string, idleAfter time.Duration, minMessages int) {
	updated, ok := agent.Sessions.LastModified(key)
	if !ok {
		s.skip(key, "no_mtime", nil)
		return
	}
	idleFor := time.Since(updated)
	if idleFor < idleAfter {
		s.skip(key, "not_idle", map[string]any{"idle_minutes": idleFor.Minutes()})
		return
	}
	if ts := s.al.getActiveTurnState(key); ts != nil {
		s.skip(key, "turn_active", nil)
		return
	}

	rec := s.records[key]
	if rec != nil {
		// Activity since the last bookkeeping resets the record: the session
		// earned a fresh compaction eligibility (and cleared its backoff).
		if updated.After(rec.seenUpdated) {
			delete(s.records, key)
			rec = nil
		} else if !rec.successAt.IsZero() {
			s.skip(key, "already_compacted", nil)
			return
		} else if rec.fails >= idleCompactMaxFails {
			s.skip(key, "backoff", map[string]any{"fails": rec.fails})
			return
		}
	}

	history := agent.Sessions.GetHistory(key)
	if len(history) < minMessages {
		s.skip(key, "too_few_messages", map[string]any{"messages": len(history)})
		return
	}
	usage := computeContextUsage(agent, key)
	if usage == nil {
		// Scanner context: nil (broken agent/window) means skip — unlike the
		// turn-end path where nil conservatively compacts, a 5-minute loop
		// over-compacting is worse than missing one.
		s.skip(key, "no_usage", nil)
		return
	}
	if !shouldCompactNow(agent.CompactUsageThreshold, usage, agent) {
		s.skip(key, "under_threshold", map[string]any{"history_tokens": usage.HistoryTokens})
		return
	}

	s.launchCompaction(agent, key, updated)
}

func (s *idleCompactScanner) launchCompaction(agent *AgentInstance, key string, updated time.Time) {
	scheduler := s.al.compactScheduler
	if scheduler == nil {
		return
	}
	if _, loaded := scheduler.inFlight.LoadOrStore(key, struct{}{}); loaded {
		s.skip(key, "in_flight", nil)
		return
	}
	// Snapshot the budget BEFORE the goroutine (review P3-5): CompactionBudget
	// reads agent.ContextWindow/MaxTokens and /switch's write path does not
	// take the model-state lock against background readers — evaluating it
	// here (scan time) keeps the data-race window to zero.
	budget := agent.CompactionBudget()
	scheduler.wg.Add(1)
	go func() {
		defer scheduler.wg.Done()
		defer scheduler.inFlight.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), compactCallTimeout)
		defer cancel()
		err := s.al.contextManager.Compact(ctx, &CompactRequest{
			SessionKey: key,
			Reason:     ContextCompressReasonSummarize,
			Budget:     budget,
		})
		select {
		case s.resultCh <- idleCompactResult{sessionKey: key, updated: updated, err: err}:
		case <-s.stop:
		}
	}()
	logger.InfoCF("agent", "Idle compaction started", map[string]any{
		"session_key":  key,
		"idle_minutes": time.Since(updated).Minutes(),
	})
}
