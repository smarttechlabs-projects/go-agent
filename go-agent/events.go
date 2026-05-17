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
	"encoding/json"
	"sync"
	"time"
)

// EventType identifies the kind of agent loop event.
type EventType string

const (
	EventQueryStart       EventType = "query_start"
	EventRoundStart       EventType = "round_start"
	EventLLMRequest       EventType = "llm_request"
	EventLLMResponse      EventType = "llm_response"
	EventToolCall         EventType = "tool_call"
	EventToolResult       EventType = "tool_result"
	EventRoundEnd         EventType = "round_end"
	EventQueryEnd         EventType = "query_end"
	EventError            EventType = "error"
	EventApprovalRequest  EventType = "approval_request"  // tool waiting for HITL go-ahead
	EventApprovalResolved EventType = "approval_resolved" // resolved (approved or denied)
)

// Event represents a single step in the agent loop, sent to the web dashboard.
type Event struct {
	Type      EventType `json:"type"`
	Timestamp int64     `json:"timestamp"`
	Round     int       `json:"round,omitempty"`
	Data      EventData `json:"data"`
}

// EventData carries step-specific information.
type EventData struct {
	// Query
	Input string `json:"input,omitempty"`

	// LLM request
	MessageCount int    `json:"message_count,omitempty"`
	ToolCount    int    `json:"tool_count,omitempty"`
	ContextChars int    `json:"context_chars,omitempty"`
	Messages     string `json:"messages,omitempty"` // Serialized message history for inspection

	// LLM response
	Content          string  `json:"content,omitempty"`
	FinishReason     string  `json:"finish_reason,omitempty"`
	HasToolCalls     bool    `json:"has_tool_calls,omitempty"`
	ToolCallCount    int     `json:"tool_call_count,omitempty"`
	TTFT             float64 `json:"ttft_ms,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	TokensPerSec     float64 `json:"tokens_per_sec,omitempty"`
	ElapsedMs        float64 `json:"elapsed_ms,omitempty"`

	// Tool call/result
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	ToolResult string `json:"tool_result,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`

	// Error
	Error string `json:"error,omitempty"`

	// Query end
	TotalRounds       int    `json:"total_rounds,omitempty"`
	FinalAnswer       string `json:"final_answer,omitempty"`
	TerminationReason string `json:"termination_reason,omitempty"`
	EarlyStopped      bool   `json:"early_stopped,omitempty"`

	// Approval request / resolved
	ApprovalID       string `json:"approval_id,omitempty"`
	ApprovalArgs     string `json:"approval_args,omitempty"` // redacted tool args
	ApprovalApproved bool   `json:"approval_approved,omitempty"`
}

// EventBus broadcasts events to all connected SSE clients.
type EventBus struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
	history []Event // Keep recent events for new clients.
	maxHist int
}

// NewEventBus creates an event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan Event]struct{}),
		maxHist: 200,
	}
}

// Subscribe registers a new SSE client. Returns a channel and an unsubscribe function.
func (eb *EventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	eb.mu.Lock()
	eb.clients[ch] = struct{}{}
	// Send history to new client.
	hist := make([]Event, len(eb.history))
	copy(hist, eb.history)
	eb.mu.Unlock()

	// Replay history in a goroutine to avoid blocking.
	go func() {
		for _, e := range hist {
			ch <- e
		}
	}()

	unsub := func() {
		eb.mu.Lock()
		delete(eb.clients, ch)
		eb.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

// Emit sends an event to all connected clients.
func (eb *EventBus) Emit(evt Event) {
	evt.Timestamp = time.Now().UnixMilli()

	eb.mu.Lock()
	eb.history = append(eb.history, evt)
	if len(eb.history) > eb.maxHist {
		eb.history = eb.history[len(eb.history)-eb.maxHist:]
	}
	clients := make([]chan Event, 0, len(eb.clients))
	for ch := range eb.clients {
		clients = append(clients, ch)
	}
	eb.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- evt:
		default:
			// Client too slow, skip.
		}
	}
}

// ClearHistory removes all stored events (called on /clear).
func (eb *EventBus) ClearHistory() {
	eb.mu.Lock()
	eb.history = nil
	eb.mu.Unlock()
}

// JSON serializes an event for SSE.
func (e Event) JSON() string {
	b, _ := json.Marshal(e)
	return string(b)
}
