// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import "testing"

func TestClampLimit(t *testing.T) {
	tests := []struct {
		name          string
		override      int
		serverDefault int
		want          int
	}{
		{"override missing falls back to default", 0, 100, 100},
		{"override negative falls back to default", -5, 100, 100},
		{"server unlimited accepts any override", 500, 0, 500},
		{"stricter override honored", 50, 100, 50},
		{"looser override rejected", 500, 100, 100},
		{"equal override uses default", 100, 100, 100},
		{"both zero stays zero", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampLimit(tt.override, tt.serverDefault)
			if got != tt.want {
				t.Errorf("clampLimit(%d, %d) = %d, want %d", tt.override, tt.serverDefault, got, tt.want)
			}
		})
	}
}

func TestResolveLimits_NilOverrideReturnsDefaults(t *testing.T) {
	cfg := &AgentConfig{
		MaxToolRounds:   10,
		MaxTokenBudget:  50000,
		TimeoutSeconds:  300,
		LoopFingerprint: 3,
		MaxResultLen:    8192,
		EarlyStop:       false,
	}
	got := cfg.ResolveLimits(nil)

	if got.MaxToolRounds != 10 {
		t.Errorf("MaxToolRounds = %d, want 10", got.MaxToolRounds)
	}
	if got.MaxTokenBudget != 50000 {
		t.Errorf("MaxTokenBudget = %d, want 50000", got.MaxTokenBudget)
	}
	if got.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %d, want 300", got.TimeoutSeconds)
	}
	if got.LoopFingerprint != 3 {
		t.Errorf("LoopFingerprint = %d, want 3", got.LoopFingerprint)
	}
	if got.MaxResultLen != 8192 {
		t.Errorf("MaxResultLen = %d, want 8192", got.MaxResultLen)
	}
	if got.EarlyStop != false {
		t.Errorf("EarlyStop = %v, want false", got.EarlyStop)
	}
}

func TestResolveLimits_StricterOverrideHonored(t *testing.T) {
	cfg := &AgentConfig{
		MaxToolRounds:   10,
		MaxTokenBudget:  50000,
		TimeoutSeconds:  300,
		LoopFingerprint: 3,
		MaxResultLen:    8192,
	}
	override := &AgentLimits{
		MaxToolRounds:  5,
		MaxTokenBudget: 10000,
		TimeoutSeconds: 60,
		EarlyStop:      true,
	}
	got := cfg.ResolveLimits(override)

	if got.MaxToolRounds != 5 {
		t.Errorf("MaxToolRounds = %d, want 5", got.MaxToolRounds)
	}
	if got.MaxTokenBudget != 10000 {
		t.Errorf("MaxTokenBudget = %d, want 10000", got.MaxTokenBudget)
	}
	if got.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", got.TimeoutSeconds)
	}
	// Fields not set in override should fall back to server defaults.
	if got.LoopFingerprint != 3 {
		t.Errorf("LoopFingerprint = %d, want 3 (default)", got.LoopFingerprint)
	}
	if got.MaxResultLen != 8192 {
		t.Errorf("MaxResultLen = %d, want 8192 (default)", got.MaxResultLen)
	}
	if !got.EarlyStop {
		t.Errorf("EarlyStop = false, want true (client opted in)")
	}
}

func TestResolveLimits_LooserOverrideRejected(t *testing.T) {
	cfg := &AgentConfig{
		MaxToolRounds:   10,
		MaxTokenBudget:  50000,
		TimeoutSeconds:  300,
		LoopFingerprint: 3,
		MaxResultLen:    8192,
	}
	// Client asking for LOOSER limits -- must be clamped to server defaults.
	override := &AgentLimits{
		MaxToolRounds:   999,
		MaxTokenBudget:  999999,
		TimeoutSeconds:  9999,
		LoopFingerprint: 100,
		MaxResultLen:    99999,
	}
	got := cfg.ResolveLimits(override)

	if got.MaxToolRounds != 10 {
		t.Errorf("MaxToolRounds = %d, want 10 (server cap)", got.MaxToolRounds)
	}
	if got.MaxTokenBudget != 50000 {
		t.Errorf("MaxTokenBudget = %d, want 50000 (server cap)", got.MaxTokenBudget)
	}
	if got.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %d, want 300 (server cap)", got.TimeoutSeconds)
	}
	if got.LoopFingerprint != 3 {
		t.Errorf("LoopFingerprint = %d, want 3 (server cap)", got.LoopFingerprint)
	}
	if got.MaxResultLen != 8192 {
		t.Errorf("MaxResultLen = %d, want 8192 (server cap)", got.MaxResultLen)
	}
}

