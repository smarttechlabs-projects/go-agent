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
	"regexp"
	"strconv"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// Text-based tool call patterns used by models like Qwen that embed tool calls
// in content rather than using structured tool_calls.
//
// Format:
//
//	<function=ToolName>
//	<parameter=paramName>
//	value
//	</parameter>
//	</function>
var (
	reFunctionBlock = regexp.MustCompile(`(?s)<function=(\w+)>(.*?)</function>`)
	reParameter     = regexp.MustCompile(`(?s)<parameter=(\w+)>\s*(.*?)\s*</parameter>`)
)

// parseTextToolCalls extracts tool calls embedded as text in the LLM response
// content. Returns the cleaned content (text before the first tool call) and
// any parsed tool calls. If no text-based tool calls are found, returns the
// original content and nil.
func parseTextToolCalls(content string) (cleanContent string, calls []openai.ToolCall) {
	idx := strings.Index(content, "<function=")
	if idx < 0 {
		return content, nil
	}

	cleanContent = strings.TrimSpace(content[:idx])

	matches := reFunctionBlock.FindAllStringSubmatch(content, -1)
	for i, match := range matches {
		if len(match) < 3 {
			continue
		}
		toolName := match[1]
		body := match[2]

		args := make(map[string]any)
		paramMatches := reParameter.FindAllStringSubmatch(body, -1)
		for _, pm := range paramMatches {
			if len(pm) >= 3 {
				args[pm[1]] = inferType(pm[2])
			}
		}

		argsJSON, err := json.Marshal(args)
		if err != nil {
			continue
		}

		calls = append(calls, openai.ToolCall{
			ID:   fmt.Sprintf("text_call_%d", i),
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      toolName,
				Arguments: string(argsJSON),
			},
		})
	}

	return cleanContent, calls
}

// inferType attempts to parse a string value as a number or boolean,
// returning the typed value for correct JSON serialization.
func inferType(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if s == "null" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
