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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LLMsTxtCache caches llms.txt lookups per domain to avoid repeated requests.
type LLMsTxtCache struct {
	mu      sync.RWMutex
	entries map[string]*llmsTxtEntry
	mode    LLMsTxtMode
	log     *Logger
	client  *http.Client
}

type llmsTxtEntry struct {
	content string // The llms.txt content (empty if not found).
	found   bool   // Whether the domain has a llms.txt.
	checked bool   // Whether we've already checked this domain.
}

// NewLLMsTxtCache creates a cache for llms.txt lookups.
func NewLLMsTxtCache(mode LLMsTxtMode, log *Logger) *LLMsTxtCache {
	return &LLMsTxtCache{
		entries: make(map[string]*llmsTxtEntry),
		mode:    mode,
		log:     log,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// CheckURL inspects a URL being fetched and returns llms.txt content if
// available and appropriate. Returns:
//   - content, true  -- use this instead of fetching the URL
//   - "", false       -- proceed with normal fetch
func (c *LLMsTxtCache) CheckURL(rawURL string) (string, bool) {
	if c.mode == LLMsTxtIgnore {
		return "", false
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", false
	}

	domain := parsed.Host
	entry := c.getOrFetch(domain, parsed.Scheme)

	if !entry.found {
		return "", false
	}

	// In "prefer" mode, always use llms.txt for any URL on this domain.
	if c.mode == LLMsTxtPrefer {
		c.log.Info("llms.txt: using cached content for %s (mode=prefer)", domain)
		return c.formatResult(domain, entry.content), true
	}

	// In "auto" mode, use llms.txt only for root/homepage requests.
	// For specific deep URLs, let the normal fetch proceed.
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" || path == "/index.html" || path == "/index.htm" {
		c.log.Info("llms.txt: using cached content for %s (root URL, mode=auto)", domain)
		return c.formatResult(domain, entry.content), true
	}

	return "", false
}

// getOrFetch returns the cached entry for a domain, fetching if needed.
func (c *LLMsTxtCache) getOrFetch(domain, scheme string) *llmsTxtEntry {
	c.mu.RLock()
	if entry, ok := c.entries[domain]; ok && entry.checked {
		c.mu.RUnlock()
		return entry
	}
	c.mu.RUnlock()

	// Fetch llms.txt for this domain.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after lock.
	if entry, ok := c.entries[domain]; ok && entry.checked {
		return entry
	}

	entry := &llmsTxtEntry{checked: true}

	if scheme == "" {
		scheme = "https"
	}
	llmsURL := fmt.Sprintf("%s://%s/llms.txt", scheme, domain)

	c.log.Info("llms.txt: checking %s", llmsURL)
	resp, err := c.client.Get(llmsURL)
	if err != nil {
		// Check if this is a network error (offline).
		if isNetworkError(err) {
			c.log.Warn("llms.txt: network error for %s (offline?): %v", domain, err)
		}
		c.entries[domain] = entry
		return entry
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		c.log.Info("llms.txt: not found at %s (%d)", llmsURL, resp.StatusCode)
		c.entries[domain] = entry
		return entry
	}

	// Check content type -- should be text, not HTML.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		c.log.Info("llms.txt: %s returned HTML (not a real llms.txt)", llmsURL)
		c.entries[domain] = entry
		return entry
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024)) // Cap at 200KB.
	if err != nil {
		c.entries[domain] = entry
		return entry
	}

	content := string(body)
	if len(content) < 50 {
		// Too short to be useful.
		c.entries[domain] = entry
		return entry
	}

	entry.content = content
	entry.found = true
	c.log.Info("llms.txt: found at %s (%d chars)", llmsURL, len(content))
	c.entries[domain] = entry
	return entry
}

func (c *LLMsTxtCache) formatResult(domain string, content string) string {
	return fmt.Sprintf("[llms.txt from %s — site-provided summary for AI assistants]\n\n%s", domain, content)
}

// isNetworkError checks if an error is a network connectivity issue.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// Check for DNS, connection refused, timeout errors.
	var netErr *net.OpError
	if ok := false; ok {
		_ = netErr
	}
	errStr := err.Error()
	return strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "dial tcp")
}
