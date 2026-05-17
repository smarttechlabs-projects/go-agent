// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	openai "github.com/sashabaranov/go-openai"
)

// Session represents one conversation thread with its own history and
// per-tool failure counter. Concurrent callers sharing the same session id
// are serialized (one query at a time per session); different session ids
// proceed independently. This is how we keep REST / MCP callers from
// corrupting each other's conversations.
//
// CLI mode uses a single implicit "default" session.
type Session struct {
	ID        string
	agent     *Agent
	runMu     sync.Mutex // held for the duration of a single QueryDetailed call
	history   []openai.ChatCompletionMessage
	failedTools map[string]int
	createdAt   time.Time
	lastUsed    atomic.Int64 // unix nanoseconds; touched on any access
}

// queryRun carries per-query state for a single Session.QueryDetailed call.
// Living on the stack (one per call) means concurrent queries on different
// sessions never stomp on each other's counters.
type queryRun struct {
	session          *Session
	tokenCounter     int
	promptTokens     int // for cost calc (commercial APIs price input/output separately)
	completionTokens int
	recentCallHashes []string
	activeLimits     *AgentLimits
	toolCallsMade    int
	lastRound        int
	queryStart       time.Time
}

// computeCost returns USD for this run based on the configured pricing
// table. Zero when no pricing is configured for the active model (the
// typical case for local backends).
func (run *queryRun) computeCost() float64 {
	if run.session == nil {
		return 0
	}
	cfg := run.session.agent.config
	if len(cfg.Pricing) == 0 {
		return 0
	}
	pricing, ok := cfg.Pricing[cfg.Model]
	if !ok {
		return 0
	}
	return (float64(run.promptTokens)*pricing.PromptPerMTokens +
		float64(run.completionTokens)*pricing.CompletionPerMTokens) / 1_000_000
}

// generateSessionID produces a short random id. Format: sess-<24 hex chars>.
func generateSessionID() string {
	b := make([]byte, 12)
	if _, err := cryptorand.Read(b); err != nil {
		// crypto/rand failures are vanishingly rare; fall back to time-based id.
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("sess-%x", b)
}

// newSession constructs a Session bound to the given agent, seeded with the
// system prompt. Callers normally go through Agent.GetOrCreateSession.
func newSession(id string, a *Agent) *Session {
	if id == "" {
		id = generateSessionID()
	}
	s := &Session{
		ID:    id,
		agent: a,
		history: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: a.config.SystemPrompt},
		},
		failedTools: make(map[string]int),
		createdAt:   time.Now(),
	}
	s.touch()
	return s
}

// touch updates the idle timer so the reaper doesn't evict an active session.
func (s *Session) touch() {
	s.lastUsed.Store(time.Now().UnixNano())
}

// IdleDuration returns how long since this session was last used.
func (s *Session) IdleDuration() time.Duration {
	last := s.lastUsed.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// Clear resets the conversation history back to just the system prompt.
func (s *Session) Clear() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.history = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: s.agent.config.SystemPrompt},
	}
	s.failedTools = make(map[string]int)
	s.touch()
}

// HistoryLen returns the number of messages in the history. Useful for tests
// and the dashboard. Thread-safe.
func (s *Session) HistoryLen() int {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return len(s.history)
}

// trimHistory drops the oldest non-system messages when the conversation
// exceeds the configured char cap. Always preserves:
//   - The system prompt (history[0])
//   - The last `keep` user/assistant turns (a turn is a user msg through its
//     assistant reply and any tool results)
//
// Called at the top of each loop round, before the LLM request, so outgoing
// context stays bounded even across long sessions.
//
// Must be called with s.runMu held.
func (s *Session) trimHistory(maxChars, keepRecent int) {
	if maxChars <= 0 || len(s.history) <= 1 {
		return
	}
	size := historySize(s.history)
	if size <= maxChars {
		return
	}

	// Walk from the end backward, counting "turns". A turn boundary is the
	// transition FROM a non-user message TO a user message (i.e. the start of
	// a user turn). Find the cut-point that preserves `keepRecent` user turns.
	userTurns := 0
	cutIdx := 1 // anything strictly before cutIdx (and after index 0) can be dropped
	for i := len(s.history) - 1; i >= 1; i-- {
		if s.history[i].Role == "user" {
			userTurns++
			if userTurns >= keepRecent {
				cutIdx = i
				break
			}
		}
	}
	if cutIdx <= 1 {
		return // nothing safe to drop
	}

	// Preserve system prompt (index 0) + history[cutIdx:].
	trimmed := make([]openai.ChatCompletionMessage, 0, 1+len(s.history)-cutIdx)
	trimmed = append(trimmed, s.history[0])
	trimmed = append(trimmed, s.history[cutIdx:]...)
	dropped := len(s.history) - len(trimmed)
	s.history = trimmed
	s.agent.log.Warn("history trimmed: dropped %d older messages (was %d chars, now %d, cap %d)",
		dropped, size, historySize(s.history), maxChars)
}

