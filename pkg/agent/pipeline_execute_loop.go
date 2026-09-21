// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// This file holds the internals of Pipeline.ExecuteTools (see
// pipeline_execute.go): the per-loop state carrier, the BeforeTool /
// AfterTool hook decision handlers and the tool-invocation / result-handling
// helpers they share. Everything here is behavior-preserving extraction —
// the orchestration order lives in ExecuteTools.

// toolLoopAction tells ExecuteTools what to do after a helper returns.
type toolLoopAction int

const (
	// toolLoopProceed: continue within the current tool call (hook allowed
	// or modified it; approval/execution/result handling continues).
	toolLoopProceed toolLoopAction = iota
	// toolLoopNext: this tool call is fully handled; move to the next one.
	toolLoopNext
	// toolLoopBreak: stop the tool loop (checkpoint hit); fall through to
	// the post-loop steering/finalize logic.
	toolLoopBreak
	// toolLoopAbort: return ToolControlBreak immediately (hook abort).
	toolLoopAbort
)

// toolLoopState carries the mutable state of one ExecuteTools pass so the
// extracted handlers can share it without long parameter lists. messages and
// handledAttachments are written back to exec by finishToolExecution.
type toolLoopState struct {
	al   *AgentLoop
	ts   *turnState
	exec *turnExecution

	ctx     context.Context // ExecuteTools ctx: media sends, user publishes
	turnCtx context.Context // turn-lifetime ctx: hooks, tool exec, streaming

	iteration int

	toolCalls []providers.ToolCall // exec.normalizedToolCalls
	index     int                  // current tool-call index (checkpoint math)

	messages           []providers.Message
	handledAttachments []providers.Attachment
}

// appendToolResultMessage appends a tool-role message and, when history is
// recorded, persists and ingests it. gateOnToolPersist mirrors the
// hook-respond condition (!NoHistory && persistsToolMessages()).
func (ls *toolLoopState) appendToolResultMessage(msg providers.Message, gateOnToolPersist bool) {
	ls.messages = append(ls.messages, msg)
	if ls.ts.opts.NoHistory {
		return
	}
	if gateOnToolPersist && !ls.ts.persistsToolMessages() {
		return
	}
	ls.ts.agent.Sessions.AddFullMessage(ls.ts.sessionKey, msg)
	ls.ts.recordPersistedMessage(msg)
	ls.ts.ingestMessage(ls.turnCtx, ls.al, msg)
}

// appendToolNoticeMessage appends a tool-role notice (deny / skip) and
// persists it without ingesting it into the context manager.
func (ls *toolLoopState) appendToolNoticeMessage(msg providers.Message, gateOnToolPersist bool) {
	ls.messages = append(ls.messages, msg)
	if ls.ts.opts.NoHistory {
		return
	}
	if gateOnToolPersist && !ls.ts.persistsToolMessages() {
		return
	}
	ls.ts.agent.Sessions.AddFullMessage(ls.ts.sessionKey, msg)
	ls.ts.recordPersistedMessage(msg)
}

// appendSubTurnMessage appends a drained sub-turn result message and
// persists it (no recordPersistedMessage, mirroring the original paths).
func (ls *toolLoopState) appendSubTurnMessage(msg providers.Message, gateOnToolPersist bool) {
	ls.messages = append(ls.messages, msg)
	if ls.ts.opts.NoHistory {
		return
	}
	if gateOnToolPersist && !ls.ts.persistsToolMessages() {
		return
	}
	ls.ts.agent.Sessions.AddFullMessage(ls.ts.sessionKey, msg)
}

// appendDeniedToolResult answers one tool call with a denial notice: marks
// the responses unhandled, emits the skip event and persists the message.
// Shared by the token-limit, turn-profile, hook-deny and approval-deny paths.
func (p *Pipeline) appendDeniedToolResult(ls *toolLoopState, tc providers.ToolCall, toolName, denyContent string) {
	ls.exec.allResponsesHandled = false
	ls.al.emitEvent(
		runtimeevents.KindAgentToolExecSkipped,
		ls.ts.eventMeta("runTurn", "turn.tool.skipped"),
		ToolExecSkippedPayload{
			Tool:   toolName,
			Reason: denyContent,
		},
	)
	deniedMsg := providers.Message{
		Role:       "tool",
		Content:    denyContent,
		ToolCallID: tc.ID,
	}
	ls.appendToolNoticeMessage(deniedMsg, true)
}

