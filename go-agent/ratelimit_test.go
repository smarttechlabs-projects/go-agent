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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPLimiter_DisabledAlwaysAllows(t *testing.T) {
	l := NewIPLimiter(0)
	if l.Enabled() {
		t.Error("limiter should be disabled")
	}
	for i := 0; i < 1000; i++ {
		ok, _ := l.Allow("1.2.3.4")
		if !ok {
			t.Fatal("disabled limiter denied a request")
		}
	}
}

func TestIPLimiter_AllowsUpToBurst(t *testing.T) {
	l := NewIPLimiter(60) // burst = 60, rate = 1/sec
	for i := 0; i < 60; i++ {
		ok, _ := l.Allow("1.2.3.4")
		if !ok {
			t.Fatalf("denied at %d, burst should be 60", i)
		}
	}
	// 61st should fail.
	ok, retry := l.Allow("1.2.3.4")
	if ok {
		t.Error("61st request unexpectedly allowed")
	}
	if retry <= 0 || retry > 2*time.Second {
		t.Errorf("retry = %v, want ~1s", retry)
	}
}

func TestIPLimiter_IPsIndependent(t *testing.T) {
	l := NewIPLimiter(60)
	// Drain IP A.
	for i := 0; i < 60; i++ {
		l.Allow("A")
	}
	if ok, _ := l.Allow("A"); ok {
		t.Error("A should be drained")
	}
	// B should be untouched.
	if ok, _ := l.Allow("B"); !ok {
		t.Error("B denied despite independent bucket")
	}
}

func TestIPLimiter_Refill(t *testing.T) {
	// 120/min = 2/sec. Drain burst, wait for refill.
	l := NewIPLimiter(120)
	for i := 0; i < 120; i++ {
		l.Allow("x")
	}
	if ok, _ := l.Allow("x"); ok {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(1500 * time.Millisecond)
	// Should have refilled ~3 tokens; one request succeeds.
	if ok, _ := l.Allow("x"); !ok {
		t.Error("refill did not restore capacity")
	}
}

func TestIPLimiter_Wrap_RejectsWith429(t *testing.T) {
	l := NewIPLimiter(1) // strict: 1/min, burst=1
	handler := l.Wrap(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First request passes.
	req1 := httptest.NewRequest("POST", "/x", nil)
	req1.RemoteAddr = "10.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	handler(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request code = %d, want 200", rec1.Code)
	}

	// Second gets 429 with Retry-After.
	req2 := httptest.NewRequest("POST", "/x", nil)
	req2.RemoteAddr = "10.0.0.1:5678"
	rec2 := httptest.NewRecorder()
	handler(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second request code = %d, want 429", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	// With multiple hops, we want the LAST entry (closest proxy).
	if ip := clientIP(req); ip != "5.6.7.8" {
		t.Errorf("clientIP = %q, want 5.6.7.8", ip)
	}
}

func TestClientIP_Fallback(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:55555"
	if ip := clientIP(req); ip != "192.168.1.1" {
		t.Errorf("clientIP = %q, want 192.168.1.1", ip)
	}
}
