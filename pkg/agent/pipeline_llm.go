// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// truncatedAnswerNote is appended to a direct answer whose finish_reason is
// "length": the text was cut by an output token cap and the user deserves to
// know why it stops mid-sentence (fork, 2026-10-05; observed with a channel
// capped at ~512 output tokens on MiniMax-M3).
const truncatedAnswerNote = "\n\n⚠ 本段输出因达到输出 token 上限被截断，可回复“继续”让我补全余下内容。"

// answerContinuationDirective is the request-view-only user message that
// seeds the continuation call. It is never persisted to session history —
// Finalize writes the stitched answer as the one assistant message.
func answerContinuationDirective() providers.Message {
	return providers.Message{
		Role: "user",
		Content: "（系统指令：你的上一条回复因输出 token 上限被截断。请从上一次输出结束的精确位置续写剩余内容：" +
			"不要重复已输出的任何内容，不要道歉或重新开头，直接继续写到完整结束。）",
	}
}

// parrotRetryDirective is the request-view-only corrective message for the
// parrot-only answer auto-retry. It is never persisted to session history —
// the parroted draft is dropped, not shipped or stored.
func parrotRetryDirective() providers.Message {
	return providers.Message{
		Role: "user",
		Content: "（系统指令：你上一条回复把工具调用格式当作正文复述了——那些 [tool_use: ...]、[tool_result ...] 行不是真实的工具调用，" +
			"不会被任何系统执行。请重新回答用户的问题：需要执行操作时必须通过真实的工具调用通道发起；" +
			"绝不要在回复正文里输出这类标记行。）",
	}
}

