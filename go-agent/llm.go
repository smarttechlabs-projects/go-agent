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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	openai "github.com/sashabaranov/go-openai"
)

// LLMClient wraps an LLM backend. Supports OpenAI-compatible, Gemini, and Anthropic.
type LLMClient struct {
	client    *openai.Client
	gemini    *GeminiClient    // Non-nil when provider is Gemini.
	anthropic *AnthropicClient // Non-nil when provider is Anthropic.
	model     string
	provider  LLMProvider
	log       *Logger
	stream    bool

	// MaxRetries is the total number of attempts for ChatCompletion on
	// transient errors (connection refused, 429/503/504, mid-stream EOF).
	// Zero or negative disables retry (single attempt). Default 3 attempts.
	MaxRetries int
}

// LLMResult holds the complete result of an LLM call with performance metrics.
type LLMResult struct {
	Message      openai.ChatCompletionMessage
	FinishReason string
	Usage        LLMUsage
	TTFT         time.Duration // Time to first token (streaming only)
	Elapsed      time.Duration // Total response time
	ServerError  string        // Raw server error from stream body (if any)
}

// LLMUsage holds token counts from the LLM response.
type LLMUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// TokensPerSecond computes the generation throughput.
func (u LLMUsage) TokensPerSecond(elapsed time.Duration) float64 {
	if u.CompletionTokens == 0 || elapsed <= 0 {
		return 0
	}
	return float64(u.CompletionTokens) / elapsed.Seconds()
}

// NewLLMClient creates a client configured for the given endpoint, model, and provider.
func NewLLMClient(endpointURL, model, apiKey string, provider LLMProvider, log *Logger, stream bool) *LLMClient {
	llm := &LLMClient{
		model:      model,
		provider:   provider,
		log:        log,
		stream:     stream,
		MaxRetries: 3,
	}

	if provider == ProviderGemini {
		llm.gemini = NewGeminiClient(endpointURL, apiKey, model, log)
		log.Info("using Gemini provider")
	} else if provider == ProviderAnthropic {
		llm.anthropic = NewAnthropicClient(endpointURL, apiKey, model, log)
		log.Info("using Anthropic provider")
	} else {
		config := openai.DefaultConfig(apiKey)
		config.BaseURL = endpointURL

		if log.Verbose() {
			config.HTTPClient = &http.Client{
				Transport: &debugTransport{base: http.DefaultTransport, log: log},
			}
		}

		llm.client = openai.NewClientWithConfig(config)
	}

	return llm
}

// debugTransport logs the raw HTTP request body and response status.
type debugTransport struct {
	base http.RoundTripper
	log  *Logger
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Log request body size (don't dump full body, just size).
	var bodySize int
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			bodySize = len(body)
			req.Body = io.NopCloser(bytes.NewReader(body))

			// In very verbose scenarios, dump the first part of the body.
			if bodySize > 0 {
				preview := string(body)
				if len(preview) > 300 {
					preview = preview[:300] + "..."
				}
				// Redact API keys / tokens before they land in logs.
				t.log.Data("http-body", Redact(preview), 300)
			}
		}
	}
	t.log.Send("HTTP", "%s %s (%d bytes)", req.Method, req.URL.Path, bodySize)

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.log.Error("HTTP transport error: %v", err)
		return nil, err
	}

	ct := resp.Header.Get("Content-Type")
	t.log.Recv("HTTP", "%d %s (content-type: %s)", resp.StatusCode, resp.Status, ct)

	// If the response is an error, read and log the body.
	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			t.log.Error("HTTP %d body: %s", resp.StatusCode, truncateLog(string(body), 500))
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}

	// For streaming responses, wrap the body to capture the first bytes.
	if strings.Contains(ct, "event-stream") {
		resp.Body = &peekReadCloser{
			rc:     resp.Body,
			log:    t.log,
			maxLog: 500,
		}
	}

	return resp, nil
}

// peekReadCloser wraps a ReadCloser and logs the first N bytes read.
type peekReadCloser struct {
	rc      io.ReadCloser
	log     *Logger
	buf     []byte
	maxLog  int
	logged  bool
}

