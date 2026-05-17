// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: web_types.go -- request and response DTOs shared across the REST
// handlers. Kept separate so the wire shapes are easy to find without
// scanning the handler files. The MCP gateway has its own JSON-RPC envelope
// shapes inline in web_mcp.go because they're tied to the protocol spec.

type queryRequest struct {
	Query     string       `json:"query"`
	Limits    *AgentLimits `json:"limits,omitempty"`
	SessionID string       `json:"session_id,omitempty"`
}

// queryResponse is the REST wire shape for a single query. It always carries
// the structured termination diagnostics (reason, rounds_used, tokens_used,
// etc.) so callers don't need to regex error strings to branch on why a
// query ended. On success Answer is populated and Error is empty; on hard
// errors Answer is empty and Error holds a human-readable message.
type queryResponse struct {
	Answer            string       `json:"answer"`
	Error             string       `json:"error,omitempty"`
	TerminationReason string       `json:"termination_reason,omitempty"`
	Details           string       `json:"details,omitempty"`
	RoundsUsed        int          `json:"rounds_used,omitempty"`
	TokensUsed        int          `json:"tokens_used,omitempty"`
	PromptTokens      int          `json:"prompt_tokens,omitempty"`
	CompletionTokens  int          `json:"completion_tokens,omitempty"`
	ToolCallsMade     int          `json:"tool_calls_made,omitempty"`
	ElapsedMs         int64        `json:"elapsed_ms,omitempty"`
	CostUSD           float64      `json:"cost_usd,omitempty"`
	EarlyStopped      bool         `json:"early_stopped,omitempty"`
	SessionID         string       `json:"session_id,omitempty"`
	Limits            *AgentLimits `json:"limits,omitempty"`
}

// queryResponseFromResult populates a queryResponse from a QueryResult so
// all three REST query handlers stay in sync on the wire shape.
func queryResponseFromResult(r *QueryResult) queryResponse {
	return queryResponse{
		Answer:            r.Answer,
		Error:             r.Error,
		TerminationReason: r.TerminationReason,
		Details:           r.Details,
		RoundsUsed:        r.RoundsUsed,
		TokensUsed:        r.TokensUsed,
		PromptTokens:      r.PromptTokens,
		CompletionTokens:  r.CompletionTokens,
		ToolCallsMade:     r.ToolCallsMade,
		ElapsedMs:         r.ElapsedMs,
		CostUSD:           r.CostUSD,
		EarlyStopped:      r.EarlyStopped,
		SessionID:         r.SessionID,
		Limits:            r.Limits,
	}
}

type toolInfo struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

type healthResponse struct {
	Status    string `json:"status"`
	Model     string `json:"model"`
	ToolCount int    `json:"tool_count"`
	QueueLen  int    `json:"queue_length"`
	QueueMax  int    `json:"queue_max"`
	Version   string `json:"version"`
}
