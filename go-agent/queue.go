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
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// JobStatus represents the state of a queued job.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Job represents a queued agent query.
type Job struct {
	ID        string       `json:"id"`
	Query     string       `json:"query"`
	Status    JobStatus    `json:"status"`
	Answer    string       `json:"answer,omitempty"`
	Error     string       `json:"error,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	StartedAt time.Time    `json:"started_at,omitempty"`
	DoneAt    time.Time    `json:"done_at,omitempty"`
	Position  int          `json:"position,omitempty"` // Queue position (0 = running)
	SessionID string       `json:"session_id,omitempty"`
	Limits    *AgentLimits `json:"-"` // Per-query overrides, not serialized.

	// Structured termination info (populated on completion).
	TerminationReason string  `json:"termination_reason,omitempty"`
	Details           string  `json:"details,omitempty"`
	RoundsUsed        int     `json:"rounds_used,omitempty"`
	TokensUsed        int     `json:"tokens_used,omitempty"`
	PromptTokens      int     `json:"prompt_tokens,omitempty"`
	CompletionTokens  int     `json:"completion_tokens,omitempty"`
	ToolCallsMade     int     `json:"tool_calls_made,omitempty"`
	ElapsedMs         int64   `json:"elapsed_ms,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	EarlyStopped      bool    `json:"early_stopped,omitempty"`

	done chan struct{} // Closed when job is finished.
}

// Wait blocks until the job completes or the context is cancelled.
func (j *Job) Wait(ctx context.Context) error {
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// JobQueue processes agent queries sequentially with a bounded queue.
type JobQueue struct {
	agent    *Agent
	maxSize  int
	jobs     chan *Job
	registry sync.Map // id -> *Job
	counter  atomic.Int64
	log      *Logger
}

// NewJobQueue creates a queue with the given max pending size.
func NewJobQueue(agent *Agent, maxSize int, log *Logger) *JobQueue {
	q := &JobQueue{
		agent:   agent,
		maxSize: maxSize,
		jobs:    make(chan *Job, maxSize),
		log:     log,
	}
	go q.worker()
	return q
}

// Submit adds a job to the queue. Returns the job (with ID and position)
// or an error if the queue is full.
func (q *JobQueue) Submit(query string, limits *AgentLimits, sessionID string) (*Job, error) {
	id := fmt.Sprintf("job-%d-%d", time.Now().Unix(), q.counter.Add(1))
	job := &Job{
		ID:        id,
		Query:     query,
		Status:    JobQueued,
		CreatedAt: time.Now(),
		SessionID: sessionID,
		Limits:    limits,
		done:      make(chan struct{}),
	}

	select {
	case q.jobs <- job:
		job.Position = len(q.jobs)
		q.registry.Store(id, job)
		q.log.Info("queued job %s (position %d/%d)", id, job.Position, q.maxSize)
		return job, nil
	default:
		return nil, fmt.Errorf("queue full (%d/%d)", len(q.jobs), q.maxSize)
	}
}

// Get returns a job by ID.
func (q *JobQueue) Get(id string) *Job {
	if v, ok := q.registry.Load(id); ok {
		return v.(*Job)
	}
	return nil
}

// QueueLen returns the current number of pending jobs.
func (q *JobQueue) QueueLen() int {
	return len(q.jobs)
}

// worker processes jobs sequentially.
func (q *JobQueue) worker() {
	for job := range q.jobs {
		job.Status = JobRunning
		job.StartedAt = time.Now()
		job.Position = 0
		q.log.Info("processing job %s: %s", job.ID, truncateLog(job.Query, 60))

		// Route through a session so concurrent jobs don't corrupt each other's
		// history (matters when Submit is called with different SessionIDs).
		session := q.agent.GetOrCreateSession(job.SessionID)
		job.SessionID = session.ID // Echo back auto-generated id if one was made.
		result := session.QueryDetailed(context.Background(), job.Query, job.Limits)

		job.DoneAt = time.Now()
		job.TerminationReason = result.TerminationReason
		job.Details = result.Details
		job.RoundsUsed = result.RoundsUsed
		job.TokensUsed = result.TokensUsed
		job.PromptTokens = result.PromptTokens
		job.CompletionTokens = result.CompletionTokens
		job.ToolCallsMade = result.ToolCallsMade
		job.ElapsedMs = result.ElapsedMs
		job.CostUSD = result.CostUSD
		job.EarlyStopped = result.EarlyStopped
		if result.Error != "" {
			job.Status = JobFailed
			job.Error = result.Error
			q.log.Error("job %s failed (%s): %s", job.ID, result.TerminationReason, result.Error)
		} else {
			job.Status = JobCompleted
			job.Answer = result.Answer
			q.log.Info("job %s completed (%s, %d chars, %d rounds, %d tokens)",
				job.ID, result.TerminationReason, len(result.Answer), result.RoundsUsed, result.TokensUsed)
		}
		close(job.done)

		// Update positions for remaining queued jobs.
		q.updatePositions()
	}
}

func (q *JobQueue) updatePositions() {
	pos := 1
	q.registry.Range(func(key, value any) bool {
		job := value.(*Job)
		if job.Status == JobQueued {
			job.Position = pos
			pos++
		}
		return true
	})
}
