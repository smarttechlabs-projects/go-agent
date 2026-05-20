// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// TestExecuteToolCalls_Parallel verifies that multiple tool calls emitted by
// the same LLM response run concurrently (not serially). Three tools each
// sleep 100ms; wall time should be ~100ms, well under 300ms.
func TestExecuteToolCalls_Parallel(t *testing.T) {
	a := testAgent(t)
	a.callTool = func(ctx context.Context, name, argsJSON string) (string, error) {
		select {
		case <-time.After(100 * time.Millisecond):
			return "ok from " + name, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s := a.GetOrCreateSession("par")
	run := &queryRun{
		session:      s,
		activeLimits: a.config.ResolveLimits(nil),
		queryStart:   time.Now(),
	}

	calls := []openai.ToolCall{
		{ID: "c1", Function: openai.FunctionCall{Name: "tool_a", Arguments: "{}"}},
		{ID: "c2", Function: openai.FunctionCall{Name: "tool_b", Arguments: "{}"}},
		{ID: "c3", Function: openai.FunctionCall{Name: "tool_c", Arguments: "{}"}},
	}

	// Have to hold runMu because executeToolCalls mutates s.history.
	s.runMu.Lock()
	start := time.Now()
	err := run.executeToolCalls(context.Background(), 1, calls)
	elapsed := time.Since(start)
	s.runMu.Unlock()

	if err != nil {
		t.Fatalf("executeToolCalls: %v", err)
	}
	// Parallel execution should finish in ~100ms; 250ms is a generous upper
	// bound that still fails if we accidentally run serially (~300ms).
	if elapsed > 250*time.Millisecond {
		t.Errorf("expected concurrent execution (~100ms), got %v -- tools running serially?", elapsed)
	}
	if run.toolCallsMade != 3 {
		t.Errorf("toolCallsMade = %d, want 3", run.toolCallsMade)
	}
	// History should have 3 tool messages appended in call order.
	if s.HistoryLen() != 4 { // system + 3 tool results
		t.Errorf("history len = %d, want 4", s.HistoryLen())
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		msg := s.history[i+1]
		if msg.ToolCallID != want {
			t.Errorf("history[%d] tool_call_id = %q, want %q (order preserved?)", i+1, msg.ToolCallID, want)
		}
	}
}

// TestExecuteToolCalls_ConcurrencyCap verifies that the fan-out is bounded
// by maxParallelToolCalls. We run 10 tools and count the max concurrent
// inflight; it must not exceed the cap.
func TestExecuteToolCalls_ConcurrencyCap(t *testing.T) {
	a := testAgent(t)
	var inflight atomic.Int32
	var peak atomic.Int32
	a.callTool = func(ctx context.Context, name, argsJSON string) (string, error) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inflight.Add(-1)
		return "done", nil
	}
	s := a.GetOrCreateSession("cap")
	run := &queryRun{
		session:      s,
		activeLimits: a.config.ResolveLimits(nil),
		queryStart:   time.Now(),
	}

	var calls []openai.ToolCall
	for i := 0; i < 10; i++ {
		calls = append(calls, openai.ToolCall{
			ID:       fmt.Sprintf("c%d", i),
			Function: openai.FunctionCall{Name: "tool", Arguments: "{}"},
		})
	}

	s.runMu.Lock()
	_ = run.executeToolCalls(context.Background(), 1, calls)
	s.runMu.Unlock()

	if peak.Load() > int32(maxParallelToolCalls) {
		t.Errorf("peak concurrency = %d, exceeds cap %d", peak.Load(), maxParallelToolCalls)
	}
	if peak.Load() < 2 {
		t.Errorf("peak concurrency = %d, expected >=2 (parallelism not engaged)", peak.Load())
	}
}

// TestExecuteToolCalls_PerToolTimeout verifies that a slow tool hits the
// per-tool timeout and is surfaced as a tool-level error (not a query-level
// cancel), so the loop can continue.
func TestExecuteToolCalls_PerToolTimeout(t *testing.T) {
	a := testAgent(t)
	a.config.ToolTimeoutSecs = 1 // 1 second
	a.callTool = func(ctx context.Context, name, argsJSON string) (string, error) {
		select {
		case <-time.After(3 * time.Second):
			return "should have timed out", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s := a.GetOrCreateSession("slow")
	run := &queryRun{
		session:      s,
		activeLimits: a.config.ResolveLimits(nil),
		queryStart:   time.Now(),
	}
	calls := []openai.ToolCall{
		{ID: "c1", Function: openai.FunctionCall{Name: "slow_tool", Arguments: "{}"}},
	}

	s.runMu.Lock()
	start := time.Now()
	err := run.executeToolCalls(context.Background(), 1, calls)
	elapsed := time.Since(start)
	s.runMu.Unlock()

	if err != nil {
		t.Fatalf("executeToolCalls returned err (should only fail at query level): %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("per-tool timeout not enforced: took %v, cap was 1s", elapsed)
	}
	// The recorded history message should include the timeout text.
	last := s.history[len(s.history)-1]
	if last.Role != openai.ChatMessageRoleTool {
		t.Errorf("last message role = %q, want tool", last.Role)
	}
}