// denyToolByTurnProfile denies the call when the active turn profile does
// not allow this tool.
func (p *Pipeline) denyToolByTurnProfile(ls *toolLoopState, tc providers.ToolCall, toolName string) bool {
	if turnProfileToolAllowed(ls.ts.profile, toolName) {
		return false
	}
	p.appendDeniedToolResult(ls, tc, toolName,
		fmt.Sprintf("Tool %q is not allowed by the active turn profile.", toolName))
	return true
}

// handleBeforeToolDecision runs the BeforeTool hook and applies its
// decision. Returns the loop action plus the (possibly modified) tool name
// and arguments. A respond decision without a HookResult only logs a warning
// and lets the call proceed to approval/execution (historical behavior).
func (p *Pipeline) handleBeforeToolDecision(
	ls *toolLoopState,
	tc providers.ToolCall,
	toolName string,
	toolArgs map[string]any,
) (toolLoopAction, string, map[string]any) {
	al := ls.al
	ts := ls.ts
	toolReq, decision := al.hooks.BeforeTool(ls.turnCtx, &ToolCallHookRequest{
		Meta:      ts.eventMeta("runTurn", "turn.tool.before"),
		Context:   cloneTurnContext(ts.turnCtx),
		Tool:      toolName,
		Arguments: toolArgs,
	})
	switch decision.normalizedAction() {
	case HookActionContinue, HookActionModify:
		if toolReq != nil {
			toolName = toolReq.Tool
			toolArgs = toolReq.Arguments
		}
		return toolLoopProceed, toolName, toolArgs

	case HookActionRespond:
		if toolReq != nil && toolReq.HookResult != nil {
			return p.handleHookRespond(ls, tc, toolName, toolArgs, toolReq.HookResult), toolName, toolArgs
		}
		logger.WarnCF("agent", "Hook returned respond action but no HookResult provided",
			map[string]any{
				"agent_id": ts.agent.ID,
				"tool":     toolName,
				"action":   "respond",
			})

	case HookActionDenyTool:
		p.appendDeniedToolResult(ls, tc, toolName,
			hookDeniedToolContent("Tool execution denied by hook", decision.Reason))
		return toolLoopNext, toolName, toolArgs

	case HookActionAbortTurn:
		ls.exec.abortedByHook = true
		return toolLoopAbort, toolName, toolArgs

	case HookActionHardAbort:
		_ = ts.requestHardAbort()
		ls.exec.abortedByHardAbort = true
		return toolLoopAbort, toolName, toolArgs
	}
	return toolLoopProceed, toolName, toolArgs
}

