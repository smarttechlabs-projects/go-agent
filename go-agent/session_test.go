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
	"strings"
	"sync"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// testAgent builds a minimal Agent suitable for exercising session plumbing
// (no LLM, no MCP -- anything that would actually run the loop is out of
// scope here; we just verify session creation, isolation, and reaping).
func testAgent(t *testing.T) *Agent {
	t.Helper()
	cfg := &AgentConfig{
		SystemPrompt:    "test system prompt",
		MaxToolRounds:   10,
		MaxTokenBudget:  100000,
		TimeoutSeconds:  300,
		LoopFingerprint: 3,
		MaxResultLen:    16000,
	}
	a := &Agent{
		config:   cfg,
		sessions: make(map[string]*Session),
		log:      NewLogger(false),
	}
	a.defaultSession = newSession("default", a)
	a.sessions["default"] = a.defaultSession
	return a
}

func TestSession_GenerateID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := generateSessionID()
		if !strings.HasPrefix(id, "sess-") {
			t.Fatalf("id %q missing sess- prefix", id)
		}
		if seen[id] {
			t.Fatalf("collision after %d: %s", i, id)
		}
		seen[id] = true
	}
}

func TestAgent_GetOrCreateSession_NewID(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("")
	if s == nil {
		t.Fatal("GetOrCreateSession returned nil")
	}
	if s.ID == "" {
		t.Error("auto-generated session has empty id")
	}
	if s.ID == "default" {
		t.Error("auto-generated session collided with default")
	}
	if a.LookupSession(s.ID) != s {
		t.Error("session not registered in agent map")
	}
}

func TestAgent_GetOrCreateSession_Idempotent(t *testing.T) {
	a := testAgent(t)
	s1 := a.GetOrCreateSession("my-session")
	s2 := a.GetOrCreateSession("my-session")
	if s1 != s2 {
		t.Error("GetOrCreateSession not idempotent for same id")
	}
}

func TestAgent_DefaultSessionSurvivesClear(t *testing.T) {
	a := testAgent(t)
	def := a.DefaultSession()
	a.ClearHistory()
	if a.DefaultSession() != def {
		t.Error("default session was replaced by ClearHistory")
	}
	if def.HistoryLen() != 1 {
		t.Errorf("after clear, default history len = %d, want 1 (system prompt only)", def.HistoryLen())
	}
}

func TestAgent_CannotDeleteDefault(t *testing.T) {
	a := testAgent(t)
	if a.DeleteSession("default") {
		t.Error("DeleteSession returned true for default session")
	}
	if a.LookupSession("default") == nil {
		t.Error("default session was deleted anyway")
	}
}

func TestAgent_DeleteSession_RemovesFromMap(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("temp")
	if !a.DeleteSession(s.ID) {
		t.Fatal("DeleteSession returned false for existing session")
	}
	if a.LookupSession(s.ID) != nil {
		t.Error("session still present after DeleteSession")
	}
}

func TestSession_Clear_ResetsHistory(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("s1")
	s.history = append(s.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: "hello",
	})
	if s.HistoryLen() != 2 {
		t.Fatalf("pre-clear history len = %d, want 2", s.HistoryLen())
	}
	s.Clear()
	if s.HistoryLen() != 1 {
		t.Errorf("post-clear history len = %d, want 1", s.HistoryLen())
	}
}

// TestSession_IsolationBetweenSessions verifies the core correctness
// property: mutating session A's history does not affect session B.
func TestSession_IsolationBetweenSessions(t *testing.T) {
	a := testAgent(t)
	sA := a.GetOrCreateSession("a")
	sB := a.GetOrCreateSession("b")

	sA.history = append(sA.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: "question for A",
	})

	if sB.HistoryLen() != 1 {
		t.Errorf("session B history was mutated by session A: len=%d, want 1", sB.HistoryLen())
	}
	if sA.HistoryLen() != 2 {
		t.Errorf("session A history len = %d, want 2", sA.HistoryLen())
	}
}

func TestSession_IdleDuration(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("timed")
	// touch() fires in newSession; should be near zero now.
	if d := s.IdleDuration(); d > 100*time.Millisecond {
		t.Errorf("fresh session idle duration = %v, want <100ms", d)
	}
	time.Sleep(50 * time.Millisecond)
	if d := s.IdleDuration(); d < 40*time.Millisecond {
		t.Errorf("idle duration = %v, want >=40ms", d)
	}
	s.touch()
	if d := s.IdleDuration(); d > 100*time.Millisecond {
		t.Errorf("after touch idle duration = %v, want <100ms", d)
	}
}

