// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: agent.go -- Agent struct, lifecycle, and session table management.
//
// Responsibilities:
//   - Construct the Agent (NewAgent): start MCP servers, snapshot tools,
//     install LLM client, create the default session, kick off the
//     idle-session reaper.
//   - Own the sessions map: GetOrCreateSession, LookupSession,
//     DeleteSession, SessionCount, ListSessionIDs.
//   - Provide thin Query / QueryWithLimits / QueryDetailed wrappers that
//     delegate to the default session for CLI use.
//   - Startup sanity check via checkContextSize against the Lemonade
//     llamacpp backend.
//
// What lives elsewhere:
//   - The agent loop itself (LLM <-> tool dispatch <-> termination) is
//     in session.go. Sessions own conversation state and run the loop.
//   - Termination heuristics (isTerminalError, isToolFailure) live in
//     policy.go so the policy is discoverable as a unit.
//   - Stateless helpers (truncateLog, summarizeMessages, historySize,
//     extractURL, countTrailingMatches) live in util.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// Defaults for agent loop limits. Overridden by AgentConfig fields.
const (
	defaultMaxToolRounds   = 10
	defaultMaxResultLen    = 16000  // ~4K tokens, safe for 32K context window
	defaultMaxTokenBudget  = 100000 // Hard cap on cumulative tokens per query
	defaultTimeoutSeconds  = 300    // 5 minute wall-clock limit per query
	defaultLoopFingerprint = 3      // Abort if same (tool, args) repeats 3 times
	defaultMaxHistoryChars = 80000  // ~20K tokens; trim older turns past this cap
	defaultKeepRecentTurns = 4      // Keep the last 4 round-trips (user+assistant pairs) when trimming
	defaultToolTimeoutSecs = 60     // Per-tool wall-clock timeout
)

// Session lifecycle constants.
const (
	sessionIdleTimeout  = 30 * time.Minute // idle sessions older than this are reaped
	sessionReapInterval = 60 * time.Second // how often the reaper scans
)

// Agent orchestrates the LLM and MCP tool servers. Sessions own the actual
// conversation state and run the loop; the agent is the shared container
// for LLM client, MCP manager, tool registry, logger, and session table.
//
// Thread-safety:
//   - sessions map is guarded by mu.
//   - Concurrent QueryDetailed calls on *different* sessions run in parallel.
//   - Concurrent calls on the *same* session are serialized by Session.runMu.
type Agent struct {
	llm     *LLMClient
	mcp     *MCPManager
	log     *Logger
	events  *EventBus
	llmsTxt *LLMsTxtCache
	tools   []openai.Tool
	config  *AgentConfig

	// callTool dispatches a tool call. Indirected through a field so tests
	// can plug in an in-memory implementation without spawning stdio MCP
	// subprocesses. Defaults to Agent.mcp.CallTool.
	callTool func(ctx context.Context, name, argsJSON string) (string, error)

	mu             sync.Mutex
	sessions       map[string]*Session
	defaultSession *Session

	// Pending HITL tool-approval requests. Keyed by approval id; values
	// are *pendingApproval. See approval.go.
	pendingApprovals sync.Map
}

// NewAgent creates an agent, starts MCP servers, and discovers tools.
func NewAgent(ctx context.Context, cfg *AgentConfig, log *Logger, stream bool, events *EventBus) (*Agent, error) {
	llm := NewLLMClient(cfg.EndpointURL, cfg.Model, cfg.ApiKey, cfg.Provider, log, stream)

	mcpMgr := NewMCPManager(log)
	if err := mcpMgr.StartServers(ctx, cfg.Servers); err != nil {
		return nil, fmt.Errorf("starting MCP servers: %w", err)
	}

	llmsTxtMode := cfg.LLMsTxt
	if llmsTxtMode == "" {
		llmsTxtMode = LLMsTxtAuto
	}
	agent := &Agent{
		llm:      llm,
		mcp:      mcpMgr,
		log:      log,
		events:   events,
		llmsTxt:  NewLLMsTxtCache(llmsTxtMode, log),
		tools:    mcpMgr.OpenAITools(),
		config:   cfg,
		sessions: make(map[string]*Session),
	}
	agent.callTool = mcpMgr.CallTool

	// CLI uses an implicit "default" session. REST/MCP callers create their own.
	agent.defaultSession = newSession("default", agent)
	agent.sessions["default"] = agent.defaultSession

	// Check context size at startup and warn if too small.
	agent.checkContextSize(cfg.EndpointURL)

	// Start the session reaper. It evicts non-default sessions that have been
	// idle longer than sessionIdleTimeout, preventing unbounded memory growth
	// on long-running servers.
	go agent.reapIdleSessions(ctx)

	return agent, nil
}

// reapIdleSessions is a background goroutine that periodically evicts idle
// non-default sessions. Runs until ctx is cancelled.
func (a *Agent) reapIdleSessions(ctx context.Context) {
	ticker := time.NewTicker(sessionReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			for id, s := range a.sessions {
				if id == "default" {
					continue
				}
				if s.IdleDuration() > sessionIdleTimeout {
					delete(a.sessions, id)
					a.log.Info("reaped idle session %s (idle %s)", id, s.IdleDuration().Round(time.Second))
				}
			}
			a.mu.Unlock()
		}
	}
}

// Close shuts down all MCP server connections.
func (a *Agent) Close() {
	a.mcp.Close()
}