func (p *peekReadCloser) Read(b []byte) (int, error) {
	n, err := p.rc.Read(b)
	if n > 0 && !p.logged {
		p.buf = append(p.buf, b[:n]...)
		if len(p.buf) >= p.maxLog || err != nil {
			p.logged = true
			p.log.Data("stream-start", string(p.buf), p.maxLog)
		}
	}
	if err != nil && !p.logged {
		p.logged = true
		if len(p.buf) > 0 {
			p.log.Data("stream-start", string(p.buf), p.maxLog)
		} else {
			p.log.Warn("stream body: 0 bytes read, err=%v", err)
		}
	}
	return n, err
}

func (p *peekReadCloser) Close() error {
	if !p.logged && len(p.buf) > 0 {
		p.log.Data("stream-start", string(p.buf), p.maxLog)
	}
	return p.rc.Close()
}

// parseServerError extracts a user-friendly error from a raw server error string.
// Handles the common "exceeds context size" error from Lemonade.
func parseServerError(raw string) error {
	// Try to parse as JSON error from Lemonade.
	var envelope struct {
		Error struct {
			Message       string `json:"message"`
			Type          string `json:"type"`
			PromptTokens  int    `json:"n_prompt_tokens"`
			ContextSize   int    `json:"n_ctx"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Error.Message != "" {
		if envelope.Error.Type == "exceed_context_size_error" {
			return fmt.Errorf("context window too small: request needs %d tokens but model is loaded with %d. "+
				"Increase it with: ./start-lemonade.sh config ctx-size %d",
				envelope.Error.PromptTokens, envelope.Error.ContextSize,
				nextPowerOf2(envelope.Error.PromptTokens*2))
		}
		return fmt.Errorf("server error: %s", envelope.Error.Message)
	}
	return fmt.Errorf("server error: %s", truncateLog(raw, 200))
}

// nextPowerOf2 returns the next power of 2 >= n, clamped to common context sizes.
func nextPowerOf2(n int) int {
	for _, size := range []int{4096, 8192, 16384, 32768, 65536, 131072} {
		if size >= n {
			return size
		}
	}
	return 131072
}

// debugRequestJSON logs the serialized request for debugging.
func debugRequestJSON(log *Logger, req openai.ChatCompletionRequest) {
	if !log.Verbose() {
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return
	}
	log.Info("request JSON size: %d bytes", len(raw))
}

// ChatCompletion sends a chat request, collects the full response, and
// returns it with TTFT, token usage, and throughput metrics. Transient
// failures (429/503/504, connection refused, mid-stream EOF) are retried
// with exponential backoff up to MaxRetries attempts. Terminal errors and
// context cancellations are NOT retried.
func (l *LLMClient) ChatCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (result *LLMResult, err error) {
	ctxSize := historySize(messages)
	l.log.LLMRequest(l.model, len(messages), len(tools), ctxSize)

	ctx, span := startSpan(ctx, "llm.chat_completion",
		attrModel.String(l.model),
		attrMessageCount.Int(len(messages)),
		attrToolCount.Int(len(tools)),
		attrContextChars.Int(ctxSize),
	)
	defer span.End()

	callStart := time.Now()
	defer func() {
		status := "ok"
		if err != nil {
			status = "error"
		}
		recordLLMCall(ctx, string(l.provider), status, time.Since(callStart))
	}()

	maxAttempts := l.MaxRetries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	err = retryWithBackoff(ctx, maxAttempts, func(attempt int) error {
		if attempt > 0 {
			span.AddEvent("llm.retry", trace.WithAttributes(attribute.Int("attempt", attempt)))
		}
		r, e := l.chatOnce(ctx, messages, tools)
		if e != nil {
			return e
		}
		result = r
		return nil
	}, l.log)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	tps := result.Usage.TokensPerSecond(result.Elapsed)
	span.SetAttributes(
		attrFinishReason.String(result.FinishReason),
		attrHasToolCalls.Bool(len(result.Message.ToolCalls) > 0),
		attrContentLen.Int(len(result.Message.Content)),
		attribute.String("llm.duration", result.Elapsed.Round(time.Millisecond).String()),
		attribute.String("llm.ttft", result.TTFT.Round(time.Millisecond).String()),
		attribute.Int("llm.prompt_tokens", result.Usage.PromptTokens),
		attribute.Int("llm.completion_tokens", result.Usage.CompletionTokens),
		attribute.Float64("llm.tokens_per_second", tps),
	)

	return result, nil
}

// chatOnce dispatches a single attempt to the active provider. Called by
// ChatCompletion's retry wrapper; any transient error returned bubbles up
// and triggers a backoff retry.
func (l *LLMClient) chatOnce(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (*LLMResult, error) {
	if l.provider == ProviderGemini {
		return l.gemini.ChatCompletion(ctx, messages, tools)
	}
	if l.provider == ProviderAnthropic {
		return l.anthropic.ChatCompletion(ctx, messages, tools)
	}

	if l.stream {
		result, err := l.chatStream(ctx, messages, tools)
		if err != nil {
			return nil, err
		}
		// Streaming returned empty -- check for embedded server error, else
		// fall back to non-streaming.
		if result.Message.Content == "" && len(result.Message.ToolCalls) == 0 && result.FinishReason == "" {
			if result.ServerError != "" {
				return nil, parseServerError(result.ServerError)
			}
			l.log.Warn("streaming returned empty, falling back to non-streaming")
			return l.chatSync(ctx, messages, tools)
		}
		return result, nil
	}
	return l.chatSync(ctx, messages, tools)
}

// chatStream sends a streaming request and collects the response.
func (l *LLMClient) chatStream(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (*LLMResult, error) {
	req := openai.ChatCompletionRequest{
		Model:    l.model,
		Messages: messages,
		Stream:   true,
		StreamOptions: &openai.StreamOptions{
			IncludeUsage: true,
		},
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	debugRequestJSON(l.log, req)

	start := time.Now()
	stream, err := l.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		l.log.Error("LLM stream request failed: %v", err)
		return nil, fmt.Errorf("chat completion stream: %w", err)
	}
	defer stream.Close()

	result := &LLMResult{}
	var content strings.Builder
	toolAccum := make(map[int]*openai.ToolCall)
	firstChunk := true
	chunkCount := 0
	var role string

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// If we got an error on the very first read, it might be a server
			// error embedded in the stream body. Capture the error text.
			if chunkCount == 0 {
				errStr := err.Error()
				l.log.Error("LLM stream error (no chunks received): %v", err)
				result.ServerError = errStr
				result.Elapsed = time.Since(start)
				return result, nil // Return empty result with ServerError set
			}
			l.log.Error("LLM stream error: %v", err)
			return nil, fmt.Errorf("chat completion stream: %w", err)
		}
		chunkCount++

		if firstChunk {
			result.TTFT = time.Since(start)
			firstChunk = false
		}

		if chunk.Usage != nil {
			result.Usage = LLMUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		if delta.Role != "" {
			role = delta.Role
		}
		content.WriteString(delta.Content)

		for _, tc := range delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			existing, ok := toolAccum[idx]
			if !ok {
				toolAccum[idx] = &openai.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openai.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			} else {
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				existing.Function.Name += tc.Function.Name
				existing.Function.Arguments += tc.Function.Arguments
			}
		}

		if choice.FinishReason != "" {
			result.FinishReason = string(choice.FinishReason)
		}
	}

	result.Elapsed = time.Since(start)

	if role == "" {
		role = openai.ChatMessageRoleAssistant
	}
	result.Message = openai.ChatCompletionMessage{
		Role:    role,
		Content: content.String(),
	}

	if len(toolAccum) > 0 {
		maxIdx := 0
		for idx := range toolAccum {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if tc, ok := toolAccum[i]; ok {
				result.Message.ToolCalls = append(result.Message.ToolCalls, *tc)
			}
		}
	}

	return result, nil
}

// chatSync sends a non-streaming request (fallback when streaming fails).
func (l *LLMClient) chatSync(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
) (*LLMResult, error) {
	req := openai.ChatCompletionRequest{
		Model:    l.model,
		Messages: messages,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	start := time.Now()
	resp, err := l.client.CreateChatCompletion(ctx, req)
	if err != nil {
		l.log.Error("LLM request failed: %v", err)
		return nil, fmt.Errorf("chat completion: %w", err)
	}
	elapsed := time.Since(start)

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from model (no choices)")
	}

	choice := resp.Choices[0]
	result := &LLMResult{
		Message:      choice.Message,
		FinishReason: string(choice.FinishReason),
		Elapsed:      elapsed,
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}

	return result, nil
}
