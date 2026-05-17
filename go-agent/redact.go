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
	"regexp"
	"strings"
	"sync"
)

// Secret redaction: catches common API key and credential shapes in strings
// before they hit logs, events, or OTEL attributes. Not a replacement for
// real secret hygiene -- it's a defense-in-depth layer that prevents
// accidental key leaks when tool arguments or HTTP bodies are logged.
//
// Patterns intentionally err on the side of false positives for
// high-entropy values where the cost of exposure is asymmetric.

const redactionPlaceholder = "***REDACTED***"

// redactRule binds a compiled regex to the replacement template it should
// use. The template can reference capture groups via ${1} ${2} etc so a
// rule can preserve structural context (e.g. the JSON field name) while
// replacing only the secret value.
type redactRule struct {
	re   *regexp.Regexp
	repl string
}

var (
	redactorOnce sync.Once
	redactRules  []redactRule

	// Fast-path: most tool args/results don't contain anything redaction
	// would touch. We skip the regex pass unless at least one suspect
	// substring appears.
	redactSuspectMarker = []string{
		"key", "token", "secret", "password", "auth",
		"api_key", "apikey", "bearer", "basic",
		"sk-", "sk_", "aiza", "gsk_", "ghp_", "xoxb-", "xoxp-",
	}
)

func initRedactors() {
	placeholder := redactionPlaceholder
	redactRules = []redactRule{
		// Vendor-specific key prefixes. Full-match replace.
		{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`), placeholder},           // OpenAI / Anthropic (sk-proj-, sk-ant-)
		{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`), placeholder},          // Google / Gemini
		{regexp.MustCompile(`\bgsk_[A-Za-z0-9]{20,}\b`), placeholder},            // Groq
		{regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`), placeholder},            // GitHub PAT
		{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), placeholder},    // Slack
		{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), placeholder}, // JWT

		// HTTP Authorization headers -- preserve the "Authorization: Bearer "
		// prefix so we still see what kind of auth was attempted.
		{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"',\r\n]+`), "${1}" + placeholder},
		{regexp.MustCompile(`(?i)(authorization:\s*basic\s+)[^\s"',\r\n]+`), "${1}" + placeholder},

		// JSON credential fields: preserve "api_key":" prefix and the closing
		// quote so JSON structure survives.
		{regexp.MustCompile(`(?i)("(?:api[_-]?key|apikey|access[_-]?token|auth[_-]?token|secret|password|passwd|bearer[_-]?token|client[_-]?secret)"\s*:\s*")[^"]*(")`), "${1}" + placeholder + "${2}"},

		// Shell / env-var style: KEY=value. Preserve the KEY= prefix.
		{regexp.MustCompile(`(?i)([A-Z][A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD)=)[^\s"',\r\n]+`), "${1}" + placeholder},
	}
}

// Redact returns s with recognized secret patterns replaced by
// ***REDACTED***. Safe to call on any string; cheap when the input clearly
// contains no secret markers.
func Redact(s string) string {
	if s == "" {
		return s
	}
	// Fast path: only run regexes if the string contains at least one
	// suspect substring. Saves ~90% of work on typical non-secret inputs.
	lower := strings.ToLower(s)
	hit := false
	for _, m := range redactSuspectMarker {
		if strings.Contains(lower, m) {
			hit = true
			break
		}
	}
	if !hit {
		return s
	}

	redactorOnce.Do(initRedactors)
	out := s
	for _, rule := range redactRules {
		out = rule.re.ReplaceAllString(out, rule.repl)
	}
	return out
}

// RedactBytes is a convenience wrapper for byte slices; returns a new slice.
func RedactBytes(b []byte) []byte {
	return []byte(Redact(string(b)))
}
