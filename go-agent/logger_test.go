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
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// captureLogger creates a verbose JSON logger writing into the returned buffer.
func captureLogger() (*Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := NewLogger(true)
	l.SetFormat(LogFormatJSON)
	l.out = buf
	return l, buf
}

// Each JSON log line should be valid standalone JSON with expected fields.
func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestLogger_JSON_Info(t *testing.T) {
	l, buf := captureLogger()
	l.Info("starting %s on %s", "agent", "localhost")

	recs := decodeLines(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	if r["level"] != "info" {
		t.Errorf("level = %v, want info", r["level"])
	}
	if r["event"] != "info" {
		t.Errorf("event = %v, want info", r["event"])
	}
	if !strings.Contains(r["msg"].(string), "starting agent on localhost") {
		t.Errorf("msg = %v", r["msg"])
	}
	if _, ok := r["ts"]; !ok {
		t.Error("missing ts field")
	}
}

func TestLogger_JSON_ToolCall(t *testing.T) {
	l, buf := captureLogger()
	l.Tool("check_port", `{"port":8000}`)

	recs := decodeLines(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r["event"] != "tool_call" {
		t.Errorf("event = %v, want tool_call", r["event"])
	}
	if r["tool"] != "check_port" {
		t.Errorf("tool = %v", r["tool"])
	}
	if r["args"] != `{"port":8000}` {
		t.Errorf("args = %v", r["args"])
	}
}

func TestLogger_JSON_LLMMetrics(t *testing.T) {
	l, buf := captureLogger()
	l.LLMMetrics(150*time.Millisecond, LLMUsage{
		PromptTokens:     500,
		CompletionTokens: 200,
		TotalTokens:      700,
	}, 2*time.Second)

	recs := decodeLines(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r["event"] != "llm_metrics" {
		t.Errorf("event = %v", r["event"])
	}
	if r["prompt_tokens"].(float64) != 500 {
		t.Errorf("prompt_tokens = %v, want 500", r["prompt_tokens"])
	}
	if r["ttft_ms"].(float64) != 150 {
		t.Errorf("ttft_ms = %v, want 150", r["ttft_ms"])
	}
}

func TestLogger_JSON_Error_AlwaysEmits(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewLogger(false) // verbose OFF
	l.SetFormat(LogFormatJSON)
	l.out = buf

	l.Info("should be silenced")
	l.Error("should still emit: %v", "boom")

	recs := decodeLines(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (error only)", len(recs))
	}
	if recs[0]["level"] != "error" {
		t.Errorf("level = %v", recs[0]["level"])
	}
}

func TestLogger_JSON_NotVerbose_SilencesInfo(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewLogger(false)
	l.SetFormat(LogFormatJSON)
	l.out = buf

	l.Info("hi")
	l.Warn("careful")
	l.Tool("x", "{}")

	if buf.Len() != 0 {
		t.Errorf("expected empty output when !verbose, got %q", buf.String())
	}
}

func TestLogger_JSON_SetFormatValidates(t *testing.T) {
	l, _ := captureLogger()
	// Invalid format should be ignored (no crash, no silent-switch-to-empty).
	l.SetFormat(LogFormat("garbage"))
	if !l.isJSON() {
		t.Error("SetFormat accepted garbage value")
	}
}

func TestLogger_JSON_ConcurrentSafe(t *testing.T) {
	l, buf := captureLogger()
	done := make(chan struct{})
	// 20 goroutines each emitting 10 events -- no race, all lines valid JSON.
	for i := 0; i < 20; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				l.Info("from goroutine %d iter %d", id, j)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	recs := decodeLines(t, buf)
	if len(recs) != 200 {
		t.Errorf("got %d records, want 200", len(recs))
	}
}
