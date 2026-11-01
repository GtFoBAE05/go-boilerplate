package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type recordingSpanExporter struct {
	shutdown bool
}

func (e *recordingSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}

func (e *recordingSpanExporter) Shutdown(context.Context) error {
	e.shutdown = true
	return nil
}

// A failure after the tracer provider is built must shut it down: leaving a
// batcher running would export to a provider the process no longer uses.
func TestSetupShutsDownBuiltProvidersWhenLaterStepFails(t *testing.T) {
	oldTrace, oldMetric := newTraceExporter, newMetricExporter
	t.Cleanup(func() {
		newTraceExporter = oldTrace
		newMetricExporter = oldMetric
	})

	exporter := &recordingSpanExporter{}
	newTraceExporter = func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) {
		return exporter, nil
	}
	newMetricExporter = func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error) {
		return nil, errors.New("metric exporter unavailable")
	}

	_, err := Setup(context.Background(), "test-service", "test",
		WithTracing("http://localhost:4317", sdktrace.AlwaysSample()),
		WithMetrics("http://localhost:4317", time.Second),
	)
	if err == nil {
		t.Fatal("want a setup error when the metric exporter fails")
	}
	if !exporter.shutdown {
		t.Fatal("want the built tracer provider shut down after a partial setup failure")
	}
}