// Query runs the agent loop with server-default limits and returns the final
// answer text. Shortcut for QueryDetailed(...).Answer.
func (s *Session) Query(ctx context.Context, input string) (string, error) {
	return s.QueryWithLimits(ctx, input, nil)
}

// QueryWithLimits is the legacy (string, error) entry point per session.
func (s *Session) QueryWithLimits(ctx context.Context, input string, override *AgentLimits) (string, error) {
	r := s.QueryDetailed(ctx, input, override)
	if r.Error != "" {
		return "", errors.New(r.Error)
	}
	return r.Answer, nil
}

// QueryDetailed runs the full agent loop on this session's conversation
// history and returns a structured QueryResult (always non-nil). Safe to
// call concurrently with QueryDetailed calls on *other* sessions; calls on
// the *same* session are serialized.
func (s *Session) QueryDetailed(ctx context.Context, input string, override *AgentLimits) (result *QueryResult) {
	// Serialize queries on this session. Two concurrent callers with the same
	// session_id queue up; they don't corrupt each other's history.
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.touch()
	defer s.touch()

	run := &queryRun{
		session:      s,
		activeLimits: s.agent.config.ResolveLimits(override),
		queryStart:   time.Now(),
	}

	// Record a metric on every exit path (success, error, early stop).
	defer func() {
		reason := "unknown"
		if result != nil {
			reason = result.TerminationReason
		}
		recordQuery(ctx, reason, time.Since(run.queryStart))
	}()

	if override != nil {
		s.agent.log.Info("query limits: %s (overridden from defaults)", run.activeLimits.Describe())
	}

	// Apply wall-clock timeout if configured.
	if run.activeLimits.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(run.activeLimits.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	ctx, span := startSpan(ctx, "agent.query",
		attribute.String("user.input", truncateLog(input, 200)),
		attribute.String("session.id", s.ID),
	)
	defer span.End()

	s.history = append(s.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: input,
	})

	s.agent.emit(Event{Type: EventQueryStart, Data: EventData{Input: input}})

	for round := 1; round <= run.activeLimits.MaxToolRounds; round++ {
		run.lastRound = round
		// Bound outgoing context: drop the oldest non-system turns when the
		// session's history is larger than the configured cap. Cheap, runs
		// every round; usually a no-op until a long session accumulates.
		s.trimHistory(s.agent.config.MaxHistoryChars, s.agent.config.KeepRecentTurns)
		ctxSize := historySize(s.history)
		s.agent.log.Round(round, len(s.history), ctxSize)
		s.agent.emit(Event{Type: EventRoundStart, Round: round})

		roundCtx, roundSpan := startSpan(ctx, "agent.round",
			attrRound.Int(round),
			attrMaxRound.Int(run.activeLimits.MaxToolRounds),
			attrContextChars.Int(ctxSize),
			attrMessageCount.Int(len(s.history)),
		)

		s.agent.emit(Event{Type: EventLLMRequest, Round: round, Data: EventData{
			MessageCount: len(s.history),
			ToolCount:    len(s.agent.tools),
			ContextChars: ctxSize,
			Messages:     summarizeMessages(s.history),
		}})

		result, err := s.agent.llm.ChatCompletion(roundCtx, s.history, s.agent.tools)
		if err != nil {
			s.agent.emit(Event{Type: EventError, Round: round, Data: EventData{Error: err.Error()}})
			roundSpan.RecordError(err)
			roundSpan.SetStatus(codes.Error, err.Error())
			roundSpan.End()

			if errors.Is(err, context.DeadlineExceeded) {
				return run.finalizeError(TermTimeout,
					fmt.Sprintf("wall-clock timeout after %ds (round %d)", run.activeLimits.TimeoutSeconds, round),
					err)
			}
			if errors.Is(err, context.Canceled) {
				return run.finalizeError(TermUserCancel,
					fmt.Sprintf("query cancelled by caller (round %d)", round),
					err)
			}
			if isTerminalError(err) {
				s.agent.log.Error("terminal error (will not retry): %v", err)
				return run.finalizeError(TermTerminalError,
					fmt.Sprintf("non-retryable API error (round %d)", round),
					fmt.Errorf("terminal error: %w", err))
			}
			return run.finalizeError(TermLLMError,
				fmt.Sprintf("LLM call failed (round %d)", round),
				err)
		}

		// Token budget enforcement.
		run.tokenCounter += result.Usage.TotalTokens
		run.promptTokens += result.Usage.PromptTokens
		run.completionTokens += result.Usage.CompletionTokens
		if run.activeLimits.MaxTokenBudget > 0 && run.tokenCounter >= run.activeLimits.MaxTokenBudget {
			s.agent.log.Warn("token budget exceeded: %d >= %d (round %d)", run.tokenCounter, run.activeLimits.MaxTokenBudget, round)
			roundSpan.End()
			details := fmt.Sprintf("token budget exceeded: %d tokens used (limit %d, round %d)",
				run.tokenCounter, run.activeLimits.MaxTokenBudget, round)
			if run.activeLimits.EarlyStop {
				return run.earlyStopSynthesis(ctx, span, round, TermTokenBudget, details)
			}
			return run.finalizeError(TermTokenBudget, details, errors.New(details))
		}

		msg := result.Message
		finishReason := result.FinishReason

		// Handle text-based tool calls for models that embed them in content.
		if len(msg.ToolCalls) == 0 && s.agent.config.ToolCallStyle == ToolCallText {
			if strings.Contains(msg.Content, "<function=") {
				cleanContent, textCalls := parseTextToolCalls(msg.Content)
				if len(textCalls) > 0 {
					s.agent.log.Warn("parsed %d text-embedded tool call(s) from content (toolCallStyle=text)", len(textCalls))
					msg.ToolCalls = textCalls
					msg.Content = cleanContent
				}
			}
		}

		hasToolCalls := len(msg.ToolCalls) > 0
		s.agent.log.LLMResponse(finishReason, hasToolCalls, len(msg.ToolCalls), len(msg.Content), result.Elapsed)
		s.agent.log.LLMMetrics(result.TTFT, result.Usage, result.Elapsed)

		s.agent.emit(Event{Type: EventLLMResponse, Round: round, Data: EventData{
			Content:          truncateLog(Redact(msg.Content), 2000),
			FinishReason:     finishReason,
			HasToolCalls:     hasToolCalls,
			ToolCallCount:    len(msg.ToolCalls),
			TTFT:             float64(result.TTFT.Milliseconds()),
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
			TokensPerSec:     result.Usage.TokensPerSecond(result.Elapsed),
			ElapsedMs:        float64(result.Elapsed.Milliseconds()),
		}})

		if msg.Content != "" && s.agent.log.Verbose() {
			s.agent.log.Data("content", Redact(msg.Content), 200)
		}

		roundSpan.SetAttributes(
			attrFinishReason.String(finishReason),
			attrHasToolCalls.Bool(hasToolCalls),
			attrContentLen.Int(len(msg.Content)),
		)

		s.history = append(s.history, msg)

		if !hasToolCalls {
			roundSpan.End()
			span.SetAttributes(attribute.Int("agent.total_rounds", round))
			if msg.Content == "" {
				s.agent.log.Warn("LLM returned empty content (finish_reason=%s)", finishReason)
				span.SetStatus(codes.Error, "empty response")
				details := fmt.Sprintf("empty response from model (finish_reason=%s, round=%d, context~%d chars)",
					finishReason, round, ctxSize)
				s.agent.emit(Event{Type: EventRoundEnd, Round: round})
				return run.finalizeError(TermEmptyResponse, details, errors.New(details))
			}
			r := run.finalizeSuccess(msg.Content)
			s.agent.emit(Event{Type: EventRoundEnd, Round: round})
			s.agent.emit(Event{Type: EventQueryEnd, Data: EventData{
				TotalRounds:       round,
				FinalAnswer:       truncateLog(msg.Content, 2000),
				TerminationReason: r.TerminationReason,
			}})
			return r
		}

		// Loop fingerprint check.
		if run.activeLimits.LoopFingerprint > 0 {
			for _, tc := range msg.ToolCalls {
				hash := fmt.Sprintf("%s:%s", tc.Function.Name, tc.Function.Arguments)
				run.recentCallHashes = append(run.recentCallHashes, hash)
				if len(run.recentCallHashes) > run.activeLimits.LoopFingerprint+1 {
					run.recentCallHashes = run.recentCallHashes[len(run.recentCallHashes)-run.activeLimits.LoopFingerprint-1:]
				}
				if countTrailingMatches(run.recentCallHashes, hash) >= run.activeLimits.LoopFingerprint {
					s.agent.log.Warn("loop detected: %s called %d times with same arguments", tc.Function.Name, run.activeLimits.LoopFingerprint)
					roundSpan.End()
					details := fmt.Sprintf("loop detected: tool %s called %d times with identical arguments (round %d)",
						tc.Function.Name, run.activeLimits.LoopFingerprint, round)
					if run.activeLimits.EarlyStop {
						return run.earlyStopSynthesis(ctx, span, round, TermLoopDetected, details)
					}
					return run.finalizeError(TermLoopDetected, details, errors.New(details))
				}
			}
		}

		if err := run.executeToolCalls(roundCtx, round, msg.ToolCalls); err != nil {
			s.agent.emit(Event{Type: EventError, Round: round, Data: EventData{Error: err.Error()}})
			roundSpan.RecordError(err)
			roundSpan.SetStatus(codes.Error, err.Error())
			roundSpan.End()
			if errors.Is(err, context.DeadlineExceeded) {
				return run.finalizeError(TermTimeout,
					fmt.Sprintf("wall-clock timeout during tool execution (round %d)", round),
					err)
			}
			if errors.Is(err, context.Canceled) {
				return run.finalizeError(TermUserCancel,
					fmt.Sprintf("query cancelled during tool execution (round %d)", round),
					err)
			}
			return run.finalizeError(TermToolError,
				fmt.Sprintf("tool execution failed (round %d)", round),
				err)
		}
		s.agent.emit(Event{Type: EventRoundEnd, Round: round})
		roundSpan.End()
	}

	// Max rounds exceeded.
	details := fmt.Sprintf("reached max tool call rounds (%d)", run.activeLimits.MaxToolRounds)
	if run.activeLimits.EarlyStop {
		return run.earlyStopSynthesis(ctx, span, run.activeLimits.MaxToolRounds, TermMaxRounds, details)
	}
	span.SetStatus(codes.Error, "max rounds exceeded")
	return run.finalizeError(TermMaxRounds, details, errors.New(details))
}