// CallLLM performs an LLM call with fallback support, hook invocation, and retry logic.
// It handles PreLLM setup, the actual LLM invocation with retry, and AfterLLM processing.
// Returns Control indicating what the coordinator should do next.
//
// Turn-level retry bounds are structural, not budgeted: each CallLLM burns at
// most (1+MaxLLMRetries)×len(candidates) real upstream calls, and a fully
// failed call terminates the turn immediately (turn_coord.go). Exponential
// cooldowns (providers/cooldown.go) keep repeated real failures per candidate
// bounded within long turns. See docs/design/turn-llm-failure-resilience.zh.md.
func (p *Pipeline) CallLLM(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) (Control, error) {
	al := p.al
	maxMediaSize := p.Cfg.Agents.Defaults.GetMaxMediaSize()
	maxContextImages := p.Cfg.Agents.Defaults.GetMaxContextImages()

	// PreLLM: resolve media refs (except on iteration 1 where user media is already resolved)
	if iteration > 1 {
		exec.messages = resolveMediaRefs(exec.messages, p.MediaStore, maxMediaSize, maxContextImages, exec.currentTurnStart)
	}

	// PreLLM: graceful terminal handling
	exec.gracefulTerminal, _ = ts.gracefulInterruptRequested()
	exec.providerToolDefs = ts.agent.Tools.ToProviderDefs()
	exec.providerToolDefs = filterToolsByTurnProfile(exec.providerToolDefs, ts.profile)

	// Native web search support
	webSearchEnabled := al.cfg.Tools.IsToolEnabled("web") && turnProfileToolAllowed(ts.profile, "web_search")
	exec.useNativeSearch = webSearchEnabled && al.cfg.Tools.Web.PreferNative &&
		func() bool {
			if ns, ok := exec.activeProvider.(providers.NativeSearchCapable); ok {
				return ns.SupportsNativeSearch()
			}
			return false
		}()
	if exec.useNativeSearch {
		filtered := make([]providers.ToolDefinition, 0, len(exec.providerToolDefs))
		for _, td := range exec.providerToolDefs {
			if td.Function.Name != "web_search" {
				filtered = append(filtered, td)
			}
		}
		exec.providerToolDefs = filtered
	}

	exec.callMessages = exec.messages
	if exec.gracefulTerminal {
		exec.callMessages = append(append([]providers.Message(nil), exec.messages...), ts.interruptHintMessage())
		exec.providerToolDefs = nil
		ts.markGracefulTerminalUsed()
	}
	if err := p.routeMediaTurn(ts, exec); err != nil {
		return ControlBreak, err
	}

	// Settle the effective model BEFORE any consumer of exec.llmModel runs
	// below (review P1-1a): routeMediaTurn may have rotated the active
	// provider/model to a vision candidate, and the split-turn summarizer
	// inside compactBeforeLLMCall reads the model name — previously it fired
	// with the vision provider but a stale (first iteration: empty) model id.
	exec.llmModel = exec.activeModel

	// Tool-definition token cost is fixed once the tool set is filtered:
	// compute it once and reuse across the boundary check, clamp and retries
	// (review P3-2: re-marshaling every tool schema per estimate).
	exec.toolDefTokens = EstimateToolDefsTokens(exec.providerToolDefs)

	// Iteration-boundary compaction (fork, 2026-10-05, pi's prepareNextTurn
	// pattern): long heavy turns grow context 60%→over-window between turn
	// endings, which post-turn async compaction never sees. Check the
	// assembled context here — after tool results landed, before the LLM
	// call — and compact synchronously when it exceeds the window.
	p.compactBeforeLLMCall(ctx, ts, exec)

	// Dynamic output-budget clamp (pi's clampMaxTokensToContext): leave
	// room for generation instead of requesting max_tokens the window
	// cannot hold — a stuffed window with a large output budget is the
	// classic recipe for silent empty responses on aggregate gateways.
	clampedMaxTokens := clampMaxTokensToContext(
		ts.agent.MaxTokens, ts.agent.ContextWindow, estimateCallTokens(exec))

	exec.llmOpts = map[string]any{
		"max_tokens":       clampedMaxTokens,
		"temperature":      ts.agent.Temperature,
		"prompt_cache_key": ts.agent.ID,
	}
	if exec.useNativeSearch {
		exec.llmOpts["native_search"] = true
	}
	applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)

	nativeSearchBeforeHook := exec.useNativeSearch

	// BeforeLLM hook
	if p.Hooks != nil {
		llmReq, decision := p.Hooks.BeforeLLM(turnCtx, &LLMHookRequest{
			Meta:             ts.eventMeta("runTurn", "turn.llm.request"),
			Context:          cloneTurnContext(ts.turnCtx),
			Model:            exec.llmModel,
			Messages:         exec.callMessages,
			Tools:            exec.providerToolDefs,
			Options:          exec.llmOpts,
			GracefulTerminal: exec.gracefulTerminal,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				prevModel := exec.llmModel
				exec.llmModel = llmReq.Model
				exec.callMessages = llmReq.Messages
				exec.providerToolDefs = filterToolsByTurnProfile(llmReq.Tools, ts.profile)
				exec.llmOpts = llmReq.Options
				if strings.TrimSpace(exec.llmModel) != "" && exec.llmModel != prevModel {
					if err := p.applyBeforeLLMModelRewrite(ts, exec); err != nil {
						return ControlBreak, err
					}
					applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)
				}
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLMWithReason(turnCtx, exec, streamCancelReasonHookAbort)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLMWithReason(turnCtx, exec, streamCancelReasonHardAbort)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}
	exec.useNativeSearch = webSearchEnabled && al.cfg.Tools.Web.PreferNative &&
		func() bool {
			if ns, ok := exec.activeProvider.(providers.NativeSearchCapable); ok {
				return ns.SupportsNativeSearch()
			}
			return false
		}()
	if nativeSearchBeforeHook && !exec.useNativeSearch {
		exec.providerToolDefs = restoreToolDefinition(
			exec.providerToolDefs,
			filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefs(), ts.profile),
			"web_search",
		)
	}
	if exec.useNativeSearch {
		exec.providerToolDefs = filterClientWebSearch(exec.providerToolDefs)
		if exec.llmOpts == nil {
			exec.llmOpts = make(map[string]any)
		}
		exec.llmOpts["native_search"] = true
	} else {
		delete(exec.llmOpts, "native_search")
	}

	al.emitEvent(
		runtimeevents.KindAgentLLMRequest,
		ts.eventMeta("runTurn", "turn.llm.request"),
		LLMRequestPayload{
			Model:         exec.llmModel,
			MessagesCount: len(exec.callMessages),
			ToolsCount:    len(exec.providerToolDefs),
			MaxTokens:     ts.agent.MaxTokens,
			Temperature:   ts.agent.Temperature,
		},
	)

	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          ts.agent.ID,
			"iteration":         iteration,
			"model":             exec.llmModel,
			"messages_count":    len(exec.callMessages),
			"tools_count":       len(exec.providerToolDefs),
			"max_tokens":        ts.agent.MaxTokens,
			"temperature":       ts.agent.Temperature,
			"system_prompt_len": len(exec.callMessages[0].Content),
		})
	logger.DebugCF("agent", "Full LLM request",
		map[string]any{
			"iteration":     iteration,
			"messages_json": formatMessagesForLog(exec.callMessages),
			"tools_json":    formatToolsForLog(exec.providerToolDefs),
		})

	// LLM call closure with fallback support
	callLLM := func(
		messagesForCall []providers.Message,
		toolDefsForCall []providers.ToolDefinition,
	) (*providers.LLMResponse, error) {
		providerCtx, providerCancel := context.WithCancel(turnCtx)
		ts.setProviderCancel(providerCancel)
		defer func() {
			providerCancel()
			ts.clearProviderCancel(providerCancel)
		}()

		al.activeRequestsInc()
		defer al.activeRequestsDec()

		if response, handled, streamErr := p.tryConfiguredStreamingLLM(
			providerCtx,
			ts,
			exec,
			messagesForCall,
			toolDefsForCall,
		); handled {
			return response, streamErr
		}

		runCandidate := func(
			ctx context.Context,
			candidate providers.FallbackCandidate,
		) (*providers.LLMResponse, error) {
			candidateProvider, err := providerForFallbackCandidate(
				ts.agent,
				exec.activeProvider,
				exec.activeCandidates,
				candidate,
			)
			if err != nil {
				return nil, err
			}
			callOpts := shallowCloneLLMOptions(exec.llmOpts)
			delete(callOpts, "thinking_level")
			candidateTools := toolDefsForCall
			candidateNativeSearch := webSearchEnabled && al.cfg.Tools.Web.PreferNative &&
				func() bool {
					if ns, ok := candidateProvider.(providers.NativeSearchCapable); ok {
						return ns.SupportsNativeSearch()
					}
					return false
				}()
			if candidateNativeSearch {
				candidateTools = filterClientWebSearch(candidateTools)
				callOpts["native_search"] = true
			} else {
				delete(callOpts, "native_search")
				if exec.useNativeSearch {
					candidateTools = restoreToolDefinition(
						candidateTools,
						filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefs(), ts.profile),
						"web_search",
					)
				}
			}
			candidateCfg := resolveActiveModelConfig(
				p.Cfg,
				ts.agent.Workspace,
				[]providers.FallbackCandidate{candidate},
				candidate.Model,
				p.Cfg.Agents.Defaults.Provider,
			)
			candidateThinking := thinkingSettingsFromModelConfig(candidateCfg)
			applyThinkingOption(callOpts, candidateProvider, candidateThinking, true, ts.agent.ID)
			exec.suppressReasoning = shouldSuppressReasoningFor(candidateThinking)
			return candidateProvider.Chat(ctx, messagesForCall, candidateTools, candidate.Model, callOpts)
		}

		if len(exec.activeCandidates) > 1 && p.Fallback != nil {
			var (
				fbResult *providers.FallbackResult
				fbErr    error
			)
			if hasMediaRefs(messagesForCall) {
				fbResult, fbErr = p.Fallback.ExecuteImageCandidate(
					providerCtx,
					exec.activeCandidates,
					func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
						return runCandidate(ctx, candidate)
					},
				)
			} else {
				fbResult, fbErr = p.Fallback.ExecuteCandidate(
					providerCtx,
					exec.activeCandidates,
					runCandidate,
				)
			}
			if fbErr != nil {
				return nil, fbErr
			}
			if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
				logger.InfoCF(
					"agent",
					fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
						fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
					map[string]any{"agent_id": ts.agent.ID, "iteration": iteration},
				)
			}
			for _, candidate := range exec.activeCandidates {
				if candidate.StableKey() != fbResult.IdentityKey {
					continue
				}
				exec.llmModelName = resolvedCandidateModelName(
					[]providers.FallbackCandidate{candidate},
					exec.llmModelName,
				)
				break
			}
			return fbResult.Response, nil
		}
		return exec.activeProvider.Chat(providerCtx, messagesForCall, toolDefsForCall, exec.llmModel, exec.llmOpts)
	}

	// Retry loop
	var err error
	maxRetries := p.Cfg.Agents.Defaults.MaxLLMRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	backoffSecs := p.Cfg.Agents.Defaults.LLMRetryBackoffSecs
	if backoffSecs <= 0 {
		backoffSecs = 2
	}
	for retry := 0; retry <= maxRetries; retry++ {
		// Re-clamp the output budget on EVERY attempt (review P2-1/P2-2):
		// context-error retries rebuild (shrink) exec.callMessages and the
		// BeforeLLM hook may replace llmOpts wholesale — a clamp computed
		// once before the loop froze the budget at the worst moment (stuffed
		// context → floor 1024) and starved the recovered request's output.
		if exec.llmOpts == nil {
			exec.llmOpts = map[string]any{
				"temperature":      ts.agent.Temperature,
				"prompt_cache_key": ts.agent.ID,
			}
		}
		exec.llmOpts["max_tokens"] = clampMaxTokensToContext(
			ts.agent.MaxTokens, ts.agent.ContextWindow, estimateCallTokens(exec))

		exec.response, err = callLLM(exec.callMessages, exec.providerToolDefs)
		if err == nil {
			break
		}
		if ts.hardAbortRequested() && errors.Is(err, context.Canceled) {
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
		// Stall watchdog gracefully cancelled the in-flight call to unblock
		// the turn: treat it as a checkpoint, not an LLM failure. Continuing
		// re-enters the loop with a fresh provider context so the model gets
		// its promised wrap-up call; if it stalls again the watchdog's
		// hard-abort escalation bounds the loop.
		if graceful, hint := ts.gracefulInterruptRequested(); graceful && errors.Is(err, context.Canceled) {
			logger.WarnCF("agent", "LLM call cancelled by graceful interrupt; continuing turn for wrap-up",
				map[string]any{
					"agent_id": ts.agent.ID,
					"hint":     hint,
				})
			return ControlContinue, nil
		}
		if isConfiguredStreamingVisibleError(err) {
			break
		}

		if hasMediaRefs(exec.callMessages) && isVisionUnsupportedError(err) {
			return ControlBreak, visionUnsupportedModelError(
				exec.llmModelName,
				len(ts.agent.ImageCandidates) > 0,
			)
		}

		errMsg := strings.ToLower(err.Error())
		retryReason, isTransientError := transientLLMRetryReason(err)
		// An empty completion (200 + zero choices / an empty stream) is
		// typically an upstream silently dropping an over-limit prompt:
		// resending the identical payload returns the same emptiness. When
		// there is history to compress, route it to the compression path
		// below instead of the plain transient retry; NoHistory turns keep
		// the transient retry (nothing to compress, a gateway glitch is then
		// the likelier cause).
		var emptyCompletion *common.EmptyCompletionError
		isEmptyCompletion := !ts.opts.NoHistory && errors.As(err, &emptyCompletion)
		isContextError := isEmptyCompletion || (!isTransientError && (strings.Contains(errMsg, "context_length_exceeded") ||
			strings.Contains(errMsg, "context window") ||
			strings.Contains(errMsg, "context_window") ||
			strings.Contains(errMsg, "maximum context length") ||
			strings.Contains(errMsg, "token limit") ||
			strings.Contains(errMsg, "too many tokens") ||
			strings.Contains(errMsg, "max_tokens") ||
			strings.Contains(errMsg, "invalidparameter") ||
			strings.Contains(errMsg, "prompt is too long") ||
			strings.Contains(errMsg, "request too large")))

		if isTransientError && !isEmptyCompletion && retry < maxRetries {
			backoff := time.Duration(retry+1) * time.Duration(backoffSecs) * time.Second
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     retryReason,
					Error:      err.Error(),
					Backoff:    backoff,
				},
			)
			logger.WarnCF("agent", "Transient LLM error, retrying after backoff", map[string]any{
				"error":   err.Error(),
				"reason":  retryReason,
				"retry":   retry,
				"backoff": backoff.String(),
			})
			if sleepErr := sleepWithContext(turnCtx, backoff); sleepErr != nil {
				if ts.hardAbortRequested() {
					_ = ts.requestHardAbort()
					return ControlBreak, nil
				}
				err = sleepErr
				break
			}
			continue
		}

		if isContextError && retry < maxRetries && !ts.opts.NoHistory {
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     "context_limit",
					Error:      err.Error(),
				},
			)
			logger.WarnCF(
				"agent",
				"Context window error detected, attempting compression",
				map[string]any{
					"error": err.Error(),
					"retry": retry,
				},
			)

			if retry == 0 && !constants.IsInternalChannel(ts.channel) {
				al.bus.PublishOutbound(ctx, outboundMessageForTurn(
					ts,
					"Context window exceeded. Compressing history and retrying...",
				))
			}

			if compactErr := p.ContextManager.Compact(ctx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonRetry,
				Budget:     ts.agent.CompactionBudget(),
			}); compactErr != nil {
				logger.WarnCF("agent", "Context overflow compact failed", map[string]any{
					"session_key": ts.sessionKey,
					"error":       compactErr.Error(),
				})
			}
			if asmResp, asmErr := p.ContextManager.Assemble(ctx, &AssembleRequest{
				SessionKey: ts.sessionKey,
				Budget:     ts.agent.ContextWindow,
				MaxTokens:  ts.agent.MaxTokens,
			}); asmErr == nil && asmResp != nil {
				exec.history = asmResp.History
				exec.summary = asmResp.Summary
			}
			contextualSkills := ts.activeSkills
			if ts.agent.ContextBuilder != nil {
				contextualSkills = ts.agent.ContextBuilder.ResolveActiveSkillsForContext(ts.activeSkills)
			}
			ts.recordSkillContextSnapshot(skillContextTriggerContextRetryRebuild, contextualSkills)
			stableHistory, protectedTurnTail := splitHistoryForActiveTurn(
				exec.history,
				ts.persistedMessagesSnapshot(),
			)
			buildMessages := func(trimmedHistory []providers.Message) []providers.Message {
				fullHistory := append(append([]providers.Message(nil), trimmedHistory...), protectedTurnTail...)
				rebuildPromptReq := promptBuildRequestForTurn(ts, fullHistory, exec.summary, "", nil, p.Cfg)
				rebuildPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
				rebuilt := ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildPromptReq)
				return resolveMediaRefs(
					rebuilt,
					p.MediaStore,
					maxMediaSize,
					maxContextImages,
					len(rebuilt)-len(protectedTurnTail),
				)
			}
			originalHistoryCount := len(exec.history)
			var fit bool
			var trimmedStableHistory []providers.Message
			trimmedStableHistory, exec.callMessages, fit = trimHistoryToFitContextWindow(
				stableHistory,
				func(trimmedHistory []providers.Message) []providers.Message {
					rebuilt := buildMessages(trimmedHistory)
					if exec.gracefulTerminal {
						return append(append([]providers.Message(nil), rebuilt...), ts.interruptHintMessage())
					}
					return rebuilt
				},
				ts.agent.ContextWindow,
				exec.providerToolDefs,
				ts.agent.MaxTokens,
			)
			exec.history = append(trimmedStableHistory, protectedTurnTail...)
			exec.messages = buildMessages(trimmedStableHistory)
			exec.currentTurnStart = len(exec.messages) - len(protectedTurnTail)
			if exec.gracefulTerminal {
				msgs := append([]providers.Message(nil), exec.messages...)
				exec.callMessages = append(msgs, ts.interruptHintMessage())
			}
			if dropped := originalHistoryCount - len(exec.history); dropped > 0 {
				logger.WarnCF("agent", "Trimmed rebuilt history after context retry compaction", map[string]any{
					"session_key":     ts.sessionKey,
					"retry":           retry,
					"dropped_msgs":    dropped,
					"remaining_msgs":  len(exec.history),
					"context_window":  ts.agent.ContextWindow,
					"max_tokens":      ts.agent.MaxTokens,
					"still_overlimit": !fit,
				})
			} else if !fit {
				logger.WarnCF("agent", "Context still exceeds budget after retry compaction rebuild", map[string]any{
					"session_key":         ts.sessionKey,
					"retry":               retry,
					"history_msgs":        len(exec.history),
					"protected_turn_msgs": len(protectedTurnTail),
					"context_window":      ts.agent.ContextWindow,
					"max_tokens":          ts.agent.MaxTokens,
				})
			}
			if !fit {
				// The stable history is fully trimmed yet the payload still
				// overflows — the active turn tail alone fills the window.
				// The retry rebuild above reconstructed callMessages from the
				// RAW tail, undoing any earlier split-turn view. Try a
				// (forced) split-turn reduction on the rebuilt view before
				// giving up (review P2-3): the force flag bypasses the
				// one-per-turn throttle because each entry here is a fresh
				// context failure, and total attempts stay bounded by
				// maxRetries.
				if p.doSplitTurnCompact(ctx, ts, exec, true) {
					logger.WarnCF("agent", "Active turn tail still over budget after retry compaction; split-turn summarized and retrying", map[string]any{
						"session_key": ts.sessionKey,
						"retry":       retry,
					})
					continue
				}
				err = fmt.Errorf(
					"context window still exceeded after retry compaction; refusing to drop active turn messages: %w",
					err,
				)
				break
			}
			continue
		}
		break
	}

	if err != nil {
		al.emitEvent(
			runtimeevents.KindAgentError,
			ts.eventMeta("runTurn", "turn.error"),
			ErrorPayload{
				Stage:   "llm",
				Message: err.Error(),
			},
		)
		logger.ErrorCF("agent", "LLM call failed",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"model":     exec.llmModel,
				"error":     err.Error(),
			})
		return ControlBreak, fmt.Errorf("LLM call failed after retries: %w", err)
	}

	// AfterLLM hook
	if p.Hooks != nil {
		llmResp, decision := p.Hooks.AfterLLM(turnCtx, &LLMHookResponse{
			Meta:     ts.eventMeta("runTurn", "turn.llm.response"),
			Context:  cloneTurnContext(ts.turnCtx),
			Model:    exec.llmModel,
			Response: exec.response,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				exec.response = llmResp.Response
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLMWithReason(turnCtx, exec, streamCancelReasonHookAbort)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLMWithReason(turnCtx, exec, streamCancelReasonHardAbort)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}

	// Save finishReason and usage on the turn state. Use ts directly (the
	// authoritative turn state for this call) rather than a context lookup:
	// the raw ctx passed to CallLLM is not seeded with turnState (only turnCtx
	// is), so turnStateFromContext(ctx) returns nil here and silently dropped
	// both the finish reason and the per-turn token usage. ts is also exactly
	// what the streaming publisher reads via GetLastUsage at finalize.
	if ts != nil {
		ts.SetLastFinishReason(exec.response.FinishReason)
		if exec.response.Usage != nil {
			ts.SetLastUsage(exec.response.Usage)
		}
	}

	if exec.suppressReasoning {
		exec.response.Reasoning = ""
		exec.response.ReasoningContent = ""
		exec.response.ReasoningDetails = nil
	}
	reasoningContent := responseReasoningContent(exec.response)
	shouldPublishPicoToolCallInterim := ts.channel == "pico" && len(exec.response.ToolCalls) > 0
	if shouldPublishPicoToolCallInterim {
		// Pico tool-call turns publish their reasoning/content/tool summary as a
		// structured sequence after the tool-call payload is normalized below.
	} else if ts.channel == "pico" {
		if exec.streamingPublisher != nil && exec.streamingPublisher.ReasoningPublished() {
			if err := exec.streamingPublisher.FinalizeReasoning(turnCtx, reasoningContent); err != nil {
				logger.WarnCF("agent", "Failed to finalize streamed pico reasoning", map[string]any{
					"channel": ts.channel,
					"chat_id": ts.chatID,
					"error":   err.Error(),
				})
			}
		} else {
			// Publish pico thoughts before the turn context is canceled at return time.
			// The async variant can race with turn teardown and intermittently drop the
			// thought message in CI even though the LLM produced reasoning content.
			al.publishPicoReasoning(turnCtx, reasoningContent, ts.chatID, ts.sessionKey, exec.llmModelName)
		}
	} else {
		go al.handleReasoning(
			turnCtx,
			reasoningContent,
			ts.channel,
			al.targetReasoningChannelID(ts.channel),
		)
	}
	al.emitEvent(
		runtimeevents.KindAgentLLMResponse,
		ts.eventMeta("runTurn", "turn.llm.response"),
		LLMResponsePayload{
			ContentLen:   len(exec.response.Content),
			ToolCalls:    len(exec.response.ToolCalls),
			HasReasoning: exec.response.Reasoning != "" || exec.response.ReasoningContent != "",
		},
	)

	llmResponseFields := map[string]any{
		"agent_id":       ts.agent.ID,
		"iteration":      iteration,
		"content_chars":  len(exec.response.Content),
		"tool_calls":     len(exec.response.ToolCalls),
		"reasoning":      exec.response.Reasoning,
		"target_channel": al.targetReasoningChannelID(ts.channel),
		"channel":        ts.channel,
	}
	if exec.response.Usage != nil {
		llmResponseFields["prompt_tokens"] = exec.response.Usage.PromptTokens
		llmResponseFields["completion_tokens"] = exec.response.Usage.CompletionTokens
		llmResponseFields["total_tokens"] = exec.response.Usage.TotalTokens
	}
	logger.DebugCF("agent", "LLM response", llmResponseFields)

	// No-tool-call path: steering check and direct response
	if len(exec.response.ToolCalls) == 0 || exec.gracefulTerminal {
		responseContent := exec.response.Content
		if responseContent == "" && exec.response.ReasoningContent != "" && ts.channel != "pico" {
			responseContent = exec.response.ReasoningContent
		}
		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			// Keep the streaming card alive across steering continuations — the
			// next iteration reuses it via BeginStream, so the whole turn stays
			// on one card. Only fold the finished reasoning round into the panel.
			if exec.streamingPublisher != nil {
				if rc := responseReasoningContent(exec.response); strings.TrimSpace(rc) != "" {
					_ = exec.streamingPublisher.FinalizeReasoning(turnCtx, rc)
				}
			}
			// A length-truncated piece overtaken by steering must not be lost:
			// bank it so the eventual stitched answer still carries it (the
			// steering context supersedes the continuation directive).
			if responseContent != "" && exec.response.FinishReason == "length" {
				exec.answerContinuationParts = append(exec.answerContinuationParts, responseContent)
			}
			logger.InfoCF("agent", "Steering arrived after direct LLM response; continuing turn",
				map[string]any{
					"agent_id":       ts.agent.ID,
					"iteration":      iteration,
					"steering_count": len(steerMsgs),
				})
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			return ControlContinue, nil
		}

		// Parrot-only answer auto-retry (fork, 2026-10-05): a direct answer
		// that strips to nothing under the tool-call parrot filter is the
		// model replaying history marker lines instead of answering. Ending
		// the turn there shows the user the interception note and asks them
		// to re-ask; re-asking is exactly what the model needs, so do it
		// automatically: drop the parroted draft, correct the model once,
		// and keep the same card. If the retry parrots again the normal
		// path takes over (filter + interception note at seal). Checked
		// before the length continuation — continuing a parroted draft is
		// pointless.
		if responseContent != "" && !exec.gracefulTerminal &&
			strings.TrimSpace(stripToolCallParrot(responseContent)) == "" &&
			!exec.parrotRetryUsed && iteration < ts.agent.MaxIterations {
			exec.parrotRetryUsed = true
			// Request view only: the parroted draft must not reach history.
			exec.messages = append(exec.messages,
				providers.Message{
					Role:      "assistant",
					Content:   responseContent,
					ModelName: exec.llmModelName,
				},
				parrotRetryDirective(),
			)
			logger.InfoCF("agent", "Parrot-only direct answer; retrying with corrective directive",
				map[string]any{
					"agent_id":      ts.agent.ID,
					"iteration":     iteration,
					"content_chars": len(responseContent),
				})
			return ControlContinue, nil
		}

		// Truncated-answer auto-continuation (fork, 2026-10-05): output caps
		// that live server-side (aggregate "token plan" channels cap at a few
		// hundred completion tokens regardless of the requested max_tokens)
		// cut direct answers mid-sentence. Instead of ending the turn on a
		// truncation note, keep the turn alive: append the partial answer to
		// the request view, ask the model to continue from the cut, and
		// stitch the pieces into the final answer. The note only appears
		// when the continuation budget is exhausted and the text is still
		// cut; graceful interrupts skip the loop (the user asked to stop).
		// Budget: agents.defaults.answer_continuation_limit (nil=3, 0=off).
		answerContinuationLimit := 3
		if p.Cfg != nil {
			answerContinuationLimit = p.Cfg.Agents.Defaults.GetAnswerContinuationLimit()
		}
		if responseContent != "" && exec.response.FinishReason == "length" &&
			!exec.gracefulTerminal &&
			len(exec.answerContinuationParts) < answerContinuationLimit &&
			iteration < ts.agent.MaxIterations {
			exec.answerContinuationParts = append(exec.answerContinuationParts, responseContent)
			// Request view only: Finalize persists the stitched whole to
			// history once, so the pieces must not be written separately.
			exec.messages = append(exec.messages,
				providers.Message{
					Role:      "assistant",
					Content:   responseContent,
					ModelName: exec.llmModelName,
				},
				answerContinuationDirective(),
			)
			logger.InfoCF("agent", "Direct answer truncated by output cap; auto-continuing",
				map[string]any{
					"agent_id":     ts.agent.ID,
					"iteration":    iteration,
					"piece_chars":  len(responseContent),
					"continuation": len(exec.answerContinuationParts),
				})
			return ControlContinue, nil
		}
		if len(exec.answerContinuationParts) > 0 {
			exec.answerContinuationParts = append(exec.answerContinuationParts, responseContent)
			responseContent = strings.Join(exec.answerContinuationParts, "")
			exec.answerContinuationParts = nil
		}
		// A "length" finish on the direct answer means the output was cut by
		// the token cap (config max_tokens, the dynamic context clamp, or a
		// channel-side output limit — all surface identically here) AND the
		// continuation budget could not mend it. Append an honest note so
		// card, history, and outbound all say why the text stops
		// mid-sentence; tool-call batches already have their own guard in
		// ExecuteTools and are re-issued, so this is the only silent case.
		if responseContent != "" && exec.response.FinishReason == "length" {
			responseContent += truncatedAnswerNote
		}
		exec.finalContent = responseContent
		logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
			map[string]any{
				"agent_id":      ts.agent.ID,
				"iteration":     iteration,
				"content_chars": len(exec.finalContent),
			})
		return ControlBreak, nil
	}
	// Tool-call path: fold this iteration's reasoning round into the process
	// panel but keep the streaming card alive — BeginStream reuses it for the
	// next iteration, so one turn renders as one card instead of one card per
	// LLM call. tryConfiguredStreamingLLM replaces the publisher on the next
	// call (same underlying streamer), so tool steps keep flowing to the panel.
	if exec.streamingPublisher != nil {
		if rc := responseReasoningContent(exec.response); strings.TrimSpace(rc) != "" {
			_ = exec.streamingPublisher.FinalizeReasoning(turnCtx, rc)
		}
	}

	// Tool-call path: normalize and prepare for tool execution
	exec.normalizedToolCalls = make([]providers.ToolCall, 0, len(exec.response.ToolCalls))
	for _, tc := range exec.response.ToolCalls {
		exec.normalizedToolCalls = append(exec.normalizedToolCalls, providers.NormalizeToolCall(tc))
	}

	toolNames := make([]string, 0, len(exec.normalizedToolCalls))
	for _, tc := range exec.normalizedToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  ts.agent.ID,
			"tools":     toolNames,
			"count":     len(exec.normalizedToolCalls),
			"iteration": iteration,
		})

	exec.allResponsesHandled = len(exec.normalizedToolCalls) > 0
	assistantMsg := providers.Message{
		Role:             "assistant",
		Content:          exec.response.Content,
		ModelName:        exec.llmModelName,
		ReasoningContent: reasoningContent,
	}
	for _, tc := range exec.normalizedToolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
			exec.response,
			tc,
			exec.messages,
		)
		extraContent := tc.ExtraContent
		if strings.TrimSpace(toolFeedbackExplanation) != "" {
			if extraContent == nil {
				extraContent = &providers.ExtraContent{}
			}
			extraContent.ToolFeedbackExplanation = toolFeedbackExplanation
		}
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     extraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	exec.messages = append(exec.messages, assistantMsg)
	if !ts.opts.NoHistory {
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, assistantMsg)
		ts.recordPersistedMessage(assistantMsg)
		ts.ingestMessage(turnCtx, al, assistantMsg)
	}
	if shouldPublishPicoToolCallInterim {
		al.publishPicoToolCallInterim(
			turnCtx,
			ts,
			exec.llmModelName,
			reasoningContent,
			exec.response.Content,
			assistantMsg.ToolCalls,
		)
	}

	return ControlToolLoop, nil
}

