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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestAnthropicTranslateResponse_TextOnly(t *testing.T) {
	a := NewAnthropicClient("", "test-key", "claude-3-5-sonnet", NewLogger(false))

	resp := &anthropicResponse{
		Content: []anthropicContentBlock{
			{Type: "text", Text: "Hello from Claude!"},
		},
		StopReason: "end_turn",
		Usage:      anthropicUsage{InputTokens: 15, OutputTokens: 8},
	}

	result, err := a.translateResponse(resp, 0)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "Hello from Claude!" {
		t.Errorf("content = %q", result.Message.Content)
	}
	if result.FinishReason != "stop" {
		t.Errorf("finish = %q, want 'stop'", result.FinishReason)
	}
	if result.Usage.PromptTokens != 15 {
		t.Errorf("prompt tokens = %d, want 15", result.Usage.PromptTokens)
	}
	if result.Usage.CompletionTokens != 8 {
		t.Errorf("completion tokens = %d, want 8", result.Usage.CompletionTokens)
	}
	if result.Usage.TotalTokens != 23 {
		t.Errorf("total tokens = %d, want 23", result.Usage.TotalTokens)
	}
}

func TestAnthropicTranslateResponse_ToolUse(t *testing.T) {
	a := NewAnthropicClient("", "test-key", "claude-3-5-sonnet", NewLogger(false))

	resp := &anthropicResponse{
		Content: []anthropicContentBlock{
			{Type: "text", Text: "I'll check that port."},
			{Type: "tool_use", ID: "toolu_01", Name: "check_port", Input: map[string]any{"port": float64(8000)}},
		},
		StopReason: "tool_use",
		Usage:      anthropicUsage{InputTokens: 20, OutputTokens: 12},
	}

	result, err := a.translateResponse(resp, 0)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "I'll check that port." {
		t.Errorf("content = %q", result.Message.Content)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want 'tool_calls'", result.FinishReason)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(result.Message.ToolCalls))
	}

	tc := result.Message.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Errorf("tool ID = %q", tc.ID)
	}
	if tc.Function.Name != "check_port" {
		t.Errorf("tool name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"port":8000}` {
		t.Errorf("tool args = %s", tc.Function.Arguments)
	}
}

func TestAnthropicTranslateResponse_MaxTokens(t *testing.T) {
	a := NewAnthropicClient("", "test-key", "claude-3-5-sonnet", NewLogger(false))

	resp := &anthropicResponse{
		Content:    []anthropicContentBlock{{Type: "text", Text: "Truncated..."}},
		StopReason: "max_tokens",
		Usage:      anthropicUsage{InputTokens: 10, OutputTokens: 4096},
	}

	result, err := a.translateResponse(resp, 0)
	if err != nil {
		t.Fatal(err)
	}

	if result.FinishReason != "length" {
		t.Errorf("finish = %q, want 'length'", result.FinishReason)
	}
}

func TestAnthropicEndToEnd_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers.
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing anthropic-version header")
		}

		// Verify request format.
		var req anthropicRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Model != "claude-3-5-sonnet" {
			t.Errorf("model = %q", req.Model)
		}
		if req.System != "You are helpful." {
			t.Errorf("system = %q", req.System)
		}
		if len(req.Messages) != 1 {
			t.Errorf("messages = %d, want 1", len(req.Messages))
		}
		if len(req.Tools) != 1 {
			t.Errorf("tools = %d, want 1", len(req.Tools))
		}

		// Return mock response.
		resp := anthropicResponse{
			ID:   "msg_01",
			Type: "message",
			Role: "assistant",
			Content: []anthropicContentBlock{
				{Type: "text", Text: "Port 8000 is in use."},
			},
			StopReason: "end_turn",
			Usage:      anthropicUsage{InputTokens: 25, OutputTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	a := NewAnthropicClient(server.URL, "test-key", "claude-3-5-sonnet", NewLogger(false))

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "You are helpful."},
		{Role: openai.ChatMessageRoleUser, Content: "Is port 8000 in use?"},
	}
	tools := []openai.Tool{{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "check_port",
			Description: "Check if a port is in use",
		},
	}}

	result, err := a.ChatCompletion(context.Background(), messages, tools)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "Port 8000 is in use." {
		t.Errorf("content = %q", result.Message.Content)
	}
	if result.Usage.TotalTokens != 35 {
		t.Errorf("total = %d, want 35", result.Usage.TotalTokens)
	}
}

func TestDetectProvider_Anthropic(t *testing.T) {
	tests := []struct {
		endpoint string
		want     LLMProvider
	}{
		{"https://api.anthropic.com", ProviderAnthropic},
		{"https://api.anthropic.com/v1/messages", ProviderAnthropic},
		{"http://localhost:8000/api/v1", ProviderOpenAI},
		{"https://generativelanguage.googleapis.com/v1beta", ProviderGemini},
	}

	for _, tt := range tests {
		got := detectProvider(tt.endpoint)
		if got != tt.want {
			t.Errorf("detectProvider(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}