// finalizeSuccess builds a QueryResult for the happy path.
func (run *queryRun) finalizeSuccess(answer string) *QueryResult {
	return &QueryResult{
		Answer:            answer,
		TerminationReason: TermSuccess,
		RoundsUsed:        run.lastRound,
		TokensUsed:        run.tokenCounter,
		PromptTokens:      run.promptTokens,
		CompletionTokens:  run.completionTokens,
		CostUSD:           run.computeCost(),
		ToolCallsMade:     run.toolCallsMade,
		ElapsedMs:         time.Since(run.queryStart).Milliseconds(),
		Limits:            run.activeLimits,
		SessionID:         run.sessionID(),
	}
}

// finalizeError builds a QueryResult for a hard-error exit path.
func (run *queryRun) finalizeError(reason, details string, err error) *QueryResult {
	msg := details
	if err != nil && err.Error() != details {
		msg = err.Error()
	}
	return &QueryResult{
		Error:             msg,
		TerminationReason: reason,
		Details:           details,
		RoundsUsed:        run.lastRound,
		TokensUsed:        run.tokenCounter,
		PromptTokens:      run.promptTokens,
		CompletionTokens:  run.completionTokens,
		CostUSD:           run.computeCost(),
		ToolCallsMade:     run.toolCallsMade,
		ElapsedMs:         time.Since(run.queryStart).Milliseconds(),
		Limits:            run.activeLimits,
		SessionID:         run.sessionID(),
	}
}

