package agent

import (
	"context"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// 迭代边界压缩（fork, 2026-10-05，借鉴 pi 的 prepareNextTurn 阈值压缩）。
//
// 背景：回合尾异步压缩只能处理「跨回合」的上下文增长；长重任务（代码评
// 审等）在单个 turn 的几十次工具迭代内就能把上下文从 60% 撑到超窗，触发
// 网关静默截断（空响应/length 截断）。唯一能保证每次 LLM 调用满足窗口
// 约束的位置是迭代边界——工具结果返回后、下一次调用前。
//
// 语义：
//   - NoHistory turn 不压缩（无历史可压）；
//   - 检查用的 token 是本地估算（EstimateMessageTokens 全量消息 + 工具
//     定义 + maxTokens 预留），与 isOverContextBudget 同基准；宁可早压
//     不可漏压，估算偏差由压缩后的重装兜底；
//   - 压缩走 ContextManager.Compact 同步执行（迭代间隙本就是等待点，
//     用户有 spinner/心跳反馈），成功后重新 Assemble 装载上下文；
//   - 压缩失败不阻断 turn：记录告警，继续用未压缩上下文调用（超限时
//     由既有的重试压缩链兜底）。
const (
	// iterationCompactSafetyTokens 在窗口之外额外预留的安全余量：估算
	// 器的偏差（模板开销、消息包装）与输出波动都从这里吸收。
	iterationCompactSafetyTokens = 8192
)

// compactBeforeLLMCall 在 CallLLM 的重试循环前执行：估算即将发送的上下
// 文，超窗则同步压缩并重装。返回是否发生了压缩。
func (p *Pipeline) compactBeforeLLMCall(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
) bool {
	if ts.opts.NoHistory || p.ContextManager == nil {
		return false
	}
	window := ts.agent.ContextWindow
	if window <= 0 {
		return false
	}
	msgTokens := 0
	for _, m := range exec.callMessages {
		msgTokens += EstimateMessageTokens(m)
	}
	toolTokens := EstimateToolDefsTokens(exec.providerToolDefs)
	if msgTokens+toolTokens+ts.agent.MaxTokens+iterationCompactSafetyTokens <= window {
		return false
	}

	logger.WarnCF("agent", "Context over budget at iteration boundary; compacting before LLM call", map[string]any{
		"agent_id":      ts.agent.ID,
		"session_key":   ts.sessionKey,
		"msg_tokens":    msgTokens,
		"tool_tokens":   toolTokens,
		"max_tokens":    ts.agent.MaxTokens,
		"window":        window,
		"message_count": len(exec.callMessages),
	})

	if err := p.ContextManager.Compact(ctx, &CompactRequest{
		SessionKey: ts.sessionKey,
		Reason:     ContextCompressReasonIteration,
		Budget:     ts.agent.CompactionBudget(),
	}); err != nil {
		logger.WarnCF("agent", "Iteration-boundary compact failed; continuing with current context", map[string]any{
			"session_key": ts.sessionKey,
			"error":       err.Error(),
		})
		return false
	}

	asmResp, asmErr := p.ContextManager.Assemble(ctx, &AssembleRequest{
		SessionKey: ts.sessionKey,
		Budget:     ts.agent.ContextWindow,
		MaxTokens:  ts.agent.MaxTokens,
	})
	if asmErr != nil || asmResp == nil {
		logger.WarnCF("agent", "Post-compact assemble failed; continuing with current context", map[string]any{
			"session_key": ts.sessionKey,
			"error":       asmErrText(asmErr),
		})
		return false
	}

	// 重装（与重试循环的 compact-rebuild 同模式）：splitHistoryForActiveTurn
	// 找出本轮已落盘的活动尾部（工具调用/结果），压缩只作用于稳定历
	// 史。ContextBuilder 重建后必须验证保形——重建把 [History+尾部] 折叠
	// 成别的请求形状时（工具消息归组/合并），currentTurnStart 的算术
	// （len−tailLen）会失真甚至为负；不保形则保持旧请求视图不动（稳定
	// 历史的压缩经由 exec.history 在下回合生效），本次的超窗交给第二段
	// split-turn 在旧视图上处理。
	_, activeTail := splitHistoryForActiveTurn(exec.messages, ts.persistedMessagesSnapshot())
	exec.history = asmResp.History
	if len(activeTail) > 0 {
		var rebuilt []providers.Message
		if ts.agent.ContextBuilder != nil {
			fullHistory := append(append([]providers.Message(nil), asmResp.History...), activeTail...)
			rebuildReq := promptBuildRequestForTurn(ts, fullHistory, asmResp.Summary, "", nil, p.Cfg)
			rebuilt = ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildReq)
		} else {
			rebuilt = append(append([]providers.Message(nil), asmResp.History...), activeTail...)
		}
		if matchingTurnMessageTail(rebuilt, activeTail) == len(activeTail) {
			exec.messages = rebuilt
			exec.currentTurnStart = len(rebuilt) - len(activeTail)
			exec.callMessages = exec.messages
			if exec.gracefulTerminal {
				exec.callMessages = append(append([]providers.Message(nil), exec.messages...), ts.interruptHintMessage())
			}
		} else {
			logger.WarnCF("agent", "Post-compact rebuild changed the turn tail shape; keeping current request view", map[string]any{
				"session_key": ts.sessionKey,
				"tail_msgs":   len(activeTail),
				"rebuilt":     len(rebuilt),
			})
		}
	}

	// 第二段（design ②）：稳定历史压完仍超窗——活动尾部独占窗口，唯一
	// 出路是把尾部的老前缀摘要化（不丢弃、不落盘），保留 recent tail。
	if estimateCallTokens(exec)+ts.agent.MaxTokens+iterationCompactSafetyTokens > window {
		p.doSplitTurnCompact(ctx, ts, exec)
	}
	return true
}