func restoreToolDefinition(
	current,
	available []providers.ToolDefinition,
	name string,
) []providers.ToolDefinition {
	for _, tool := range current {
		if strings.EqualFold(tool.Function.Name, name) {
			return current
		}
	}
	for _, tool := range available {
		if strings.EqualFold(tool.Function.Name, name) {
			return append(current, tool)
		}
	}
	return current
}

func (p *Pipeline) applyBeforeLLMModelRewrite(ts *turnState, exec *turnExecution) error {
	if p == nil || ts == nil || ts.agent == nil || exec == nil {
		return nil
	}
	rawModel := strings.TrimSpace(exec.llmModel)
	if rawModel == "" {
		return nil
	}

	defaultProvider := "openai"
	if p.Cfg != nil {
		if provider := strings.TrimSpace(p.Cfg.Agents.Defaults.Provider); provider != "" {
			defaultProvider = provider
		}
	}
	defaultProvider = effectiveDefaultProvider(defaultProvider)
	candidates := resolveModelCandidates(p.Cfg, defaultProvider, rawModel, nil)
	if len(candidates) == 0 {
		return fmt.Errorf("hook-selected model %q could not be resolved", rawModel)
	}
	candidate := candidates[0]
	provider := ts.agent.CandidateProviders[candidateProviderKey(candidate)]
	if provider == nil {
		if candidate.ConfigKey == "" && candidate.ConfigIndex == 0 {
			if len(exec.activeCandidates) > 0 &&
				providers.NormalizeProvider(exec.activeCandidates[0].Provider) !=
					providers.NormalizeProvider(candidate.Provider) {
				return fmt.Errorf("hook-selected model %q has no configured provider %q", rawModel, candidate.Provider)
			}
			provider = exec.activeProvider
		} else if len(exec.activeCandidates) > 0 &&
			providers.NormalizeProvider(exec.activeCandidates[0].Provider) ==
				providers.NormalizeProvider(candidate.Provider) &&
			candidateCanInheritProvider(p.Cfg, ts.agent.Workspace, candidate) {
			provider = exec.activeProvider
		} else {
			modelCfg, err := resolvedCandidateModelConfig(p.Cfg, candidate, ts.agent.Workspace)
			if err != nil {
				return fmt.Errorf("resolve hook-selected model %q: %w", rawModel, err)
			}
			factory := providers.CreateProviderFromConfig
			if p.al != nil && p.al.providerFactory != nil {
				factory = p.al.providerFactory
			}
			provider, _, err = factory(modelCfg)
			if err != nil {
				return fmt.Errorf("initialize hook-selected model %q: %w", rawModel, err)
			}
			exec.ownedProviders = append(exec.ownedProviders, provider)
		}
	}
	if provider == nil {
		return fmt.Errorf("hook-selected model %q has no active provider", rawModel)
	}
	exec.activeCandidates = candidates
	exec.activeProvider = provider
	exec.activeModel = resolvedCandidateModel(candidates, rawModel)
	exec.llmModel = exec.activeModel
	exec.activeModelConfig = resolveActiveModelConfig(p.Cfg, ts.agent.Workspace, candidates, rawModel, defaultProvider)
	return nil
}

