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
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestGeminiTranslateResponse_TextOnly(t *testing.T) {
	g := NewGeminiClient("", "test-key", "gemini-pro", NewLogger(false))

	resp := &geminiResponse{
		Candidates: []geminiCandidate{{
			Content: geminiContent{
				Role: "model",
				Parts: []geminiPart{{Text: "Hello, world!"}},
			},
			FinishReason: "STOP",
		}},
		UsageMetadata: &geminiUsage{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
			TotalTokenCount:      15,
		},
	}

	result, err := g.translateResponse(resp, 0)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "Hello, world!" {
		t.Errorf("content = %q, want 'Hello, world!'", result.Message.Content)
	}
	if result.FinishReason != "stop" {
		t.Errorf("finish = %q, want 'stop'", result.FinishReason)
	}
	if len(result.Message.ToolCalls) != 0 {
		t.Errorf("tool calls = %d, want 0", len(result.Message.ToolCalls))
	}
	if result.Usage.PromptTokens != 10 {
		t.Errorf("prompt tokens = %d, want 10", result.Usage.PromptTokens)
	}
}

func TestGeminiTranslateResponse_ToolCall(t *testing.T) {
	g := NewGeminiClient("", "test-key", "gemini-pro", NewLogger(false))

	resp := &geminiResponse{
		Candidates: []geminiCandidate{{
			Content: geminiContent{
				Role: "model",
				Parts: []geminiPart{
					{Text: "Let me check that."},
					{FunctionCall: &geminiFunctionCall{
						Name: "check_port",
						Args: map[string]any{"port": float64(8000)},
					}},
				},
			},
			FinishReason: "STOP",
		}},
	}

	result, err := g.translateResponse(resp, 0)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "Let me check that." {
		t.Errorf("content = %q", result.Message.Content)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want 'tool_calls'", result.FinishReason)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(result.Message.ToolCalls))
	}
	tc := result.Message.ToolCalls[0]
	if tc.Function.Name != "check_port" {
		t.Errorf("tool name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"port":8000}` {
		t.Errorf("tool args = %s", tc.Function.Arguments)
	}
}

func TestGeminiTranslateResponse_NoCandidates(t *testing.T) {
	g := NewGeminiClient("", "test-key", "gemini-pro", NewLogger(false))

	resp := &geminiResponse{Candidates: nil}
	_, err := g.translateResponse(resp, 0)
	if err == nil {
		t.Fatal("expected error for no candidates")
	}
}

func TestToolCallIDToName(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: "assistant", ToolCalls: []openai.ToolCall{
			{ID: "call_1", Function: openai.FunctionCall{Name: "check_port"}},
			{ID: "call_2", Function: openai.FunctionCall{Name: "fetch"}},
		}},
	}

	if name := toolCallIDToName("call_1", messages); name != "check_port" {
		t.Errorf("call_1 → %q, want 'check_port'", name)
	}
	if name := toolCallIDToName("call_2", messages); name != "fetch" {
		t.Errorf("call_2 → %q, want 'fetch'", name)
	}
	if name := toolCallIDToName("missing", messages); name != "unknown" {
		t.Errorf("missing → %q, want 'unknown'", name)
	}
}

func TestGeminiEndToEnd_MockServer(t *testing.T) {
	// Mock Gemini API server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}

		// Verify the request contains Gemini format.
		var req geminiRequest
		json.NewDecoder(r.Body).Decode(&req)

		if len(req.Contents) == 0 {
			t.Error("no contents in request")
		}
		if req.SystemInstruction == nil {
			t.Error("no system instruction")
		}

		// Return a mock Gemini response.
		resp := geminiResponse{
			Candidates: []geminiCandidate{{
				Content: geminiContent{
					Role:  "model",
					Parts: []geminiPart{{Text: "Port 8000 is in use."}},
				},
				FinishReason: "STOP",
			}},
			UsageMetadata: &geminiUsage{
				PromptTokenCount:     20,
				CandidatesTokenCount: 8,
				TotalTokenCount:      28,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := NewGeminiClient(server.URL, "test-key", "gemini-pro", NewLogger(false))

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "You are helpful."},
		{Role: openai.ChatMessageRoleUser, Content: "Is port 8000 in use?"},
	}
	tools := []openai.Tool{{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "check_port",
			Description: "Check if a port is in use",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"port": map[string]any{"type": "number"}}},
		},
	}}

	result, err := g.ChatCompletion(context.Background(), messages, tools)
	if err != nil {
		t.Fatal(err)
	}

	if result.Message.Content != "Port 8000 is in use." {
		t.Errorf("content = %q", result.Message.Content)
	}
	if result.Usage.TotalTokens != 28 {
		t.Errorf("total tokens = %d, want 28", result.Usage.TotalTokens)
	}
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		endpoint string
		want     LLMProvider
	}{
		{"https://generativelanguage.googleapis.com/v1beta", ProviderGemini},
		{"https://gemini.example.com/v1", ProviderGemini},
		{"http://localhost:8000/api/v1", ProviderOpenAI},
		{"http://localhost:1234/v1", ProviderOpenAI},
		{"https://api.openai.com/v1", ProviderOpenAI},
	}

	for _, tt := range tests {
		got := detectProvider(tt.endpoint)
		if got != tt.want {
			t.Errorf("detectProvider(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}
