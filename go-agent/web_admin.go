// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: web_admin.go -- REST inspection and admin endpoints.
//
//   GET    /api/v1/tools           -> handleListTools
//   GET    /api/v1/limits          -> handleLimits
//   GET    /api/v1/sessions        -> handleListSessions
//   GET    /api/v1/sessions/{id}   -> handleSession (GET)
//   DELETE /api/v1/sessions/{id}   -> handleSession (DELETE)
//   GET    /api/v1/approvals       -> handleListApprovals
//   POST   /api/v1/approvals/{id}  -> handleResolveApproval
//   GET    /api/v1/health          -> handleHealth
//
// These are the operator-facing surfaces: discover what's available,
// inspect live state, and unblock HITL approvals. None of them invoke the
// agent loop -- that lives in web_query.go.

import (
	"encoding/json"
	"net/http"
)

func (ws *WebServer) handleListTools(w http.ResponseWriter, r *http.Request) {
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}

	names := agent.ToolNames()
	tools := make([]toolInfo, len(names))
	for i, name := range names {
		tools[i] = toolInfo{Name: name}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools, "count": len(tools)})
}

// handleLimits exposes the server's configured safety caps so that clients
// and the dashboard can discover the current policy. Clients may override
// per-query but will be clamped to these values (client ≤ server).
func (ws *WebServer) handleLimits(w http.ResponseWriter, r *http.Request) {
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}

	// Compute the effective defaults (same as ResolveLimits with nil override).
	defaults := agent.config.ResolveLimits(nil)

	writeJSON(w, http.StatusOK, map[string]any{
		"defaults": defaults,
		"override_channels": map[string]any{
			"rest_api": map[string]string{
				"endpoint": "POST /api/v1/query",
				"field":    "limits",
				"note":     "Request stricter limits in the request body; looser values are clamped.",
			},
			"mcp_meta": map[string]any{
				"endpoint": "tools/call name=agent_query",
				"prefix":   "io.llm-agent/",
				"keys":     []string{"max_rounds", "max_tokens", "timeout", "loop_detect", "max_result", "early_stop"},
				"note":     "Per MCP spec _meta field. Looser values are clamped to server defaults.",
			},
		},
	})
}

// handleListSessions exposes the current session roster so operators can see
// how many independent conversations are live on this agent.
func (ws *WebServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}
	ids := agent.ListSessionIDs()
	infos := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		s := agent.LookupSession(id)
		if s == nil {
			continue
		}
		infos = append(infos, map[string]any{
			"id":           s.ID,
			"history_len":  s.HistoryLen(),
			"idle_seconds": int(s.IdleDuration().Seconds()),
			"is_default":   s.ID == "default",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":    len(infos),
		"sessions": infos,
	})
}

// handleSession handles GET (inspect) and DELETE (clear or drop) for a
// single session. DELETE /api/v1/sessions/default clears (reset history);
// DELETE of any other session id removes it entirely.
func (ws *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}
	id := r.URL.Path[len("/api/v1/sessions/"):]
	if id == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s := agent.LookupSession(id)
		if s == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":           s.ID,
			"history_len":  s.HistoryLen(),
			"idle_seconds": int(s.IdleDuration().Seconds()),
			"is_default":   s.ID == "default",
		})
	case http.MethodDelete:
		s := agent.LookupSession(id)
		if s == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		if id == "default" {
			// The default session can be cleared but not removed -- the CLI
			// needs it to exist.
			s.Clear()
			writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
			return
		}
		if !agent.DeleteSession(id) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "cannot delete"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "GET or DELETE required", http.StatusMethodNotAllowed)
	}
}

// handleListApprovals returns the set of tool calls currently waiting on
// operator approval. Useful for dashboards and "what's stuck?" debugging.
func (ws *WebServer) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending": agent.PendingApprovals(),
	})
}

// handleResolveApproval accepts POST /api/v1/approvals/{id} with a JSON
// body {"approved": bool} and unblocks the corresponding RequestApproval
// call. Returns 404 if the id isn't pending (already resolved or unknown).
func (ws *WebServer) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}
	id := r.URL.Path[len("/api/v1/approvals/"):]
	if id == "" {
		http.Error(w, "approval id required", http.StatusBadRequest)
		return
	}
	var body struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body: expected {\"approved\": bool}", http.StatusBadRequest)
		return
	}
	if !agent.ResolveApproval(id, body.Approved) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "approval not pending (unknown id or already resolved)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "resolved",
		"approved": body.Approved,
	})
}

func (ws *WebServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	agent := ws.getAgent()
	status := "ready"
	model := ""
	toolCount := 0
	queueLen := 0
	queueMax := 0
	if agent == nil {
		status = "starting"
	} else {
		model = agent.config.Model
		toolCount = agent.ToolCount()
	}
	ws.mu.Lock()
	if ws.queue != nil {
		queueLen = ws.queue.QueueLen()
		queueMax = ws.queue.maxSize
	}
	ws.mu.Unlock()
	writeJSON(w, http.StatusOK, healthResponse{
		Status:    status,
		Model:     model,
		ToolCount: toolCount,
		QueueLen:  queueLen,
		QueueMax:  queueMax,
		Version:   version,
	})
}
