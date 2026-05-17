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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamable_RejectsNonPost(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false), limiter: NewIPLimiter(0)}
	req := httptest.NewRequest("GET", "/mcp", nil)
	rec := httptest.NewRecorder()
	ws.handleMCPStreamable(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp code = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != "POST" {
		t.Errorf("missing Allow: POST header")
	}
}

func TestStreamable_InvalidJSON(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false), limiter: NewIPLimiter(0)}
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"broken`))
	rec := httptest.NewRecorder()
	ws.handleMCPStreamable(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON code = %d, want 400", rec.Code)
	}
}

func TestStreamable_InitializeReturnsJSON(t *testing.T) {
	a := testAgent(t)
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false), agent: a, limiter: NewIPLimiter(0)}

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	ws.handleMCPStreamable(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if rec.Header().Get("Mcp-Protocol-Version") == "" {
		t.Error("missing Mcp-Protocol-Version header")
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body not JSON: %v (body=%s)", err, rec.Body.String())
	}
	if resp["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", resp["jsonrpc"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing or wrong type: %v", resp)
	}
	if result["protocolVersion"] == nil {
		t.Error("result missing protocolVersion")
	}
}

func TestStreamable_EchoesSessionIDHeader(t *testing.T) {
	a := testAgent(t)
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false), agent: a, limiter: NewIPLimiter(0)}

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Mcp-Session-Id", "client-abc-123")
	rec := httptest.NewRecorder()
	ws.handleMCPStreamable(rec, req)

	if got := rec.Header().Get("Mcp-Session-Id"); got != "client-abc-123" {
		t.Errorf("Mcp-Session-Id echoed = %q, want %q", got, "client-abc-123")
	}
}

func TestStreamable_NotificationReturns202(t *testing.T) {
	a := testAgent(t)
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false), agent: a, limiter: NewIPLimiter(0)}

	// notifications/initialized is a notification: no id, no response expected.
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	ws.handleMCPStreamable(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("notification code = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification response body = %q, want empty", rec.Body.String())
	}
}
