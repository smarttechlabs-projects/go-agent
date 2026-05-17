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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/codes"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// mcpServer represents a single connected MCP server with its tools.
type mcpServer struct {
	name   string
	client mcpclient.MCPClient
	tools  []mcp.Tool
}

// MCPManager manages multiple MCP server connections and routes tool calls.
type MCPManager struct {
	servers []*mcpServer
	toolMap map[string]*mcpServer // tool name -> owning server
	log     *Logger
}

// NewMCPManager creates an empty manager.
func NewMCPManager(log *Logger) *MCPManager {
	return &MCPManager{
		toolMap: make(map[string]*mcpServer),
		log:     log,
	}
}

// StartServers launches each configured MCP server, initializes the MCP
// session, and discovers available tools.
func (m *MCPManager) StartServers(ctx context.Context, configs []ServerConfig) error {
	for i, sc := range configs {
		if sc.Type != "stdio" {
			return fmt.Errorf("server %d: unsupported type %q (only stdio supported)", i, sc.Type)
		}

		serverName := serverLabel(sc, i)

		ctx, span := startSpan(ctx, "mcp.start_server",
			attrMCPServer.String(serverName),
		)

		fmt.Printf("  Starting MCP server: %s ...\n", serverName)
		m.log.MCPStart(serverName)

		env := buildEnv(sc.Config.Env)

		start := time.Now()
		c, err := mcpclient.NewStdioMCPClient(
			sc.Config.Command,
			env,
			sc.Config.Args...,
		)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return fmt.Errorf("starting server %s: %w", serverName, err)
		}

		initReq := mcp.InitializeRequest{}
		initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initReq.Params.ClientInfo = mcp.Implementation{
			Name:    "llm-agent",
			Version: version,
		}

		if _, err := c.Initialize(ctx, initReq); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return fmt.Errorf("initializing server %s: %w", serverName, err)
		}

		toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return fmt.Errorf("listing tools from %s: %w", serverName, err)
		}
		elapsed := time.Since(start)

		// Filter tools if allowTools is specified.
		tools := toolsResult.Tools
		if len(sc.Config.AllowTools) > 0 {
			allowed := make(map[string]bool)
			for _, name := range sc.Config.AllowTools {
				allowed[name] = true
			}
			var filtered []mcp.Tool
			for _, t := range tools {
				if allowed[t.Name] {
					filtered = append(filtered, t)
				}
			}
			m.log.Info("%s: filtered %d → %d tools", serverName, len(tools), len(filtered))
			tools = filtered
		}

		srv := &mcpServer{
			name:   serverName,
			client: c,
			tools:  tools,
		}
		m.servers = append(m.servers, srv)

		var toolNames []string
		for _, tool := range tools {
			m.toolMap[tool.Name] = srv
			toolNames = append(toolNames, tool.Name)
			fmt.Printf("    + %s\n", tool.Name)
		}

		m.log.MCPTools(serverName, toolNames)
		m.log.Info("%s ready (%d tools, %s)", serverName, len(toolNames), elapsed.Round(time.Millisecond))

		span.SetAttributes(attrMCPToolCount.Int(len(toolNames)))
		span.End()
	}
	return nil
}

// Close shuts down all MCP server connections.
func (m *MCPManager) Close() {
	for _, s := range m.servers {
		if closer, ok := s.client.(interface{ Close() error }); ok {
			closer.Close()
		}
	}
}

// OpenAITools converts all MCP tools to OpenAI function tool definitions.
// Uses raw JSON round-trip to preserve the full schema from each MCP server,
// avoiding field loss from mcp-go's typed ToolInputSchema struct.
func (m *MCPManager) OpenAITools() []openai.Tool {
	var tools []openai.Tool
	for _, s := range m.servers {
		for _, t := range s.tools {
			// Round-trip through JSON to capture the full schema as a map,
			// including fields like $schema that mcp-go's struct drops.
			var params any = t.InputSchema
			raw, err := json.Marshal(t.InputSchema)
			if err == nil {
				var full map[string]any
				if json.Unmarshal(raw, &full) == nil && len(full) > 0 {
					params = full
				}
			}
			tools = append(tools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}
	}
	return tools
}

// ToolNames returns the names of all registered tools.
func (m *MCPManager) ToolNames() []string {
	var names []string
	for name := range m.toolMap {
		names = append(names, name)
	}
	return names
}

// ToolCount returns the total number of tools across all servers.
func (m *MCPManager) ToolCount() int {
	return len(m.toolMap)
}

// CallTool routes a tool call to the correct MCP server and returns the
// text result.
func (m *MCPManager) CallTool(ctx context.Context, name string, argsJSON string) (text string, err error) {
	callStart := time.Now()
	defer func() {
		status := "ok"
		if err != nil {
			status = "error"
		}
		recordToolCall(ctx, name, status, time.Since(callStart))
	}()

	srv, ok := m.toolMap[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	ctx, span := startSpan(ctx, "mcp.call_tool",
		attrToolName.String(name),
		attrMCPServer.String(srv.name),
	)
	defer span.End()

	m.log.Send("MCP", "%s → %s", name, srv.name)

	var args map[string]any
	if argsJSON != "" && argsJSON != "null" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			span.RecordError(err)
			return "", fmt.Errorf("parsing arguments for %s: %w", name, err)
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = name
	callReq.Params.Arguments = args

	start := time.Now()
	result, err := srv.client.CallTool(ctx, callReq)
	elapsed := time.Since(start)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		m.log.Error("MCP call %s failed (%s): %v", name, elapsed.Round(time.Millisecond), err)
		return "", fmt.Errorf("calling %s: %w", name, err)
	}

	text = formatToolResult(result)
	m.log.Recv("MCP", "%s: %d chars (%s)", name, len(text), elapsed.Round(time.Millisecond))

	span.SetAttributes(
		attrToolResultLen.Int(len(text)),
	)

	return text, nil
}

// formatToolResult extracts text from an MCP CallToolResult.
// Handles different content type representations robustly.
func formatToolResult(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return "(no content)"
	}

	var parts []string
	for _, c := range result.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			// JSON round-trip to extract text from unknown content types.
			raw, err := json.Marshal(c)
			if err != nil {
				parts = append(parts, fmt.Sprintf("%v", c))
				continue
			}
			var textItem struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &textItem) == nil && textItem.Text != "" {
				parts = append(parts, textItem.Text)
			} else {
				parts = append(parts, string(raw))
			}
		}
	}

	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, "\n")
}

// buildEnv merges extra env vars with the current process environment.
// Returns nil if extra is empty (inherits parent env).
func buildEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// serverLabel generates a human-readable label for a server config.
func serverLabel(sc ServerConfig, index int) string {
	for i := len(sc.Config.Args) - 1; i >= 0; i-- {
		arg := sc.Config.Args[i]
		if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, ".") && !strings.HasPrefix(arg, "/") {
			return arg
		}
	}
	return fmt.Sprintf("server-%d", index)
}