// handleHookRespond materializes a hook-supplied answer for the tool call:
// publishes feedback/for-user/media, records the tool result message, then
// applies the steering / graceful-interrupt checkpoint.
func (p *Pipeline) handleHookRespond(
	ls *toolLoopState,
	tc providers.ToolCall,
	toolName string,
	toolArgs map[string]any,
	hookResult *tools.ToolResult,
) toolLoopAction {
	al := ls.al
	ts := ls.ts

	argsJSON, _ := json.Marshal(toolArgs)
	argsPreview := utils.Truncate(string(argsJSON), 200)
	logger.InfoCF("agent", fmt.Sprintf("Tool call (hook respond): %s(%s)", toolName, argsPreview),
		map[string]any{
			"agent_id":  ts.agent.ID,
			"tool":      toolName,
			"iteration": ls.iteration,
		})

	al.emitEvent(
		runtimeevents.KindAgentToolExecStart,
		ts.eventMeta("runTurn", "turn.tool.start"),
		ToolExecStartPayload{
			Tool:      toolName,
			Arguments: cloneEventArguments(toolArgs),
		},
	)

	p.publishToolFeedback(ls, tc, toolName, toolArgs)

	toolDuration := time.Duration(0)

	shouldSendForUser := !hookResult.Silent && hookResult.ForUser != "" &&
		(ts.opts.SendResponse || hookResult.ResponseHandled)
	if shouldSendForUser {
		al.bus.PublishOutbound(ls.ctx, bus.OutboundMessage{
			Context: bus.InboundContext{
				Channel: ts.channel,
				ChatID:  ts.chatID,
				Raw: map[string]string{
					"is_tool_call": "true",
				},
			},
			Content: hookResult.ForUser,
		})
	}

	if len(hookResult.Media) > 0 && hookResult.ResponseHandled {
		p.deliverHandledMedia(ls, toolName, hookResult.Media,
			"Failed to deliver hook media",
			func(sendErr error) {
				hookResult.IsError = true
				hookResult.ForLLM = fmt.Sprintf("failed to deliver attachment: %v", sendErr)
			}, func() {
				hookResult.ResponseHandled = false
			})
	}

	if !hookResult.ResponseHandled {
		ls.exec.allResponsesHandled = false
	}

	contentForLLM := hookResult.ContentForLLM()
	if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
		contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
	}

	var toolResultMedia []string
	if len(hookResult.Media) > 0 && !hookResult.ResponseHandled {
		hookResult.ArtifactTags = buildArtifactTags(al.mediaStore, hookResult.Media)
		contentForLLM = hookResult.ContentForLLM()
		if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
			contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
		}
		toolResultMedia = append(toolResultMedia, hookResult.Media...)
	}
	toolResultMsg := toolResultPromptMessage(contentForLLM, tc.ID, toolResultMedia)

	al.emitEvent(
		runtimeevents.KindAgentToolExecEnd,
		ts.eventMeta("runTurn", "turn.tool.end"),
		ToolExecEndPayload{
			Tool:       toolName,
			Duration:   toolDuration,
			ForLLMLen:  len(contentForLLM),
			ForUserLen: len(hookResult.ForUser),
			IsError:    hookResult.IsError,
			Async:      hookResult.Async,
		},
	)
	ts.recordToolExecution(
		toolName,
		!hookResult.IsError,
		toolErrorSummary(hookResult),
		inferSkillNamesFromToolCall(ts, toolName, toolArgs),
	)

	ls.appendToolResultMessage(toolResultMsg, true)

	if p.checkTurnCheckpoint(ls, true) {
		return toolLoopBreak
	}
	p.drainPendingSubTurnResults(ls, true)
	return toolLoopNext
}

// handleAfterToolDecision runs the AfterTool hook and applies its decision.
// Returns the loop action plus the (possibly modified) tool name and result.
func (p *Pipeline) handleAfterToolDecision(
	ls *toolLoopState,
	toolName string,
	toolArgs map[string]any,
	toolResult *tools.ToolResult,
	toolDuration time.Duration,
) (toolLoopAction, string, *tools.ToolResult) {
	al := ls.al
	ts := ls.ts
	toolResp, decision := al.hooks.AfterTool(ls.turnCtx, &ToolResultHookResponse{
		Meta:      ts.eventMeta("runTurn", "turn.tool.after"),
		Context:   cloneTurnContext(ts.turnCtx),
		Tool:      toolName,
		Arguments: toolArgs,
		Result:    toolResult,
		Duration:  toolDuration,
	})
	switch decision.normalizedAction() {
	case HookActionContinue, HookActionModify:
		if toolResp != nil {
			if toolResp.Tool != "" {
				toolName = toolResp.Tool
			}
			if toolResp.Result != nil {
				toolResult = toolResp.Result
			}
		}
	case HookActionAbortTurn:
		ls.exec.abortedByHook = true
		return toolLoopAbort, toolName, toolResult
	case HookActionHardAbort:
		_ = ts.requestHardAbort()
		ls.exec.abortedByHardAbort = true
		return toolLoopAbort, toolName, toolResult
	}
	return toolLoopProceed, toolName, toolResult
}

