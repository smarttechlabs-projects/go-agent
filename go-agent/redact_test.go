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
	"testing"
)

func TestRedact_VendorKeys(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"OpenAI sk-", "my key is sk-proj-abcdef1234567890ABCDEF1234567890 please"},
		{"Anthropic sk-ant-", "sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxx-ABC"},
		{"Google AIza", "fetching with AIzaSyABCDEFGHIJKLMNOPQRSTUVWXYZ1234567 and"},
		{"Groq gsk_", "token: gsk_abcdef123456789ABCDEF0"},
		{"GitHub ghp_", "auth ghp_1234567890ABCDEF1234"},
		{"Slack xoxb-", "xoxb-123-456-ABCdefGHIjkl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			if !strings.Contains(got, redactionPlaceholder) {
				t.Errorf("Redact(%q) = %q, want placeholder", tt.in, got)
			}
			// The surrounding text should survive.
			if tt.name == "OpenAI sk-" && !strings.Contains(got, "my key is") {
				t.Errorf("redaction consumed context: %q", got)
			}
		})
	}
}

func TestRedact_HTTPAuthorizationHeader(t *testing.T) {
	in := "Authorization: Bearer abc.def.ghi"
	got := Redact(in)
	if !strings.Contains(got, "Authorization: Bearer "+redactionPlaceholder) {
		t.Errorf("Bearer header not redacted: %q", got)
	}

	in = "authorization: basic dXNlcjpwYXNzCg=="
	got = Redact(in)
	if !strings.Contains(got, redactionPlaceholder) {
		t.Errorf("Basic header not redacted: %q", got)
	}
}

func TestRedact_JSONCredentialFields(t *testing.T) {
	tests := []string{
		`{"api_key": "sekret123"}`,
		`{"apiKey": "sekret123"}`,
		`{"access_token":"tok-abc"}`,
		`{"password": "hunter2"}`,
		`{"client_secret": "verylongvalue"}`,
	}
	for _, in := range tests {
		got := Redact(in)
		if !strings.Contains(got, redactionPlaceholder) {
			t.Errorf("credential not redacted: %q → %q", in, got)
		}
		// Key name must survive so debugging still works.
		if !strings.Contains(got, `":"`) && !strings.Contains(got, `": "`) {
			t.Errorf("JSON structure broken: %q", got)
		}
	}
}

func TestRedact_ShellEnvVars(t *testing.T) {
	in := "ANTHROPIC_API_KEY=sk-ant-abc123 other=stuff"
	got := Redact(in)
	if !strings.Contains(got, "ANTHROPIC_API_KEY="+redactionPlaceholder) {
		t.Errorf("env var not redacted: %q", got)
	}
	// Non-secret assignment preserved.
	if !strings.Contains(got, "other=stuff") {
		t.Errorf("non-secret var consumed: %q", got)
	}
}

func TestRedact_JWT(t *testing.T) {
	in := "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyMTIzIn0.abcdef1234567890"
	got := Redact(in)
	if !strings.Contains(got, redactionPlaceholder) {
		t.Errorf("JWT not redacted: %q", got)
	}
}

func TestRedact_NoSecretsUnchanged(t *testing.T) {
	safe := []string{
		"",
		"just plain text",
		`{"city": "Berlin", "population": 3700000}`,
		"fetch https://example.com/page",
		"Port 8000 is in use by lemonade-router (PID 467250)",
	}
	for _, s := range safe {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want unchanged", s, got)
		}
	}
}

// Fast-path optimization: inputs with no suspect markers should skip regex.
// This test is a smoke check that the behavior is equivalent either way.
func TestRedact_Idempotent(t *testing.T) {
	s := `api_key: sk-abc123456789012345678 done`
	once := Redact(s)
	twice := Redact(once)
	if once != twice {
		t.Errorf("Redact not idempotent: %q vs %q", once, twice)
	}
}

func BenchmarkRedact_NoSecrets(b *testing.B) {
	// Common case: nothing to redact. Fast path should dominate.
	s := strings.Repeat("the quick brown fox jumps over the lazy dog ", 10)
	for i := 0; i < b.N; i++ {
		Redact(s)
	}
}

func BenchmarkRedact_WithSecret(b *testing.B) {
	s := `Authorization: Bearer abc.def.ghi and {"api_key":"sk-proj-aaaaaaaaaaaaaaaaaaaa"}`
	for i := 0; i < b.N; i++ {
		Redact(s)
	}
}
