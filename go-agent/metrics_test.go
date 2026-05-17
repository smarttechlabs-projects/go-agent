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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installManualMeter swaps in a meter backed by a ManualReader so tests can
// inspect emitted instrument values. Restores the previous meter on cleanup.
func installManualMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	prevMeter := meter
	prev := otel.GetMeterProvider()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	meter = mp.Meter(meterName)
	buildInstruments()

	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
		meter = prevMeter
		buildInstruments()
	})

	return reader
}

// collect pulls a fresh snapshot from the reader.
func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	return rm
}

// findCounter looks up a counter by name and returns the sum across all data
// points. Returns -1 if the instrument was not recorded against.
func findCounter(rm metricdata.ResourceMetrics, name string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	return -1
}

// histogramCount returns the total count of observations across all data
// points for the named histogram instrument. -1 if absent.
func histogramCount(rm metricdata.ResourceMetrics, name string) uint64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			var total uint64
			for _, dp := range h.DataPoints {
				total += dp.Count
			}
			return total
		}
	}
	return 0
}

func TestRecordQuery(t *testing.T) {
	reader := installManualMeter(t)

	recordQuery(context.Background(), "success", 250*time.Millisecond)
	recordQuery(context.Background(), "success", 175*time.Millisecond)
	recordQuery(context.Background(), "max_rounds", 800*time.Millisecond)

	rm := collect(t, reader)

	if got := findCounter(rm, "agent_queries_total"); got != 3 {
		t.Errorf("agent_queries_total = %d, want 3", got)
	}
	if got := histogramCount(rm, "agent_query_duration_seconds"); got != 3 {
		t.Errorf("agent_query_duration_seconds observations = %d, want 3", got)
	}
}

func TestRecordLLMCall(t *testing.T) {
	reader := installManualMeter(t)

	recordLLMCall(context.Background(), "openai", "ok", 500*time.Millisecond)
	recordLLMCall(context.Background(), "openai", "error", 50*time.Millisecond)
	recordLLMCall(context.Background(), "gemini", "ok", 300*time.Millisecond)

	rm := collect(t, reader)

	if got := findCounter(rm, "llm_calls_total"); got != 3 {
		t.Errorf("llm_calls_total = %d, want 3", got)
	}
	if got := histogramCount(rm, "llm_call_duration_seconds"); got != 3 {
		t.Errorf("llm_call_duration_seconds observations = %d, want 3", got)
	}
}

func TestRecordToolCall(t *testing.T) {
	reader := installManualMeter(t)

	recordToolCall(context.Background(), "check_port", "ok", 10*time.Millisecond)
	recordToolCall(context.Background(), "check_port", "ok", 12*time.Millisecond)
	recordToolCall(context.Background(), "fetch", "error", 5*time.Millisecond)

	rm := collect(t, reader)

	if got := findCounter(rm, "tool_calls_total"); got != 3 {
		t.Errorf("tool_calls_total = %d, want 3", got)
	}
	if got := histogramCount(rm, "tool_call_duration_seconds"); got != 3 {
		t.Errorf("tool_call_duration_seconds observations = %d, want 3", got)
	}
}

func TestRecordersAreSafeWithNoopMeter(t *testing.T) {
	// No installManualMeter — instruments are whatever package init produced
	// against the global no-op provider. The recorders must not panic.
	recordQuery(context.Background(), "success", time.Millisecond)
	recordLLMCall(context.Background(), "openai", "ok", time.Millisecond)
	recordToolCall(context.Background(), "fetch", "ok", time.Millisecond)
}

func TestInitMetricsAllDisabledIsNoop(t *testing.T) {
	handler, shutdown, err := initMetrics(context.Background(), "", false)
	if err != nil {
		t.Fatalf("initMetrics(\"\", false) error = %v, want nil", err)
	}
	if handler != nil {
		t.Errorf("expected nil handler when Prometheus disabled, got %T", handler)
	}
	if shutdown == nil {
		t.Fatal("initMetrics returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("noop shutdown returned error: %v", err)
	}
}

func TestInitMetricsPrometheusOnly(t *testing.T) {
	handler, shutdown, err := initMetrics(context.Background(), "", true)
	if err != nil {
		t.Fatalf("initMetrics(\"\", true) error = %v", err)
	}
	defer shutdown(context.Background())
	if handler == nil {
		t.Fatal("expected non-nil handler when Prometheus enabled")
	}
	// Exercise the recorders so the exposition has data.
	recordQuery(context.Background(), "completed", 100*time.Millisecond)
	recordLLMCall(context.Background(), "openai", "ok", 50*time.Millisecond)
	recordToolCall(context.Background(), "check_port", "ok", 5*time.Millisecond)

	req := httptest.NewRequest("GET", "/api/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"agent_queries_total",
		"agent_query_duration_seconds",
		"llm_calls_total",
		"tool_calls_total",
		"# TYPE",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Prometheus output missing %q\nfull body (first 500 bytes):\n%s", want, body[:min(500, len(body))])
		}
	}
}
