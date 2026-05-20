// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import "testing"

func TestCountTrailingMatches(t *testing.T) {
	tests := []struct {
		hashes []string
		target string
		want   int
	}{
		{[]string{}, "x", 0},
		{[]string{"a"}, "x", 0},
		{[]string{"x"}, "x", 1},
		{[]string{"a", "x"}, "x", 1},
		{[]string{"x", "x", "x"}, "x", 3},
		{[]string{"a", "x", "x", "x"}, "x", 3},
		{[]string{"x", "x", "a", "x"}, "x", 1}, // non-consecutive
		{[]string{"a", "b", "x", "y"}, "x", 0},  // last doesn't match
	}

	for _, tt := range tests {
		got := countTrailingMatches(tt.hashes, tt.target)
		if got != tt.want {
			t.Errorf("countTrailingMatches(%v, %q) = %d, want %d", tt.hashes, tt.target, got, tt.want)
		}
	}
}

func TestIsTerminalError(t *testing.T) {
	tests := []struct {
		err  string
		want bool
	}{
		{"", false},
		{"connection refused", false},
		{"timeout", false},
		{"500 internal server error", false},
		{"429 too many requests", false},
		{"503 service unavailable", false},
		{"401 Unauthorized", true},
		{"403 Forbidden", true},
		{"404 Not Found", true},
		{"invalid API key", true},
		{"authentication failed", true},
		{"permission denied", true},
		{"quota exceeded", true},
	}

	for _, tt := range tests {
		var err error
		if tt.err != "" {
			err = &testError{tt.err}
		}
		got := isTerminalError(err)
		if got != tt.want {
			t.Errorf("isTerminalError(%q) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestConfigDefaults_SafetyFields(t *testing.T) {
	// Verify defaults are applied for the new safety fields.
	cfg := &AgentConfig{}

	// Simulate what LoadConfig does for defaults.
	if cfg.MaxTokenBudget <= 0 {
		cfg.MaxTokenBudget = defaultMaxTokenBudget
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultTimeoutSeconds
	}
	if cfg.LoopFingerprint <= 0 {
		cfg.LoopFingerprint = defaultLoopFingerprint
	}

	if cfg.MaxTokenBudget != 100000 {
		t.Errorf("MaxTokenBudget default = %d, want 100000", cfg.MaxTokenBudget)
	}
	if cfg.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds default = %d, want 300", cfg.TimeoutSeconds)
	}
	if cfg.LoopFingerprint != 3 {
		t.Errorf("LoopFingerprint default = %d, want 3", cfg.LoopFingerprint)
	}
}