// publishToolFeedback sends the pre-execution tool feedback message on
// channels that use message-based feedback (streaming panels render steps
// instead, so they suppress it).
func (p *Pipeline) publishToolFeedback(ls *toolLoopState, tc providers.ToolCall, toolName string, toolArgs map[string]any) {
	al := ls.al
	ts := ls.ts
	if !(shouldPublishToolFeedback(al.cfg, ts) && ts.channel != "pico" && ls.exec.streamingPublisher == nil) {
		return
	}
	toolFeedbackMaxLen := al.cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength()
	toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
		ls.exec.response,
		tc,
		ls.messages,
	)
	feedbackMsg := utils.FormatToolFeedbackMessage(
		toolName,
		toolFeedbackExplanation,
		toolFeedbackArgsPreview(toolArgs, toolFeedbackMaxLen),
	)
	fbCtx, fbCancel := context.WithTimeout(ls.turnCtx, 3*time.Second)
	_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
		ts,
		feedbackMsg,
		outboundTurnMessageOptions{kind: messageKindToolFeedback},
	))
	fbCancel()
}

// buildMediaParts resolves media references into deliverable parts.
func (p *Pipeline) buildMediaParts(mediaRefs []string) []bus.MediaPart {
	al := p.al
	parts := make([]bus.MediaPart, 0, len(mediaRefs))
	for _, ref := range mediaRefs {
		part := bus.MediaPart{Ref: ref}
		if al.mediaStore != nil {
			if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
				part.Filename = meta.Filename
				part.ContentType = meta.ContentType
				part.Type = inferMediaType(meta.Filename, meta.ContentType)
			}
		}
		parts = append(parts, part)
	}
	return parts
}

// deliverHandledMedia sends media for a response the tool/hook already
// handled. Delivery goes through the channel manager on real channels; on
// failure warnMessage is logged and onSendError is invoked, and when no
// channel manager exists the message falls back to the bus and onBusFallback
// must clear the handled flag.
func (p *Pipeline) deliverHandledMedia(
	ls *toolLoopState,
	toolName string,
	mediaRefs []string,
	warnMessage string,
	onSendError func(err error),
	onBusFallback func(),
) {
	al := ls.al
	ts := ls.ts
	outboundMedia := bus.OutboundMediaMessage{
		Channel: ts.channel,
		ChatID:  ts.chatID,
		Context: outboundContextFromInbound(
			ts.opts.Dispatch.InboundContext,
			ts.channel,
			ts.chatID,
			ts.opts.Dispatch.ReplyToMessageID(),
		),
		AgentID:    ts.agent.ID,
		SessionKey: ts.sessionKey,
		Scope:      outboundScopeFromSessionScope(ts.opts.Dispatch.SessionScope),
		Parts:      p.buildMediaParts(mediaRefs),
	}
	if al.channelManager != nil && ts.channel != "" && !constants.IsInternalChannel(ts.channel) {
		if err := al.channelManager.SendMedia(ls.ctx, outboundMedia); err != nil {
			logger.WarnCF("agent", warnMessage,
				map[string]any{
					"agent_id": ts.agent.ID,
					"tool":     toolName,
					"channel":  ts.channel,
					"chat_id":  ts.chatID,
					"error":    err.Error(),
				})
			onSendError(err)
			return
		}
		ls.handledAttachments = append(
			ls.handledAttachments,
			buildProviderAttachments(al.mediaStore, mediaRefs)...,
		)
		return
	}
	if al.bus != nil {
		al.bus.PublishOutboundMedia(ls.ctx, outboundMedia)
		onBusFallback()
	}
}

