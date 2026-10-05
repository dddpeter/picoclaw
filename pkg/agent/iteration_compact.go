package agent

import (
	"context"

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
	// 找出本轮尚未落盘的活动尾部（工具调用/结果），压缩只作用于稳定历
	// 史；重建后的 messages = 新稳定历史 + 活动尾部，currentTurnStart
	// 指向活动尾部起点，封口/中止语义不漂移。
	_, activeTail := splitHistoryForActiveTurn(exec.messages, ts.persistedMessagesSnapshot())
	if ts.agent.ContextBuilder != nil {
		fullHistory := append(append([]providers.Message(nil), asmResp.History...), activeTail...)
		rebuildReq := promptBuildRequestForTurn(ts, fullHistory, asmResp.Summary, "", nil, p.Cfg)
		exec.messages = ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildReq)
	} else {
		exec.messages = append(append([]providers.Message(nil), asmResp.History...), activeTail...)
	}
	exec.history = asmResp.History
	exec.currentTurnStart = len(exec.messages) - len(activeTail)
	exec.callMessages = exec.messages
	if exec.gracefulTerminal {
		exec.callMessages = append(append([]providers.Message(nil), exec.messages...), ts.interruptHintMessage())
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
