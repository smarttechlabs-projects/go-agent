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
	"testing"
)

func TestHandleInitialize(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "initialize",
	}

	resp := handleRequest(req)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("result is not a map")
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("serverInfo missing")
	}
	if info["name"] != "mcp-server-ports" {
		t.Errorf("name = %v, want mcp-server-ports", info["name"])
	}
}

func TestHandleToolsList(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(2),
		Method:  "tools/list",
	}

	resp := handleRequest(req)

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("result is not a map")
	}
	tools, ok := result["tools"].([]mcpToolDef)
	if !ok {
		t.Fatal("tools not found")
	}
	if len(tools) != 2 {
		t.Errorf("got %d tools, want 2", len(tools))
	}

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["list_listening_ports"] {
		t.Error("missing list_listening_ports")
	}
	if !names["check_port"] {
		t.Error("missing check_port")
	}
}

func TestHandleCheckPort_InvalidPort(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name":      "check_port",
		"arguments": map[string]any{"port": float64(0)},
	})

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(3),
		Method:  "tools/call",
		Params:  params,
	}

	resp := handleRequest(req)
	result, ok := resp.Result.(mcpCallResult)
	if !ok {
		t.Fatal("result is not mcpCallResult")
	}
	if !result.IsError {
		t.Error("expected isError=true for invalid port")
	}
}

func TestHandleCheckPort_FreePort(t *testing.T) {
	// Port 19999 is very unlikely to be in use.
	params, _ := json.Marshal(map[string]any{
		"name":      "check_port",
		"arguments": map[string]any{"port": float64(19999)},
	})

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(4),
		Method:  "tools/call",
		Params:  params,
	}

	resp := handleRequest(req)
	result, ok := resp.Result.(mcpCallResult)
	if !ok {
		t.Fatal("result is not mcpCallResult")
	}
	if result.IsError {
		t.Error("unexpected error")
	}
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	text := result.Content[0].Text
	if !searchStr(text, "free") {
		t.Errorf("expected 'free' in result: %s", text)
	}
}

func TestHandleListPorts(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name":      "list_listening_ports",
		"arguments": map[string]any{},
	})

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(5),
		Method:  "tools/call",
		Params:  params,
	}

	resp := handleRequest(req)
	result, ok := resp.Result.(mcpCallResult)
	if !ok {
		t.Fatal("result is not mcpCallResult")
	}
	if result.IsError {
		t.Error("unexpected error")
	}
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	// Should at least have the header and some ports.
	text := result.Content[0].Text
	if !searchStr(text, "PROTO") {
		t.Errorf("missing table header in: %s", text[:min(200, len(text))])
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(6),
		Method:  "unknown/method",
	}

	resp := handleRequest(req)
	if resp.Error == nil {
		t.Error("expected error for unknown method")
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		input    string
		wantAddr string
		wantPort string
	}{
		{"127.0.0.1:8000", "127.0.0.1", "8000"},
		{"*:3000", "*", "3000"},
		{"[::1]:8080", "[::1]", "8080"},
		{"0.0.0.0:22", "0.0.0.0", "22"},
	}

	for _, tt := range tests {
		addr, port := splitHostPort(tt.input)
		if addr != tt.wantAddr || port != tt.wantPort {
			t.Errorf("splitHostPort(%q) = (%q, %q), want (%q, %q)",
				tt.input, addr, port, tt.wantAddr, tt.wantPort)
		}
	}
}

func TestParseSSUsers(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantPID  int
	}{
		{`users:(("lemonade-router",pid=12345,fd=4))`, "lemonade-router", 12345},
		{`users:(("node",pid=999,fd=11))`, "node", 999},
		{``, "", 0},
	}

	for _, tt := range tests {
		name, pid := parseSSUsers(tt.input)
		if name != tt.wantName || pid != tt.wantPID {
			t.Errorf("parseSSUsers(%q) = (%q, %d), want (%q, %d)",
				tt.input, name, pid, tt.wantName, tt.wantPID)
		}
	}
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