// runToolInvocation logs the call, publishes feedback, surfaces the running
// step on the streaming panel and executes the tool with its async callback.
// Returns the tool result and its execution duration.
func (p *Pipeline) runToolInvocation(
	ls *toolLoopState,
	tc providers.ToolCall,
	toolName string,
	toolArgs map[string]any,
) (*tools.ToolResult, time.Duration) {
	al := ls.al
	ts := ls.ts

	argsJSON, _ := json.Marshal(toolArgs)
	argsPreview := utils.Truncate(string(argsJSON), 200)
	logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", toolName, argsPreview),
		map[string]any{
			"agent_id":  ts.agent.ID,
			"tool":      toolName,
			"iteration": ls.iteration,
		})
	al.emitEvent(
		runtimeevents.KindAgentToolExecStart,
		ts.eventMeta("runTurn", "turn.tool.start"),
		ToolExecStartPayload{
			Tool:      toolName,
			Arguments: cloneEventArguments(toolArgs),
		},
	)

	p.publishToolFeedback(ls, tc, toolName, toolArgs)

	asyncToolName := toolName
	asyncCallback := func(_ context.Context, result *tools.ToolResult) {
		if !result.Silent && result.ForUser != "" {
			outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer outCancel()
			_ = al.bus.PublishOutbound(outCtx, outboundMessageForTurn(ts, result.ForUser))
		}

		content := result.ContentForLLM()
		if content == "" {
			return
		}

		// The async callback bypasses the registry's synchronous exit, so the
		// output budget backstop is applied here too.
		content = tools.ApplyOutputBudget(asyncToolName, content, ts.agent.Tools.OutputBudget())

		content = al.cfg.FilterSensitiveData(content)

		logger.InfoCF("agent", "Async tool completed, publishing result",
			map[string]any{
				"tool":        asyncToolName,
				"content_len": len(content),
				"channel":     ts.channel,
			})
		al.emitEvent(
			runtimeevents.KindAgentFollowUpQueued,
			ts.scope.meta(ls.iteration, "runTurn", "turn.follow_up.queued"),
			FollowUpQueuedPayload{
				SourceTool: asyncToolName,
				ContentLen: len(content),
			},
		)
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		_ = al.bus.PublishInbound(pubCtx, bus.InboundMessage{
			Context: bus.InboundContext{
				Channel:  "system",
				ChatID:   fmt.Sprintf("%s:%s", ts.channel, ts.chatID),
				ChatType: "direct",
				SenderID: fmt.Sprintf("async:%s", asyncToolName),
			},
			Content: content,
		})
	}

	// Surface the invocation on the streaming panel before it runs, so
	// long executions show a live "running" entry instead of a silent gap.
	if ls.exec.streamingPublisher != nil {
		execArgsJSON, _ := json.Marshal(toolArgs)
		ls.exec.streamingPublisher.AppendToolStep(ls.turnCtx, bus.ToolStep{
			Tool:    toolName,
			Args:    utils.Truncate(string(execArgsJSON), 200),
			Kind:    toolStepKind(toolName),
			Running: true,
		})
	}

	toolStart := time.Now()
	execCtx := tools.WithToolInboundContext(
		ls.turnCtx,
		ts.channel,
		ts.chatID,
		ts.opts.Dispatch.MessageID(),
		ts.opts.Dispatch.ReplyToMessageID(),
	)
	execCtx = tools.WithToolSessionContext(
		execCtx,
		ts.agent.ID,
		ts.sessionKey,
		ts.opts.Dispatch.SessionScope,
	)
	toolResult := ts.agent.Tools.ExecuteWithContext(
		execCtx,
		toolName,
		toolArgs,
		ts.channel,
		ts.chatID,
		asyncCallback,
	)
	return toolResult, time.Since(toolStart)
}

