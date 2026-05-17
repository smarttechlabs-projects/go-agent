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
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// version is the agent version. Overridden at build time via:
//   go build -ldflags "-X main.version=$(git describe --tags --dirty)"
// The Makefile does this automatically.
var version = "dev"

func main() {
	var (
		configPath   = flag.String("config", "agent.json", "path to agent config file")
		model        = flag.String("model", "", "override model name")
		endpoint     = flag.String("endpoint", "", "override endpoint URL")
		apiKey       = flag.String("api-key", "", "LLM API key (or set LLM_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY env var)")
		provider     = flag.String("provider", "", "LLM provider: auto, openai, gemini (default: auto-detect from endpoint)")
		promptFile   = flag.String("prompt", "", "path to system prompt file")
		toolStyle    = flag.String("tool-style", "", "tool call style: auto, native, text (overrides config/detection)")
		llmsTxt      = flag.String("llms-txt", "", "llms.txt mode: auto (default), prefer, ignore")
		maxResult     = flag.Int("max-result", 0, "max tool result size in chars (default 16000)")
		maxRounds     = flag.Int("max-rounds", 0, "max agent loop rounds (default 10)")
		maxTokens     = flag.Int("max-tokens", 0, "max cumulative tokens per query (default 100000, 0 = unlimited)")
		timeout       = flag.Int("timeout", 0, "wall-clock timeout per query in seconds (default 300, 0 = unlimited)")
		loopDetect    = flag.Int("loop-detect", 0, "abort if same tool call repeats N times (default 3, 0 = disabled)")
		earlyStop     = flag.Bool("early-stop", false, "synthesize a final answer when max rounds/budget hit (instead of erroring)")
		maxHistChars  = flag.Int("max-history-chars", 0, "bound per-session history size; oldest non-system turns drop when exceeded (default 80000 chars, ~20K tokens; 0 = unlimited)")
		keepTurns     = flag.Int("keep-recent-turns", 0, "min recent turns to retain when trimming history (default 4)")
		toolTimeout   = flag.Int("tool-timeout", 0, "per-tool wall-clock timeout in seconds (default 60)")
		stream       = flag.Bool("stream", false, "use streaming API for TTFT metrics (may not work with all servers)")
		verbose      = flag.Bool("verbose", false, "show detailed colorized data flow")
		webAddr      = flag.String("web", "", "start web dashboard on address (e.g. localhost:3131)")
		rateLimit    = flag.Int("rate-limit", 60, "requests per minute per IP on mutating endpoints (0 = unlimited)")
		logFormat    = flag.String("log-format", "text", "log output format: text (colorized, for humans) or json (one object per line, for log aggregators)")
		otelEndpoint = flag.String("otel-endpoint", "", "OTLP HTTP endpoint (e.g. localhost:4318)")
		showVer      = flag.Bool("version", false, "print version and exit")
	)
	flag.BoolVar(verbose, "v", false, "shorthand for -verbose")
	flag.Parse()

	if *showVer {
		fmt.Printf("llm-agent %s\n", version)
		os.Exit(0)
	}

	log := NewLogger(*verbose)
	if *logFormat == "json" {
		log.SetFormat(LogFormatJSON)
	}

	// Top-level context: cancelled only by SIGTERM or double Ctrl+C.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Set up OpenTelemetry tracing.
	shutdownTracer, err := initTracer(rootCtx, *otelEndpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to init tracing: %v\n", err)
	}
	defer shutdownTracer(context.Background())

	// Set up OpenTelemetry metrics. The OTLP push exporter activates when
	// -otel-endpoint is set; a Prometheus pull endpoint at /api/v1/metrics
	// activates when -web is set.
	promHandler, shutdownMetrics, err := initMetrics(rootCtx, *otelEndpoint, *webAddr != "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to init metrics: %v\n", err)
	}
	defer shutdownMetrics(context.Background())

	if *otelEndpoint != "" {
		log.Info("OTLP tracing + metrics enabled → %s", *otelEndpoint)
	}
	if *webAddr != "" && promHandler != nil {
		log.Info("Prometheus metrics enabled → http://%s/api/v1/metrics", *webAddr)
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	if *model != "" {
		cfg.Model = *model
		if *toolStyle == "" && cfg.ToolCallStyle != ToolCallNative && cfg.ToolCallStyle != ToolCallText {
			cfg.ToolCallStyle = detectToolCallStyle(cfg.Model)
		}
	}
	if *endpoint != "" {
		cfg.EndpointURL = *endpoint
	}
	if *apiKey != "" {
		cfg.ApiKey = *apiKey
	}
	if *provider != "" {
		cfg.Provider = LLMProvider(*provider)
	}
	if *toolStyle != "" {
		cfg.ToolCallStyle = ToolCallStyle(*toolStyle)
	}
	if *llmsTxt != "" {
		cfg.LLMsTxt = LLMsTxtMode(*llmsTxt)
	}
	if *maxResult > 0 {
		cfg.MaxResultLen = *maxResult
	}
	if *maxRounds > 0 {
		cfg.MaxToolRounds = *maxRounds
	}
	if *maxTokens > 0 {
		cfg.MaxTokenBudget = *maxTokens
	}
	if *timeout > 0 {
		cfg.TimeoutSeconds = *timeout
	}
	if *loopDetect > 0 {
		cfg.LoopFingerprint = *loopDetect
	}
	if *earlyStop {
		cfg.EarlyStop = true
	}
	if *maxHistChars > 0 {
		cfg.MaxHistoryChars = *maxHistChars
	}
	if *keepTurns > 0 {
		cfg.KeepRecentTurns = *keepTurns
	}
	if *toolTimeout > 0 {
		cfg.ToolTimeoutSecs = *toolTimeout
	}
	if *promptFile != "" {
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading prompt file: %v\n", err)
			os.Exit(1)
		}
		cfg.SystemPrompt = string(data)
	}

	fmt.Printf("llm-agent %s\n", version)
	fmt.Printf("Model:    %s\n", cfg.Model)
	fmt.Printf("Endpoint: %s\n", cfg.EndpointURL)
	fmt.Printf("Provider: %s\n", cfg.Provider)
	fmt.Printf("Tools:    %s\n", cfg.ToolCallStyle)
	fmt.Printf("Servers:  %d configured\n", len(cfg.Servers))

	// Pre-flight check: verify local MCP server binaries exist.
	mcpMissing := false
	for _, sc := range cfg.Servers {
		cmd := sc.Config.Command
		// Skip npx/uvx -- they download on demand.
		if cmd == "npx" || cmd == "uvx" || cmd == "node" || cmd == "python3" || cmd == "python" {
			continue
		}
		// Resolve relative to config file directory.
		binPath := cmd
		if !filepath.IsAbs(binPath) {
			binPath = filepath.Join(filepath.Dir(*configPath), binPath)
		}
		if _, err := os.Stat(binPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: MCP server binary not found: %s\n", binPath)
			fmt.Fprintf(os.Stderr, "  Build it with: cd %s && go build -o %s .\n",
				filepath.Dir(binPath), filepath.Base(binPath))
			mcpMissing = true
		} else {
			label := filepath.Base(binPath)
			fmt.Printf("MCP:      %s (%s)\n", label, binPath)
		}
	}
	if mcpMissing {
		fmt.Fprintf(os.Stderr, "\nRun 'make build' to build all binaries.\n")
		os.Exit(1)
	}

	if *verbose {
		fmt.Printf("Verbose:  enabled\n")
	}
	if *otelEndpoint != "" {
		fmt.Printf("OTLP:     %s\n", *otelEndpoint)
	}

	// Start event bus and web server (dashboard + API) if requested.
	var events *EventBus
	var webSrv *WebServer
	if *webAddr != "" {
		events = NewEventBus()
		var webURL string
		var err error
		webOpts := WebServerOptions{
			RatePerMinute:  *rateLimit,
			MetricsHandler: promHandler,
		}
		if *rateLimit == 0 {
			webOpts.RatePerMinute = -1 // explicit disable
		}
		webSrv, webURL, err = StartWebServerWithOptions(*webAddr, events, log, webOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to start web server: %v\n", err)
		} else {
			fmt.Printf("Web:      %s\n", webURL)
			fmt.Printf("API:      %s/api/v1/query\n", webURL)
			fmt.Printf("MCP SSE:  %s/mcp/sse\n", webURL)
		}
	}
	fmt.Println()

	agent, err := NewAgent(rootCtx, cfg, log, *stream, events)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating agent: %v\n", err)
		os.Exit(1)
	}
	defer agent.Close()

	// Wire the agent into the web server for API access.
	if webSrv != nil {
		webSrv.SetAgent(agent)
	}

	fmt.Printf("Tools:    %d loaded\n", agent.ToolCount())
	fmt.Println()

	// Single query mode: positional args.
	if flag.NArg() > 0 {
		// In single-query mode, Ctrl+C kills the process.
		ctx, cancel := signal.NotifyContext(rootCtx, syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		query := strings.Join(flag.Args(), " ")
		resp, err := agent.Query(ctx, query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(resp)
		return
	}

	// Interactive mode with per-query signal handling.
	runInteractive(rootCtx, rootCancel, agent, log)
}

// runInteractive runs the interactive REPL with proper signal handling:
//   - First Ctrl+C during a query: cancels the query, returns to prompt
//   - Ctrl+C at the prompt (or second Ctrl+C during a query): exits
//   - SIGTERM: exits immediately
func runInteractive(rootCtx context.Context, rootCancel context.CancelFunc, agent *Agent, log *Logger) {
	fmt.Println("Type your message, or /quit to exit, /clear to reset conversation.")
	fmt.Println("Press Ctrl+C to cancel a running query.")
	fmt.Println()

	// Channel for SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Per-query cancellation.
	var queryCancel context.CancelFunc
	var queryMu sync.Mutex

	// Handle signals in a goroutine.
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGTERM {
				fmt.Fprintf(os.Stderr, "\nReceived SIGTERM, shutting down...\n")
				rootCancel()
				return
			}
			// SIGINT (Ctrl+C)
			queryMu.Lock()
			cancel := queryCancel
			queryMu.Unlock()

			if cancel != nil {
				// Cancel the running query.
				fmt.Fprintf(os.Stderr, "\n  ^C (cancelling query...)\n")
				cancel()
			} else {
				// No query running -- exit.
				fmt.Fprintf(os.Stderr, "\n")
				rootCancel()
				return
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		if rootCtx.Err() != nil {
			return
		}

		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		switch input {
		case "/quit", "/exit":
			return
		case "/clear":
			agent.ClearHistory()
			fmt.Println("Conversation cleared.")
			continue
		case "/tools":
			for _, name := range agent.ToolNames() {
				fmt.Printf("  - %s\n", name)
			}
			continue
		case "/help":
			fmt.Println("Commands:")
			fmt.Println("  /quit    exit the agent")
			fmt.Println("  /clear   reset conversation history")
			fmt.Println("  /tools   list available tools")
			fmt.Println("  /help    show this help")
			fmt.Println("  Ctrl+C   cancel running query / exit at prompt")
			continue
		}

		// Create a per-query context that Ctrl+C can cancel.
		queryCtx, cancel := context.WithCancel(rootCtx)
		queryMu.Lock()
		queryCancel = cancel
		queryMu.Unlock()

		resp, err := agent.Query(queryCtx, input)

		// Clear the cancel function so the next Ctrl+C exits.
		queryMu.Lock()
		queryCancel = nil
		queryMu.Unlock()
		cancel()

		if err != nil {
			if rootCtx.Err() != nil {
				return
			}
			if queryCtx.Err() != nil {
				fmt.Fprintf(os.Stderr, "  (query cancelled)\n\n")
				continue
			}
			fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
			continue
		}
		fmt.Println(resp)
		fmt.Println()
	}
}
