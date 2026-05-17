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
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

// LogFormat controls how log lines are rendered.
type LogFormat string

const (
	LogFormatText LogFormat = "text" // human-friendly colorized output
	LogFormatJSON LogFormat = "json" // one JSON object per line, for aggregation
)

// Logger provides verbose output for tracing agent data flow. Renders in
// colorized text (default, suitable for dev/CLI) or structured JSON
// (suitable for log aggregation systems like Loki, CloudWatch, Elastic).
type Logger struct {
	verbose bool
	format  LogFormat
	out     io.Writer
	mu      sync.Mutex // serializes writes in JSON mode
}

// NewLogger creates a logger. When verbose is false, most output is
// suppressed (Error still emits). Format defaults to text.
func NewLogger(verbose bool) *Logger {
	return &Logger{verbose: verbose, format: LogFormatText, out: os.Stdout}
}

// SetFormat switches output format. Call once at startup; safe to mix.
func (l *Logger) SetFormat(f LogFormat) {
	if f == LogFormatJSON || f == LogFormatText {
		l.format = f
	}
}

// Color palette for different message categories (text mode only).
var (
	clrSend   = color.New(color.FgCyan, color.Bold)
	clrRecv   = color.New(color.FgGreen, color.Bold)
	clrErr    = color.New(color.FgRed, color.Bold)
	clrWarn   = color.New(color.FgYellow)
	clrDim    = color.New(color.Faint)
	clrLabel  = color.New(color.FgMagenta, color.Bold)
	clrInfo   = color.New(color.FgWhite, color.Bold)
	clrData   = color.New(color.FgHiBlack)
	clrRound  = color.New(color.FgHiCyan)
	clrTool   = color.New(color.FgHiYellow, color.Bold)
)

// logJSON emits one JSON record per line. The "fields" map carries
// structured context so log aggregators can filter by tool, round,
// component, etc. without regex-parsing human text.
func (l *Logger) logJSON(level, event string, fields map[string]any, msg string) {
	if l.out == nil {
		l.out = os.Stdout
	}
	rec := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"event": event,
	}
	if msg != "" {
		rec["msg"] = msg
	}
	for k, v := range fields {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out.Write(b)
	l.out.Write([]byte{'\n'})
}

// isJSON returns true iff the logger is currently in JSON mode.
func (l *Logger) isJSON() bool { return l.format == LogFormatJSON }

// Send logs an outgoing message (agent → external system).
func (l *Logger) Send(component, format string, args ...any) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "send", map[string]any{"component": component}, fmt.Sprintf(format, args...))
		return
	}
	clrSend.Printf("  → %-4s ", component)
	fmt.Printf(format+"\n", args...)
}

// Recv logs an incoming message (external system → agent).
func (l *Logger) Recv(component, format string, args ...any) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "recv", map[string]any{"component": component}, fmt.Sprintf(format, args...))
		return
	}
	clrRecv.Printf("  ← %-4s ", component)
	fmt.Printf(format+"\n", args...)
}

// Tool logs a tool call event.
func (l *Logger) Tool(name, args string) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "tool_call", map[string]any{
			"tool": name,
			"args": truncateLog(args, 500),
		}, "")
		return
	}
	clrTool.Printf("  ⚡ TOOL ")
	fmt.Printf("%s", name)
	if args != "" && args != "{}" && args != "null" {
		clrData.Printf(" %s", truncateLog(args, 120))
	}
	fmt.Println()
}

// ToolResult logs the result of a tool call.
func (l *Logger) ToolResult(name string, size int, elapsed time.Duration) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "tool_result", map[string]any{
			"tool":       name,
			"size_bytes": size,
			"elapsed_ms": elapsed.Milliseconds(),
		}, "")
		return
	}
	clrRecv.Printf("  ← TOOL ")
	fmt.Printf("%s ", name)
	clrDim.Printf("(%d chars, %s)\n", size, elapsed.Round(time.Millisecond))
}

// Round logs the start of a new agent loop round.
func (l *Logger) Round(round, msgCount, contextChars int) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "round", map[string]any{
			"round":         round,
			"message_count": msgCount,
			"context_chars": contextChars,
		}, "")
		return
	}
	clrRound.Printf("  ── round %d ", round)
	clrDim.Printf("[%d messages, ~%s]\n", msgCount, formatBytes(contextChars))
}

// LLMRequest logs details about an outgoing LLM request.
func (l *Logger) LLMRequest(model string, msgCount, toolCount, contextChars int) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "llm_request", map[string]any{
			"model":         model,
			"message_count": msgCount,
			"tool_count":    toolCount,
			"context_chars": contextChars,
		}, "")
		return
	}
	clrSend.Printf("  → LLM  ")
	fmt.Printf("POST /chat/completions ")
	clrDim.Printf("model=%s msgs=%d tools=%d ctx~%s\n", model, msgCount, toolCount, formatBytes(contextChars))
}

