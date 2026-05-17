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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// GeminiClient translates between the OpenAI message format (used internally
// by the agent) and the Google Gemini REST API. This is an example of how to
// add a non-OpenAI provider without changing the agent loop.
//
// Gemini API docs: https://ai.google.dev/api/generate-content
type GeminiClient struct {
	baseURL string
	apiKey  string
	model   string
	log     *Logger
	client  *http.Client
}

// NewGeminiClient creates a Gemini adapter.
func NewGeminiClient(baseURL, apiKey, model string, log *Logger) *GeminiClient {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &GeminiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		log:     log,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// --- Gemini API types ---

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	Tools            []geminiTool            `json:"tools,omitempty"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

type geminiFunctionDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type geminiGenerationConfig struct {
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate  `json:"candidates"`
	UsageMetadata *geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// --- Translation: OpenAI → Gemini ---

// ChatCompletion translates an OpenAI-format request to Gemini, calls the API,
// and translates the response back to an LLMResult.
func (g *GeminiClient) ChatCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (*LLMResult, error) {
	// Convert messages.
	var contents []geminiContent
	var sysInstruction *geminiContent

	for _, msg := range messages {
		switch msg.Role {
		case openai.ChatMessageRoleSystem:
			sysInstruction = &geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: msg.Content}},
			}

		case openai.ChatMessageRoleUser:
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: msg.Content}},
			})

		case openai.ChatMessageRoleAssistant:
			var parts []geminiPart
			if msg.Content != "" {
				parts = append(parts, geminiPart{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					},
				})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{
					Role:  "model",
					Parts: parts,
				})
			}

		case openai.ChatMessageRoleTool:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResponse{
						Name: toolCallIDToName(msg.ToolCallID, messages),
						Response: map[string]any{
							"result": msg.Content,
						},
					},
				}},
			})
		}
	}

	// Convert tools.
	var geminiTools []geminiTool
	if len(tools) > 0 {
		var decls []geminiFunctionDecl
		for _, t := range tools {
			if t.Function == nil {
				continue
			}
			decls = append(decls, geminiFunctionDecl{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		geminiTools = []geminiTool{{FunctionDeclarations: decls}}
	}

	req := geminiRequest{
		Contents:         contents,
		Tools:            geminiTools,
		SystemInstruction: sysInstruction,
	}

	// Call Gemini API.
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", g.baseURL, g.model, g.apiKey)
	g.log.Send("LLM", "POST %s (gemini, %d contents, %d tools)", g.model, len(contents), len(tools))

	start := time.Now()
	resp, err := g.doRequest(ctx, url, req)
	elapsed := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}

	// Translate response back to LLMResult.
	return g.translateResponse(resp, elapsed)
}

// doRequest sends the Gemini API request and returns the parsed response.
func (g *GeminiClient) doRequest(ctx context.Context, url string, req geminiRequest) (*geminiResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	g.log.Info("gemini request size: %d bytes", len(body))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != 200 {
		g.log.Error("gemini HTTP %d: %s", httpResp.StatusCode, truncateLog(string(respBody), 300))
		return nil, fmt.Errorf("gemini HTTP %d: %s", httpResp.StatusCode, truncateLog(string(respBody), 200))
	}

	var resp geminiResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &resp, nil
}

// --- Translation: Gemini → OpenAI ---

func (g *GeminiClient) translateResponse(resp *geminiResponse, elapsed time.Duration) (*LLMResult, error) {
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	candidate := resp.Candidates[0]
	result := &LLMResult{
		Elapsed: elapsed,
		Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
		},
	}

	// Map finish reason.
	switch candidate.FinishReason {
	case "STOP":
		result.FinishReason = "stop"
	case "MAX_TOKENS":
		result.FinishReason = "length"
	default:
		result.FinishReason = strings.ToLower(candidate.FinishReason)
	}

	// Extract content and tool calls from parts.
	var textParts []string
	for i, part := range candidate.Content.Parts {
		if part.Text != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall != nil {
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			result.Message.ToolCalls = append(result.Message.ToolCalls, openai.ToolCall{
				ID:   fmt.Sprintf("gemini_call_%d", i),
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      part.FunctionCall.Name,
					Arguments: string(argsJSON),
				},
			})
			result.FinishReason = "tool_calls"
		}
	}
	result.Message.Content = strings.Join(textParts, "")

	// Map usage.
	if resp.UsageMetadata != nil {
		result.Usage = LLMUsage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}

	g.log.Recv("LLM", "gemini: %d chars, %d tool_calls, finish=%s, %s",
		len(result.Message.Content), len(result.Message.ToolCalls),
		result.FinishReason, elapsed.Round(time.Millisecond))

	return result, nil
}

// toolCallIDToName finds the tool name that produced a given tool call ID
// by scanning the message history for the assistant message with that call.
func toolCallIDToName(callID string, messages []openai.ChatCompletionMessage) string {
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID {
				return tc.Function.Name
			}
		}
	}
	return "unknown"
}
