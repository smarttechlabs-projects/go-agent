// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: policy.go -- termination heuristics that decide when the agent
// loop should stop or stop trying a particular tool.
//
// These are *advisory* string-matching heuristics, not contracts. Both
// LLM provider error messages and MCP tool result formats vary widely
// and aren't standardised, so we match the patterns we've observed in
// practice. False positives are bounded:
//
//   - isTerminalError false positive   -> loop aborts a query that might
//                                         have recovered. User retries.
//   - isToolFailure   false positive   -> agent stops retrying a tool that
//                                         was actually working. User notices
//                                         and rephrases.
//
// Neither corrupts data or escalates privileges. When we want stronger
// guarantees we should switch to typed errors from the LLM client and
// structured error envelopes from MCP servers; until then, these
// heuristics are the pragmatic compromise.

import "strings"

// isTerminalError returns true if an error should abort the loop
// immediately rather than being retried. Authentication problems, missing
// resources, malformed requests, and quota exhaustion fall into this
// category -- retrying them just burns budget.
//
// Implementation note: we substring-match a lower-cased error message
// because Go's net/http and most LLM SDKs return errors as plain strings
// without typed status codes. Substring matching is intentionally loose
// to catch the same condition rendered slightly differently across
// providers (e.g. "401 Unauthorized" vs "invalid_api_key" vs
// "authentication failed"). The trade-off is that an error message that
// happens to contain one of these tokens for unrelated reasons (e.g. a
// 5xx response body that quotes "401" in a stack trace) will also be
// treated as terminal. Acceptable cost for the simplicity.
func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	terminalMarkers := []string{
		"401", "403", "404",
		"unauthorized", "forbidden",
		"invalid api key", "invalid_api_key",
		"authentication", "permission denied",
		"quota exceeded",
		"invalid request",
	}
	for _, marker := range terminalMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// isToolFailure heuristically classifies a tool result as unhelpful so
// the session can stop re-invoking a tool that keeps coming up empty.
// Returns true for empty results and for the few error markers we've
// seen in practice from MCP servers.
//
// Anchoring matters: error markers are matched at the start of the
// (trimmed, lower-cased) result so that legitimate output discussing
// "errors" in passing isn't flagged. There is intentionally NO minimum
// length check -- short-but-valid responses like "Port 8000: idle"
// (16 chars) used to be misclassified, which broke the port-scanner
// reference server.
//
// This is heuristic, not authoritative. The MCP spec does not standardise
// error result formats; tools that want their failures detected reliably
// should either return an empty string or prefix with "error:". When MCP
// gains structured error envelopes we should switch to those.
func isToolFailure(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)

	// Anchored prefix checks for MCP-style error markers. Conservative on
	// purpose -- prose that happens to mention "errors" must not trip
	// this branch (see policy.go header for the rationale).
	if strings.HasPrefix(lower, "error:") ||
		strings.HasPrefix(lower, `{"error"`) ||
		strings.HasPrefix(lower, "no results") ||
		strings.HasPrefix(lower, "(no results)") ||
		strings.HasPrefix(lower, "(no content)") {
		return true
	}

	// Anti-bot / CAPTCHA challenge markers. Search engines and CDNs serve
	// a 200-OK challenge page when they detect a headless browser, so the
	// tool result looks successful but contains no useful content -- the
	// model would otherwise retry the same URL until max-rounds. These
	// markers are highly specific to challenge pages; false positives on
	// real content quoting them in passing are unlikely enough that the
	// alternative (silently looping on /sorry/index) is the worse failure.
	antiBotMarkers := []string{
		"google.com/sorry",
		"/sorry/index",
		"our systems have detected unusual traffic",
		"unusual traffic from your computer network",
		"complete the captcha",
		"verify you are a human",
		"are you a robot",
		"cloudflare ray id",
	}
	for _, m := range antiBotMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
