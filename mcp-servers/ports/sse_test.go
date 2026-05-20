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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "mcp-server-ports",
			"version":   serverVersion,
			"transport": "sse",
			"tools":     len(tools),
		})
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["name"] != "mcp-server-ports" {
		t.Errorf("name = %v", resp["name"])
	}
	if resp["transport"] != "sse" {
		t.Errorf("transport = %v", resp["transport"])
	}
	if int(resp["tools"].(float64)) != 2 {
		t.Errorf("tools = %v, want 2", resp["tools"])
	}
}

func TestSSEMessage_BadMethod(t *testing.T) {
	req := httptest.NewRequest("GET", "/message?sessionId=test", nil)
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestSSEMessage_InvalidSession(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest("POST", "/message?sessionId=nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSSEMessage_InvalidJSON(t *testing.T) {
	// Create a valid session.
	session := &sseSession{id: "test-json", events: make(chan []byte, 64)}
	sessions.Store("test-json", session)
	defer sessions.Delete("test-json")

	req := httptest.NewRequest("POST", "/message?sessionId=test-json", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSSEMessage_ToolCall(t *testing.T) {
	// Create a valid session with buffered channel.
	session := &sseSession{id: "test-tool", events: make(chan []byte, 64)}
	sessions.Store("test-tool", session)
	defer sessions.Delete("test-tool")

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_port","arguments":{"port":19999}}}`
	req := httptest.NewRequest("POST", "/message?sessionId=test-tool", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	// The response should be on the session's event channel.
	select {
	case data := <-session.events:
		var resp jsonRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatal("result is not a map")
		}
		content := result["content"].([]any)
		text := content[0].(map[string]any)["text"].(string)
		if !searchStr(text, "free") {
			t.Errorf("expected 'free' in result for unused port: %s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSSEMessage_ToolsList(t *testing.T) {
	session := &sseSession{id: "test-list", events: make(chan []byte, 64)}
	sessions.Store("test-list", session)
	defer sessions.Delete("test-list")

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	req := httptest.NewRequest("POST", "/message?sessionId=test-list", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	select {
	case data := <-session.events:
		var resp jsonRPCResponse
		json.Unmarshal(data, &resp)
		result := resp.Result.(map[string]any)
		toolList := result["tools"].([]any)
		if len(toolList) != 2 {
			t.Errorf("got %d tools, want 2", len(toolList))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSSEMessage_Notification(t *testing.T) {
	session := &sseSession{id: "test-notif", events: make(chan []byte, 64)}
	sessions.Store("test-notif", session)
	defer sessions.Delete("test-notif")

	// notifications/initialized has no ID, should return 202 with no event.
	body := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	req := httptest.NewRequest("POST", "/message?sessionId=test-notif", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleSSEMessage(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	// No event should be on the channel.
	select {
	case <-session.events:
		t.Error("should not have received an event for a notification")
	case <-time.After(100 * time.Millisecond):
		// Good, no event.
	}
}

func TestSSEConnect_EndpointEvent(t *testing.T) {
	// Use a real HTTP test server so we get proper SSE streaming.
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", handleSSEConnect)
	mux.HandleFunc("/message", handleSSEMessage)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Connect to SSE endpoint.
	resp, err := http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	// Read the first event (endpoint).
	scanner := bufio.NewScanner(resp.Body)
	var eventType, eventData string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		}
		if line == "" && eventType != "" {
			break // End of first event.
		}
	}

	if eventType != "endpoint" {
		t.Errorf("first event type = %q, want 'endpoint'", eventType)
	}
	if !strings.Contains(eventData, "/message?sessionId=") {
		t.Errorf("endpoint data missing session: %s", eventData)
	}

	// Extract session ID and send a tool call.
	sessionID := eventData[strings.Index(eventData, "sessionId=")+10:]

	callBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_port","arguments":{"port":19999}}}`
	postResp, err := http.Post(
		fmt.Sprintf("%s/message?sessionId=%s", ts.URL, sessionID),
		"application/json",
		bytes.NewReader([]byte(callBody)),
	)
	if err != nil {
		t.Fatal(err)
	}
	postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		t.Errorf("POST status = %d, want 202", postResp.StatusCode)
	}

	// Read the response event from SSE stream.
	eventType = ""
	eventData = ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		}
		if line == "" && eventType == "message" {
			break
		}
	}

	if eventType != "message" {
		t.Errorf("response event type = %q, want 'message'", eventType)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal([]byte(eventData), &rpcResp); err != nil {
		t.Fatalf("failed to parse response: %v\ndata: %s", err, eventData)
	}

	result := rpcResp.Result.(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !searchStr(text, "free") {
		t.Errorf("expected 'free' for unused port: %s", text)
	}
}
