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
	"os"
	"path/filepath"
	"strings"
)

// ServerConfig describes one MCP server entry in agent.json.
type ServerConfig struct {
	Type   string       `json:"type"`
	Config ServerDetail `json:"config"`
}

// ServerDetail holds the command, args, and optional env for an MCP server.
type ServerDetail struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env,omitempty"`
	AllowTools []string          `json:"allowTools,omitempty"`
}

// ToolCallStyle controls how tool calls are extracted from LLM responses.
type ToolCallStyle string

const (
	// ToolCallAuto detects the style from the model name.
	ToolCallAuto ToolCallStyle = "auto"
	// ToolCallNative uses the structured tool_calls field (OpenAI-compatible).
	ToolCallNative ToolCallStyle = "native"
	// ToolCallText parses tool calls from <function=...> markup in content.
	ToolCallText ToolCallStyle = "text"
)

// LLMsTxtMode controls how llms.txt files are used.
type LLMsTxtMode string

const (
	LLMsTxtAuto   LLMsTxtMode = "auto"   // Check llms.txt first, fall back to normal fetch
	LLMsTxtPrefer LLMsTxtMode = "prefer" // Use only llms.txt, skip full site scraping
	LLMsTxtIgnore LLMsTxtMode = "ignore" // Never check llms.txt
)

// LLMProvider identifies the LLM backend type.
type LLMProvider string

const (
	ProviderAuto      LLMProvider = "auto"      // Detect from endpoint URL
	ProviderOpenAI    LLMProvider = "openai"    // OpenAI-compatible (Lemonade, LM Studio, vLLM, Ollama, OpenAI, Groq, etc.)
	ProviderGemini    LLMProvider = "gemini"    // Google Gemini API
	ProviderAnthropic LLMProvider = "anthropic" // Anthropic Claude API
)

// AgentConfig is the top-level configuration loaded from agent.json.
type AgentConfig struct {
	Model         string         `json:"model"`
	EndpointURL   string         `json:"endpointUrl"`
	ApiKey        string         `json:"apiKey,omitempty"`
	Provider      LLMProvider    `json:"provider,omitempty"`
	Servers       []ServerConfig `json:"servers"`
	SystemPrompt  string         `json:"systemPrompt,omitempty"`
	ToolCallStyle  ToolCallStyle  `json:"toolCallStyle,omitempty"`
	LLMsTxt        LLMsTxtMode   `json:"llmsTxt,omitempty"`
	MaxResultLen    int           `json:"maxResultLen,omitempty"`
	MaxToolRounds   int           `json:"maxToolRounds,omitempty"`
	MaxTokenBudget  int           `json:"maxTokenBudget,omitempty"`  // Hard cap on total tokens per query (0 = unlimited)
	TimeoutSeconds  int           `json:"timeoutSeconds,omitempty"`  // Wall-clock timeout per query (0 = unlimited)
	LoopFingerprint int           `json:"loopFingerprint,omitempty"` // Stop if same tool call repeats N times (0 = disabled)
	EarlyStop       bool          `json:"earlyStop,omitempty"`       // If true, synthesize a final answer when max rounds hit

	// History management (per-session).
	MaxHistoryChars  int `json:"maxHistoryChars,omitempty"`  // Approx cap on total conversation chars; oldest non-system turns drop when exceeded (0 = unlimited). Uses char count as a proxy for tokens (1 tok ≈ 4 chars).
	KeepRecentTurns  int `json:"keepRecentTurns,omitempty"`  // Minimum recent turns to retain during trim (default 4 round-trips).
	ToolTimeoutSecs  int `json:"toolTimeoutSeconds,omitempty"` // Per-tool call timeout (0 = inherit wall-clock timeout).

	// Pricing table for cost tracking. Keyed by model name; optional.
	// When a query's model has an entry, cost_usd is computed and
	// surfaced in the response. Local-only backends typically leave this
	// empty (cost stays zero). Rates are USD per 1M tokens.
	Pricing map[string]ModelPricing `json:"pricing,omitempty"`

	// Human-in-the-loop tool approval. Tools whose names match any of
	// these glob patterns (path.Match syntax) pause before dispatch and
	// wait for a POST /api/v1/approvals/{id} decision. Empty disables.
	RequireApproval []string `json:"requireApproval,omitempty"`

	// Seconds to wait for an approval decision before auto-denying
	// (fail-safe default). 0 uses defaultApprovalTimeoutSecs.
	ApprovalTimeoutSecs int `json:"approvalTimeoutSeconds,omitempty"`
}

// ModelPricing describes per-million-token rates for a single model.
// Separate input/output rates match commercial-API pricing conventions.
type ModelPricing struct {
	PromptPerMTokens     float64 `json:"promptPerM"`
	CompletionPerMTokens float64 `json:"completionPerM"`
}