// LLMResponse logs details about an LLM response.
func (l *Logger) LLMResponse(finishReason string, hasToolCalls bool, toolCallCount int, contentLen int, elapsed time.Duration) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "llm_response", map[string]any{
			"finish_reason":   finishReason,
			"has_tool_calls":  hasToolCalls,
			"tool_call_count": toolCallCount,
			"content_len":     contentLen,
			"elapsed_ms":      elapsed.Milliseconds(),
		}, "")
		return
	}
	clrRecv.Printf("  ← LLM  ")
	if hasToolCalls {
		clrWarn.Printf("%d tool_call(s) ", toolCallCount)
	} else {
		fmt.Printf("text (%d chars) ", contentLen)
	}
	clrDim.Printf("finish=%s %s\n", finishReason, elapsed.Round(time.Millisecond))
}

// LLMMetrics logs token usage and throughput metrics.
func (l *Logger) LLMMetrics(ttft time.Duration, usage LLMUsage, elapsed time.Duration) {
	if !l.verbose {
		return
	}
	tps := usage.TokensPerSecond(elapsed)
	if l.isJSON() {
		l.logJSON("info", "llm_metrics", map[string]any{
			"ttft_ms":           ttft.Milliseconds(),
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.TotalTokens,
			"tokens_per_sec":    tps,
			"elapsed_ms":        elapsed.Milliseconds(),
		}, "")
		return
	}
	clrDim.Printf("    ╰─ ")
	fmt.Printf("ttft=%s ", ttft.Round(time.Millisecond))
	fmt.Printf("prompt=%d ", usage.PromptTokens)
	fmt.Printf("completion=%d ", usage.CompletionTokens)
	fmt.Printf("total=%d ", usage.TotalTokens)
	if tps > 0 {
		fmt.Printf("tok/s=%.1f", tps)
	}
	fmt.Println()
}

// MCPStart logs an MCP server startup event.
func (l *Logger) MCPStart(name string) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "mcp_start", map[string]any{"server": name}, "")
		return
	}
	clrLabel.Printf("  ● MCP  ")
	fmt.Printf("starting %s\n", name)
}

// MCPTools logs discovered tools for a server.
func (l *Logger) MCPTools(serverName string, tools []string) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "mcp_tools", map[string]any{
			"server": serverName,
			"tools":  tools,
		}, "")
		return
	}
	clrRecv.Printf("  ← MCP  ")
	fmt.Printf("%s: %s\n", serverName, strings.Join(tools, ", "))
}

// Info logs a general informational message.
func (l *Logger) Info(format string, args ...any) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("info", "info", nil, fmt.Sprintf(format, args...))
		return
	}
	clrInfo.Printf("  ℹ ")
	fmt.Printf(format+"\n", args...)
}

// Warn logs a warning.
func (l *Logger) Warn(format string, args ...any) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		l.logJSON("warn", "warn", nil, fmt.Sprintf(format, args...))
		return
	}
	clrWarn.Printf("  ⚠ ")
	fmt.Printf(format+"\n", args...)
}

// Error logs an error. Always emits (both formats), including when not verbose.
func (l *Logger) Error(format string, args ...any) {
	if l.isJSON() {
		l.logJSON("error", "error", nil, fmt.Sprintf(format, args...))
		return
	}
	clrErr.Printf("  ✗ ")
	fmt.Printf(format+"\n", args...)
}

// Data logs raw data content (only in verbose mode, dimmed).
func (l *Logger) Data(label, content string, maxLen int) {
	if !l.verbose {
		return
	}
	if l.isJSON() {
		preview := content
		if len(preview) > maxLen {
			preview = preview[:maxLen]
		}
		l.logJSON("debug", "data", map[string]any{
			"label":   label,
			"content": preview,
		}, "")
		return
	}
	clrDim.Printf("    %s: ", label)
	if len(content) > maxLen {
		clrData.Printf("%s...\n", content[:maxLen])
	} else {
		clrData.Printf("%s\n", content)
	}
}

// Verbose returns true if verbose mode is enabled.
func (l *Logger) Verbose() bool {
	return l.verbose
}

// formatBytes formats a character count as a human-readable size.
func formatBytes(chars int) string {
	if chars < 1024 {
		return fmt.Sprintf("%d", chars)
	}
	return fmt.Sprintf("%.1fK", float64(chars)/1024)
}
