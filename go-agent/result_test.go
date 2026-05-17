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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueryResult_IsSuccess(t *testing.T) {
	tests := []struct {
		name string
		r    *QueryResult
		want bool
	}{
		{"nil not success", nil, false},
		{"success path", &QueryResult{Answer: "ok", TerminationReason: TermSuccess}, true},
		{"early-stop still counts as success", &QueryResult{Answer: "[Note: ...]", TerminationReason: TermMaxRounds, EarlyStopped: true}, true},
		{"error path", &QueryResult{Error: "boom", TerminationReason: TermMaxRounds}, false},
		{"empty answer is not success", &QueryResult{TerminationReason: TermSuccess}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.IsSuccess(); got != tt.want {
				t.Errorf("IsSuccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryResult_MetaKeys_AllFields(t *testing.T) {
	r := &QueryResult{
		Answer:            "hello",
		TerminationReason: TermLoopDetected,
		Details:           "loop detected on browser_click",
		RoundsUsed:        4,
		TokensUsed:        1234,
		ToolCallsMade:     6,
		ElapsedMs:         2500,
		EarlyStopped:      true,
	}
	meta := r.MetaKeys()

	// Every MCP _meta key must use the reserved "io.llm-agent/" vendor prefix.
	required := []string{
		"io.llm-agent/termination_reason",
		"io.llm-agent/rounds_used",
		"io.llm-agent/tokens_used",
		"io.llm-agent/tool_calls_made",
		"io.llm-agent/elapsed_ms",
		"io.llm-agent/early_stopped",
		"io.llm-agent/details",
	}
	for _, key := range required {
		if _, ok := meta[key]; !ok {
			t.Errorf("missing key %q in meta: %+v", key, meta)
		}
	}
	if meta["io.llm-agent/termination_reason"] != TermLoopDetected {
		t.Errorf("termination_reason = %v, want %v", meta["io.llm-agent/termination_reason"], TermLoopDetected)
	}
}

func TestQueryResult_MetaKeys_OmitsOptionalsWhenZero(t *testing.T) {
	r := &QueryResult{
		TerminationReason: TermSuccess,
		RoundsUsed:        1,
	}
	meta := r.MetaKeys()
	// Optional fields not set → must not appear.
	if _, ok := meta["io.llm-agent/early_stopped"]; ok {
		t.Error("early_stopped should be omitted when false")
	}
	if _, ok := meta["io.llm-agent/details"]; ok {
		t.Error("details should be omitted when empty")
	}
	// Numeric fields are always present (even 0) -- they're diagnostic data.
	for _, key := range []string{
		"io.llm-agent/termination_reason",
		"io.llm-agent/rounds_used",
		"io.llm-agent/tokens_used",
		"io.llm-agent/tool_calls_made",
		"io.llm-agent/elapsed_ms",
	} {
		if _, ok := meta[key]; !ok {
			t.Errorf("required key %q missing", key)
		}
	}
}

func TestQueryResult_MetaKeys_NilSafe(t *testing.T) {
	var r *QueryResult
	if got := r.MetaKeys(); got != nil {
		t.Errorf("nil receiver should return nil meta, got %+v", got)
	}
}

func TestQueryResult_JSONShape(t *testing.T) {
	r := &QueryResult{
		Answer:            "ok",
		TerminationReason: TermSuccess,
		RoundsUsed:        2,
		TokensUsed:        500,
		ToolCallsMade:     3,
		ElapsedMs:         1234,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// Snake-case keys must all be present.
	for _, key := range []string{
		`"answer":"ok"`,
		`"termination_reason":"success"`,
		`"rounds_used":2`,
		`"tokens_used":500`,
		`"tool_calls_made":3`,
		`"elapsed_ms":1234`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON missing %q in %s", key, s)
		}
	}
	// Error must be omitted when empty.
	if strings.Contains(s, `"error"`) {
		t.Errorf("JSON should omit empty error field: %s", s)
	}
}

func TestFinalizeSuccess(t *testing.T) {
	run := &queryRun{
		tokenCounter:  500,
		toolCallsMade: 3,
		lastRound:     2,
		queryStart:    time.Now().Add(-150 * time.Millisecond),
		activeLimits:  &AgentLimits{MaxToolRounds: 10},
	}
	r := run.finalizeSuccess("the answer")

	if r.Answer != "the answer" {
		t.Errorf("Answer = %q, want %q", r.Answer, "the answer")
	}
	if r.Error != "" {
		t.Errorf("Error = %q, want empty", r.Error)
	}
	if r.TerminationReason != TermSuccess {
		t.Errorf("TerminationReason = %q, want %q", r.TerminationReason, TermSuccess)
	}
	if r.RoundsUsed != 2 {
		t.Errorf("RoundsUsed = %d, want 2", r.RoundsUsed)
	}
	if r.TokensUsed != 500 {
		t.Errorf("TokensUsed = %d, want 500", r.TokensUsed)
	}
	if r.ToolCallsMade != 3 {
		t.Errorf("ToolCallsMade = %d, want 3", r.ToolCallsMade)
	}
	if r.ElapsedMs < 100 || r.ElapsedMs > 1000 {
		t.Errorf("ElapsedMs = %d, want in ~[100,1000]", r.ElapsedMs)
	}
	if r.EarlyStopped {
		t.Error("EarlyStopped should be false on success")
	}
	if r.Limits == nil || r.Limits.MaxToolRounds != 10 {
		t.Errorf("Limits not propagated: %+v", r.Limits)
	}
	if !r.IsSuccess() {
		t.Error("IsSuccess() should return true")
	}
}

func TestFinalizeError_Shape(t *testing.T) {
	run := &queryRun{
		tokenCounter:  750,
		toolCallsMade: 5,
		lastRound:     4,
		queryStart:    time.Now().Add(-50 * time.Millisecond),
		activeLimits:  &AgentLimits{MaxToolRounds: 10},
	}
	err := errors.New("loop detected: tool X called 3 times")
	r := run.finalizeError(TermLoopDetected, "loop detected on X", err)

	if r.Answer != "" {
		t.Errorf("Answer = %q, want empty on error", r.Answer)
	}
	if r.Error != err.Error() {
		t.Errorf("Error = %q, want %q", r.Error, err.Error())
	}
	if r.TerminationReason != TermLoopDetected {
		t.Errorf("TerminationReason = %q, want %q", r.TerminationReason, TermLoopDetected)
	}
	if r.Details != "loop detected on X" {
		t.Errorf("Details = %q, want %q", r.Details, "loop detected on X")
	}
	if r.RoundsUsed != 4 {
		t.Errorf("RoundsUsed = %d, want 4", r.RoundsUsed)
	}
	if r.TokensUsed != 750 {
		t.Errorf("TokensUsed = %d, want 750", r.TokensUsed)
	}
	if r.IsSuccess() {
		t.Error("IsSuccess() should return false on error")
	}
}

func TestFinalizeError_NilErrUsesDetails(t *testing.T) {
	run := &queryRun{queryStart: time.Now()}
	r := run.finalizeError(TermEmptyResponse, "model returned nothing", nil)
	if r.Error != "model returned nothing" {
		t.Errorf("Error = %q, want %q (fall back to details)", r.Error, "model returned nothing")
	}
}

// TestContextErrorDistinction verifies the loop-exit logic correctly
// distinguishes DeadlineExceeded (timeout) from Canceled (user-cancel) via
// errors.Is. This is the mapping that drives TermTimeout vs TermUserCancel.
func TestContextErrorDistinction(t *testing.T) {
	timeoutCtx, cancel1 := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel1()
	time.Sleep(2 * time.Millisecond) // ensure deadline fires
	if !errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		t.Fatal("expected DeadlineExceeded from timeout context")
	}
	if errors.Is(timeoutCtx.Err(), context.Canceled) {
		// context.DeadlineExceeded implements the Canceled semantics too in some
		// libraries, but errors.Is for these two sentinels must stay distinct.
		// If this ever fires it means the Go runtime changed semantics; our
		// termination-reason mapping would need updating.
		if timeoutCtx.Err() == context.Canceled {
			t.Fatal("timeout context incorrectly equals Canceled")
		}
	}

	cancelCtx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if !errors.Is(cancelCtx.Err(), context.Canceled) {
		t.Fatal("expected Canceled from cancelled context")
	}
	if errors.Is(cancelCtx.Err(), context.DeadlineExceeded) {
		t.Fatal("cancelled context must not match DeadlineExceeded")
	}
}

// TestTerminationReasonConstants ensures the stable string values the public
// API documents haven't drifted. External clients branch on these exact
// strings; changing them is a breaking change.
func TestTerminationReasonConstants(t *testing.T) {
	expected := map[string]string{
		TermSuccess:       "success",
		TermMaxRounds:     "max_rounds",
		TermTokenBudget:   "token_budget",
		TermTimeout:       "timeout",
		TermLoopDetected:  "loop_detected",
		TermEmptyResponse: "empty_response",
		TermTerminalError: "terminal_error",
		TermLLMError:      "llm_error",
		TermToolError:     "tool_error",
		TermUserCancel:    "user_cancel",
	}
	for got, want := range expected {
		if got != want {
			t.Errorf("termination reason drifted: got %q, want %q", got, want)
		}
	}
}

func TestQueryRun_ComputeCost_ZeroWithoutPricing(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("c1")
	run := &queryRun{
		session:          s,
		promptTokens:     1000,
		completionTokens: 500,
	}
	if got := run.computeCost(); got != 0 {
		t.Errorf("computeCost with empty pricing = %v, want 0", got)
	}
}

func TestQueryRun_ComputeCost_AppliesRates(t *testing.T) {
	a := testAgent(t)
	a.config.Model = "gpt-4o"
	a.config.Pricing = map[string]ModelPricing{
		"gpt-4o": {PromptPerMTokens: 2.50, CompletionPerMTokens: 10.00},
	}
	s := a.GetOrCreateSession("c2")
	run := &queryRun{
		session:          s,
		promptTokens:     1_000_000, // 1M prompt tokens → $2.50
		completionTokens: 500_000,   // 0.5M completion tokens → $5.00
	}
	got := run.computeCost()
	want := 7.50
	if got != want {
		t.Errorf("computeCost = %f, want %f", got, want)
	}
}

func TestQueryRun_ComputeCost_UnknownModel(t *testing.T) {
	a := testAgent(t)
	a.config.Model = "some-local-model"
	a.config.Pricing = map[string]ModelPricing{
		"gpt-4o": {PromptPerMTokens: 2.50, CompletionPerMTokens: 10.00},
	}
	s := a.GetOrCreateSession("c3")
	run := &queryRun{
		session:          s,
		promptTokens:     1_000_000,
		completionTokens: 500_000,
	}
	if got := run.computeCost(); got != 0 {
		t.Errorf("computeCost for unknown model = %v, want 0", got)
	}
}

func TestQueryResult_MetaKeys_IncludesCost(t *testing.T) {
	r := &QueryResult{
		TerminationReason: TermSuccess,
		CostUSD:           0.0042,
		PromptTokens:      500,
		CompletionTokens:  200,
	}
	meta := r.MetaKeys()
	if meta["io.llm-agent/cost_usd"] != 0.0042 {
		t.Errorf("cost_usd missing or wrong: %v", meta["io.llm-agent/cost_usd"])
	}
	if meta["io.llm-agent/prompt_tokens"] != 500 {
		t.Errorf("prompt_tokens missing or wrong: %v", meta["io.llm-agent/prompt_tokens"])
	}
}