// sessionID returns the session id for diagnostics, tolerating a nil session
// (used by some unit tests that construct queryRun directly).
func (run *queryRun) sessionID() string {
	if run.session == nil {
		return ""
	}
	return run.session.ID
}

// earlyStopSynthesis makes one final LLM call with NO tools available and a
// synthesis prompt, producing the best possible answer from the work done.
func (run *queryRun) earlyStopSynthesis(ctx context.Context, span trace.Span, round int, reason, details string) *QueryResult {
	s := run.session
	s.agent.log.Warn("early stop: %s (round %d) -- synthesizing final answer", details, round)

	synthesisHint := fmt.Sprintf(
		"[SYSTEM: Loop terminated (%s). Based on the work done so far, provide your best final answer to the user. Do NOT call any more tools -- synthesize what you already know.]",
		details,
	)
	historyForSynth := append(s.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: synthesisHint,
	})

	result, err := s.agent.llm.ChatCompletion(ctx, historyForSynth, nil)
	if err != nil {
		span.SetStatus(codes.Error, "early-stop synthesis failed")
		if errors.Is(err, context.DeadlineExceeded) {
			return run.finalizeError(TermTimeout,
				fmt.Sprintf("early-stop synthesis hit wall-clock timeout after %s", details),
				err)
		}
		if errors.Is(err, context.Canceled) {
			return run.finalizeError(TermUserCancel,
				fmt.Sprintf("early-stop synthesis cancelled by caller after %s", details),
				err)
		}
		return run.finalizeError(TermLLMError,
			fmt.Sprintf("early-stop synthesis failed after %s", details),
			fmt.Errorf("early stop (%s): synthesis failed: %w", details, err))
	}

	answer := result.Message.Content
	if answer == "" {
		span.SetStatus(codes.Error, "early-stop synthesis empty")
		return run.finalizeError(TermEmptyResponse,
			fmt.Sprintf("early-stop synthesis returned empty content after %s", details),
			fmt.Errorf("early stop (%s): LLM returned empty synthesis", details))
	}

	run.tokenCounter += result.Usage.TotalTokens
	run.promptTokens += result.Usage.PromptTokens
	run.completionTokens += result.Usage.CompletionTokens
	annotated := fmt.Sprintf("[Note: agent terminated early (%s). Synthesized answer:]\n\n%s", details, answer)

	span.SetAttributes(attribute.String("agent.early_stop_reason", reason))
	s.agent.emit(Event{Type: EventQueryEnd, Data: EventData{
		TotalRounds:       round,
		FinalAnswer:       truncateLog(annotated, 2000),
		TerminationReason: reason,
		EarlyStopped:      true,
	}})

	return &QueryResult{
		Answer:            annotated,
		TerminationReason: reason,
		Details:           details,
		RoundsUsed:        round,
		TokensUsed:        run.tokenCounter,
		PromptTokens:      run.promptTokens,
		CompletionTokens:  run.completionTokens,
		CostUSD:           run.computeCost(),
		ToolCallsMade:     run.toolCallsMade,
		ElapsedMs:         time.Since(run.queryStart).Milliseconds(),
		EarlyStopped:      true,
		Limits:            run.activeLimits,
		SessionID:         run.sessionID(),
	}
}

