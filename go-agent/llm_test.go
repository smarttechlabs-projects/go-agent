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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextPowerOf2(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 4096},
		{1, 4096},
		{4095, 4096},
		{4096, 4096},
		{4097, 8192},
		{16384, 16384},
		{16385, 32768},
		{100000, 131072},
		{500000, 131072}, // capped
	}
	for _, tc := range cases {
		if got := nextPowerOf2(tc.in); got != tc.want {
			t.Errorf("nextPowerOf2(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLLMUsageTokensPerSecond(t *testing.T) {
	t.Run("zero completion tokens returns zero", func(t *testing.T) {
		u := LLMUsage{CompletionTokens: 0}
		if got := u.TokensPerSecond(time.Second); got != 0 {
			t.Errorf("TokensPerSecond = %f, want 0", got)
		}
	})

	t.Run("zero elapsed returns zero", func(t *testing.T) {
		u := LLMUsage{CompletionTokens: 100}
		if got := u.TokensPerSecond(0); got != 0 {
			t.Errorf("TokensPerSecond = %f, want 0", got)
		}
	})

	t.Run("100 tokens over 2 seconds is 50/s", func(t *testing.T) {
		u := LLMUsage{CompletionTokens: 100}
		got := u.TokensPerSecond(2 * time.Second)
		if got != 50 {
			t.Errorf("TokensPerSecond = %f, want 50", got)
		}
	})
}

func TestParseServerErrorContextOverflow(t *testing.T) {
	raw := `{"error":{"type":"exceed_context_size_error","message":"too big","n_prompt_tokens":40000,"n_ctx":32768}}`
	err := parseServerError(raw)
	if err == nil {
		t.Fatal("parseServerError returned nil for context overflow")
	}
	msg := err.Error()
	if !strings.Contains(msg, "40000") || !strings.Contains(msg, "32768") {
		t.Errorf("error message missing token counts: %q", msg)
	}
	if !strings.Contains(msg, "config ctx-size") {
		t.Errorf("error message missing remediation hint: %q", msg)
	}
}

func TestParseServerErrorGenericMessage(t *testing.T) {
	raw := `{"error":{"type":"other","message":"model unavailable"}}`
	err := parseServerError(raw)
	if err == nil {
		t.Fatal("parseServerError returned nil")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("error did not surface server message: %q", err.Error())
	}
}

func TestParseServerErrorPlainText(t *testing.T) {
	raw := `garbage not json at all`
	err := parseServerError(raw)
	if err == nil {
		t.Fatal("parseServerError returned nil")
	}
	if !strings.Contains(err.Error(), "server error") {
		t.Errorf("error prefix missing: %q", err.Error())
	}
}

func TestPeekReadCloserReadsAndLogs(t *testing.T) {
	body := strings.Repeat("data: hello\n\n", 50) // ~650 bytes
	src := io.NopCloser(strings.NewReader(body))
	log := NewLogger(false)

	prc := &peekReadCloser{rc: src, log: log, maxLog: 500}

	got, err := io.ReadAll(prc)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != body {
		t.Errorf("read body mismatch (got %d bytes, want %d)", len(got), len(body))
	}
	if err := prc.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestPeekReadCloserEmptyBody(t *testing.T) {
	src := io.NopCloser(strings.NewReader(""))
	log := NewLogger(false)
	prc := &peekReadCloser{rc: src, log: log, maxLog: 500}

	got, err := io.ReadAll(prc)
	if err != nil {
		t.Fatalf("ReadAll on empty source: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 bytes, got %d", len(got))
	}
	_ = prc.Close()
}

func TestDebugTransportWrapsBaseRoundTripper(t *testing.T) {
	// Stub upstream server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the request body in the response so we can verify body preservation.
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	log := NewLogger(false)
	transport := &debugTransport{base: http.DefaultTransport, log: log}
	client := &http.Client{Transport: transport}

	reqBody := `{"hello":"world"}`
	resp, err := client.Post(server.URL, "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST through debugTransport: %v", err)
	}
	defer resp.Body.Close()

	got, _ := io.ReadAll(resp.Body)
	if string(got) != reqBody {
		t.Errorf("debugTransport corrupted body: got %q, want %q", got, reqBody)
	}
}

func TestDebugTransportLogsErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	log := NewLogger(false)
	transport := &debugTransport{base: http.DefaultTransport, log: log}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("GET through debugTransport: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}

	// The body should still be readable after the transport peeked at it.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body after transport: %v", err)
	}
	if !strings.Contains(string(body), "boom") {
		t.Errorf("body lost after debugTransport: %q", body)
	}
}