// ToolCount returns the number of available tools.
func (a *Agent) ToolCount() int {
	return a.mcp.ToolCount()
}

// ToolNames returns the names of all available tools.
func (a *Agent) ToolNames() []string {
	return a.mcp.ToolNames()
}

// lookupToolSchema returns the declared InputSchema for the named tool, or
// nil if not found. Used by argument validation before dispatch.
func (a *Agent) lookupToolSchema(name string) any {
	for _, t := range a.tools {
		if t.Function != nil && t.Function.Name == name {
			return t.Function.Parameters
		}
	}
	return nil
}

// ClearHistory resets the *default* session -- used by the CLI /clear command.
// For a specific session, call Session.Clear() instead.
func (a *Agent) ClearHistory() {
	a.defaultSession.Clear()
}

// DefaultSession returns the CLI's implicit session.
func (a *Agent) DefaultSession() *Session {
	return a.defaultSession
}

// GetOrCreateSession returns the existing session with the given id, or
// creates a new one if id is empty or not found. An empty id generates a
// fresh random id, returned on the Session struct.
func (a *Agent) GetOrCreateSession(id string) *Session {
	if id == "" {
		return a.newSessionLocked("")
	}
	a.mu.Lock()
	s, ok := a.sessions[id]
	a.mu.Unlock()
	if ok {
		s.touch()
		return s
	}
	return a.newSessionLocked(id)
}

// LookupSession returns the session with the given id, or nil if not found.
// Unlike GetOrCreateSession this does not create on miss.
func (a *Agent) LookupSession(id string) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[id]; ok {
		s.touch()
		return s
	}
	return nil
}

// DeleteSession removes a session by id. The default session cannot be deleted.
func (a *Agent) DeleteSession(id string) bool {
	if id == "default" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sessions[id]; !ok {
		return false
	}
	delete(a.sessions, id)
	return true
}

// SessionCount returns the number of active sessions (including default).
func (a *Agent) SessionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions)
}

// ListSessionIDs returns the ids of all active sessions, sorted lexically.
// Used for debugging and dashboards.
func (a *Agent) ListSessionIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		ids = append(ids, id)
	}
	return ids
}

// newSessionLocked creates a session under the agent mutex and registers it.
func (a *Agent) newSessionLocked(id string) *Session {
	s := newSession(id, a)
	a.mu.Lock()
	// If the caller passed an explicit id and we lost a race, prefer the winner.
	if existing, ok := a.sessions[s.ID]; ok {
		a.mu.Unlock()
		existing.touch()
		return existing
	}
	a.sessions[s.ID] = s
	a.mu.Unlock()
	return s
}

// Query runs the default session with server-default limits. Primarily used
// by the CLI. For per-session or per-query-limit use, go through
// GetOrCreateSession first.
func (a *Agent) Query(ctx context.Context, input string) (string, error) {
	return a.defaultSession.Query(ctx, input)
}

// QueryWithLimits runs the default session with a per-query override.
func (a *Agent) QueryWithLimits(ctx context.Context, input string, override *AgentLimits) (string, error) {
	return a.defaultSession.QueryWithLimits(ctx, input, override)
}

// QueryDetailed runs the default session and returns a structured result.
func (a *Agent) QueryDetailed(ctx context.Context, input string, override *AgentLimits) *QueryResult {
	return a.defaultSession.QueryDetailed(ctx, input, override)
}

// checkContextSize queries the Lemonade llamacpp backend for the actual context
// window size and warns if it's too small for tool use. One-shot at startup;
// runtime context changes (server restart with different --ctx-size) are not
// detected here.
func (a *Agent) checkContextSize(endpointURL string) {
	baseURL := strings.TrimSuffix(endpointURL, "/v1")
	baseURL = strings.TrimSuffix(baseURL, "/")

	healthResp, err := http.Get(baseURL + "/v1/health")
	if err != nil {
		return
	}
	defer healthResp.Body.Close()

	var health struct {
		AllModelsLoaded []struct {
			BackendURL string `json:"backend_url"`
			ModelName  string `json:"model_name"`
		} `json:"all_models_loaded"`
	}
	if json.NewDecoder(healthResp.Body).Decode(&health) != nil || len(health.AllModelsLoaded) == 0 {
		return
	}

	backendURL := health.AllModelsLoaded[0].BackendURL
	if backendURL == "" {
		return
	}

	slotsResp, err := http.Get(backendURL + "/../slots")
	if err != nil {
		backendBase := strings.TrimSuffix(backendURL, "/v1")
		slotsResp, err = http.Get(backendBase + "/slots")
		if err != nil {
			return
		}
	}
	defer slotsResp.Body.Close()

	var slots []struct {
		NCtx int `json:"n_ctx"`
	}
	if json.NewDecoder(slotsResp.Body).Decode(&slots) != nil || len(slots) == 0 {
		return
	}

	nCtx := slots[0].NCtx
	a.log.Info("context: %d tokens", nCtx)

	minNeeded := 8192
	if nCtx < minNeeded {
		a.log.Warn("context size %d is too small for tool use (need >= %d)", nCtx, minNeeded)
		a.log.Warn("restart server with: lemonade-server serve --ctx-size 32768")
	}
}

// emit sends an event to the web dashboard (no-op if no event bus).
func (a *Agent) emit(evt Event) {
	if a.events != nil {
		a.events.Emit(evt)
	}
}
