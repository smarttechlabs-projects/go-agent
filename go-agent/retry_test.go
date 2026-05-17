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
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{"nil", "", false},
		{"429", "http 429 too many requests", true},
		{"503", "HTTP error: 503 Service Unavailable", true},
		{"504", "HTTP 504 gateway timeout", true},
		{"connection refused", "dial tcp: connection refused", true},
		{"connection reset", "connection reset by peer", true},
		{"EOF mid-stream", "unexpected EOF", true},
		{"i/o timeout", "net/http: i/o timeout", true},
		{"401 not transient", "HTTP 401 unauthorized", false},
		{"403 not transient", "HTTP 403 forbidden", false},
		{"500 not transient", "HTTP 500 internal server error", false},
		{"plain parse error not transient", "unexpected character in JSON", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.err != "" {
				err = errors.New(tt.err)
			}
			got := isTransientError(err)
			if got != tt.want {
				t.Errorf("isTransientError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// Context errors are NEVER transient -- a caller who gave up shouldn't be
// held hostage by retry loops.
func TestIsTransientError_ContextErrorsNotRetryable(t *testing.T) {
	if isTransientError(context.Canceled) {
		t.Error("context.Canceled treated as transient")
	}
	if isTransientError(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded treated as transient")
	}
}

func TestBackoffDuration_Bounds(t *testing.T) {
	// Attempt 0: ~500ms (400-600 with jitter)
	d := backoffDuration(0)
	if d < 400*time.Millisecond || d > 600*time.Millisecond {
		t.Errorf("attempt 0 backoff = %v, want ~500ms", d)
	}
	// Attempt 5: clamped to ~10s
	d = backoffDuration(5)
	if d < 8*time.Second || d > 12*time.Second {
		t.Errorf("attempt 5 backoff = %v, want ~10s (clamped)", d)
	}
	// Attempt 20: still clamped
	d = backoffDuration(20)
	if d > 12*time.Second {
		t.Errorf("attempt 20 backoff = %v, expected clamp around 10s", d)
	}
}

func TestRetry_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), 3, func(attempt int) error {
		calls++
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry needed)", calls)
	}
}

func TestRetry_RetriesOnTransient(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retryWithBackoff(context.Background(), 3, func(attempt int) error {
		calls++
		if attempt < 2 {
			return errors.New("HTTP 503 service unavailable")
		}
		return nil
	}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (fail, fail, succeed)", calls)
	}
	// Should have waited ~500ms + ~1000ms = 1.5s total with jitter.
	if elapsed < 1200*time.Millisecond {
		t.Errorf("elapsed = %v, want >=1.2s (backoff should have fired)", elapsed)
	}
}

func TestRetry_NonTransientAbortsImmediately(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retryWithBackoff(context.Background(), 3, func(attempt int) error {
		calls++
		return errors.New("HTTP 401 unauthorized")
	}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (non-transient should not retry)", calls)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, non-transient should abort fast", elapsed)
	}
}

func TestRetry_GivesUpAfterMax(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), 2, func(attempt int) error {
		calls++
		return errors.New("HTTP 503 service unavailable")
	}, nil)
	if err == nil {
		t.Fatal("expected last error to propagate")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (maxAttempts)", calls)
	}
}

func TestRetry_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()

	// Cancel after 100ms, while the retry wrapper is in the backoff sleep.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := retryWithBackoff(ctx, 5, func(attempt int) error {
		calls++
		return errors.New("HTTP 503")
	}, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	// Should have bailed out within ~500ms, well before a full 5-attempt cycle.
	if elapsed > 800*time.Millisecond {
		t.Errorf("elapsed = %v, want <800ms (cancellation should interrupt backoff sleep)", elapsed)
	}
	if calls < 1 {
		t.Error("op should have been called at least once")
	}
}

func TestRetry_MaxAttemptsZeroNormalizedToOne(t *testing.T) {
	calls := 0
	_ = retryWithBackoff(context.Background(), 0, func(attempt int) error {
		calls++
		return errors.New("transient: 503")
	}, nil)
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (zero attempts normalized to 1)", calls)
	}
}
