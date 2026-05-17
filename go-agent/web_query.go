// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: web_query.go -- REST query endpoints.
//
//   POST /api/v1/query        -> handleQuery        (synchronous)
//   POST /api/v1/query/stream -> handleQueryStream  (SSE event stream)
//   POST /api/v1/query/async  -> handleQueryAsync   (job queue, returns id)
//   GET  /api/v1/jobs/{id}    -> handleGetJob       (poll/wait async result)
//
// All four read a queryRequest body, route to the named (or fresh) session
// via Agent.GetOrCreateSession, and emit a queryResponse on the wire.
// Termination diagnostics are derived from QueryResult so callers can
// branch on the reason code instead of regexing error strings.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (ws *WebServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	agent := ws.getAgent()
	if agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, queryResponse{Error: "agent not ready"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, queryResponse{Error: "failed to read body"})
		return
	}

	var req queryRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Query == "" {
		writeJSON(w, http.StatusBadRequest, queryResponse{Error: "invalid request: need {\"query\": \"...\"}"})
		return
	}

	// Route to the named session (or create a fresh one if session_id is empty).
	// Two REST clients with different session_ids run concurrently without
	// corrupting each other's history; two clients with the same session_id
	// are serialized per-session.
	session := agent.GetOrCreateSession(req.SessionID)
	result := session.QueryDetailed(r.Context(), req.Query, req.Limits)
	resp := queryResponseFromResult(result)

	// Map termination reason to HTTP status. Success and early-stop (which still
	// produced an annotated answer) are 200 OK; hard-error paths are 500 except
	// TermTerminalError which maps to 502 (upstream LLM refused us).
	status := http.StatusOK
	if result.Error != "" {
		status = http.StatusInternalServerError
		if result.TerminationReason == TermTerminalError {
			status = http.StatusBadGateway
		}
		if result.TerminationReason == TermTimeout {
			status = http.StatusGatewayTimeout
		}
		if result.TerminationReason == TermUserCancel {
			status = 499 // nginx-style "client closed request"
		}
	}
	writeJSON(w, status, resp)
}

func (ws *WebServer) handleQueryStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	agent := ws.getAgent()
	if agent == nil {
		http.Error(w, "agent not ready", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req queryRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Query == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Subscribe to events before starting the query so we capture everything.
	ch, unsub := ws.bus.Subscribe()
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	session := agent.GetOrCreateSession(req.SessionID)

	// Run the query in a goroutine; stream events as they arrive.
	doneCh := make(chan queryResponse, 1)
	go func() {
		result := session.QueryDetailed(r.Context(), req.Query, req.Limits)
		doneCh <- queryResponseFromResult(result)
	}()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", evt.JSON())
			flusher.Flush()

			// If this is the final event, also send the done marker.
			if evt.Type == EventQueryEnd || evt.Type == EventError {
				resp := <-doneCh
				final, _ := json.Marshal(resp)
				fmt.Fprintf(w, "event: done\ndata: %s\n\n", string(final))
				flusher.Flush()
				return
			}
		case resp := <-doneCh:
			// Query finished without a query_end event (error or cancellation).
			final, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", string(final))
			flusher.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (ws *WebServer) handleQueryAsync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	ws.mu.Lock()
	queue := ws.queue
	ws.mu.Unlock()

	if queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var req queryRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: need {\"query\": \"...\"}"})
		return
	}

	job, err := queue.Submit(req.Query, req.Limits, req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, job)
}

func (ws *WebServer) handleGetJob(w http.ResponseWriter, r *http.Request) {
	ws.mu.Lock()
	queue := ws.queue
	ws.mu.Unlock()

	if queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent not ready"})
		return
	}

	// Extract job ID from path: /api/v1/jobs/{id}
	id := r.URL.Path[len("/api/v1/jobs/"):]
	if id == "" {
		// List queue status.
		writeJSON(w, http.StatusOK, map[string]any{
			"queue_length": queue.QueueLen(),
			"max_size":     queue.maxSize,
		})
		return
	}

	job := queue.Get(id)
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	// If ?wait=true, block until the job completes.
	if r.URL.Query().Get("wait") == "true" {
		if err := job.Wait(r.Context()); err != nil {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "timeout waiting for job"})
			return
		}
	}

	writeJSON(w, http.StatusOK, job)
}
