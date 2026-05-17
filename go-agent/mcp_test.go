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

	"github.com/mark3labs/mcp-go/mcp"
)

func TestFormatToolResultTextContent(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: "hello"},
			mcp.TextContent{Type: "text", Text: "world"},
		},
	}
	got := formatToolResult(res)
	if got != "hello\nworld" {
		t.Errorf("formatToolResult = %q, want \"hello\\nworld\"", got)
	}
}

func TestFormatToolResultPointerTextContent(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Type: "text", Text: "via pointer"},
		},
	}
	if got := formatToolResult(res); got != "via pointer" {
		t.Errorf("formatToolResult = %q, want \"via pointer\"", got)
	}
}

func TestFormatToolResultNilOrEmpty(t *testing.T) {
	if got := formatToolResult(nil); got != "(no content)" {
		t.Errorf("formatToolResult(nil) = %q, want \"(no content)\"", got)
	}
	if got := formatToolResult(&mcp.CallToolResult{}); got != "(no content)" {
		t.Errorf("formatToolResult(empty) = %q, want \"(no content)\"", got)
	}
}

func TestBuildEnv(t *testing.T) {
	t.Run("nil extra inherits parent env", func(t *testing.T) {
		if got := buildEnv(nil); got != nil {
			t.Errorf("buildEnv(nil) = %v, want nil (inherit)", got)
		}
		if got := buildEnv(map[string]string{}); got != nil {
			t.Errorf("buildEnv({}) = %v, want nil (inherit)", got)
		}
	})

	t.Run("extras appended to parent env", func(t *testing.T) {
		got := buildEnv(map[string]string{"FOO": "bar"})
		if got == nil {
			t.Fatal("buildEnv returned nil for non-empty extras")
		}
		// Must include the new key as KEY=VALUE.
		found := false
		for _, kv := range got {
			if kv == "FOO=bar" {
				found = true
				break
			}
		}
		if !found {
			t.Error("buildEnv did not append FOO=bar")
		}
		// Must also include at least one parent-env entry (PATH almost always set).
		hasParent := false
		for _, kv := range got {
			if strings.HasPrefix(kv, "PATH=") {
				hasParent = true
				break
			}
		}
		if !hasParent {
			t.Error("buildEnv did not inherit parent env (no PATH)")
		}
	})
}

func TestServerLabel(t *testing.T) {
	cases := []struct {
		name string
		sc   ServerConfig
		idx  int
		want string
	}{
		{
			"positional arg used as label",
			ServerConfig{Config: ServerDetail{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", ".", "/tmp"}}},
			0,
			"/tmp", // wait — actually starts with /, so skipped; the last non-flag/non-path is @modelcontextprotocol/server-filesystem
		},
	}
	// The implementation walks args from the end and returns the first arg that
	// does NOT start with "-", ".", or "/". For the args above, "/tmp" starts
	// with "/", "." starts with ".", "@modelcontextprotocol/server-filesystem"
	// starts with "@" → picked. Override expected value:
	cases[0].want = "@modelcontextprotocol/server-filesystem"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverLabel(tc.sc, tc.idx); got != tc.want {
				t.Errorf("serverLabel = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("all flags/paths falls back to server-N", func(t *testing.T) {
		sc := ServerConfig{Config: ServerDetail{Command: "npx", Args: []string{"--headless", "./foo", "/bar"}}}
		if got := serverLabel(sc, 3); got != "server-3" {
			t.Errorf("serverLabel = %q, want \"server-3\"", got)
		}
	})

	t.Run("empty args falls back to server-N", func(t *testing.T) {
		sc := ServerConfig{Config: ServerDetail{Command: "uvx"}}
		if got := serverLabel(sc, 5); got != "server-5" {
			t.Errorf("serverLabel = %q, want \"server-5\"", got)
		}
	})

	t.Run("simple last positional arg wins", func(t *testing.T) {
		sc := ServerConfig{Config: ServerDetail{Command: "uvx", Args: []string{"mcp-server-fetch"}}}
		if got := serverLabel(sc, 0); got != "mcp-server-fetch" {
			t.Errorf("serverLabel = %q, want \"mcp-server-fetch\"", got)
		}
	})
}

func TestMCPManagerToolCountAndNames(t *testing.T) {
	log := NewLogger(false)
	mgr := NewMCPManager(log)

	srvA := &mcpServer{name: "a"}
	srvB := &mcpServer{name: "b"}
	mgr.servers = []*mcpServer{srvA, srvB}
	mgr.toolMap["t1"] = srvA
	mgr.toolMap["t2"] = srvA
	mgr.toolMap["t3"] = srvB

	if got := mgr.ToolCount(); got != 3 {
		t.Errorf("ToolCount = %d, want 3", got)
	}
	names := mgr.ToolNames()
	if len(names) != 3 {
		t.Errorf("ToolNames len = %d, want 3", len(names))
	}
}

func TestMCPManagerOpenAIToolsRoundTripsSchema(t *testing.T) {
	log := NewLogger(false)
	mgr := NewMCPManager(log)

	mgr.servers = []*mcpServer{{
		name: "fake",
		tools: []mcp.Tool{
			{
				Name:        "check_port",
				Description: "Check if a TCP port is in use.",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"port": map[string]any{"type": "integer"},
					},
					Required: []string{"port"},
				},
			},
		},
	}}

	got := mgr.OpenAITools()
	if len(got) != 1 {
		t.Fatalf("OpenAITools returned %d tools, want 1", len(got))
	}
	tool := got[0]
	if tool.Type != "function" {
		t.Errorf("tool.Type = %q, want \"function\"", tool.Type)
	}
	if tool.Function == nil || tool.Function.Name != "check_port" {
		t.Errorf("tool function name = %v, want check_port", tool.Function)
	}
	params, ok := tool.Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("Parameters not a map[string]any, got %T", tool.Function.Parameters)
	}
	if params["type"] != "object" {
		t.Errorf("schema.type = %v, want object", params["type"])
	}
	if _, ok := params["properties"]; !ok {
		t.Error("schema missing properties key after round-trip")
	}
}
