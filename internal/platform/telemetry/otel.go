// Package telemetry wires the three signals this app emits: structured logs
// (NewLogger) plus OpenTelemetry traces and metrics, both pushed over OTLP/gRPC
// to a collector (Setup).
//
// Logging and tracing live together because they are one seam, not two: the
// logger's handler reads the active span to stamp trace_id/span_id, so log
// correlation only works when both are configured consistently.
//
// Metrics are pushed, not scraped: nothing here is readable without a
// reachable collector.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Option enables one telemetry pipeline. Neither signal is on unless its
// option is passed — the caller decides from its own config.
type Option func(*options)

type options struct {
	tracing *tracingOptions
	metrics *metricsOptions
	build   *buildOptions
}

type buildOptions struct {
	version  string
	revision string
}

type tracingOptions struct {
	endpointURL string
	sampler     sdktrace.Sampler
}

type metricsOptions struct {
	endpointURL string
	interval    time.Duration
}

// WithTracing exports spans over OTLP/gRPC to endpointURL, whose scheme selects
// transport security (http:// is insecure). See NewSampler for sampler.
func WithTracing(endpointURL string, sampler sdktrace.Sampler) Option {
	return func(o *options) {
		o.tracing = &tracingOptions{endpointURL: endpointURL, sampler: sampler}
	}
}

// WithMetrics pushes metrics over OTLP/gRPC to endpointURL every interval, and
// starts Go runtime instrumentation (goroutines, heap, GC).
//
// interval is the floor on dashboard staleness: nothing recorded inside one is
// visible until it elapses.
func WithMetrics(endpointURL string, interval time.Duration) Option {
	return func(o *options) {
		o.metrics = &metricsOptions{endpointURL: endpointURL, interval: interval}
	}
}

// WithBuild stamps build provenance onto the OTel resource, surfacing in
// Prometheus as target_info{service_version, vcs_ref_head_revision} — which is
// what ties a regression to a deploy. Values come from link-time -ldflags;
// empty strings leave the attributes off rather than recording "".
func WithBuild(version, revision string) Option {
	return func(o *options) {
		o.build = &buildOptions{version: version, revision: revision}
	}
}

// NewSampler builds a sampler from an OTEL_TRACES_SAMPLER value. arg is the
// ratio for the traceidratio strategies and ignored by the rest. An unknown
// name is an error, not a fallback: sampling governs what evidence exists
// after an incident.
func NewSampler(name string, arg float64) (sdktrace.Sampler, error) {
	switch name {
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(arg), nil
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(arg)), nil
	default:
		return nil, fmt.Errorf("unknown trace sampler %q", name)
	}
}

// Setup installs the global tracer and meter providers. The returned function
// flushes and shuts down every provider; call it on process exit.
//
// A signal whose option is absent installs no provider, leaving OTel's no-op in
// place — so no caller downstream needs a nil check.
func Setup(ctx context.Context, serviceName, env string, opts ...Option) (func(context.Context) error, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.DeploymentEnvironmentName(env),
	}
	// Becomes Prometheus' instance label. Scraping derived it from the target
	// address; pushing, only the process can say who it is, and without it
	// every replica collapses onto one series.
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(hostname))
	}
	if b := o.build; b != nil {
		if b.version != "" {
			attrs = append(attrs, semconv.ServiceVersion(b.version))
		}
		if b.revision != "" {
			attrs = append(attrs, semconv.VCSRefHeadRevision(b.revision))
		}
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	var shutdowns []func(context.Context) error

	// Traces: OTLP over gRPC (Jaeger, Tempo, or any OTLP collector).
	if t := o.tracing; t != nil {
		traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(t.endpointURL))
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		tracerProvider := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(t.sampler),
		)
		otel.SetTracerProvider(tracerProvider)
		shutdowns = append(shutdowns, tracerProvider.Shutdown)
	}

	// Metrics: OTLP over gRPC on a fixed interval.
	if m := o.metrics; m != nil {
		metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(m.endpointURL))
		if err != nil {
			return nil, fmt.Errorf("create otlp metric exporter: %w", err)
		}
		meterProvider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
				sdkmetric.WithInterval(m.interval),
			)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(meterProvider)
		shutdowns = append(shutdowns, meterProvider.Shutdown)

		// Explicit because no client_golang registry contributes a Go
		// collector here: without this, goroutine count and heap size do not
		// exist at all.
		if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
			return nil, fmt.Errorf("start runtime metrics: %w", err)
		}
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
