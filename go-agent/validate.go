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
	"fmt"
	"strings"
)

// Minimal JSON Schema validator covering the subset actually used by MCP
// tools in the wild: object type, required fields, primitive property
// types, additionalProperties:false. Not a general JSON Schema engine --
// intentionally small so we can validate model-generated tool arguments
// before dispatching them, and inject a descriptive error back into the
// loop when the model hallucinates an argument shape.
//
// Supported keywords per node:
//
//	type:                  "object" | "string" | "number" | "integer"
//	                       | "boolean" | "array" | "null"
//	                       (or []string for unions, "any" to skip)
//	required:              []string (on object schemas)
//	properties:            map[string]schema
//	additionalProperties:  false | true | schema (schema is ignored here;
//	                       treated as additionalProperties:true)
//	items:                 schema (array element type)
//	enum:                  []any
//
// Unknown keywords are ignored (permissive).

// ValidateToolArgs parses argsJSON and validates it against the tool's
// schema. Returns nil on success, or a []error listing every problem
// encountered (we collect rather than short-circuit so the model gets the
// full picture in one pass).
//
// The schema argument is typically the raw InputSchema map from an MCP
// tool definition -- stored on openai.Tool.Function.Parameters as a
// map[string]any after the JSON round-trip in mcp.go.
func ValidateToolArgs(argsJSON string, schema any) []error {
	if schema == nil {
		return nil
	}
	var args any
	// Empty string and "null" mean "no arguments" -- treat as empty object.
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "null" {
		args = map[string]any{}
	} else if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return []error{fmt.Errorf("arguments are not valid JSON: %v", err)}
	}
	var errs []error
	validateAgainst(&errs, "", args, schema)
	return errs
}

// ValidateToolArgsBrief is a convenience that returns a single formatted
// error string suitable for injection into a tool result, or "" if valid.
func ValidateToolArgsBrief(argsJSON string, schema any) string {
	errs := ValidateToolArgs(argsJSON, schema)
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, "- "+e.Error())
	}
	return "Tool arguments failed schema validation:\n" + strings.Join(parts, "\n") +
		"\nPlease call the tool again with corrected arguments, or answer from your own knowledge."
}

func validateAgainst(errs *[]error, path string, val any, schemaNode any) {
	schema, ok := schemaNode.(map[string]any)
	if !ok {
		return // not a schema object; nothing we can check
	}

	// Type check.
	if t, ok := schema["type"]; ok {
		if !typeMatches(val, t) {
			*errs = append(*errs, fmt.Errorf("%s: expected type %v, got %T", displayPath(path), t, val))
			return // further checks would cascade from a type mismatch
		}
	}

	// Enum check.
	if e, ok := schema["enum"]; ok {
		if enum, ok := e.([]any); ok && len(enum) > 0 {
			match := false
			for _, candidate := range enum {
				if deepEqual(val, candidate) {
					match = true
					break
				}
			}
			if !match {
				*errs = append(*errs, fmt.Errorf("%s: value %v not in enum", displayPath(path), val))
			}
		}
	}

	switch v := val.(type) {
	case map[string]any:
		validateObject(errs, path, v, schema)
	case []any:
		validateArray(errs, path, v, schema)
	}
}

func validateObject(errs *[]error, path string, obj map[string]any, schema map[string]any) {
	// Required fields.
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if _, present := obj[name]; !present {
				*errs = append(*errs, fmt.Errorf("%s: missing required field %q", displayPath(path), name))
			}
		}
	}

	properties, _ := schema["properties"].(map[string]any)

	// additionalProperties: false -> reject unknown fields.
	if ap, ok := schema["additionalProperties"]; ok {
		if allow, isBool := ap.(bool); isBool && !allow {
			for key := range obj {
				if _, declared := properties[key]; !declared {
					*errs = append(*errs, fmt.Errorf("%s: unexpected field %q (additionalProperties is false)", displayPath(path), key))
				}
			}
		}
	}

	// Recurse into known properties.
	for key, child := range obj {
		if propSchema, ok := properties[key]; ok {
			validateAgainst(errs, joinPath(path, key), child, propSchema)
		}
	}
}

func validateArray(errs *[]error, path string, arr []any, schema map[string]any) {
	itemSchema, ok := schema["items"]
	if !ok {
		return
	}
	for i, el := range arr {
		validateAgainst(errs, fmt.Sprintf("%s[%d]", displayPath(path), i), el, itemSchema)
	}
}

// typeMatches handles JSON Schema's tolerant type semantics:
//   - "integer" accepts float64 values with zero fractional part (JSON
//     numbers round-trip through float64 by default).
//   - "number" accepts any numeric.
//   - A []any type schema means "union of these types".
func typeMatches(val any, want any) bool {
	if want == "any" {
		return true
	}
	if types, ok := want.([]any); ok {
		for _, t := range types {
			if typeMatches(val, t) {
				return true
			}
		}
		return false
	}
	t, ok := want.(string)
	if !ok {
		return true // unknown type keyword shape -- be permissive
	}
	switch t {
	case "object":
		_, ok := val.(map[string]any)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "null":
		return val == nil
	case "number":
		_, ok := val.(float64)
		if ok {
			return true
		}
		_, ok = val.(json.Number)
		return ok
	case "integer":
		if f, ok := val.(float64); ok {
			return f == float64(int64(f))
		}
		if n, ok := val.(json.Number); ok {
			_, err := n.Int64()
			return err == nil
		}
		return false
	}
	return true // unknown type name -- be permissive
}

func deepEqual(a, b any) bool {
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ba) == string(bb)
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func displayPath(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}
