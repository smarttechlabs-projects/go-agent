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
	"encoding/json"
	"strings"
	"testing"
)

func parseSchema(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("bad test schema: %v", err)
	}
	return out
}

func TestValidateToolArgs_ValidObject(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {
			"port": {"type": "integer"},
			"host": {"type": "string"}
		},
		"required": ["port"],
		"additionalProperties": false
	}`)
	errs := ValidateToolArgs(`{"port": 8000, "host": "localhost"}`, schema)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateToolArgs_MissingRequired(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {"port": {"type": "integer"}},
		"required": ["port"]
	}`)
	errs := ValidateToolArgs(`{}`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "missing required field") {
		t.Errorf("error = %q, want missing-required text", errs[0])
	}
}

func TestValidateToolArgs_WrongType(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {"port": {"type": "integer"}},
		"required": ["port"]
	}`)
	errs := ValidateToolArgs(`{"port": "eight-thousand"}`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "expected type") {
		t.Errorf("error = %q, want type-mismatch text", errs[0])
	}
}

func TestValidateToolArgs_IntegerAcceptsWholeFloat(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {"port": {"type": "integer"}},
		"required": ["port"]
	}`)
	// JSON numbers unmarshal to float64; "port": 8000 is float64(8000).
	errs := ValidateToolArgs(`{"port": 8000}`, schema)
	if len(errs) != 0 {
		t.Errorf("unexpected errors on whole float: %v", errs)
	}
	// But fractional should fail.
	errs = ValidateToolArgs(`{"port": 8000.5}`, schema)
	if len(errs) != 1 {
		t.Errorf("fractional float allowed for integer: %v", errs)
	}
}

func TestValidateToolArgs_AdditionalPropertiesFalse(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {"port": {"type": "integer"}},
		"additionalProperties": false
	}`)
	errs := ValidateToolArgs(`{"port": 8000, "extra": "field"}`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "unexpected field") {
		t.Errorf("error = %q, want unexpected-field text", errs[0])
	}
}

func TestValidateToolArgs_AdditionalPropertiesTrue(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {"port": {"type": "integer"}}
	}`)
	// No additionalProperties declared -> permissive.
	errs := ValidateToolArgs(`{"port": 8000, "extra": "field"}`, schema)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateToolArgs_Nested(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {
			"target": {
				"type": "object",
				"properties": {"port": {"type": "integer"}},
				"required": ["port"]
			}
		},
		"required": ["target"]
	}`)
	errs := ValidateToolArgs(`{"target": {}}`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "target") {
		t.Errorf("error missing path: %q", errs[0])
	}
}

func TestValidateToolArgs_ArrayItems(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {
			"ports": {
				"type": "array",
				"items": {"type": "integer"}
			}
		}
	}`)
	errs := ValidateToolArgs(`{"ports": [80, 443, "https"]}`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "ports[2]") {
		t.Errorf("error should reference index 2: %q", errs[0])
	}
}

func TestValidateToolArgs_Enum(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {
			"protocol": {"type": "string", "enum": ["tcp", "udp"]}
		}
	}`)
	if errs := ValidateToolArgs(`{"protocol": "tcp"}`, schema); len(errs) != 0 {
		t.Errorf("tcp should be valid: %v", errs)
	}
	if errs := ValidateToolArgs(`{"protocol": "sctp"}`, schema); len(errs) != 1 {
		t.Errorf("sctp should fail enum check: %v", errs)
	}
}

func TestValidateToolArgs_MalformedJSON(t *testing.T) {
	schema := parseSchema(t, `{"type": "object"}`)
	errs := ValidateToolArgs(`{"broken`, schema)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "not valid JSON") {
		t.Errorf("error = %q, want JSON-parse text", errs[0])
	}
}

func TestValidateToolArgs_EmptyArgs(t *testing.T) {
	// No-arg tools: empty string / "null" / "{}" all valid against a
	// parameters-less schema.
	schema := parseSchema(t, `{"type": "object", "properties": {}}`)
	for _, in := range []string{"", "null", "{}"} {
		if errs := ValidateToolArgs(in, schema); len(errs) != 0 {
			t.Errorf("args=%q errs=%v, want empty", in, errs)
		}
	}
}

func TestValidateToolArgs_NilSchemaAllows(t *testing.T) {
	if errs := ValidateToolArgs(`{"anything": "goes"}`, nil); len(errs) != 0 {
		t.Errorf("nil schema should not validate: %v", errs)
	}
}

func TestValidateToolArgsBrief_Formatting(t *testing.T) {
	schema := parseSchema(t, `{
		"type": "object",
		"properties": {
			"port": {"type": "integer"},
			"host": {"type": "string"}
		},
		"required": ["port", "host"]
	}`)
	brief := ValidateToolArgsBrief(`{}`, schema)
	if !strings.Contains(brief, "missing required field \"port\"") {
		t.Errorf("brief missing port error: %q", brief)
	}
	if !strings.Contains(brief, "missing required field \"host\"") {
		t.Errorf("brief missing host error: %q", brief)
	}
	if !strings.Contains(brief, "call the tool again") {
		t.Errorf("brief missing guidance: %q", brief)
	}

	// Valid args -> empty string.
	if got := ValidateToolArgsBrief(`{"port": 80, "host": "x"}`, schema); got != "" {
		t.Errorf("brief on valid args = %q, want empty", got)
	}
}

func TestTypeMatches_Union(t *testing.T) {
	// type: ["string", "null"] -- nullable string.
	if !typeMatches("hello", []any{"string", "null"}) {
		t.Error("string should match [string, null]")
	}
	if !typeMatches(nil, []any{"string", "null"}) {
		t.Error("nil should match [string, null]")
	}
	if typeMatches(42.0, []any{"string", "null"}) {
		t.Error("number should not match [string, null]")
	}
}
