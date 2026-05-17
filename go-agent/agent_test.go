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
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// newSyntheticAgent constructs an Agent without spawning real MCP servers.
// Tool registry and session table are wired manually. callTool is a stub.
func newSyntheticAgent(t *testing.T) *Agent {
	t.Helper()
	log := NewLogger(false)
	cfg := &AgentConfig{Model: "test-model", SystemPrompt: "test"}

	mgr := NewMCPManager(log)
	// Populate one fake server with two tools so OpenAITools/ToolCount/ToolNames have data.
	srv := &mcpServer{name: "fake", tools: nil}
	mgr.servers = []*mcpServer{srv}
	mgr.toolMap["check_port"] = srv
	mgr.toolMap["fetch"] = srv

	a := &Agent{
		log:      log,
		mcp:      mgr,
		config:   cfg,
		sessions: make(map[string]*Session),
		tools: []openai.Tool{
			{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "check_port",
				Description: "Check whether a port is in use.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"port": map[string]any{"type": "integer"}}},
			}},
			{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "fetch",
				Description: "Fetch a URL.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}},
			}},
		},
	}
	a.defaultSession = newSession("default", a)
	a.sessions["default"] = a.defaultSession
	return a
}

func TestAgentToolCountAndNames(t *testing.T) {
	a := newSyntheticAgent(t)

	if got := a.ToolCount(); got != 2 {
		t.Errorf("ToolCount = %d, want 2", got)
	}

	names := a.ToolNames()
	sort.Strings(names)
	want := []string{"check_port", "fetch"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("ToolNames = %v, want %v", names, want)
	}
}

func TestAgentLookupToolSchema(t *testing.T) {
	a := newSyntheticAgent(t)

	if schema := a.lookupToolSchema("check_port"); schema == nil {
		t.Error("lookupToolSchema(check_port) returned nil")
	}
	if schema := a.lookupToolSchema("does-not-exist"); schema != nil {
		t.Errorf("lookupToolSchema(missing) = %v, want nil", schema)
	}
}

func TestAgentSessionTable(t *testing.T) {
	a := newSyntheticAgent(t)

	if got := a.SessionCount(); got != 1 {
		t.Errorf("initial SessionCount = %d, want 1 (default)", got)
	}

	// GetOrCreateSession with empty id mints a new one.
	s1 := a.GetOrCreateSession("")
	if s1.ID == "" || s1.ID == "default" {
		t.Errorf("GetOrCreateSession(\"\") gave id %q, want fresh random", s1.ID)
	}

	// Re-fetching by id returns the same instance.
	if got := a.GetOrCreateSession(s1.ID); got != s1 {
		t.Errorf("GetOrCreateSession(%q) returned different instance", s1.ID)
	}

	// LookupSession on miss returns nil.
	if got := a.LookupSession("ghost"); got != nil {
		t.Errorf("LookupSession(missing) = %v, want nil", got)
	}

	// DeleteSession on default is rejected.
	if a.DeleteSession("default") {
		t.Error("DeleteSession(default) returned true, want false")
	}

	// DeleteSession on a real one removes it.
	if !a.DeleteSession(s1.ID) {
		t.Errorf("DeleteSession(%q) returned false, want true", s1.ID)
	}
	if a.LookupSession(s1.ID) != nil {
		t.Error("session still present after delete")
	}

	// ListSessionIDs always includes default.
	ids := a.ListSessionIDs()
	found := false
	for _, id := range ids {
		if id == "default" {
			found = true
		}
	}
	if !found {
		t.Errorf("ListSessionIDs = %v, missing default", ids)
	}
}

func TestAgentDefaultSession(t *testing.T) {
	a := newSyntheticAgent(t)
	if a.DefaultSession() == nil {
		t.Fatal("DefaultSession() returned nil")
	}
	if a.DefaultSession().ID != "default" {
		t.Errorf("DefaultSession ID = %q, want default", a.DefaultSession().ID)
	}
}

func TestCheckContextSizeSuccess(t *testing.T) {
	// Stub backend server that exposes /v1/health and /slots.
	mux := http.NewServeMux()
	backend := httptest.NewServer(mux)
	defer backend.Close()
	mux.HandleFunc("/slots", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"n_ctx": 16384}]`))
	})

	// Lemonade-facing server that points checkContextSize at the backend's /v1.
	fronting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The backend_url must end with /v1 so checkContextSize hits /../slots → /slots.
		_, _ = w.Write([]byte(`{"all_models_loaded":[{"backend_url":"` + backend.URL + `/v1","model_name":"m"}]}`))
	}))
	defer fronting.Close()

	a := newSyntheticAgent(t)
	// Should not panic and should not produce an error; logs are captured at /dev/null verbosity.
	a.checkContextSize(fronting.URL + "/v1")
}

func TestCheckContextSizeMissingHealthIsSilent(t *testing.T) {
	a := newSyntheticAgent(t)
	// Point at a closed port — http.Get returns an error and checkContextSize returns silently.
	a.checkContextSize("http://127.0.0.1:1") // port 1 reserved, no listener
}

func TestCheckContextSizeNoLoadedModelsIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"all_models_loaded": []}`))
	}))
	defer srv.Close()

	a := newSyntheticAgent(t)
	a.checkContextSize(srv.URL + "/v1") // empty list → silent return
}

func TestCheckContextSizeBadJSONIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	a := newSyntheticAgent(t)
	a.checkContextSize(srv.URL + "/v1") // unparseable → silent return
}

func TestAgentClearHistory(t *testing.T) {
	a := newSyntheticAgent(t)
	// Seed history on the default session.
	a.defaultSession.history = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "system"},
		{Role: openai.ChatMessageRoleUser, Content: "hi"},
	}
	if got := a.defaultSession.HistoryLen(); got == 0 {
		t.Fatal("test setup failed: history not seeded")
	}

	a.ClearHistory()

	// After Clear the user message is gone; the system prompt is retained.
	for _, m := range a.defaultSession.history {
		if m.Role == openai.ChatMessageRoleUser {
			t.Errorf("Clear left a user message: %q", m.Content)
		}
	}
}

func TestAgentLookupToolSchemaCaseSensitive(t *testing.T) {
	// Sanity: tool names are matched verbatim; the agent doesn't downcase.
	a := newSyntheticAgent(t)
	if a.lookupToolSchema(strings.ToUpper("check_port")) != nil {
		t.Error("lookupToolSchema should be case-sensitive but matched uppercase")
	}
}
