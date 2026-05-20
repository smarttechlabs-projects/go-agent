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
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgent_RequiresApproval_PatternMatching(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"write_*", "execute_shell", "browser_*"}

	cases := []struct {
		tool string
		want bool
	}{
		{"write_file", true},
		{"write_many", true},
		{"execute_shell", true},
		{"browser_navigate", true},
		{"browser_click", true},
		{"fetch", false},
		{"list_ports", false},
		{"read_file", false},
	}
	for _, tc := range cases {
		got := a.requiresApproval(tc.tool)
		if got != tc.want {
			t.Errorf("requiresApproval(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func TestAgent_RequiresApproval_EmptyConfigNeverMatches(t *testing.T) {
	a := testAgent(t)
	// No RequireApproval configured.
	if a.requiresApproval("write_file") {
		t.Error("empty RequireApproval should match nothing")
	}
}

func TestAgent_RequestApproval_FastPathWhenNotRequired(t *testing.T) {
	a := testAgent(t)
	// RequireApproval is empty -> every tool passes immediately.
	approved, err := a.RequestApproval(context.Background(), "fetch", "{}", "s1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !approved {
		t.Error("approved = false, want true (fast path)")
	}
}

func TestAgent_ResolveApproval_UnblocksWaiter(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"write_*"}

	// Start RequestApproval in a goroutine and resolve from the main thread.
	done := make(chan struct {
		approved bool
		err      error
	}, 1)
	go func() {
		approved, err := a.RequestApproval(context.Background(), "write_file", `{"path":"/tmp/x"}`, "s1")
		done <- struct {
			approved bool
			err      error
		}{approved, err}
	}()

	// Give the goroutine a moment to register.
	var id string
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		pending := a.PendingApprovals()
		if len(pending) == 1 {
			id = pending[0].ID
			break
		}
	}
	if id == "" {
		t.Fatal("approval never became pending")
	}

	if !a.ResolveApproval(id, true) {
		t.Fatal("ResolveApproval returned false for known id")
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("RequestApproval err = %v", result.err)
	}
	if !result.approved {
		t.Error("approved = false, want true")
	}
	if len(a.PendingApprovals()) != 0 {
		t.Error("pending approval not cleaned up")
	}
}

func TestAgent_ResolveApproval_Deny(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"*"}

	done := make(chan bool, 1)
	go func() {
		approved, _ := a.RequestApproval(context.Background(), "any_tool", "{}", "s")
		done <- approved
	}()

	var id string
	for i := 0; i < 50 && id == ""; i++ {
		time.Sleep(10 * time.Millisecond)
		if p := a.PendingApprovals(); len(p) == 1 {
			id = p[0].ID
		}
	}
	a.ResolveApproval(id, false)

	if approved := <-done; approved {
		t.Error("approved = true, want false (denied)")
	}
}

func TestAgent_ResolveApproval_UnknownID(t *testing.T) {
	a := testAgent(t)
	if a.ResolveApproval("nonexistent", true) {
		t.Error("ResolveApproval returned true for unknown id")
	}
}

func TestAgent_RequestApproval_Timeout(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"*"}
	a.config.ApprovalTimeoutSecs = 1 // 1 second for fast test

	start := time.Now()
	approved, err := a.RequestApproval(context.Background(), "dangerous_tool", "{}", "s")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved {
		t.Error("approved = true, want false (auto-denied on timeout)")
	}
	if elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want ~1s", elapsed)
	}
	// Pending map must be cleaned up.
	if n := len(a.PendingApprovals()); n != 0 {
		t.Errorf("pending = %d, want 0 (timeout should clean up)", n)
	}
}

func TestAgent_RequestApproval_ContextCancel(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"*"}
	a.config.ApprovalTimeoutSecs = 30 // long enough that timeout won't fire

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	approved, err := a.RequestApproval(ctx, "anything", "{}", "s")
	if err == nil {
		t.Error("expected error from cancelled context")
	}
	if approved {
		t.Error("approved = true despite cancellation")
	}
}

func TestAgent_RequestApproval_ArgsAreRedacted(t *testing.T) {
	a := testAgent(t)
	a.config.RequireApproval = []string{"*"}

	done := make(chan struct{})
	go func() {
		_, _ = a.RequestApproval(context.Background(), "x", `{"api_key":"sk-proj-supersecretlongenough0123456789"}`, "s")
		close(done)
	}()

	var req ApprovalRequest
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		if p := a.PendingApprovals(); len(p) == 1 {
			req = p[0]
			break
		}
	}
	if req.ID == "" {
		t.Fatal("pending request never appeared")
	}
	if strings.Contains(req.Args, "sk-proj-supersecretlongenough0123456789") {
		t.Errorf("args not redacted: %q", req.Args)
	}
	if !strings.Contains(req.Args, "REDACTED") {
		t.Errorf("args missing redaction placeholder: %q", req.Args)
	}

	a.ResolveApproval(req.ID, false)
	<-done
}

func TestAgent_ResolveApproval_Concurrent(t *testing.T) {
	// Multiple goroutines trying to resolve the same id -- only one wins.
	a := testAgent(t)
	a.config.RequireApproval = []string{"*"}

	var req ApprovalRequest
	done := make(chan bool, 1)
	go func() {
		approved, _ := a.RequestApproval(context.Background(), "x", "{}", "s")
		done <- approved
	}()

	for i := 0; i < 50 && req.ID == ""; i++ {
		time.Sleep(10 * time.Millisecond)
		if p := a.PendingApprovals(); len(p) == 1 {
			req = p[0]
		}
	}

	// 10 concurrent resolvers, only one should return true.
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.ResolveApproval(req.ID, true) {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	<-done

	if got := winners.Load(); got != 1 {
		t.Errorf("winners = %d, want exactly 1", got)
	}
}
