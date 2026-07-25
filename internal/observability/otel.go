// Package observability wires OpenTelemetry tracing (OTLP/gRPC) and metrics
// (Prometheus exporter served on /metrics).
package observability

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Option enables optional telemetry pipelines. Metrics are always on;
// tracing only when WithTracing is passed.
type Option func(*options)

type options struct {
	tracing *tracingOptions
}

type tracingOptions struct {
	endpoint    string
	insecure    bool
	sampleRatio float64
}

// WithTracing exports spans over OTLP/gRPC to endpoint. Passing this option
// is what turns tracing on — the caller decides from its own config whether
// to include it.
func WithTracing(endpoint string, insecure bool, sampleRatio float64) Option {
	return func(o *options) {
		o.tracing = &tracingOptions{endpoint: endpoint, insecure: insecure, sampleRatio: sampleRatio}
	}
}

// Setup installs global tracer and meter providers. The returned function
// flushes and shuts down every provider; call it on process exit.
func Setup(ctx context.Context, serviceName, env string, opts ...Option) (func(context.Context) error, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.DeploymentEnvironmentName(env),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	var shutdowns []func(context.Context) error

	// Metrics: Prometheus exporter registers with the default registry,
	// exposed by promhttp.Handler on /metrics.
	promExporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(promExporter),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)
	shutdowns = append(shutdowns, meterProvider.Shutdown)

	// Traces: OTLP over gRPC (Jaeger, Tempo, or any OTLP collector).
	if t := o.tracing; t != nil {
		traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(t.endpoint)}
		if t.insecure {
			traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		}
		traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		tracerProvider := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(t.sampleRatio))),
		)
		otel.SetTracerProvider(tracerProvider)
		shutdowns = append(shutdowns, tracerProvider.Shutdown)
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(ctx context.Context) error {
		var errs error
		for _, fn := range shutdowns {
			errs = errors.Join(errs, fn(ctx))
		}
		return errs
	}
	return shutdown, nil
}
