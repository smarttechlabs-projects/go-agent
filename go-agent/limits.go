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
	"fmt"
	"strings"
)

// AgentLimits holds the runtime safety parameters for a single query.
// They can be overridden per-request (REST API body or MCP _meta field)
// but are always clamped to the server-side configured maximums -- a client
// can request STRICTER limits than the server default, but never LOOSER.
type AgentLimits struct {
	MaxToolRounds   int  `json:"max_rounds,omitempty"`
	MaxTokenBudget  int  `json:"max_tokens,omitempty"`
	TimeoutSeconds  int  `json:"timeout,omitempty"`
	LoopFingerprint int  `json:"loop_detect,omitempty"`
	MaxResultLen    int  `json:"max_result,omitempty"`
	EarlyStop       bool `json:"early_stop,omitempty"`
}

// ResolveLimits returns the effective limits for a query, starting from the
// server config defaults and applying the override. Each field in the override
// is clamped: a client value is accepted only if it's stricter than the server
// default (smaller timeout, fewer tokens, fewer rounds). This prevents a client
// from disabling safety limits.
//
// Rules:
//   - If override field is 0 or missing: use server default
//   - If override field is > 0: use min(override, server default)
//   - If server default is 0 (unlimited): client can request any positive value
//   - early_stop: simple bool override (not clamped)
func (cfg *AgentConfig) ResolveLimits(override *AgentLimits) *AgentLimits {
	limits := &AgentLimits{
		MaxToolRounds:   cfg.MaxToolRounds,
		MaxTokenBudget:  cfg.MaxTokenBudget,
		TimeoutSeconds:  cfg.TimeoutSeconds,
		LoopFingerprint: cfg.LoopFingerprint,
		MaxResultLen:    cfg.MaxResultLen,
		EarlyStop:       cfg.EarlyStop,
	}

	if override == nil {
		return limits
	}

	limits.MaxToolRounds = clampLimit(override.MaxToolRounds, cfg.MaxToolRounds)
	limits.MaxTokenBudget = clampLimit(override.MaxTokenBudget, cfg.MaxTokenBudget)
	limits.TimeoutSeconds = clampLimit(override.TimeoutSeconds, cfg.TimeoutSeconds)
	limits.LoopFingerprint = clampLimit(override.LoopFingerprint, cfg.LoopFingerprint)
	limits.MaxResultLen = clampLimit(override.MaxResultLen, cfg.MaxResultLen)

	// early_stop is a simple bool -- clients can always opt in or out.
	if override.EarlyStop {
		limits.EarlyStop = true
	}

	return limits
}

// clampLimit applies the client override only if it's positive AND stricter
// (smaller) than the server default. Returns the effective value.
func clampLimit(override, serverDefault int) int {
	if override <= 0 {
		return serverDefault // Use default if override is missing or non-positive.
	}
	if serverDefault == 0 {
		return override // Server has no limit; accept client value as-is.
	}
	if override < serverDefault {
		return override // Client asked for stricter limit; honor it.
	}
	return serverDefault // Client asked for looser limit; refuse, use server max.
}

// ParseMetaLimits extracts AgentLimits from the MCP _meta field of a tools/call
// request. Uses the "io.llm-agent/" prefix per the MCP spec convention for
// vendor-specific metadata.
//
// Supported keys:
//   - io.llm-agent/max_rounds (int)
//   - io.llm-agent/max_tokens (int)
//   - io.llm-agent/timeout (int, seconds)
//   - io.llm-agent/loop_detect (int)
//   - io.llm-agent/max_result (int)
//   - io.llm-agent/early_stop (bool)
//
// Unknown keys are silently ignored per MCP _meta semantics.
func ParseMetaLimits(meta map[string]any) *AgentLimits {
	if meta == nil {
		return nil
	}
	const prefix = "io.llm-agent/"

	limits := &AgentLimits{}
	found := false

	for key, val := range meta {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)

		switch name {
		case "max_rounds":
			if n, ok := asInt(val); ok {
				limits.MaxToolRounds = n
				found = true
			}
		case "max_tokens":
			if n, ok := asInt(val); ok {
				limits.MaxTokenBudget = n
				found = true
			}
		case "timeout":
			if n, ok := asInt(val); ok {
				limits.TimeoutSeconds = n
				found = true
			}
		case "loop_detect":
			if n, ok := asInt(val); ok {
				limits.LoopFingerprint = n
				found = true
			}
		case "max_result":
			if n, ok := asInt(val); ok {
				limits.MaxResultLen = n
				found = true
			}
		case "early_stop":
			if b, ok := val.(bool); ok {
				limits.EarlyStop = b
				found = true
			}
		}
	}

	if !found {
		return nil
	}
	return limits
}

// asInt converts a value from JSON (typically float64) to int.
func asInt(val any) (int, bool) {
	switch v := val.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// Describe returns a human-readable summary of the limits.
func (l *AgentLimits) Describe() string {
	parts := []string{
		fmt.Sprintf("rounds=%d", l.MaxToolRounds),
		fmt.Sprintf("tokens=%d", l.MaxTokenBudget),
		fmt.Sprintf("timeout=%ds", l.TimeoutSeconds),
		fmt.Sprintf("loop=%d", l.LoopFingerprint),
	}
	if l.EarlyStop {
		parts = append(parts, "early-stop")
	}
	return strings.Join(parts, " ")
}
