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
	"testing"
)

func TestParseTextToolCalls_SingleTool(t *testing.T) {
	content := `I'll check that for you.
<function=check_port>
<parameter=port>
3000
</parameter>
</function>`

	clean, calls := parseTextToolCalls(content)

	if clean != "I'll check that for you." {
		t.Errorf("clean content = %q, want %q", clean, "I'll check that for you.")
	}
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].Function.Name != "check_port" {
		t.Errorf("tool name = %q, want %q", calls[0].Function.Name, "check_port")
	}
	// Port should be a number, not a string.
	if calls[0].Function.Arguments != `{"port":3000}` {
		t.Errorf("args = %s, want {\"port\":3000}", calls[0].Function.Arguments)
	}
}

func TestParseTextToolCalls_MultipleTools(t *testing.T) {
	content := `Let me search.
<function=DuckDuckGoWebSearch>
<parameter=query>
LiveKit SFU
</parameter>
<parameter=maxResults>
5
</parameter>
</function>
And also fetch.
<function=fetch>
<parameter=url>
https://example.com
</parameter>
</function>`

	clean, calls := parseTextToolCalls(content)

	if clean != "Let me search." {
		t.Errorf("clean = %q", clean)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Function.Name != "DuckDuckGoWebSearch" {
		t.Errorf("call[0] name = %q", calls[0].Function.Name)
	}
	if calls[1].Function.Name != "fetch" {
		t.Errorf("call[1] name = %q", calls[1].Function.Name)
	}
}

func TestParseTextToolCalls_NoToolCalls(t *testing.T) {
	content := "Just a normal response with no tools."
	clean, calls := parseTextToolCalls(content)

	if clean != content {
		t.Errorf("clean = %q, want original", clean)
	}
	if len(calls) != 0 {
		t.Errorf("got %d calls, want 0", len(calls))
	}
}

func TestInferType(t *testing.T) {
	tests := []struct {
		input string
		want  any
	}{
		{"3000", int64(3000)},
		{"3.14", 3.14},
		{"true", true},
		{"false", false},
		{"null", nil},
		{"hello", "hello"},
		{"https://example.com", "https://example.com"},
	}

	for _, tt := range tests {
		got := inferType(tt.input)
		if got != tt.want {
			t.Errorf("inferType(%q) = %v (%T), want %v (%T)", tt.input, got, got, tt.want, tt.want)
		}
	}
}
