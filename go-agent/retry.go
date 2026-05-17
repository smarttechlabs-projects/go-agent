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
	mathrand "math/rand"
	"strings"
	"time"
)

// isTransientError reports whether an LLM/HTTP error is worth retrying.
//
// Retryable:  429, 502, 503, 504, connection refused/reset, EOF, i/o timeout.
// Not retryable: terminal auth errors (caller should check isTerminalError
// first), and context errors (caller gave up -- don't eat their cancellation).
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	// Never retry if the caller cancelled or the query-level deadline fired.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	markers := []string{
		// HTTP status codes that typically indicate transient failures.
		"429", " 502", " 503", " 504",
		"too many requests",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		// Network-layer transients.
		"connection refused",
		"connection reset",
		"broken pipe",
		"no route to host",
		"i/o timeout",
		"temporary failure",
		"temporarily unavailable",
		// Short reads / mid-stream disconnects.
		"eof",
		"unexpected eof",
	}
	for _, m := range markers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// backoffDuration returns an exponential backoff with ±20% jitter. Starts at
// 500ms on attempt 0 and doubles to a 10s cap.
func backoffDuration(attempt int) time.Duration {
	base := 500 * time.Millisecond
	d := base << attempt
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	// ±20% jitter. math/rand is fine here; we're not using it for security.
	j := time.Duration(mathrand.Int63n(int64(d) / 5))
	return d - d/10 + j
}

// retryWithBackoff invokes op up to maxAttempts times, retrying on transient
// errors with exponential backoff. Honors ctx cancellation both during the
// op and between attempts. Returns the first error the op returns if that
// error is non-transient, or the last error if all attempts fail.
//
// maxAttempts must be >= 1; the op runs at least once.
func retryWithBackoff(ctx context.Context, maxAttempts int, op func(attempt int) error, log *Logger) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientError(err) {
			return err
		}
		if attempt == maxAttempts-1 {
			break
		}
		wait := backoffDuration(attempt)
		if log != nil {
			log.Warn("retry %d/%d in %s: %v", attempt+1, maxAttempts, wait.Round(10*time.Millisecond), err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}
