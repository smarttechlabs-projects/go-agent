// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
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

func TestHealthEndpoint_NoAgent(t *testing.T) {
	bus := NewEventBus()
	ws := &WebServer{bus: bus, log: NewLogger(false)}

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	ws.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp healthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Status != "starting" {
		t.Errorf("status = %q, want 'starting'", resp.Status)
	}
	if resp.Version != version {
		t.Errorf("version = %q, want %q", resp.Version, version)
	}
}

func TestQueryEndpoint_NoAgent(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false)}

	body := strings.NewReader(`{"query":"test"}`)
	req := httptest.NewRequest("POST", "/api/v1/query", body)
	w := httptest.NewRecorder()

	ws.handleQuery(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestQueryEndpoint_BadMethod(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false)}

	req := httptest.NewRequest("GET", "/api/v1/query", nil)
	w := httptest.NewRecorder()

	ws.handleQuery(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestQueryEndpoint_EmptyBody(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false)}
	// Set a non-nil agent so we get past the agent check.
	ws.agent = &Agent{config: &AgentConfig{}}

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/query", body)
	w := httptest.NewRecorder()

	ws.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAsyncEndpoint_NoQueue(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false)}

	body := strings.NewReader(`{"query":"test"}`)
	req := httptest.NewRequest("POST", "/api/v1/query/async", body)
	w := httptest.NewRecorder()

	ws.handleQueryAsync(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	ws := &WebServer{bus: NewEventBus(), log: NewLogger(false)}
	ws.queue = &JobQueue{maxSize: 10}

	req := httptest.NewRequest("GET", "/api/v1/jobs/nonexistent", nil)
	w := httptest.NewRecorder()

	ws.handleGetJob(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
