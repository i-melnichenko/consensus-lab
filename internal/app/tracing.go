package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func (a *App) initTracing(ctx context.Context) (func(context.Context) error, error) {
	if !a.config.TracingEnabled {
		return func(context.Context) error { return nil }, nil
	}

	endpoint := strings.TrimSpace(a.config.TracingEndpoint)
	exporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("init tracing exporter: %w", err)
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			attribute.String("service.name", a.config.TracingServiceName),
			attribute.String("service.instance.id", a.config.NodeID),
			attribute.String("consensus.type", string(a.config.ConsensusType)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("init tracing resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	a.logger.Info(
		"tracing enabled",
		"exporter", "otlp/grpc",
		"endpoint", endpoint,
		"service_name", a.config.TracingServiceName,
	)

	return tp.Shutdown, nil
}
