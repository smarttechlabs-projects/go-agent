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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"time"
)

// Human-in-the-loop tool approval. When a tool name matches a pattern in
// AgentConfig.RequireApproval, the loop pauses before dispatching the call
// and emits an approval_request event. An external actor -- dashboard
// operator, REST client, or any event-bus subscriber -- decides whether to
// let the call proceed.
//
// Approval resolution:
//   - POST /api/v1/approvals/{id} {"approved": true|false} via REST
//   - Agent.ResolveApproval(id, approved) via embedded/direct use
//
// If no decision arrives within ApprovalTimeoutSecs the call is denied
// (fail-safe default), the tool sees an injected "denied" result, and the
// loop continues without executing the tool.

// defaultApprovalTimeoutSecs is the fallback timeout when config doesn't
// set one. 60 seconds matches the rate limiter's default window.
const defaultApprovalTimeoutSecs = 60

// ApprovalRequest describes a pending approval. Exposed via the REST
// sessions/approvals endpoints for operators to inspect.
type ApprovalRequest struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"` // redacted
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// pendingApproval is the internal waiter: a one-shot channel carrying the
// eventual bool decision.
type pendingApproval struct {
	request ApprovalRequest
	ch      chan bool
}

// requiresApproval returns true when the tool name matches any
// RequireApproval pattern. Patterns use path.Match glob syntax:
//
//	"write_*"       matches write_file, write_many, etc.
//	"browser_*"     matches any browser_ tool
//	"execute_shell" exact match
//	"*"             matches everything (require approval on every tool call)
func (a *Agent) requiresApproval(toolName string) bool {
	for _, pat := range a.config.RequireApproval {
		if ok, _ := path.Match(pat, toolName); ok {
			return true
		}
	}
	return false
}

// generateApprovalID returns a short random id. Format: appr-<16 hex chars>.
func generateApprovalID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("appr-%d", time.Now().UnixNano())
	}
	return "appr-" + hex.EncodeToString(b)
}

// RequestApproval blocks until an external resolver calls ResolveApproval,
// the context is cancelled, or the configured approval timeout fires. When
// the tool doesn't match any approval pattern this returns (true, nil)
// immediately -- i.e. the normal fast path.
//
// sessionID is included in the emitted event for traceability; may be empty.
func (a *Agent) RequestApproval(ctx context.Context, toolName, args, sessionID string) (bool, error) {
	if !a.requiresApproval(toolName) {
		return true, nil
	}

	id := generateApprovalID()
	req := ApprovalRequest{
		ID:        id,
		Tool:      toolName,
		Args:      Redact(args),
		SessionID: sessionID,
		CreatedAt: time.Now(),
	}
	waiter := &pendingApproval{
		request: req,
		ch:      make(chan bool, 1),
	}

	a.pendingApprovals.Store(id, waiter)
	defer a.pendingApprovals.Delete(id)

	a.emit(Event{Type: EventApprovalRequest, Data: EventData{
		ApprovalID:   id,
		ToolName:     toolName,
		ToolArgs:     req.Args,
		ApprovalArgs: req.Args,
	}})
	a.log.Warn("tool %s requires approval (id=%s, session=%s)", toolName, id, sessionID)

	timeout := a.config.ApprovalTimeoutSecs
	if timeout <= 0 {
		timeout = defaultApprovalTimeoutSecs
	}
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	select {
	case approved := <-waiter.ch:
		if approved {
			a.log.Info("tool %s approved (id=%s)", toolName, id)
		} else {
			a.log.Warn("tool %s denied (id=%s)", toolName, id)
		}
		return approved, nil
	case <-timer.C:
		a.log.Warn("tool %s approval timed out after %ds; auto-denying", toolName, timeout)
		// Emit a resolution event so watchers can clear their UI.
		a.emit(Event{Type: EventApprovalResolved, Data: EventData{
			ApprovalID:       id,
			ApprovalApproved: false,
			Error:            "approval timeout",
		}})
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// ResolveApproval delivers the decision to whoever is waiting. Returns
// false if the id isn't pending (unknown or already resolved).
func (a *Agent) ResolveApproval(id string, approved bool) bool {
	v, ok := a.pendingApprovals.LoadAndDelete(id)
	if !ok {
		return false
	}
	w := v.(*pendingApproval)
	// Non-blocking send since ch is buffered size 1 and we deleted the
	// entry first, so nobody else can race.
	w.ch <- approved
	a.emit(Event{Type: EventApprovalResolved, Data: EventData{
		ApprovalID:       id,
		ApprovalApproved: approved,
	}})
	return true
}

// PendingApprovals returns snapshots of all currently-waiting approvals,
// for dashboards and operator tooling.
func (a *Agent) PendingApprovals() []ApprovalRequest {
	var out []ApprovalRequest
	a.pendingApprovals.Range(func(_, v any) bool {
		w := v.(*pendingApproval)
		out = append(out, w.request)
		return true
	})
	return out
}

