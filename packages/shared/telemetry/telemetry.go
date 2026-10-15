// Package telemetry wires OpenTelemetry traces and metrics. When OTEL_EXPORTER_OTLP_ENDPOINT is
// set, spans and metrics are exported over OTLP/gRPC; otherwise the global no-op providers stay
// in place (zero overhead in tests and local dev).
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup installs global tracer/meter providers for service and returns a shutdown func.
func Setup(ctx context.Context, service, version string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(service), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	te, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	me, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(15*time.Second))), sdkmetric.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	slog.Info("telemetry: OTLP export enabled", "service", service)
	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}
