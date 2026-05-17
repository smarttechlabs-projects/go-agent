// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

// mcp-server-ports: A narrow MCP server that exposes local network port information.
//
// Tools:
//   - list_listening_ports: Show all listening TCP/UDP ports with process info
//   - check_port:          Check if a specific port is in use and by what process
//
// This server is read-only and cannot execute arbitrary commands.
// It demonstrates the "narrow server" approach to MCP -- each server does one
// thing well, with minimal attack surface.
//
// Supports two transports:
//   - stdio (default): reads JSON-RPC from stdin, writes to stdout. For local use.
//   - sse:             HTTP server with SSE transport. For network/remote access.
//
// Usage:
//
//	go build -o mcp-server-ports .
//
//	# Local (stdio, for agent.json):
//	# { "type": "stdio", "config": { "command": "./mcp-server-ports" } }
//
//	# Network (SSE, for remote MCP clients):
//	./mcp-server-ports --transport sse --port 4100
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const serverVersion = "1.0.0"

// --- JSON-RPC 2.0 types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// --- MCP types ---

type mcpToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// --- Port info ---

type portInfo struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	State    string `json:"state"`
	PID      int    `json:"pid,omitempty"`
	Process  string `json:"process,omitempty"`
}

// --- Tool definitions ---

var tools = []mcpToolDef{
	{
		Name:        "list_listening_ports",
		Description: "List all listening TCP and UDP ports on the local machine with process names and PIDs. The result is complete -- no further lookups are needed. Just report the table to the user.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":          map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		Name:        "check_port",
		Description: "Check if a specific port number is in use and which process is using it. Returns the protocol, address, PID, and process name. The result is a complete answer -- just report it to the user, no further lookups are needed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"port": map[string]any{
					"type":        "number",
					"description": "The port number to check (1-65535)",
				},
			},
			"required":             []string{"port"},
			"additionalProperties": false,
		},
	},
}

func main() {
	transport := flag.String("transport", "stdio", "transport mode: stdio or sse")
	port := flag.Int("port", 4100, "HTTP port for SSE transport")
	flag.Parse()

	switch *transport {
	case "stdio":
		runStdio()
	case "sse":
		runSSE(*port)
	default:
		fmt.Fprintf(os.Stderr, "unknown transport: %s (use stdio or sse)\n", *transport)
		os.Exit(1)
	}
}

// --- stdio transport (local, for agent.json) ---

func runStdio() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		resp := handleRequest(req)
		if resp.ID == nil && resp.Error == nil && resp.Result == nil {
			continue // Notification, no response.
		}
		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
	}
}

// --- SSE transport (network, for remote MCP clients) ---
//
// MCP SSE transport protocol:
//   GET  /sse       → SSE stream (server-to-client messages)
//                     First event: "endpoint" with URL for sending messages
//   POST /message   → Client sends JSON-RPC requests here
//                     Response is sent via the SSE stream

type sseSession struct {
	id     string
	events chan []byte
}

var (
	sessions   sync.Map
	sessionCtr atomic.Int64
)

func runSSE(port int) {
	mux := http.NewServeMux()

	mux.HandleFunc("/sse", handleSSEConnect)
	mux.HandleFunc("/message", handleSSEMessage)

	// Health endpoint for discovery.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "mcp-server-ports",
			"version":   serverVersion,
			"transport": "sse",
			"tools":     len(tools),
		})
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Fprintf(os.Stderr, "mcp-server-ports: SSE transport on http://%s\n", addr)
	fmt.Fprintf(os.Stderr, "  SSE endpoint:     http://%s/sse\n", addr)
	fmt.Fprintf(os.Stderr, "  Message endpoint: http://%s/message\n", addr)
	fmt.Fprintf(os.Stderr, "  Health:           http://%s/health\n", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func handleSSEConnect(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Create a session for this client.
	id := fmt.Sprintf("session-%d", sessionCtr.Add(1))
	session := &sseSession{
		id:     id,
		events: make(chan []byte, 64),
	}
	sessions.Store(id, session)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send the endpoint event -- tells the client where to POST messages.
	messageURL := fmt.Sprintf("http://%s/message?sessionId=%s", r.Host, id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messageURL)
	flusher.Flush()

	fmt.Fprintf(os.Stderr, "  [%s] client connected\n", id)

	// Stream events to the client until they disconnect.
	for {
		select {
		case data, ok := <-session.events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			flusher.Flush()
		case <-r.Context().Done():
			sessions.Delete(id)
			fmt.Fprintf(os.Stderr, "  [%s] client disconnected\n", id)
			return
		}
	}
}

func handleSSEMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	sessionVal, ok := sessions.Load(sessionID)
	if !ok {
		http.Error(w, "invalid session", http.StatusBadRequest)
		return
	}
	session := sessionVal.(*sseSession)

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
		return
	}

	resp := handleRequest(req)

	// Notifications (no ID) don't get a response.
	if resp.ID == nil && resp.Error == nil && resp.Result == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Send the response via the SSE stream.
	out, _ := json.Marshal(resp)

	select {
	case session.events <- out:
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "session buffer full", http.StatusServiceUnavailable)
	}
}