func asmErrText(err error) string {
	if err == nil {
		return "<nil response>"
	}
	return err.Error()
}

// clampMaxTokensToContext 按 pi 的 simple-options 模式动态钳制输出预
// 算（fork, 2026-10-05）：每次 LLM 调用前 max_tokens =
// min(配置值, 窗口 − 上下文估算 − 安全余量)，下限 1024。
// 上下文快满时自动压缩输出空间，避免「塞满窗口 → 无空间生成 → 网关
// 静默返回空响应/length 截断」。钳制只影响本次请求的 max_tokens 选项，
// 不改动 ts.agent.MaxTokens（门控与其他消费方语义不变）。
func clampMaxTokensToContext(maxTokens, contextWindow, contextTokens int) int {
	const minMaxTokens = 1024
	if contextWindow <= 0 || maxTokens <= 0 {
		return maxTokens
	}
	available := contextWindow - contextTokens - iterationCompactSafetyTokens
	if available >= maxTokens {
		return maxTokens
	}
	if available < minMaxTokens {
		return minMaxTokens
	}
	return available
}

// estimateCallTokens returns the estimated prompt cost of a call.
func estimateCallTokens(exec *turnExecution) int {
	msgTokens := 0
	for _, m := range exec.callMessages {
		msgTokens += EstimateMessageTokens(m)
	}
	return msgTokens + EstimateToolDefsTokens(exec.providerToolDefs)
}

// ─── Split-turn prefix summarization (design ②, 2026-10-05) ───
//
// docs/design/split-turn-compaction-design.zh.md：当稳定历史压缩后活动
// turn 尾部仍独占超窗时，把尾部的老前缀摘要化（而非整体保护），只保留
// recent tail 在请求视图里。协议安全靠切点吸附保证：切点永不落在 tool
// 消息上，前缀内 assistant(tool_calls) 与其结果的配对要么整体在前缀（一
// 起被摘要）、要么整体在保留尾。摘要只改请求消息视图，不落盘——JSONL
// 的事实记录不动，turn 结束后的常规压缩自然接管。

// turnPrefixSummarizationPrompt asks the model to compress a mid-turn
// conversation prefix so the same model can continue the task from the
// preserved tail. Structured output per the design: task goal / completed
// items / findings / current work / next steps.
const turnPrefixSummarizationPrompt = `你是对话压缩助手。以下是一个 agent 执行任务过程中的对话前缀（含工具调用与结果），请压缩成结构化摘要，供模型在只保留尾部对话的情况下继续执行同一任务。

必须保留：
- 任务目标：用户最初要求做什么（含关键约束）
- 已完成的检查/操作：编号清单，每项一句话
- 关键发现：编号清单，保留文件路径、函数名、错误信息等具体锚点
- 当前正在进行什么、下一步计划

可以丢弃：工具输出的原文细节、重复内容、冗长堆栈。

只输出摘要正文，不要任何前后缀说明或代码围栏。`

