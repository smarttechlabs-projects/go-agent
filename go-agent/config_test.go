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
	"os"
	"path/filepath"
	"testing"
)

func TestDetectToolCallStyle(t *testing.T) {
	tests := []struct {
		model string
		want  ToolCallStyle
	}{
		{"Qwen3-Coder-30B-A3B-Instruct-GGUF", ToolCallText},
		{"qwen3-8b", ToolCallText},
		{"QWEN-something", ToolCallText},
		{"Llama-xLAM-2-8b-fc-r-Hybrid", ToolCallNative},
		{"mistral-7b-instruct", ToolCallNative},
		{"gpt-4o", ToolCallNative},
		{"hermes-2-pro", ToolCallNative},
		{"functionary-v2.5", ToolCallNative},
		{"unknown-model-xyz", ToolCallNative}, // default
	}

	for _, tt := range tests {
		got := detectToolCallStyle(tt.model)
		if got != tt.want {
			t.Errorf("detectToolCallStyle(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestLoadConfig_Minimal(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")

	configJSON := `{
		"model": "test-model",
		"endpointUrl": "http://localhost:9999/api/",
		"servers": []
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Model != "test-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "test-model")
	}
	// Endpoint should be normalized to /v1.
	if cfg.EndpointURL != "http://localhost:9999/api/v1" {
		t.Errorf("EndpointURL = %q, want .../api/v1", cfg.EndpointURL)
	}
	// Defaults should be applied.
	if cfg.MaxResultLen != defaultMaxResultLen {
		t.Errorf("MaxResultLen = %d, want %d", cfg.MaxResultLen, defaultMaxResultLen)
	}
	if cfg.MaxToolRounds != defaultMaxToolRounds {
		t.Errorf("MaxToolRounds = %d, want %d", cfg.MaxToolRounds, defaultMaxToolRounds)
	}
}

func TestLoadConfig_WithPromptFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")
	promptPath := filepath.Join(dir, "PROMPT.md")

	os.WriteFile(configPath, []byte(`{"model":"m","endpointUrl":"http://localhost:8000/api/","servers":[]}`), 0644)
	os.WriteFile(promptPath, []byte("Custom prompt content"), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.SystemPrompt != "Custom prompt content" {
		t.Errorf("SystemPrompt = %q, want 'Custom prompt content'", cfg.SystemPrompt)
	}
}

func TestLoadConfig_EnvExpansion(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")

	t.Setenv("TEST_LEMONADE_MODEL", "env-model")
	configJSON := `{"model":"${TEST_LEMONADE_MODEL}","endpointUrl":"http://localhost:8000/api/","servers":[]}`
	os.WriteFile(configPath, []byte(configJSON), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Model != "env-model" {
		t.Errorf("Model = %q, want 'env-model'", cfg.Model)
	}
}

func TestLoadConfig_OverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")

	configJSON := `{
		"model": "m",
		"endpointUrl": "http://localhost:8000/api/",
		"servers": [],
		"maxResultLen": 32000,
		"maxToolRounds": 5,
		"toolCallStyle": "text",
		"llmsTxt": "prefer"
	}`
	os.WriteFile(configPath, []byte(configJSON), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.MaxResultLen != 32000 {
		t.Errorf("MaxResultLen = %d, want 32000", cfg.MaxResultLen)
	}
	if cfg.MaxToolRounds != 5 {
		t.Errorf("MaxToolRounds = %d, want 5", cfg.MaxToolRounds)
	}
	if cfg.ToolCallStyle != ToolCallText {
		t.Errorf("ToolCallStyle = %q, want 'text'", cfg.ToolCallStyle)
	}
	if cfg.LLMsTxt != LLMsTxtPrefer {
		t.Errorf("LLMsTxt = %q, want 'prefer'", cfg.LLMsTxt)
	}
}
