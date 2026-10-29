// Package telemetry wires structured logs plus OpenTelemetry traces and metrics,
// both pushed over OTLP/gRPC. The logger stamps trace_id/span_id from the active
// span; metrics are pushed, never scraped.
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

// WithMetrics pushes metrics over OTLP/gRPC every interval and starts Go runtime
// instrumentation. interval is the floor on dashboard staleness.
func WithMetrics(endpointURL string, interval time.Duration) Option {
	return func(o *options) {
		o.metrics = &metricsOptions{endpointURL: endpointURL, interval: interval}
	}
}

// WithBuild stamps link-time build provenance onto the OTel resource, surfacing
// as target_info{service_version, vcs_ref_head_revision}. Empty values are omitted.
func WithBuild(version, revision string) Option {
	return func(o *options) {
		o.build = &buildOptions{version: version, revision: revision}
	}
}

// NewSampler builds a sampler from an OTEL_TRACES_SAMPLER value. arg is the ratio
// for traceidratio strategies; an unknown name is an error, not a fallback.
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

// Setup installs the global tracer and meter providers; the returned function
// flushes and shuts them down. Absent signals leave OTel's no-op in place. If
// any step after a provider is built fails, the built providers are shut down
// before the error is returned so no exporter is left running.
func Setup(ctx context.Context, serviceName, env string, opts ...Option) (shutdown func(context.Context) error, err error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.DeploymentEnvironmentName(env),
	}
	// Becomes the metrics backend's instance label. Scraping derived it from
	// the target address; pushing, only the process can say who it is, and
	// without it every replica collapses onto one series.
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
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, fn := range shutdowns {
			_ = fn(cleanupCtx)
		}
	}()

	var tracerProvider *sdktrace.TracerProvider
	var meterProvider *sdkmetric.MeterProvider

	// Traces: OTLP over gRPC (Tempo or any OTLP collector).
	if t := o.tracing; t != nil {
		traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(t.endpointURL))
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(t.sampler),
		)
		shutdowns = append(shutdowns, tracerProvider.Shutdown)
	}

	// Metrics: OTLP over gRPC on a fixed interval.
	if m := o.metrics; m != nil {
		metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(m.endpointURL))
		if err != nil {
			return nil, fmt.Errorf("create otlp metric exporter: %w", err)
		}
		meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
				sdkmetric.WithInterval(m.interval),
			)),
			sdkmetric.WithResource(res),
		)
		shutdowns = append(shutdowns, meterProvider.Shutdown)

		// Explicit because no client_golang registry contributes a Go
		// collector here: without this, goroutine count and heap size do not
		// exist at all.
		if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
			return nil, fmt.Errorf("start runtime metrics: %w", err)
		}
	}

	// Install globals only after every exporter is ready: a partial failure
	// must not leave the process pointed at a provider this function will shut
	// down before returning.
	if tracerProvider != nil {
		otel.SetTracerProvider(tracerProvider)
	}
	if meterProvider != nil {
		otel.SetMeterProvider(meterProvider)
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown = func(ctx context.Context) error {
		var errs error
		for _, fn := range shutdowns {
			errs = errors.Join(errs, fn(ctx))
		}
		return errs
	}
	return shutdown, nil
}
