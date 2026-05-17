// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: util.go -- stateless helpers used across the agent loop, session,
// and dashboard. Nothing here holds state or has side effects beyond
// returning a value. Kept separate so agent.go and session.go stay focused
// on orchestration.

import (
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// countTrailingMatches returns how many entries at the end of the slice
// match the target. Used by the loop fingerprint detector to count how
// many times in a row the model has called the same (tool, args) pair.
func countTrailingMatches(hashes []string, target string) int {
	count := 0
	for i := len(hashes) - 1; i >= 0; i-- {
		if hashes[i] != target {
			break
		}
		count++
	}
	return count
}

// extractURL pulls the "url" field from a tool call's JSON arguments.
// Returns "" if the args don't parse or have no url field. Used to
// pre-fetch llms.txt for fetch-style tools so the model gets pre-curated
// site context instead of raw HTML.
func extractURL(argsJSON string) string {
	var args map[string]any
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return ""
	}
	if u, ok := args["url"].(string); ok {
		return u
	}
	return ""
}

// summarizeMessages renders the message history as a human-readable
// transcript for the dashboard's "context" panel. Truncates each message
// body to 200 chars and each tool-call argument blob to 100 chars to
// keep the panel scannable; not intended for replay or debugging beyond
// surface inspection.
func summarizeMessages(msgs []openai.ChatCompletionMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "[%d] role=%s", i, m.Role)
		if len(m.ToolCalls) > 0 {
			fmt.Fprintf(&b, " tool_calls=%d", len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "\n     -> %s(%s)", tc.Function.Name, truncateLog(tc.Function.Arguments, 100))
			}
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, " tool_call_id=%s", m.ToolCallID)
		}
		content := m.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		if content != "" {
			fmt.Fprintf(&b, "\n     %s", content)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// historySize returns the approximate character count of all messages
// (content plus tool-call argument JSON). Used as a cheap proxy for
// "is this conversation getting too big to fit in context?" -- the real
// trim decision is in Session.maybeTrimHistory.
func historySize(msgs []openai.ChatCompletionMessage) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Arguments)
		}
	}
	return n
}

// truncateLog shortens a string for log display, appending "..." when it
// truncates. Counts bytes, not runes -- safe for ASCII log output but
// could split a multi-byte UTF-8 character. Acceptable for human-readable
// log lines, not for anything fed back to a model or wire protocol.
func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