const defaultSystemPrompt = `You are a helpful local AI assistant with web browsing, file, and fetch tools.

To search the web, navigate to Google and read the results:
1. browser_navigate to https://www.google.com/search?q=your+search+terms
2. browser_snapshot to read the search results
3. fetch or browser_navigate to read a specific result URL

Guidelines:
- Prefer fetch when you already have a URL. Use the browser for search and JS-heavy pages.
- Be transparent about sources. Be concise.
- Confirm before overwriting files.
- All processing is local. No API keys needed.`

// LoadConfig reads an agent.json file, expands environment variables, and
// resolves the system prompt.
func LoadConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	expanded := os.ExpandEnv(string(data))

	var cfg AgentConfig
	if err := json.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Normalize endpoint: ensure it ends with /v1 for the OpenAI-compatible API.
	cfg.EndpointURL = strings.TrimRight(cfg.EndpointURL, "/")
	if strings.HasSuffix(cfg.EndpointURL, "/api") {
		cfg.EndpointURL += "/v1"
	} else if !strings.HasSuffix(cfg.EndpointURL, "/v1") && !strings.HasSuffix(cfg.EndpointURL, "/api/v1") {
		cfg.EndpointURL += "/v1"
	}

	if cfg.SystemPrompt == "" {
		promptPath := filepath.Join(filepath.Dir(path), "PROMPT.md")
		if promptData, err := os.ReadFile(promptPath); err == nil {
			cfg.SystemPrompt = string(promptData)
		} else {
			cfg.SystemPrompt = defaultSystemPrompt
		}
	}

	// Resolve tool call style.
	if cfg.ToolCallStyle == "" {
		cfg.ToolCallStyle = ToolCallAuto
	}
	if cfg.ToolCallStyle == ToolCallAuto {
		cfg.ToolCallStyle = detectToolCallStyle(cfg.Model)
	}

	// Apply defaults for optional fields.
	if cfg.MaxResultLen <= 0 {
		cfg.MaxResultLen = defaultMaxResultLen
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = defaultMaxToolRounds
	}
	if cfg.MaxTokenBudget <= 0 {
		cfg.MaxTokenBudget = defaultMaxTokenBudget
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultTimeoutSeconds
	}
	if cfg.LoopFingerprint <= 0 {
		cfg.LoopFingerprint = defaultLoopFingerprint
	}
	if cfg.MaxHistoryChars <= 0 {
		cfg.MaxHistoryChars = defaultMaxHistoryChars
	}
	if cfg.KeepRecentTurns <= 0 {
		cfg.KeepRecentTurns = defaultKeepRecentTurns
	}
	if cfg.ToolTimeoutSecs <= 0 {
		cfg.ToolTimeoutSecs = defaultToolTimeoutSecs
	}

	// Resolve API key: config > env var > default.
	if cfg.ApiKey == "" {
		if key := os.Getenv("LLM_API_KEY"); key != "" {
			cfg.ApiKey = key
		} else if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cfg.ApiKey = key
		} else if key := os.Getenv("GEMINI_API_KEY"); key != "" {
			cfg.ApiKey = key
		} else if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			cfg.ApiKey = key
		} else {
			cfg.ApiKey = "not-set" // Local servers don't need a key.
		}
	}

	// Resolve provider from endpoint URL.
	if cfg.Provider == "" || cfg.Provider == ProviderAuto {
		cfg.Provider = detectProvider(cfg.EndpointURL)
	}

	return &cfg, nil
}

// detectProvider guesses the LLM provider from the endpoint URL.
func detectProvider(endpoint string) LLMProvider {
	lower := strings.ToLower(endpoint)
	if strings.Contains(lower, "generativelanguage.googleapis.com") ||
		strings.Contains(lower, "gemini") {
		return ProviderGemini
	}
	if strings.Contains(lower, "anthropic.com") {
		return ProviderAnthropic
	}
	return ProviderOpenAI
}

// Model name patterns mapped to tool call styles.
// Models that use text-based <function=...> markup instead of structured tool_calls.
var textStylePatterns = []string{
	"qwen",
}

// Models known to support native OpenAI-compatible structured tool_calls.
var nativeStylePatterns = []string{
	"gpt-",
	"llama",
	"mistral",
	"hermes",
	"functionary",
	"gorilla",
	"nexus",
	"xlam",
	"firefunction",
}

// detectToolCallStyle guesses the tool call style from the model name.
func detectToolCallStyle(model string) ToolCallStyle {
	lower := strings.ToLower(model)
	for _, p := range textStylePatterns {
		if strings.Contains(lower, p) {
			return ToolCallText
		}
	}
	for _, p := range nativeStylePatterns {
		if strings.Contains(lower, p) {
			return ToolCallNative
		}
	}
	// Default: try native first, fall back to text parsing at runtime.
	return ToolCallNative
}