// splitTurnToolResultBudget caps each tool result fed into the summarizer
// (the summary needs facts, not full output).
const splitTurnToolResultBudget = 2000

// splitTurnSummarizeMaxTokens bounds the summary LLM call.
const splitTurnSummarizeMaxTokens = 4096

// splitTurnSummarizeTimeout bounds the summary call independently of the
// turn context (review P2-4) — a wedged summarizer fails open within 60s
// instead of stalling the turn up to the provider HTTP timeout.
const splitTurnSummarizeTimeout = 60 * time.Second

// defaultSplitTurnKeepTokens is the single source of the keep-tail budget
// default, shared by the config helper and the cut-point fallback (review
// P2-3: two independent 8192 magic numbers drifted apart waiting to happen).
const defaultSplitTurnKeepTokens = 8192

// findSplitTurnCutPoint picks the split index p (0 < p < len(tail)) so the
// prefix tail[:p] is summarized and tail[p:] is kept verbatim. Starting
// from the smallest p whose kept tail fits keepTokens, it then absorbs p
// forward past tool messages — a cut may never land ON a tool result, which
// would leave its parent assistant's tool_calls stranded in the prefix
// while the result lands in the kept tail (or vice versa).
func findSplitTurnCutPoint(tail []providers.Message, keepTokens int) int {
	if len(tail) < 2 {
		return -1
	}
	if keepTokens <= 0 {
		keepTokens = defaultSplitTurnKeepTokens
	}
	kept := 0
	p := len(tail)
	for i := len(tail) - 1; i > 0; i-- {
		kept += EstimateMessageTokens(tail[i])
		if kept >= keepTokens {
			p = i
			break
		}
	}
	if p <= 0 {
		return -1 // whole tail under budget — nothing worth splitting
	}
	// Absorb forward off tool messages: the kept tail must start at a
	// user/assistant boundary.
	for p < len(tail) && isToolResultMessage(tail[p]) {
		p++
	}
	if p >= len(tail) {
		return -1
	}
	return p
}

func isToolResultMessage(m providers.Message) bool {
	return m.Role == "tool" || m.ToolCallID != ""
}