func handleRequest(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "mcp-server-ports",
					"version": serverVersion,
				},
			},
		}

	case "notifications/initialized":
		// No response needed for notifications.
		return jsonRPCResponse{}

	case "tools/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": tools,
			},
		}

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorResponse(req.ID, -32602, "invalid params")
		}
		return handleToolCall(req.ID, params.Name, params.Arguments)

	default:
		return errorResponse(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func handleToolCall(id any, name string, args map[string]any) jsonRPCResponse {
	switch name {
	case "list_listening_ports":
		return callListPorts(id)
	case "check_port":
		portNum, _ := args["port"].(float64)
		return callCheckPort(id, int(portNum))
	default:
		return errorResponse(id, -32602, fmt.Sprintf("unknown tool: %s", name))
	}
}

func callListPorts(id any) jsonRPCResponse {
	ports, err := getListeningPorts()
	if err != nil {
		return toolResult(id, fmt.Sprintf("Error getting port info: %v", err), true)
	}

	if len(ports) == 0 {
		return toolResult(id, "No listening ports found.", false)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-25s %-6s %-12s %-8s %s\n", "PROTO", "ADDRESS", "PORT", "STATE", "PID", "PROCESS")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 80))
	for _, p := range ports {
		pid := ""
		if p.PID > 0 {
			pid = strconv.Itoa(p.PID)
		}
		fmt.Fprintf(&b, "%-6s %-25s %-6d %-12s %-8s %s\n",
			p.Protocol, p.Address, p.Port, p.State, pid, p.Process)
	}
	fmt.Fprintf(&b, "\nTotal: %d listening ports", len(ports))

	return toolResult(id, b.String(), false)
}

func callCheckPort(id any, port int) jsonRPCResponse {
	if port < 1 || port > 65535 {
		return toolResult(id, fmt.Sprintf("Invalid port number: %d (must be 1-65535)", port), true)
	}

	ports, err := getListeningPorts()
	if err != nil {
		return toolResult(id, fmt.Sprintf("Error checking port: %v", err), true)
	}

	var matches []portInfo
	for _, p := range ports {
		if p.Port == port {
			matches = append(matches, p)
		}
	}

	if len(matches) == 0 {
		return toolResult(id, fmt.Sprintf("Port %d is free (not in use by any listening process).", port), false)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Port %d is in use:\n\n", port)
	hasProcessInfo := false
	for _, p := range matches {
		fmt.Fprintf(&b, "  Protocol: %s\n", p.Protocol)
		fmt.Fprintf(&b, "  Address:  %s:%d\n", p.Address, p.Port)
		fmt.Fprintf(&b, "  State:    %s\n", p.State)
		if p.PID > 0 {
			fmt.Fprintf(&b, "  PID:      %d\n", p.PID)
			hasProcessInfo = true
		}
		if p.Process != "" {
			fmt.Fprintf(&b, "  Process:  %s\n", p.Process)
		}
		b.WriteString("\n")
	}
	if !hasProcessInfo {
		fmt.Fprintf(&b, "Note: Process info not available (port is owned by another user). Run with sudo for full details.\n")
	}

	return toolResult(id, b.String(), false)
}

// getListeningPorts uses `ss` to get listening port information.
func getListeningPorts() ([]portInfo, error) {
	// Try ss first (modern Linux), fall back to netstat.
	out, err := exec.Command("ss", "-tlnpH").CombinedOutput()
	if err != nil {
		// Try netstat as fallback.
		out, err = exec.Command("netstat", "-tlnp").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("neither ss nor netstat available: %v", err)
		}
		return parseNetstat(string(out))
	}

	ports := parseSS(string(out), "tcp")

	// Also get UDP listeners.
	out, err = exec.Command("ss", "-ulnpH").CombinedOutput()
	if err == nil {
		udp := parseSS(string(out), "udp")
		ports = append(ports, udp...)
	}

	// Fill in missing process info via /proc (works without root for own processes).
	resolveProcesses(ports)

	return ports, nil
}

// resolveProcesses attempts to find PID and process name for ports where
// ss didn't report them (happens without root). Uses /proc/net/tcp to find
// the socket inode, then scans /proc/*/fd/ to find the owning PID.
func resolveProcesses(ports []portInfo) {
	// Build a map of local_port -> inode from /proc/net/tcp and /proc/net/tcp6.
	portInodes := make(map[int]string)
	for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		f, err := os.Open(procFile)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Scan() // skip header
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				continue
			}
			// fields[1] = local_address (hex), fields[3] = state, fields[9] = inode
			localAddr := fields[1]
			state := fields[3]
			inode := fields[9]

			// State 0A = LISTEN for TCP; for UDP, all states are interesting.
			if state != "0A" && !strings.Contains(procFile, "udp") {
				continue
			}
			if inode == "0" {
				continue
			}

			// Parse port from hex: local_address is "ADDR:PORT" in hex.
			parts := strings.Split(localAddr, ":")
			if len(parts) != 2 {
				continue
			}
			portHex := parts[1]
			portNum, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil {
				continue
			}
			portInodes[int(portNum)] = inode
		}
		f.Close()
	}

	// Build inode -> PID map by scanning /proc/*/fd/.
	inodePID := make(map[string]int)
	inodeProc := make(map[string]string)
	procDirs, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fdPath := range procDirs {
		link, err := os.Readlink(fdPath)
		if err != nil {
			continue
		}
		// Links look like "socket:[12345]"
		if !strings.HasPrefix(link, "socket:[") {
			continue
		}
		inode := link[8 : len(link)-1]
		// Extract PID from path: /proc/PID/fd/N
		parts := strings.Split(fdPath, "/")
		if len(parts) < 4 {
			continue
		}
		pid, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		inodePID[inode] = pid

		// Read process name from /proc/PID/comm.
		if name, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
			inodeProc[inode] = strings.TrimSpace(string(name))
		}
	}

	// Fill in missing process info.
	for i := range ports {
		if ports[i].PID > 0 {
			continue // Already have process info.
		}
		inode, ok := portInodes[ports[i].Port]
		if !ok {
			continue
		}
		if pid, ok := inodePID[inode]; ok {
			ports[i].PID = pid
			ports[i].Process = inodeProc[inode]
		}
	}
}

