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
	"context"
	"testing"
	"time"
)

// mockAgent is a minimal Agent substitute for queue tests.
// We can't use the real Agent (needs LLM + MCP servers), so we test
// the queue mechanics directly.

func TestJob_WaitCompletes(t *testing.T) {
	job := &Job{
		ID:     "test-1",
		Status: JobQueued,
		done:   make(chan struct{}),
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		job.Status = JobCompleted
		job.Answer = "test answer"
		close(job.done)
	}()

	err := job.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if job.Status != JobCompleted {
		t.Errorf("status = %q, want completed", job.Status)
	}
}

func TestJob_WaitCancelled(t *testing.T) {
	job := &Job{
		ID:     "test-2",
		Status: JobQueued,
		done:   make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := job.Wait(ctx)
	if err == nil {
		t.Fatal("Wait should have returned context error")
	}
}