// Maximum concurrent tool calls per LLM response. Caps fan-out when the
// model returns many parallel tool_calls -- keeps CPU/FD usage bounded on
// stdio MCP servers. 4 is a good default for typical mixes of fetch/browser.
const maxParallelToolCalls = 4

// toolCallOutcome is the result of running a single tool call; results are
// collected in order before the serial bookkeeping phase.
type toolCallOutcome struct {
	tc          openai.ToolCall
	result      string
	err         error
	elapsed     time.Duration
	truncated   bool
	llmsTxtUsed bool
}

// executeToolCalls runs tool calls concurrently (capped at maxParallelToolCalls)
// but processes bookkeeping (failedTools, history append) serially in the
// order the model requested, so the assistant message and its tool results
// still line up when the LLM re-reads the history next round.
//
// The I/O phase (calling each MCP server) is where latency lives; running
// them in parallel is worth ~k× speedup when the model emits k independent
// tool calls. The serial phase is cheap (map + slice operations).
func (run *queryRun) executeToolCalls(ctx context.Context, round int, calls []openai.ToolCall) error {
	s := run.session
	run.toolCallsMade += len(calls)

	// Phase 1: concurrent I/O. Each goroutine emits its own tool_call and
	// tool_result events; events are tagged with round number so out-of-order
	// arrival on the dashboard isn't confusing.
	outcomes := make([]toolCallOutcome, len(calls))
	sem := make(chan struct{}, maxParallelToolCalls)
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tc openai.ToolCall) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = run.executeOneToolCall(ctx, round, tc)
		}(i, tc)
	}
	wg.Wait()

	// Phase 2: serial bookkeeping in the order the model requested.
	for i, tc := range calls {
		o := &outcomes[i]
		result := o.result

		if isToolFailure(result) {
			s.failedTools[tc.Function.Name]++
			if s.failedTools[tc.Function.Name] >= 2 {
				result += "\n\n[SYSTEM: This tool has failed multiple times. Do NOT retry it. Answer using your own knowledge instead.]"
				s.agent.log.Warn("%s has failed %d times, injecting stop-retry hint", tc.Function.Name, s.failedTools[tc.Function.Name])
			}
		} else {
			s.failedTools[tc.Function.Name] = 0
		}

		callID := tc.ID
		if callID == "" {
			callID = fmt.Sprintf("call_%d", i)
		}

		s.history = append(s.history, openai.ChatCompletionMessage{
			Role:       openai.ChatMessageRoleTool,
			Content:    result,
			ToolCallID: callID,
		})
	}
	return nil
}

