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
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "llm-agent"

// tracer is the package-level tracer instance.
var tracer trace.Tracer

func init() {
	// Default to a no-op tracer until initTracer is called.
	tracer = otel.Tracer(tracerName)
}

// initTracer sets up OpenTelemetry tracing with an OTLP HTTP exporter.
// Returns a shutdown function that must be called before exit.
// If endpoint is empty, returns a no-op shutdown (tracing disabled).
func initTracer(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	if endpoint == "" {
		return noop, nil
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return noop, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(tracerName),
			semconv.ServiceVersion(version),
		)),
	)

	otel.SetTracerProvider(tp)
	tracer = tp.Tracer(tracerName)

	return tp.Shutdown, nil
}

// Span attribute keys used throughout the agent.
var (
	attrModel        = attribute.Key("llm.model")
	attrMessageCount = attribute.Key("llm.message_count")
	attrToolCount    = attribute.Key("llm.tool_count")
	attrContextChars = attribute.Key("llm.context_chars")
	attrFinishReason = attribute.Key("llm.finish_reason")
	attrHasToolCalls = attribute.Key("llm.has_tool_calls")
	attrContentLen   = attribute.Key("llm.content_length")

	attrToolName      = attribute.Key("tool.name")
	attrToolArgs      = attribute.Key("tool.arguments")
	attrToolResultLen = attribute.Key("tool.result_length")
	attrToolTruncated = attribute.Key("tool.truncated")

	attrRound    = attribute.Key("agent.round")
	attrMaxRound = attribute.Key("agent.max_rounds")

	attrMCPServer    = attribute.Key("mcp.server")
	attrMCPToolCount = attribute.Key("mcp.tool_count")
)

// startSpan begins a new span and returns it with a derived context.
func startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, name,
		trace.WithAttributes(attrs...),
		trace.WithTimestamp(time.Now()),
	)
	return ctx, span
}