// parseSS parses the output of `ss -tlnpH` or `ss -ulnpH`.
func parseSS(output, proto string) []portInfo {
	var ports []portInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		state := fields[0]
		local := fields[3]

		// Parse address:port.
		addr, portStr := splitHostPort(local)
		portNum, _ := strconv.Atoi(portStr)

		p := portInfo{
			Protocol: proto,
			Address:  addr,
			Port:     portNum,
			State:    state,
		}

		// Extract process info from the users column (last field with "users:").
		for _, f := range fields {
			if strings.HasPrefix(f, "users:") {
				p.Process, p.PID = parseSSUsers(f)
			}
		}

		if portNum > 0 {
			ports = append(ports, p)
		}
	}
	return ports
}

// parseSSUsers extracts process name and PID from ss users field.
// Format: users:(("process",pid=123,fd=4))
func parseSSUsers(s string) (string, int) {
	// Extract quoted process name.
	start := strings.Index(s, "((\"")
	end := strings.Index(s, "\",")
	name := ""
	if start >= 0 && end > start {
		name = s[start+3 : end]
	}

	// Extract PID.
	pidIdx := strings.Index(s, "pid=")
	pid := 0
	if pidIdx >= 0 {
		rest := s[pidIdx+4:]
		if comma := strings.IndexAny(rest, ",)"); comma >= 0 {
			pid, _ = strconv.Atoi(rest[:comma])
		}
	}

	return name, pid
}

// parseNetstat parses `netstat -tlnp` output as fallback.
func parseNetstat(output string) ([]portInfo, error) {
	var ports []portInfo
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] != "tcp" && fields[0] != "tcp6" && fields[0] != "udp" && fields[0] != "udp6" {
			continue
		}

		proto := fields[0]
		if strings.HasPrefix(proto, "tcp") {
			proto = "tcp"
		} else {
			proto = "udp"
		}

		local := fields[3]
		addr, portStr := splitHostPort(local)
		portNum, _ := strconv.Atoi(portStr)

		p := portInfo{
			Protocol: proto,
			Address:  addr,
			Port:     portNum,
			State:    "LISTEN",
		}

		// PID/Program is the last field.
		if len(fields) >= 7 {
			pidProg := fields[6]
			parts := strings.SplitN(pidProg, "/", 2)
			if len(parts) == 2 {
				p.PID, _ = strconv.Atoi(parts[0])
				p.Process = parts[1]
			}
		}

		if portNum > 0 {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// splitHostPort splits "addr:port" or "[addr]:port" or "*:port".
func splitHostPort(s string) (string, string) {
	// Handle IPv6 [::]:port
	if idx := strings.LastIndex(s, "]"); idx >= 0 {
		if idx+1 < len(s) && s[idx+1] == ':' {
			return s[:idx+1], s[idx+2:]
		}
		return s, ""
	}
	// Handle addr:port
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

func toolResult(id any, text string, isError bool) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: mcpCallResult{
			Content: []mcpContent{{Type: "text", Text: text}},
			IsError: isError,
		},
	}
}

func errorResponse(id any, code int, msg string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]any{
			"code":    code,
			"message": msg,
		},
	}
}