// TestSession_ConcurrentCreationDistinctIDs stresses the creation path:
// many goroutines calling GetOrCreateSession("") must each get a unique id.
func TestSession_ConcurrentCreationDistinctIDs(t *testing.T) {
	a := testAgent(t)
	const n = 200
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := a.GetOrCreateSession("")
			ids <- s.ID
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate session id: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("got %d unique ids, want %d", len(seen), n)
	}
}

// TestSession_ConcurrentSameIDAllReturnSame verifies that many concurrent
// GetOrCreateSession calls with the SAME id all receive the same instance
// (no duplicate creation under racing).
func TestSession_ConcurrentSameIDAllReturnSame(t *testing.T) {
	a := testAgent(t)
	const n = 200
	sessions := make(chan *Session, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions <- a.GetOrCreateSession("shared")
		}()
	}
	wg.Wait()
	close(sessions)

	var first *Session
	for s := range sessions {
		if first == nil {
			first = s
			continue
		}
		if s != first {
			t.Error("GetOrCreateSession returned different instances for same id")
		}
	}
}

// TestSession_TrimHistory_DropsOldestTurns verifies the history trim logic
// preserves the system prompt and the last N user turns.
func TestSession_TrimHistory_DropsOldestTurns(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("trim")

	// Build a history: system + 10 user/assistant round-trips of ~1KB each.
	big := strings.Repeat("x", 1000)
	for i := 0; i < 10; i++ {
		s.history = append(s.history,
			openai.ChatCompletionMessage{Role: "user", Content: "q" + big},
			openai.ChatCompletionMessage{Role: "assistant", Content: "a" + big},
		)
	}
	preSize := historySize(s.history)
	if preSize < 15000 {
		t.Fatalf("test setup: history too small: %d", preSize)
	}

	s.runMu.Lock()
	s.trimHistory(5000, 2) // cap 5K chars, keep last 2 user turns
	s.runMu.Unlock()

	// System prompt + last 2 user turns + their assistant replies = 5 messages.
	if s.HistoryLen() != 5 {
		t.Errorf("post-trim history len = %d, want 5", s.HistoryLen())
	}
	// System prompt preserved.
	if s.history[0].Role != "system" {
		t.Errorf("system prompt missing: role[0] = %q", s.history[0].Role)
	}
	// Last message still assistant.
	if s.history[len(s.history)-1].Role != "assistant" {
		t.Errorf("last message role = %q, want assistant", s.history[len(s.history)-1].Role)
	}
}

func TestSession_TrimHistory_NoOpWhenUnderCap(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("small")
	s.history = append(s.history,
		openai.ChatCompletionMessage{Role: "user", Content: "short"},
		openai.ChatCompletionMessage{Role: "assistant", Content: "reply"},
	)
	before := s.HistoryLen()
	s.runMu.Lock()
	s.trimHistory(100000, 4)
	s.runMu.Unlock()
	if s.HistoryLen() != before {
		t.Errorf("trim modified under-cap history: len %d -> %d", before, s.HistoryLen())
	}
}

func TestSession_TrimHistory_DisabledWhenMaxZero(t *testing.T) {
	a := testAgent(t)
	s := a.GetOrCreateSession("nocap")
	big := strings.Repeat("x", 1000)
	for i := 0; i < 10; i++ {
		s.history = append(s.history,
			openai.ChatCompletionMessage{Role: "user", Content: big},
			openai.ChatCompletionMessage{Role: "assistant", Content: big},
		)
	}
	before := s.HistoryLen()
	s.runMu.Lock()
	s.trimHistory(0, 2) // disabled
	s.runMu.Unlock()
	if s.HistoryLen() != before {
		t.Errorf("trim ran despite maxChars=0: len %d -> %d", before, s.HistoryLen())
	}
}

func TestAgent_ListSessionIDs(t *testing.T) {
	a := testAgent(t)
	a.GetOrCreateSession("alpha")
	a.GetOrCreateSession("beta")
	ids := a.ListSessionIDs()
	if len(ids) != 3 { // default + alpha + beta
		t.Errorf("got %d sessions, want 3", len(ids))
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	for _, want := range []string{"default", "alpha", "beta"} {
		if !found[want] {
			t.Errorf("missing session %q in list", want)
		}
	}
}