// handleToolResult processes one executed tool call: delivers handled media,
// sends for-user output, applies the sensitive-data filter and low-progress
// loop hints, surfaces the streaming panel step and records/persists the
// tool result message. Checkpoint handling stays with the caller.
func (p *Pipeline) handleToolResult(
	ls *toolLoopState,
	tc providers.ToolCall,
	toolName string,
	toolArgs map[string]any,
	toolResult *tools.ToolResult,
	toolDuration time.Duration,
) {
	al := ls.al
	ts := ls.ts

	if toolResult == nil {
		toolResult = tools.ErrorResult("hook returned nil tool result")
	}

	if len(toolResult.Media) > 0 && toolResult.ResponseHandled {
		p.deliverHandledMedia(ls, toolName, toolResult.Media,
			"Failed to deliver handled tool media",
			func(sendErr error) {
				toolResult = tools.ErrorResult(fmt.Sprintf("failed to deliver attachment: %v", sendErr)).WithError(sendErr)
			}, func() {
				toolResult.ResponseHandled = false
			})
	}

	if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
		toolResult.ArtifactTags = buildArtifactTags(al.mediaStore, toolResult.Media)
	}

	if !toolResult.ResponseHandled {
		ls.exec.allResponsesHandled = false
	}

	shouldSendForUser := !toolResult.Silent &&
		toolResult.ForUser != "" &&
		(ts.opts.SendResponse || toolResult.ResponseHandled)
	if shouldSendForUser {
		al.bus.PublishOutbound(ls.ctx, outboundMessageForTurn(ts, toolResult.ForUser))
		logger.DebugCF("agent", "Sent tool result to user",
			map[string]any{
				"tool":        toolName,
				"content_len": len(toolResult.ForUser),
			})
	}
	contentForLLM := toolResult.ContentForLLM()

	if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
		contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
	}

	// Low-progress loop detection (Try-Best, minimal): prefix the tool
	// result with a replan warning when the execution shape says the
	// turn is spinning. The turn itself is never killed here.
	if hint := ts.noteToolHealth(toolName, toolArgs, toolErrorSummary(toolResult), toolResult.IsError); hint != "" {
		contentForLLM = hint + "\n\n" + contentForLLM
		logger.WarnCF("agent", "Low-progress loop signal fired",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"turn_id":   ts.turnID,
				"tool":      toolName,
				"iteration": ts.currentIteration(),
			})
	}

	// Tool completion is observable progress — feeds the heartbeat's
	// idle detection.
	ts.touchActivity()

	if ls.exec.streamingPublisher != nil {
		argsJSON, _ := json.Marshal(toolArgs)
		ls.exec.streamingPublisher.AppendToolStep(ls.ctx, bus.ToolStep{
			Tool:     toolName,
			Args:     utils.Truncate(string(argsJSON), 200),
			Result:   utils.Truncate(contentForLLM, 400),
			IsError:  toolResult.IsError,
			Duration: toolDuration,
			Kind:     toolStepKind(toolName),
		})
	}

	var toolResultMedia []string
	if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
		toolResultMedia = append(toolResultMedia, toolResult.Media...)
	}
	toolResultMsg := toolResultPromptMessage(contentForLLM, tc.ID, toolResultMedia)
	al.emitEvent(
		runtimeevents.KindAgentToolExecEnd,
		ts.eventMeta("runTurn", "turn.tool.end"),
		ToolExecEndPayload{
			Tool:       toolName,
			Duration:   toolDuration,
			ForLLMLen:  len(contentForLLM),
			ForUserLen: len(toolResult.ForUser),
			IsError:    toolResult.IsError,
			Async:      toolResult.Async,
		},
	)
	ts.recordToolExecution(
		toolName,
		!toolResult.IsError,
		toolErrorSummary(toolResult),
		inferSkillNamesFromToolCall(ts, toolName, toolArgs),
	)
	ls.appendToolResultMessage(toolResultMsg, true)
}

// checkTurnCheckpoint reports whether the tool loop must stop before the
// next tool call (a queued steering message or a pending graceful
// interrupt), skipping the remaining calls with notice messages. The
// afterHookRespond flag selects the historical log wording and the stricter
// persist gate (persistsToolMessages) used by the hook-respond path.
func (p *Pipeline) checkTurnCheckpoint(ls *toolLoopState, afterHookRespond bool) bool {
	al := ls.al
	ts := ls.ts

	if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
		ls.exec.pendingMessages = append(ls.exec.pendingMessages, steerMsgs...)
	}

	skipReason := ""
	skipMessage := ""
	if len(ls.exec.pendingMessages) > 0 {
		skipReason = "queued user steering message"
		skipMessage = "Skipped due to queued user message."
	} else if gracefulPending, _ := ts.gracefulInterruptRequested(); gracefulPending {
		skipReason = "graceful interrupt requested"
		skipMessage = "Skipped due to graceful interrupt."
	}
	if skipReason == "" {
		return false
	}

	logMessage := "Turn checkpoint: skipping remaining tools"
	if afterHookRespond {
		logMessage = "Turn checkpoint: skipping remaining tools after hook respond"
	}
	remaining := len(ls.toolCalls) - ls.index - 1
	if remaining > 0 {
		logger.InfoCF("agent", logMessage,
			map[string]any{
				"agent_id":  ts.agent.ID,
				"completed": ls.index + 1,
				"skipped":   remaining,
				"reason":    skipReason,
			})
		for j := ls.index + 1; j < len(ls.toolCalls); j++ {
			skippedTC := ls.toolCalls[j]
			al.emitEvent(
				runtimeevents.KindAgentToolExecSkipped,
				ts.eventMeta("runTurn", "turn.tool.skipped"),
				ToolExecSkippedPayload{
					Tool:   skippedTC.Name,
					Reason: skipReason,
				},
			)
			skippedMsg := providers.Message{
				Role:       "tool",
				Content:    skipMessage,
				ToolCallID: skippedTC.ID,
			}
			ls.appendToolNoticeMessage(skippedMsg, afterHookRespond)
		}
	}
	return true
}