// serializeTurnPrefix renders the to-be-summarized prefix as plain text for
// the summarizer: roles labeled, tool results capped.
func serializeTurnPrefix(prefix []providers.Message) string {
	var b strings.Builder
	for i := range prefix {
		m := &prefix[i]
		switch {
		case m.Role == "user":
			b.WriteString("[用户] ")
			b.WriteString(firstNRunes(m.Content, 4000))
			b.WriteString("\n")
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				name := tc.Name
				if tc.Function != nil {
					name = tc.Function.Name
				}
				b.WriteString("[调用工具] ")
				b.WriteString(name)
				b.WriteString(" ")
				if tc.Function != nil {
					b.WriteString(firstNRunes(tc.Function.Arguments, 400))
				}
				b.WriteString("\n")
			}
			if c := strings.TrimSpace(m.Content); c != "" {
				b.WriteString("[助手] ")
				b.WriteString(firstNRunes(c, 2000))
				b.WriteString("\n")
			}
		case m.Role == "assistant":
			if m.ReasoningContent != "" {
				b.WriteString("[助手思考] ")
				b.WriteString(firstNRunes(m.ReasoningContent, 2000))
				b.WriteString("\n")
			}
			if c := strings.TrimSpace(m.Content); c != "" {
				b.WriteString("[助手] ")
				b.WriteString(firstNRunes(c, 4000))
				b.WriteString("\n")
			}
		case isToolResultMessage(*m):
			b.WriteString("[工具结果] ")
			b.WriteString(firstNRunes(m.Content, splitTurnToolResultBudget))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func firstNRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// buildSplitTurnMessages reassembles the request view after the split:
// stable prefix (system + compacted history) + a rewritten user message
// carrying the turn-prefix summary + the kept tail verbatim. The summary
// rides inside the original user message so the turn keeps its leading
// user anchor (protocol shape), and the original message text survives
// word-for-word for task continuity.
func buildSplitTurnMessages(
	stable []providers.Message,
	turnUserAnchor providers.Message,
	summary string,
	keptTail []providers.Message,
) []providers.Message {
	// Clone the ORIGINAL user message and append the summary to its content
	// (review P1-1): rebuilding a Content-only message silently dropped
	// Media/Attachments/SystemParts — multimodal review turns lost their
	// images the moment split-turn fired.
	rewritten := turnUserAnchor
	rewritten.Content = rewritten.Content +
		"\n\n<history>\n" + summary + "\n</history>"
	out := make([]providers.Message, 0, len(stable)+1+len(keptTail))
	out = append(out, stable...)
	out = append(out, rewritten)
	out = append(out, keptTail...)
	return out
}

// turnUserAnchor returns the turn's original user message from the active
// tail (first user message). ok=false when absent — the split then falls
// back to a bare user anchor (nothing to preserve in that case).
func turnUserAnchor(tail []providers.Message) (providers.Message, bool) {
	for i := range tail {
		if tail[i].Role == "user" {
			return tail[i], true
		}
	}
	return providers.Message{}, false
}

// doSplitTurnCompact runs the split-turn reduction on exec's request view.
// It fires at most once per turn (ts.splitTurnDone), requires the agent to
// carry split-turn settings, and summarizes via a single direct provider
// call (no fallback chain rotation — a summarization failure must not
// switch models). Returns true when the view was rewritten.
func (p *Pipeline) doSplitTurnCompact(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
) bool {
	if !ts.agent.SplitTurnEnabled || ts.splitTurnDone {
		return false
	}
	if exec.currentTurnStart < 0 || exec.currentTurnStart >= len(exec.messages)-1 {
		return false // no meaningful active tail to split (empty stable is fine)
	}
	tail := exec.messages[exec.currentTurnStart:]
	p_ := findSplitTurnCutPoint(tail, ts.agent.SplitTurnKeepTokens)
	if p_ <= 0 {
		return false
	}

	prefix := tail[:p_]
	kept := tail[p_:]

	// The user message sits at the head of the tail and p_ >= 1 keeps it in
	// the serialized prefix — but make the task goal explicit at the top of
	// the summarizer input anyway (review P2-2): it is the one input the
	// summary cannot afford to miss, whatever future turn shapes do to the
	// prefix layout.
	summarizerInput := turnPrefixSummarizationPrompt
	if anchor, ok := turnUserAnchor(tail); ok && strings.TrimSpace(anchor.Content) != "" {
		summarizerInput += "\n\n[任务目标]\n" + firstNRunes(anchor.Content, 2000)
	}
	summarizerInput += "\n\n<conversation>\n" + serializeTurnPrefix(prefix) + "\n</conversation>"

	sumOpts := map[string]any{
		"max_tokens":  splitTurnSummarizeMaxTokens,
		"temperature": 0.3,
	}
	// Independent timeout (review P2-4): the summarizer must not hold the
	// turn hostage beyond its own budget — provider HTTP timeouts bound it
	// eventually, but 60s is all a mid-turn reduction is worth.
	sumCtx, cancelSum := context.WithTimeout(ctx, splitTurnSummarizeTimeout)
	defer cancelSum()
	resp, err := exec.activeProvider.Chat(sumCtx,
		[]providers.Message{{
			Role:    "user",
			Content: summarizerInput,
		}},
		nil, exec.llmModel, sumOpts)
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		logger.WarnCF("agent", "Split-turn summarization failed; continuing with current context", map[string]any{
			"session_key": ts.sessionKey,
			"prefix_msgs": len(prefix),
			"error":       asmErrText(err),
		})
		return false
	}
	// A length-truncated summary is still better than dropping the prefix
	// outright — accept it (design §3.3).

	stable := exec.messages[:exec.currentTurnStart]
	anchor, hasAnchor := turnUserAnchor(tail)
	if !hasAnchor {
		anchor = providers.Message{Role: "user"}
	}
	newMessages := buildSplitTurnMessages(stable, anchor, resp.Content, kept)
	exec.messages = newMessages
	exec.callMessages = newMessages
	if exec.gracefulTerminal {
		exec.callMessages = append(append([]providers.Message(nil), newMessages...), ts.interruptHintMessage())
	}
	exec.currentTurnStart = len(stable) + 1 // the rewritten user anchor
	ts.splitTurnDone = true

	dropped := 0
	for i := range prefix {
		dropped += EstimateMessageTokens(prefix[i])
	}
	logger.WarnCF("agent", "Split-turn prefix summarized", map[string]any{
		"session_key":    ts.sessionKey,
		"prefix_msgs":    len(prefix),
		"prefix_tokens":  dropped,
		"kept_msgs":      len(kept),
		"summary_tokens": EstimateMessageTokens(providers.Message{Role: "user", Content: resp.Content}),
		"turn_start_now": exec.currentTurnStart,
	})
	return true
}