func TestResolveLimits_ServerUnlimitedAcceptsOverride(t *testing.T) {
	cfg := &AgentConfig{
		MaxToolRounds:  0, // Server has no cap.
		MaxTokenBudget: 0,
		TimeoutSeconds: 0,
	}
	override := &AgentLimits{
		MaxToolRounds:  5,
		MaxTokenBudget: 1000,
		TimeoutSeconds: 30,
	}
	got := cfg.ResolveLimits(override)

	if got.MaxToolRounds != 5 {
		t.Errorf("MaxToolRounds = %d, want 5", got.MaxToolRounds)
	}
	if got.MaxTokenBudget != 1000 {
		t.Errorf("MaxTokenBudget = %d, want 1000", got.MaxTokenBudget)
	}
	if got.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", got.TimeoutSeconds)
	}
}

func TestParseMetaLimits_ValidKeys(t *testing.T) {
	// JSON numbers unmarshal to float64 by default -- simulate that.
	meta := map[string]any{
		"io.llm-agent/max_rounds":  float64(5),
		"io.llm-agent/max_tokens":  float64(20000),
		"io.llm-agent/timeout":     float64(120),
		"io.llm-agent/loop_detect": float64(2),
		"io.llm-agent/max_result":  float64(4096),
		"io.llm-agent/early_stop":  true,
	}
	got := ParseMetaLimits(meta)
	if got == nil {
		t.Fatal("ParseMetaLimits returned nil for valid meta")
	}
	if got.MaxToolRounds != 5 {
		t.Errorf("MaxToolRounds = %d, want 5", got.MaxToolRounds)
	}
	if got.MaxTokenBudget != 20000 {
		t.Errorf("MaxTokenBudget = %d, want 20000", got.MaxTokenBudget)
	}
	if got.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds = %d, want 120", got.TimeoutSeconds)
	}
	if got.LoopFingerprint != 2 {
		t.Errorf("LoopFingerprint = %d, want 2", got.LoopFingerprint)
	}
	if got.MaxResultLen != 4096 {
		t.Errorf("MaxResultLen = %d, want 4096", got.MaxResultLen)
	}
	if !got.EarlyStop {
		t.Errorf("EarlyStop = false, want true")
	}
}

func TestParseMetaLimits_UnknownPrefixIgnored(t *testing.T) {
	meta := map[string]any{
		"some-other-vendor/max_rounds": float64(99),
		"progressToken":                "abc", // Another MCP-reserved key.
	}
	got := ParseMetaLimits(meta)
	if got != nil {
		t.Errorf("ParseMetaLimits = %+v, want nil for meta without io.llm-agent/ keys", got)
	}
}

func TestParseMetaLimits_NilOrEmpty(t *testing.T) {
	if got := ParseMetaLimits(nil); got != nil {
		t.Errorf("ParseMetaLimits(nil) = %+v, want nil", got)
	}
	if got := ParseMetaLimits(map[string]any{}); got != nil {
		t.Errorf("ParseMetaLimits({}) = %+v, want nil", got)
	}
}

func TestParseMetaLimits_PartialFields(t *testing.T) {
	// Only one key -- should return a non-nil struct with just that field set.
	meta := map[string]any{
		"io.llm-agent/timeout": float64(30),
	}
	got := ParseMetaLimits(meta)
	if got == nil {
		t.Fatal("ParseMetaLimits returned nil for partial meta")
	}
	if got.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", got.TimeoutSeconds)
	}
	// Other fields should be zero (their "not set" marker).
	if got.MaxToolRounds != 0 {
		t.Errorf("MaxToolRounds = %d, want 0 (not set)", got.MaxToolRounds)
	}
}

func TestParseMetaLimits_WrongType(t *testing.T) {
	// Non-numeric value for an int field -- silently ignored per robust-parse policy.
	meta := map[string]any{
		"io.llm-agent/max_rounds": "five",
	}
	got := ParseMetaLimits(meta)
	if got != nil {
		t.Errorf("ParseMetaLimits = %+v, want nil (bad type silently ignored)", got)
	}
}

func TestAsInt(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{"float64", float64(42), 42, true},
		{"int", 42, 42, true},
		{"int64", int64(42), 42, true},
		{"string rejected", "42", 0, false},
		{"bool rejected", true, 0, false},
		{"nil rejected", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := asInt(tt.in)
			if ok != tt.ok {
				t.Errorf("asInt(%v) ok=%v, want %v", tt.in, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("asInt(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestAgentLimits_Describe(t *testing.T) {
	l := &AgentLimits{
		MaxToolRounds:   10,
		MaxTokenBudget:  50000,
		TimeoutSeconds:  300,
		LoopFingerprint: 3,
		EarlyStop:       true,
	}
	got := l.Describe()
	want := "rounds=10 tokens=50000 timeout=300s loop=3 early-stop"
	if got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}
