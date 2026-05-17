// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: web_mcp.go -- MCP server gateway. Lets external MCP clients
// (CrewAI, AutoGen, LangGraph, etc.) reach this agent's tool surface and
// the `agent_query` meta-tool that runs the full LLM loop.
//
// Two transports are exposed, both terminating in handleMCPRequest:
//
//   GET  /mcp/sse        -> handleMCPSSEConnect    (legacy SSE)
//   POST /mcp/message    -> handleMCPSSEMessage    (paired with /mcp/sse)
//   POST /mcp            -> handleMCPStreamable    (2025-03 stateless)
//
// Per-client mcpSession objects track SSE channel buffers and idle time.
// startSessionReaper sweeps zombies every 30s.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type mcpSession struct {
	id       string
	events   chan []byte
	lastSeen time.Time
	mu       sync.Mutex
}

func (s *mcpSession) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

func (s *mcpSession) idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSeen)
}

const (
	mcpSessionTimeout = 5 * time.Minute  // Kill sessions idle longer than this.
	mcpPingInterval   = 30 * time.Second // Send SSE pings to detect dead connections.
)

var (
	mcpSessions   sync.Map
	mcpSessionCtr sync.Mutex
	mcpSessionN   int
	mcpReaperOnce sync.Once
)

// startSessionReaper runs a background goroutine that cleans up zombie sessions.
func startSessionReaper(log *Logger) {
	mcpReaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				mcpSessions.Range(func(key, value any) bool {
					session := value.(*mcpSession)
					if session.idle() > mcpSessionTimeout {
						log.Warn("MCP SSE: reaping zombie session %s (idle %s)", session.id, session.idle().Round(time.Second))
						mcpSessions.Delete(key)
						close(session.events)
					}
					return true
				})
			}
		}()
	})
}

// handleMCPStreamable implements the Streamable HTTP transport per the
// 2025-03 MCP spec. Stateless subset:
//
//   - POST /mcp: JSON-RPC request body -> JSON-RPC response body.
//     Accepts Mcp-Session-Id header (echoed back), Mcp-Protocol-Version
//     header (matched against our supported version).
//   - GET /mcp: not supported in stateless mode -> 405.
//
// This does NOT stream server-initiated notifications; clients that need
// those use the legacy /mcp/sse endpoint. For the typical request/response
// MCP tool call pattern, Streamable HTTP is simpler (no session setup,
// single round trip) and matches what modern MCP clients default to.
func (ws *WebServer) handleMCPStreamable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Spec says GET can open an SSE stream for server-initiated events;
		// we only support the stateless POST subset here.
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required (stateless streamable-http)", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
		return
	}

	ws.log.Info("MCP /mcp: method=%s id=%v", req.Method, req.ID)

	resp := ws.handleMCPRequest(req.JSONRPC, req.ID, req.Method, req.Params)

	// Echo session id header if the client sent one (spec compliance).
	if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
		w.Header().Set("Mcp-Session-Id", sid)
	}
	w.Header().Set("Mcp-Protocol-Version", "2024-11-05")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Notifications have no response -- spec says 202 Accepted with no body.
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (ws *WebServer) handleMCPSSEConnect(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		ws.log.Error("MCP SSE: streaming not supported by response writer")
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Start the zombie reaper on first connection.
	startSessionReaper(ws.log)

	mcpSessionCtr.Lock()
	mcpSessionN++
	id := fmt.Sprintf("mcp-%d", mcpSessionN)
	mcpSessionCtr.Unlock()

	session := &mcpSession{
		id:       id,
		events:   make(chan []byte, 64),
		lastSeen: time.Now(),
	}
	mcpSessions.Store(id, session)

	ws.log.Info("MCP SSE: new session %s from %s", id, r.RemoteAddr)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	messageURL := fmt.Sprintf("http://%s/mcp/message?sessionId=%s", r.Host, id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messageURL)
	flusher.Flush()

	ws.log.Info("MCP SSE: [%s] sent endpoint event → %s", id, messageURL)

	// Count active sessions for debug.
	activeCount := 0
	mcpSessions.Range(func(_, _ any) bool { activeCount++; return true })
	ws.log.Info("MCP SSE: %d active session(s)", activeCount)

	// Ping ticker to detect dead connections.
	pingTicker := time.NewTicker(mcpPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case data, ok := <-session.events:
			if !ok {
				ws.log.Info("MCP SSE: [%s] session channel closed (reaped)", id)
				return
			}
			session.touch()
			_, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			if err != nil {
				ws.log.Warn("MCP SSE: [%s] write error (client gone?): %v", id, err)
				mcpSessions.Delete(id)
				return
			}
			flusher.Flush()
			ws.log.Info("MCP SSE: [%s] sent message (%d bytes)", id, len(data))

		case <-pingTicker.C:
			// SSE comment line as keepalive -- if write fails, client is gone.
			_, err := fmt.Fprintf(w, ": ping %s\n\n", time.Now().Format(time.RFC3339))
			if err != nil {
				ws.log.Warn("MCP SSE: [%s] ping failed (zombie detected): %v", id, err)
				mcpSessions.Delete(id)
				return
			}
			flusher.Flush()

		case <-r.Context().Done():
			mcpSessions.Delete(id)
			ws.log.Info("MCP SSE: [%s] client disconnected (context done)", id)
			return
		}
	}
}

