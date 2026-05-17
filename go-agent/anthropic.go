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

// AnthropicClient translates between the OpenAI message format (used internally
// by the agent) and the Anthropic Messages API. This enables using Claude models
// as the LLM backend.
//
// Anthropic API docs: https://docs.anthropic.com/en/api/messages
type AnthropicClient struct {
	baseURL string
	apiKey  string
	model   string
	log     *Logger
	client  *http.Client
}

// NewAnthropicClient creates an Anthropic adapter.
func NewAnthropicClient(baseURL, apiKey, model string, log *Logger) *AnthropicClient {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &AnthropicClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		log:     log,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// --- Anthropic API types ---

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
	Tools     []anthropicTool     `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string            `json:"role"`
	Content any               `json:"content"` // string or []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Error   struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- Translation: OpenAI → Anthropic ---

func (a *AnthropicClient) ChatCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (*LLMResult, error) {
	// Extract system prompt and convert messages.
	var system string
	var anthropicMsgs []anthropicMessage

	for _, msg := range messages {
		switch msg.Role {
		case openai.ChatMessageRoleSystem:
			system = msg.Content

		case openai.ChatMessageRoleUser:
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: msg.Content,
			})

		case openai.ChatMessageRoleAssistant:
			var blocks []anthropicContentBlock
			if msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				var input any
				json.Unmarshal([]byte(tc.Function.Arguments), &input)
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
			if len(blocks) > 0 {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "assistant",
					Content: blocks,
				})
			}

		case openai.ChatMessageRoleTool:
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role: "user",
				Content: []anthropicContentBlock{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   msg.Content,
				}},
			})
		}
	}

	// Convert tools.
	var anthropicTools []anthropicTool
	for _, t := range tools {
		if t.Function == nil {
			continue
		}
		anthropicTools = append(anthropicTools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	req := anthropicRequest{
		Model:     a.model,
		MaxTokens: 4096,
		System:    system,
		Messages:  anthropicMsgs,
		Tools:     anthropicTools,
	}

	url := fmt.Sprintf("%s/v1/messages", a.baseURL)
	a.log.Send("LLM", "POST %s (anthropic, %d messages, %d tools)", a.model, len(anthropicMsgs), len(tools))

	start := time.Now()
	resp, err := a.doRequest(ctx, url, req)
	elapsed := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}

	return a.translateResponse(resp, elapsed)
}

func (a *AnthropicClient) doRequest(ctx context.Context, url string, req anthropicRequest) (*anthropicResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	a.log.Info("anthropic request size: %d bytes", len(body))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != 200 {
		var apiErr anthropicError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			a.log.Error("anthropic HTTP %d: %s", httpResp.StatusCode, apiErr.Error.Message)
			return nil, fmt.Errorf("anthropic: %s", apiErr.Error.Message)
		}
		a.log.Error("anthropic HTTP %d: %s", httpResp.StatusCode, truncateLog(string(respBody), 300))
		return nil, fmt.Errorf("anthropic HTTP %d", httpResp.StatusCode)
	}

	var resp anthropicResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &resp, nil
}

// --- Translation: Anthropic → OpenAI ---

func (a *AnthropicClient) translateResponse(resp *anthropicResponse, elapsed time.Duration) (*LLMResult, error) {
	result := &LLMResult{
		Elapsed: elapsed,
		Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
		},
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	// Map stop reason.
	switch resp.StopReason {
	case "end_turn":
		result.FinishReason = "stop"
	case "tool_use":
		result.FinishReason = "tool_calls"
	case "max_tokens":
		result.FinishReason = "length"
	default:
		result.FinishReason = resp.StopReason
	}

	// Extract content and tool calls from content blocks.
	var textParts []string
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			result.Message.ToolCalls = append(result.Message.ToolCalls, openai.ToolCall{
				ID:   block.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      block.Name,
					Arguments: string(inputJSON),
				},
			})
		}
	}
	result.Message.Content = strings.Join(textParts, "")

	a.log.Recv("LLM", "anthropic: %d chars, %d tool_calls, finish=%s, %s",
		len(result.Message.Content), len(result.Message.ToolCalls),
		result.FinishReason, elapsed.Round(time.Millisecond))

	return result, nil
}