// executeOneToolCall runs a single tool: emits events, enforces per-tool
// timeout, truncates result. Returns the raw outcome; bookkeeping (failure
// tracking, history append) happens serially in the caller.
func (run *queryRun) executeOneToolCall(ctx context.Context, round int, tc openai.ToolCall) toolCallOutcome {
	s := run.session
	// Redact secrets from outbound observability surfaces. The actual call
	// still uses the raw args -- we only sanitize what we log/emit/trace.
	redactedArgs := Redact(tc.Function.Arguments)
	s.agent.log.Tool(tc.Function.Name, redactedArgs)
	s.agent.emit(Event{Type: EventToolCall, Round: round, Data: EventData{
		ToolName: tc.Function.Name,
		ToolArgs: redactedArgs,
	}})

	toolCtx, toolSpan := startSpan(ctx, "agent.tool_call",
		attrToolName.String(tc.Function.Name),
		attrToolArgs.String(truncateLog(redactedArgs, 500)),
	)
	defer toolSpan.End()

	outcome := toolCallOutcome{tc: tc}

	// llms.txt shortcut for fetch/navigate -- no MCP hop needed.
	if (tc.Function.Name == "fetch" || tc.Function.Name == "browser_navigate") && s.agent.llmsTxt != nil {
		if fetchURL := extractURL(tc.Function.Arguments); fetchURL != "" {
			if llmsContent, ok := s.agent.llmsTxt.CheckURL(fetchURL); ok {
				outcome.result = llmsContent
				outcome.llmsTxtUsed = true
			}
		}
	}

	// Validate arguments against the tool's JSON schema before dispatch.
	// Invalid args become a synthetic error result -- the loop continues
	// and the model can correct itself on the next round.
	if schema := s.agent.lookupToolSchema(tc.Function.Name); schema != nil {
		if msg := ValidateToolArgsBrief(tc.Function.Arguments, schema); msg != "" {
			outcome.result = msg
			outcome.err = fmt.Errorf("invalid arguments")
			s.agent.log.Warn("tool %s validation failed: skipping dispatch", tc.Function.Name)
			toolSpan.RecordError(outcome.err)
			// Fall through to the standard error-handling block below.
			outcome.elapsed = 0
			s.agent.log.ToolResult(tc.Function.Name, len(outcome.result), outcome.elapsed)
			s.agent.emit(Event{Type: EventToolResult, Round: round, Data: EventData{
				ToolName:   tc.Function.Name,
				ToolResult: truncateLog(outcome.result, 2000),
				ElapsedMs:  0,
			}})
			return outcome
		}
	}

	// Human-in-the-loop approval check. If this tool matches a RequireApproval
	// pattern, block here until an operator approves or denies. Denial injects
	// a synthetic tool result and the loop continues; the model can pick a
	// different tool or answer from its own knowledge.
	if approved, err := s.agent.RequestApproval(toolCtx, tc.Function.Name, tc.Function.Arguments, s.ID); err != nil {
		// Parent context cancellation / timeout on approval wait.
		outcome.err = err
		outcome.result = fmt.Sprintf("Approval wait cancelled: %v", err)
		toolSpan.RecordError(err)
		s.agent.emit(Event{Type: EventToolResult, Round: round, Data: EventData{
			ToolName:   tc.Function.Name,
			ToolResult: truncateLog(outcome.result, 2000),
		}})
		return outcome
	} else if !approved {
		outcome.result = fmt.Sprintf("[DENIED: operator did not approve call to %s. Do NOT retry this tool; answer using your existing knowledge or suggest a different approach to the user.]", tc.Function.Name)
		s.agent.log.Warn("tool %s denied by approval gate", tc.Function.Name)
		s.agent.emit(Event{Type: EventToolResult, Round: round, Data: EventData{
			ToolName:   tc.Function.Name,
			ToolResult: truncateLog(outcome.result, 2000),
		}})
		return outcome
	}

	start := time.Now()
	if !outcome.llmsTxtUsed {
		callCtx := toolCtx
		if t := s.agent.config.ToolTimeoutSecs; t > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(toolCtx, time.Duration(t)*time.Second)
			defer cancel()
		}
		res, err := s.agent.callTool(callCtx, tc.Function.Name, tc.Function.Arguments)
		// Per-tool timeout becomes a tool error (spiral-detector territory)
		// rather than a query-level cancel; only parent-context cancellation
		// kills the whole query.
		if err != nil && errors.Is(err, context.DeadlineExceeded) && toolCtx.Err() == nil {
			err = fmt.Errorf("tool %s timed out after %ds", tc.Function.Name, s.agent.config.ToolTimeoutSecs)
		}
		outcome.result = res
		outcome.err = err
	}
	outcome.elapsed = time.Since(start)

	if outcome.err != nil {
		outcome.result = fmt.Sprintf("Error: %v", outcome.err)
		toolSpan.RecordError(outcome.err)
		s.agent.log.Error("tool %s failed: %v", tc.Function.Name, outcome.err)
	}

	if len(outcome.result) > run.activeLimits.MaxResultLen {
		outcome.truncated = true
		outcome.result = outcome.result[:run.activeLimits.MaxResultLen] + "\n... (truncated)"
	}

	s.agent.log.ToolResult(tc.Function.Name, len(outcome.result), outcome.elapsed)
	// Redact before exposing the result anywhere observable. The result fed
	// back to the LLM history downstream is unredacted (the model needs the
	// real content to reason); we only sanitize what operators see.
	redactedResult := Redact(outcome.result)
	if s.agent.log.Verbose() {
		s.agent.log.Data("result", redactedResult, 300)
	}
	s.agent.emit(Event{Type: EventToolResult, Round: round, Data: EventData{
		ToolName:   tc.Function.Name,
		ToolResult: truncateLog(redactedResult, 2000),
		Truncated:  outcome.truncated,
		ElapsedMs:  float64(outcome.elapsed.Milliseconds()),
	}})

	toolSpan.SetAttributes(
		attrToolResultLen.Int(len(outcome.result)),
		attrToolTruncated.Bool(outcome.truncated),
	)

	if !s.agent.log.Verbose() {
		fmt.Printf("  -> %s\n", tc.Function.Name)
	}

	return outcome
}