func (ws *WebServer) handleMCPSSEMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		ws.log.Warn("MCP SSE: /message called without sessionId")
		http.Error(w, "missing sessionId query parameter", http.StatusBadRequest)
		return
	}

	sessionVal, ok := mcpSessions.Load(sessionID)
	if !ok {
		ws.log.Warn("MCP SSE: /message for unknown session %s", sessionID)
		http.Error(w, "invalid or expired session", http.StatusBadRequest)
		return
	}
	session := sessionVal.(*mcpSession)
	session.touch()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		ws.log.Error("MCP SSE: [%s] failed to read body: %v", sessionID, err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	ws.log.Info("MCP SSE: [%s] received message (%d bytes)", sessionID, len(body))

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		ws.log.Error("MCP SSE: [%s] invalid JSON-RPC: %v", sessionID, err)
		http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
		return
	}

	ws.log.Info("MCP SSE: [%s] method=%s id=%v", sessionID, req.Method, req.ID)

	resp := ws.handleMCPRequest(req.JSONRPC, req.ID, req.Method, req.Params)

	// Notifications (no ID) don't get a response.
	if resp == nil {
		ws.log.Info("MCP SSE: [%s] notification (no response)", sessionID)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	out, _ := json.Marshal(resp)
	select {
	case session.events <- out:
		ws.log.Info("MCP SSE: [%s] queued response (%d bytes)", sessionID, len(out))
		w.WriteHeader(http.StatusAccepted)
	default:
		ws.log.Error("MCP SSE: [%s] event buffer full, dropping response", sessionID)
		http.Error(w, "session buffer full", http.StatusServiceUnavailable)
	}
}

// handleMCPRequest is the JSON-RPC dispatcher shared by both transports
// (legacy SSE and stateless Streamable HTTP). Returns nil for notifications.
func (ws *WebServer) handleMCPRequest(jsonrpc string, id any, method string, params json.RawMessage) any {
	switch method {
	case "initialize":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{
					"name":    "llm-agent",
					"version": version,
				},
			},
		}

	case "notifications/initialized":
		return nil // No response for notifications.

	case "tools/list":
		agent := ws.getAgent()
		if agent == nil {
			return mcpError(id, -32603, "agent not ready")
		}

		// Build MCP tool definitions from all registered tools.
		var toolDefs []map[string]any

		// Add the agent_query meta-tool -- runs the full LLM agent loop.
		toolDefs = append(toolDefs, map[string]any{
			"name":        "agent_query",
			"description": "Send a natural language query to the LLM agent. The agent will reason, call tools as needed, and return a complete answer. Use this when you need the agent to think and act autonomously rather than calling individual tools yourself.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "A natural language question or instruction for the agent",
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
		})

		// Add all MCP tools from connected servers.
		for _, t := range agent.tools {
			if t.Function == nil {
				continue
			}
			toolDefs = append(toolDefs, map[string]any{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"inputSchema": t.Function.Parameters,
			})
		}

		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]any{"tools": toolDefs},
		}

	case "tools/call":
		agent := ws.getAgent()
		if agent == nil {
			return mcpError(id, -32603, "agent not ready")
		}

		var callParams struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Meta      map[string]any `json:"_meta,omitempty"`
		}
		if err := json.Unmarshal(params, &callParams); err != nil {
			return mcpError(id, -32602, "invalid params")
		}

		// Handle the agent_query meta-tool -- runs the full agent loop.
		if callParams.Name == "agent_query" {
			query, _ := callParams.Arguments["query"].(string)
			if query == "" {
				return mcpError(id, -32602, "agent_query requires a 'query' argument")
			}

			// Extract per-query overrides from the MCP _meta field (io.llm-agent/* prefix).
			limits := ParseMetaLimits(callParams.Meta)
			if limits != nil {
				ws.log.Info("MCP SSE: agent_query received _meta limits: %s", limits.Describe())
			}

			// External MCP clients can thread a conversation across multiple
			// agent_query calls by passing io.llm-agent/session_id in _meta.
			// Missing/empty → fresh session per call (stateless).
			sessionID := ""
			if callParams.Meta != nil {
				if v, ok := callParams.Meta["io.llm-agent/session_id"].(string); ok {
					sessionID = v
				}
			}
			session := agent.GetOrCreateSession(sessionID)

			ws.log.Info("MCP SSE: agent_query session=%s: %s", session.ID, truncateLog(query, 100))
			qr := session.QueryDetailed(r_context_bg(), query, limits)

			// Build an MCP tools/call result. We always attach a _meta block
			// with the structured termination info (io.llm-agent/* keys) so
			// external MCP clients can branch on the reason without parsing
			// the content text. Per MCP spec, tool execution failures use
			// isError=true and put the message in the content field.
			mcpResult := map[string]any{
				"_meta": qr.MetaKeys(),
			}
			if qr.Error != "" {
				mcpResult["isError"] = true
				mcpResult["content"] = []map[string]any{
					{"type": "text", "text": fmt.Sprintf("Agent terminated (%s): %s", qr.TerminationReason, qr.Error)},
				}
			} else {
				mcpResult["content"] = []map[string]any{
					{"type": "text", "text": qr.Answer},
				}
			}
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  mcpResult,
			}
		}

		// Regular tool call -- route to the MCP server that owns this tool.
		argsJSON, _ := json.Marshal(callParams.Arguments)
		result, err := agent.mcp.CallTool(r_context_bg(), callParams.Name, string(argsJSON))
		if err != nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Error: %v", err)}},
					"isError": true,
				},
			}
		}

		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": result}},
			},
		}

	default:
		return mcpError(id, -32601, fmt.Sprintf("method not found: %s", method))
	}
}

func mcpError(id any, code int, msg string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	}
}
