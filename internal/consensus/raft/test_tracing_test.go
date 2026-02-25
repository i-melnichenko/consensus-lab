package raft

import "go.opentelemetry.io/otel/trace/noop"

var (
	testTracer  = noop.NewTracerProvider().Tracer("test/internal/consensus/raft")
	testMetrics = noopMetrics{}
)
