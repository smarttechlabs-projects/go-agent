// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: web.go -- WebServer scaffolding: lifecycle, route table, dashboard
// + SSE event bus, plus the two shared response helpers (writeJSON,
// r_context_bg). Handlers live in:
//
//   - web_query.go   REST /api/v1/query{,/stream,/async} + /jobs
//   - web_admin.go   REST /api/v1/{tools,limits,sessions,approvals,health}
//   - web_mcp.go     MCP gateway (legacy SSE + Streamable HTTP transports)
//   - web_types.go   request/response DTOs

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
)

//go:embed static/index.html
var staticFS embed.FS

// WebServer holds the HTTP server state including the agent reference for API calls.
type WebServer struct {
	bus     *EventBus
	agent   *Agent
	queue   *JobQueue
	log     *Logger
	limiter *IPLimiter
	mu      sync.Mutex
}

// Default rate limit: 60 requests per minute per IP on mutating endpoints.
// Read-only and SSE endpoints bypass the limiter. Configurable via
// WebServerOptions for tests and deployment tuning.
const defaultRatePerMinute = 60

// WebServerOptions tunes the HTTP server. Currently just the rate limit
// and an optional Prometheus metrics handler.
type WebServerOptions struct {
	RatePerMinute  int          // 0 = use default, negative = disabled
	MetricsHandler http.Handler // optional; mounted at /api/v1/metrics when non-nil
}

// StartWebServer launches the dashboard + API HTTP server in the background.
// The agent can be nil initially and set later via SetAgent.
func StartWebServer(addr string, bus *EventBus, log *Logger) (*WebServer, string, error) {
	return StartWebServerWithOptions(addr, bus, log, WebServerOptions{})
}

func StartWebServerWithOptions(addr string, bus *EventBus, log *Logger, opts WebServerOptions) (*WebServer, string, error) {
	rate := opts.RatePerMinute
	if rate == 0 {
		rate = defaultRatePerMinute
	}
	if rate < 0 {
		rate = 0 // disabled
	}
	ws := &WebServer{bus: bus, log: log, limiter: NewIPLimiter(rate)}
	mux := http.NewServeMux()

	// --- Dashboard ---

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "dashboard not found", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(data); err != nil {
			ws.log.Warn("dashboard write failed: %v", err)
		}
	})

	// SSE endpoint for real-time dashboard events.
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", 500)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		flusher.Flush()

		ch, unsub := bus.Subscribe()
		defer unsub()

		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", evt.JSON())
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	// --- REST API (handlers live in web_query.go and web_admin.go) ---

	// POST /api/v1/query -- synchronous query, returns full response as JSON.
	// Rate limited: LLM + tool execution is expensive, limit per-IP to
	// prevent a single client from monopolizing the agent.
	mux.HandleFunc("/api/v1/query", ws.limiter.Wrap(ws.handleQuery))

	// POST /api/v1/query/stream -- query with SSE event stream during execution.
	mux.HandleFunc("/api/v1/query/stream", ws.limiter.Wrap(ws.handleQueryStream))

	// GET /api/v1/tools -- list available tools.
	mux.HandleFunc("/api/v1/tools", func(w http.ResponseWriter, r *http.Request) {
		ws.handleListTools(w, r)
	})

	// POST /api/v1/query/async -- submit to queue, return job ID immediately.
	mux.HandleFunc("/api/v1/query/async", ws.limiter.Wrap(ws.handleQueryAsync))

	// GET /api/v1/jobs/ -- get job status/result by ID.
	mux.HandleFunc("/api/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		ws.handleGetJob(w, r)
	})

	// GET /api/v1/health -- agent health check.
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		ws.handleHealth(w, r)
	})

	// GET /api/v1/limits -- show the server's configured default limits.
	// These are the maximum caps; clients may request stricter values per-query
	// via the `limits` body field or the MCP `_meta` field (io.llm-agent/* keys).
	mux.HandleFunc("/api/v1/limits", func(w http.ResponseWriter, r *http.Request) {
		ws.handleLimits(w, r)
	})

	// /api/v1/sessions -- list; /api/v1/sessions/{id} -- GET/DELETE a session.
	// Callers pass session_id on query requests to keep a conversation; this
	// endpoint lets them inspect state and tear sessions down explicitly.
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		ws.handleListSessions(w, r)
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		ws.handleSession(w, r)
	})

	// GET /api/v1/metrics -- Prometheus exposition format. Active only when
	// the caller supplied a handler (i.e. the metrics pipeline registered a
	// Prometheus reader). Not rate-limited: scrapers may poll frequently.
	if opts.MetricsHandler != nil {
		mux.Handle("/api/v1/metrics", opts.MetricsHandler)
	}

	// HITL approval surface. GET lists pending; POST .../{id} resolves.
	// Not rate-limited: a blocked query is waiting on this; we want
	// operators to be able to unblock without running into the rate cap.
	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		ws.handleListApprovals(w, r)
	})
	mux.HandleFunc("/api/v1/approvals/", func(w http.ResponseWriter, r *http.Request) {
		ws.handleResolveApproval(w, r)
	})

	// --- MCP gateway (handlers live in web_mcp.go) ---

	// GET /mcp/sse -- legacy MCP SSE event stream.
	mux.HandleFunc("/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		ws.handleMCPSSEConnect(w, r)
	})

	// POST /mcp/message -- MCP JSON-RPC messages from external clients.
	// Also rate-limited: agent_query via MCP has the same cost profile as
	// /api/v1/query, so the same per-IP cap applies.
	mux.HandleFunc("/mcp/message", ws.limiter.Wrap(ws.handleMCPSSEMessage))

	// POST /mcp -- Streamable HTTP transport (2025-03 MCP spec). Stateless
	// mode: one JSON-RPC request per HTTP call, single JSON response, no
	// long-lived SSE. Newer MCP clients prefer this over the legacy SSE
	// transport. Uses the same handleMCPRequest backend, just different framing.
	mux.HandleFunc("/mcp", ws.limiter.Wrap(ws.handleMCPStreamable))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("web server listen: %w", err)
	}

	url := fmt.Sprintf("http://%s", ln.Addr().String())

	go func() {
		_ = http.Serve(ln, mux)
	}()

	return ws, url, nil
}

// SetAgent sets the agent reference and starts the job queue.
func (ws *WebServer) SetAgent(a *Agent) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.agent = a
	ws.queue = NewJobQueue(a, 20, ws.log)
}

func (ws *WebServer) getAgent() *Agent {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.agent
}

// --- Shared helpers ---

// writeJSON serialises v as JSON with the given status code and the open
// CORS header. Errors during encoding are unrecoverable from a handler
// (response started, headers committed); we log nothing here on purpose
// to keep the helper trivially inlineable.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// r_context_bg returns a background context for MCP tool calls from
// external clients. The stateless Streamable HTTP and SSE transports
// don't carry a per-call cancellation context the way the REST handlers
// do, so we hand the agent a background context and rely on per-query
// safety limits (timeout, max-rounds, etc.) to bound execution.
func r_context_bg() context.Context {
	return context.Background()
}
