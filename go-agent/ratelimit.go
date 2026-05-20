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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IPLimiter is a per-IP token-bucket rate limiter. Each client IP gets its
// own bucket refilled continuously at `rate` tokens/second, capped at
// `burst`. Requests that exhaust the bucket get HTTP 429 with Retry-After.
//
// Buckets are allocated on first use and garbage-collected after an idle
// timeout so long-running servers don't accumulate state for one-off
// clients.
type IPLimiter struct {
	rate       float64 // tokens per second
	burst      float64 // max bucket size
	mu         sync.Mutex
	buckets    map[string]*bucket
	lastReap   time.Time
	reapWindow time.Duration
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// NewIPLimiter constructs a limiter that allows up to `perMinute` requests
// per IP on average, with a burst allowance of up to the same count. Pass
// perMinute <= 0 to disable rate limiting (all requests pass).
func NewIPLimiter(perMinute int) *IPLimiter {
	if perMinute <= 0 {
		return &IPLimiter{} // disabled
	}
	return &IPLimiter{
		rate:       float64(perMinute) / 60.0,
		burst:      float64(perMinute),
		buckets:    make(map[string]*bucket),
		reapWindow: 10 * time.Minute,
		lastReap:   time.Now(),
	}
}

// Enabled reports whether the limiter is active. When disabled, Allow
// always returns (true, 0).
func (l *IPLimiter) Enabled() bool {
	return l.rate > 0
}

// Allow consumes one token for the given IP and returns (true, 0) on
// success or (false, retryAfter) when the bucket is empty.
func (l *IPLimiter) Allow(ip string) (bool, time.Duration) {
	if !l.Enabled() {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.reapIdleLocked(now)

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, lastFill: now}
		l.buckets[ip] = b
	}
	// Continuous refill.
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
	b.lastFill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// Need (1 - tokens) more to get to 1.
	retryAfter := time.Duration((1-b.tokens)/l.rate*float64(time.Second)) + 100*time.Millisecond
	return false, retryAfter
}

// reapIdleLocked drops buckets that haven't been touched in reapWindow.
// Called under lock at most once per minute.
func (l *IPLimiter) reapIdleLocked(now time.Time) {
	if now.Sub(l.lastReap) < time.Minute {
		return
	}
	l.lastReap = now
	for ip, b := range l.buckets {
		if now.Sub(b.lastFill) > l.reapWindow {
			delete(l.buckets, ip)
		}
	}
}

// Wrap returns an http.Handler that applies rate limiting before invoking
// `next`. Rejected requests get 429 + Retry-After.
func (l *IPLimiter) Wrap(next http.HandlerFunc) http.HandlerFunc {
	if !l.Enabled() {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		ok, retry := l.Allow(ip)
		if !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())+1))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"rate limit exceeded","retry_after_seconds":%d}`, int(retry.Seconds())+1)
			return
		}
		next(w, r)
	}
}

// clientIP extracts the caller's IP, honoring X-Forwarded-For from a
// trusted reverse proxy. The agent doesn't know which proxies are trusted,
// so this prefers the LAST entry in X-Forwarded-For (the closest hop) when
// set, and falls back to the TCP remote. Operators behind a proxy should
// configure their proxy to append rather than replace XFF.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

