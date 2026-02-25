package raft

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func (n *Node) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	ctx, span := n.tracer.Start(ctx, name)
	span.SetAttributes(attribute.String("raft.node_id", n.id))
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return ctx, span
}

func spanRecordError(span oteltrace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

func (n *Node) tracePersistHardStateLocked(ctx context.Context, reason string) error {
	_, span := n.startSpan(ctx, "raft.storage.SaveHardState", attribute.String("raft.persist.reason", reason))
	defer span.End()
	err := n.persistHardStateLocked()
	spanRecordError(span, err)
	return err
}

func (n *Node) tracePersistAppendLogLocked(ctx context.Context, entries []LogEntry) error {
	_, span := n.startSpan(
		ctx,
		"raft.storage.AppendLog",
		attribute.Int("raft.entries_count", len(entries)),
	)
	defer span.End()
	err := n.persistAppendLogLocked(entries)
	spanRecordError(span, err)
	return err
}

func (n *Node) tracePersistTruncateLogLocked(ctx context.Context, fromIndex int64) error {
	_, span := n.startSpan(
		ctx,
		"raft.storage.TruncateLog",
		attribute.Int64("raft.from_index", fromIndex),
	)
	defer span.End()
	err := n.persistTruncateLogLocked(fromIndex)
	spanRecordError(span, err)
	return err
}

func (n *Node) tracePersistSnapshotLocked(ctx context.Context, snap Snapshot) error {
	_, span := n.startSpan(
		ctx,
		"raft.storage.SaveSnapshot",
		attribute.Int64("raft.snapshot.index", snap.LastIncludedIndex),
		attribute.Int64("raft.snapshot.term", snap.LastIncludedTerm),
		attribute.Int("raft.snapshot.bytes", len(snap.Data)),
	)
	defer span.End()
	err := n.persistSnapshotLocked(snap)
	spanRecordError(span, err)
	return err
}