func providerForFallbackCandidate(
	agent *AgentInstance,
	activeProvider providers.LLMProvider,
	activeCandidates []providers.FallbackCandidate,
	candidate providers.FallbackCandidate,
) (providers.LLMProvider, error) {
	if agent != nil {
		if cp, ok := agent.CandidateProviders[candidateProviderKey(candidate)]; ok && cp != nil {
			return cp, nil
		}
	}
	if len(activeCandidates) > 0 &&
		activeCandidates[0].StableKey() == candidate.StableKey() &&
		activeProvider != nil {
		return activeProvider, nil
	}
	if candidate.ConfigKey != "" || candidate.ConfigIndex > 0 {
		return nil, fmt.Errorf("fallback model %q has no initialized configured provider", candidate.Model)
	}
	if len(activeCandidates) > 0 &&
		providers.NormalizeProvider(activeCandidates[0].Provider) != providers.NormalizeProvider(candidate.Provider) {
		return nil, fmt.Errorf("fallback model %q has no configured provider %q", candidate.Model, candidate.Provider)
	}
	if activeProvider == nil {
		return nil, fmt.Errorf("fallback model %q has no active provider", candidate.Model)
	}
	return activeProvider, nil
}

func transientLLMRetryReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	if failErr := providers.ClassifyError(err, "", ""); failErr != nil {
		switch failErr.Reason {
		case providers.FailoverTimeout:
			if failErr.Status >= 500 {
				return "server_error", true
			}
			return "timeout", true
		case providers.FailoverNetwork:
			return "network", true
		case providers.FailoverRateLimit, providers.FailoverOverloaded:
			return "rate_limit", true
		}
	}

	errMsg := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errMsg, "deadline exceeded") ||
		strings.Contains(errMsg, "client.timeout") ||
		strings.Contains(errMsg, "timed out") ||
		strings.Contains(errMsg, "timeout exceeded") {
		return "timeout", true
	}

	if strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "network is unreachable") ||
		strings.Contains(errMsg, "read tcp") ||
		strings.Contains(errMsg, "write tcp") ||
		strings.Contains(errMsg, "eof") {
		return "network", true
	}

	return "", false
}
