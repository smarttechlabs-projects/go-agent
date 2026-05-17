// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// Termination reasons -- stable string values. External callers (REST clients,
// MCP clients, dashboards) branch on these; never change existing values.
//
// Success path:
//
//	TermSuccess         : LLM produced a final text answer (no tool calls).
//
// Early-stop path (the agent ran into a hard limit but -early-stop synthesized
// a final answer from the work done). TerminationReason carries the *trigger*;
// EarlyStopped is true:
//
//	TermMaxRounds + EarlyStopped=true
//	TermTokenBudget + EarlyStopped=true
//	TermLoopDetected + EarlyStopped=true
//
// Hard-error paths (Answer is empty, Error is set):
//
//	TermMaxRounds       : ran out of tool rounds without early-stop.
//	TermTokenBudget     : cumulative token counter exceeded the budget.
//	TermTimeout         : wall-clock deadline fired (context.DeadlineExceeded).
//	TermLoopDetected    : same (tool, args) repeated N times.
//	TermEmptyResponse   : LLM returned neither tool calls nor content.
//	TermTerminalError   : non-retryable API error (401/403/invalid key/etc).
//	TermLLMError        : other LLM/network error.
//	TermToolError       : tool execution returned an error we couldn't recover from.
//	TermUserCancel      : parent context cancelled (Ctrl+C, client disconnect).
const (
	TermSuccess       = "success"
	TermMaxRounds     = "max_rounds"
	TermTokenBudget   = "token_budget"
	TermTimeout       = "timeout"
	TermLoopDetected  = "loop_detected"
	TermEmptyResponse = "empty_response"
	TermTerminalError = "terminal_error"
	TermLLMError      = "llm_error"
	TermToolError     = "tool_error"
	TermUserCancel    = "user_cancel"
)

// QueryResult is the structured outcome of a single agent query. It is ALWAYS
// populated by QueryDetailed -- even on hard errors -- so callers can branch
// on TerminationReason and surface diagnostic metrics (rounds used, tokens
// consumed, time elapsed) without string-matching error messages.
//
// Field conventions:
//   - Answer is non-empty on success and on early-stop; empty on hard errors.
//   - Error is empty on success and on early-stop; set on hard errors.
//   - TerminationReason is ALWAYS set (one of the Term* constants above).
//   - Limits reflects the *effective* resolved limits (after server clamping).
type QueryResult struct {
	Answer            string       `json:"answer"`
	Error             string       `json:"error,omitempty"`
	TerminationReason string       `json:"termination_reason"`
	Details           string       `json:"details,omitempty"`
	RoundsUsed        int          `json:"rounds_used"`
	TokensUsed        int          `json:"tokens_used"`
	PromptTokens      int          `json:"prompt_tokens,omitempty"`
	CompletionTokens  int          `json:"completion_tokens,omitempty"`
	ToolCallsMade     int          `json:"tool_calls_made"`
	ElapsedMs         int64        `json:"elapsed_ms"`
	CostUSD           float64      `json:"cost_usd,omitempty"`
	EarlyStopped      bool         `json:"early_stopped,omitempty"`
	Limits            *AgentLimits `json:"limits,omitempty"`
	SessionID         string       `json:"session_id,omitempty"`
}

// IsSuccess reports whether the agent produced a final answer (including
// early-stop synthesis). Hard errors return false.
func (r *QueryResult) IsSuccess() bool {
	return r != nil && r.Error == "" && r.Answer != ""
}

// MetaKeys returns the subset of fields suitable for an MCP tools/call _meta
// block, using the "io.llm-agent/" vendor prefix per MCP spec. Callers that
// embed these alongside the tool result let external MCP clients inspect
// termination info without re-parsing the content text.
func (r *QueryResult) MetaKeys() map[string]any {
	if r == nil {
		return nil
	}
	meta := map[string]any{
		"io.llm-agent/termination_reason": r.TerminationReason,
		"io.llm-agent/rounds_used":        r.RoundsUsed,
		"io.llm-agent/tokens_used":        r.TokensUsed,
		"io.llm-agent/tool_calls_made":    r.ToolCallsMade,
		"io.llm-agent/elapsed_ms":         r.ElapsedMs,
	}
	if r.EarlyStopped {
		meta["io.llm-agent/early_stopped"] = true
	}
	if r.Details != "" {
		meta["io.llm-agent/details"] = r.Details
	}
	if r.SessionID != "" {
		meta["io.llm-agent/session_id"] = r.SessionID
	}
	if r.CostUSD > 0 {
		meta["io.llm-agent/cost_usd"] = r.CostUSD
	}
	if r.PromptTokens > 0 {
		meta["io.llm-agent/prompt_tokens"] = r.PromptTokens
	}
	if r.CompletionTokens > 0 {
		meta["io.llm-agent/completion_tokens"] = r.CompletionTokens
	}
	return meta
}
