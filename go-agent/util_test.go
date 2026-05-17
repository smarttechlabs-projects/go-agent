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
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestTruncateLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exactly max", "hello", 5, "hello"},
		{"longer than max", "hello world", 5, "hello..."},
		{"empty string", "", 10, ""},
		{"zero max truncates everything non-empty", "x", 0, "..."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateLog(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateLog(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestExtractURL(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"valid url field", `{"url": "https://example.com"}`, "https://example.com"},
		{"url alongside other fields", `{"url": "https://example.com", "depth": 2}`, "https://example.com"},
		{"missing url field", `{"depth": 2}`, ""},
		{"non-string url field", `{"url": 42}`, ""},
		{"invalid json returns empty", `not json`, ""},
		{"empty object", `{}`, ""},
		{"empty url string", `{"url": ""}`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractURL(tc.args)
			if got != tc.want {
				t.Errorf("extractURL(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestHistorySize(t *testing.T) {
	t.Run("empty history is zero", func(t *testing.T) {
		if got := historySize(nil); got != 0 {
			t.Errorf("historySize(nil) = %d, want 0", got)
		}
	})

	t.Run("counts content lengths", func(t *testing.T) {
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},        // 5
			{Role: openai.ChatMessageRoleAssistant, Content: "hi there"}, // 8
		}
		if got := historySize(msgs); got != 13 {
			t.Errorf("historySize = %d, want 13", got)
		}
	})

	t.Run("counts tool call argument blobs", func(t *testing.T) {
		msgs := []openai.ChatCompletionMessage{
			{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ToolCall{
					{Function: openai.FunctionCall{Name: "check_port", Arguments: `{"port":13305}`}}, // 14
					{Function: openai.FunctionCall{Name: "fetch", Arguments: `{"url":"x"}`}},          // 11
				},
			},
		}
		if got := historySize(msgs); got != 25 {
			t.Errorf("historySize = %d, want 25", got)
		}
	})

	t.Run("mixed content and tool calls", func(t *testing.T) {
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "abc"}, // 3
			{
				Role:    openai.ChatMessageRoleAssistant,
				Content: "de", // 2
				ToolCalls: []openai.ToolCall{
					{Function: openai.FunctionCall{Arguments: "fghij"}}, // 5
				},
			},
		}
		if got := historySize(msgs); got != 10 {
			t.Errorf("historySize = %d, want 10", got)
		}
	})
}

// Note: countTrailingMatches is covered by TestCountTrailingMatches in safety_test.go
// (it lives in util.go but is primarily a loop-fingerprint safety helper).

func TestSummarizeMessages(t *testing.T) {
	t.Run("empty input produces empty output", func(t *testing.T) {
		if got := summarizeMessages(nil); got != "" {
			t.Errorf("summarizeMessages(nil) = %q, want empty", got)
		}
	})

	t.Run("renders role, content, tool calls, and tool_call_id", func(t *testing.T) {
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "is port 13305 in use?"},
			{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ToolCall{
					{Function: openai.FunctionCall{Name: "check_port", Arguments: `{"port":13305}`}},
				},
			},
			{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: "port in use"},
		}
		got := summarizeMessages(msgs)

		// Spot-check the structural markers rather than asserting the exact string —
		// the format is for human inspection in the dashboard, not a wire protocol.
		checks := []string{
			"[0] role=user",
			"is port 13305 in use?",
			"[1] role=assistant tool_calls=1",
			"-> check_port(",
			"[2] role=tool",
			"tool_call_id=call_1",
			"port in use",
		}
		for _, want := range checks {
			if !strings.Contains(got, want) {
				t.Errorf("summarizeMessages output missing %q\nfull output:\n%s", want, got)
			}
		}
	})

	t.Run("truncates very long content to 200 chars plus ellipsis", func(t *testing.T) {
		long := strings.Repeat("a", 300)
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleAssistant, Content: long},
		}
		got := summarizeMessages(msgs)
		if !strings.Contains(got, strings.Repeat("a", 200)+"...") {
			t.Errorf("expected content truncated to 200 chars + ellipsis; got:\n%s", got)
		}
		if strings.Contains(got, strings.Repeat("a", 201)) {
			t.Errorf("content was not truncated at 200 chars")
		}
	})
}
