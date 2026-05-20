// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

// File: metrics.go -- OpenTelemetry metrics that pair with otel.go's tracing.
//
// Six instruments, three hot paths:
//
//	Agent query    : agent_queries_total          {termination_reason}
//	                 agent_query_duration_seconds (histogram)
//	LLM call       : llm_calls_total              {provider, status}
//	                 llm_call_duration_seconds    (histogram, {provider})
//	MCP tool call  : tool_calls_total             {tool, status}
//	                 tool_call_duration_seconds   (histogram, {tool})
//
// All instruments degrade to no-ops until initMetrics is called. The same
// -otel-endpoint flag is reused for both trace and metric export.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"github.com/prometheus/client_golang/prometheus"
)

const meterName = "llm-agent"

// Package-level meter and instruments. init() seeds them as no-ops via the
// global no-op MeterProvider; initMetrics swaps in a real OTLP exporter.
var (
	meter metric.Meter

	mAgentQueries     metric.Int64Counter
	mAgentQueryLat    metric.Float64Histogram
	mLLMCalls         metric.Int64Counter
	mLLMCallLat       metric.Float64Histogram
	mToolCalls        metric.Int64Counter
	mToolCallLat      metric.Float64Histogram
)

func init() {
	meter = otel.Meter(meterName)
	buildInstruments()
}

// buildInstruments (re)creates instrument handles against the current meter.
// Called once at init (no-op meter) and again inside initMetrics after the
// real MeterProvider is installed. Errors are silenced: an instrument that
// fails to register simply records nothing.
func buildInstruments() {
	mAgentQueries, _ = meter.Int64Counter(
		"agent_queries_total",
		metric.WithDescription("Total agent queries handled, labelled by termination reason."),
	)
	mAgentQueryLat, _ = meter.Float64Histogram(
		"agent_query_duration_seconds",
		metric.WithDescription("Wall-clock duration of a Session.QueryDetailed call."),
		metric.WithUnit("s"),
	)
	mLLMCalls, _ = meter.Int64Counter(
		"llm_calls_total",
		metric.WithDescription("Total LLM chat-completion calls, labelled by provider and status."),
	)
	mLLMCallLat, _ = meter.Float64Histogram(
		"llm_call_duration_seconds",
		metric.WithDescription("Duration of a single LLM call (includes retries)."),
		metric.WithUnit("s"),
	)
	mToolCalls, _ = meter.Int64Counter(
		"tool_calls_total",
		metric.WithDescription("Total MCP tool calls, labelled by tool name and status."),
	)
	mToolCallLat, _ = meter.Float64Histogram(
		"tool_call_duration_seconds",
		metric.WithDescription("Duration of a single MCP tool invocation."),
		metric.WithUnit("s"),
	)
}

// initMetrics sets up the metric pipeline. Two readers can be attached:
//
//   - OTLP/HTTP push reader, active when otlpEndpoint != "" (15s cadence).
//   - Prometheus pull reader, active when enablePrometheus is true; exposes
//     scrapable metrics via the returned http.Handler.
//
// If both are off the instruments stay no-op. The returned http.Handler is
// nil when Prometheus is disabled. The shutdown function is always non-nil.
func initMetrics(ctx context.Context, otlpEndpoint string, enablePrometheus bool) (http.Handler, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	if otlpEndpoint == "" && !enablePrometheus {
		return nil, noop, nil
	}

	opts := []sdkmetric.Option{
		sdkmetric.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(meterName),
			semconv.ServiceVersion(version),
		)),
	}

	if otlpEndpoint != "" {
		exporter, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpoint(otlpEndpoint),
			otlpmetrichttp.WithInsecure(),
		)
		if err != nil {
			return nil, noop, fmt.Errorf("creating OTLP metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(15*time.Second),
		)))
	}

	// Prometheus exporter is itself a Reader; it gets registered with a
	// dedicated Prometheus registry so we can hand the matching Handler back.
	var promHandler http.Handler
	if enablePrometheus {
		registry := prometheus.NewRegistry()
		promReader, err := otelprom.New(otelprom.WithRegisterer(registry))
		if err != nil {
			return nil, noop, fmt.Errorf("creating Prometheus exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(promReader))
		promHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		})
	}

	mp := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(mp)
	meter = mp.Meter(meterName)
	buildInstruments()

	return promHandler, mp.Shutdown, nil
}

// recordQuery captures a completed agent query.
func recordQuery(ctx context.Context, terminationReason string, d time.Duration) {
	if mAgentQueries == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("termination_reason", terminationReason))
	mAgentQueries.Add(ctx, 1, attrs)
	mAgentQueryLat.Record(ctx, d.Seconds(), attrs)
}

// recordLLMCall captures a completed LLM chat-completion call.
func recordLLMCall(ctx context.Context, provider string, status string, d time.Duration) {
	if mLLMCalls == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("provider", provider),
		attribute.String("status", status),
	)
	mLLMCalls.Add(ctx, 1, attrs)
	mLLMCallLat.Record(ctx, d.Seconds(),
		metric.WithAttributes(attribute.String("provider", provider)),
	)
}

// recordToolCall captures a completed MCP tool invocation.
func recordToolCall(ctx context.Context, tool string, status string, d time.Duration) {
	if mToolCalls == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("tool", tool),
		attribute.String("status", status),
	)
	mToolCalls.Add(ctx, 1, attrs)
	mToolCallLat.Record(ctx, d.Seconds(),
		metric.WithAttributes(attribute.String("tool", tool)),
	)
}