// drainPendingSubTurnResults pulls one queued sub-turn result (if any) into
// the message list. gateOnToolPersist mirrors the historical hook-respond
// persist condition.
func (p *Pipeline) drainPendingSubTurnResults(ls *toolLoopState, gateOnToolPersist bool) {
	if ls.ts.pendingResults == nil {
		return
	}
	select {
	case result, ok := <-ls.ts.pendingResults:
		if ok && result != nil && result.ForLLM != "" {
			content := ls.al.cfg.FilterSensitiveData(result.ForLLM)
			msg := subTurnResultPromptMessage(content)
			ls.appendSubTurnMessage(msg, gateOnToolPersist)
		}
	default:
	}
}

// finishToolExecution applies the post-loop control flow: pending steering
// continues the turn, an all-handled delivery finalizes it, and anything
// else hands the tool result back to the coordinator for another LLM call.
func (p *Pipeline) finishToolExecution(ls *toolLoopState) ToolControl {
	al := ls.al
	ts := ls.ts
	exec := ls.exec
	exec.messages = ls.messages

	// Continue if pending steering exists (regardless of allResponsesHandled).
	// This covers the case where tools were partially executed and skipped due to steering,
	// but one tool had ResponseHandled=false (so allResponsesHandled=false).
	if len(exec.pendingMessages) > 0 {
		logger.InfoCF("agent", "Pending steering after partial tool execution; continuing turn",
			map[string]any{
				"agent_id":            ts.agent.ID,
				"pending_count":       len(exec.pendingMessages),
				"allResponsesHandled": exec.allResponsesHandled,
			})
		exec.allResponsesHandled = false
		return ToolControlContinue
	}

	// Poll for newly arrived steering
	if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
		logger.InfoCF("agent", "Steering arrived after tool delivery; continuing turn",
			map[string]any{
				"agent_id":       ts.agent.ID,
				"steering_count": len(steerMsgs),
			})
		exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
		exec.allResponsesHandled = false
		return ToolControlContinue
	}

	// No pending steering: finalize or break depending on allResponsesHandled
	if exec.allResponsesHandled {
		summaryMsg := providers.Message{
			Role:        "assistant",
			Content:     handledToolResponseSummary,
			Attachments: append([]providers.Attachment(nil), ls.handledAttachments...),
		}
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, summaryMsg)
			ts.recordPersistedMessage(summaryMsg)
			ts.ingestMessage(ls.turnCtx, al, summaryMsg)
			if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
				logger.WarnCF("agent", "Failed to save session after tool delivery",
					map[string]any{
						"agent_id": ts.agent.ID,
						"error":    err.Error(),
					})
			}
		}
		// Async post-turn compaction (option A), same rationale as Finalize.
		if !ts.opts.NoHistory && ts.opts.EnableSummary {
			al.scheduleCompact(ts.sessionKey, ts.agent.ContextWindow, ts.opts)
		}
		ts.setPhase(TurnPhaseCompleted)
		ts.setFinalContent("")
		if al.channelManager != nil && ts.channel != "" {
			al.channelManager.DismissToolFeedback(ls.ctx, ts.channel, ts.chatID, ts.opts.InboundContext)
		}
		logger.InfoCF("agent", "Tool output satisfied delivery; ending turn without follow-up LLM",
			map[string]any{
				"agent_id":   ts.agent.ID,
				"iteration":  ls.iteration,
				"tool_count": len(ls.toolCalls),
			})
		return ToolControlBreak
	}

	// allResponsesHandled=false and no pending steering: continue so coordinator
	// makes another LLM call. The tool result is in messages and the LLM will
	// return it as finalContent in the next iteration.
	ts.agent.Tools.TickTTL()
	logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
		"agent_id":  ts.agent.ID,
		"iteration": ls.iteration,
	})
	return ToolControlContinue
}
