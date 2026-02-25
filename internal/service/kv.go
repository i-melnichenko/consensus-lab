// Package service contains application services exposed via transports.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
	"github.com/i-melnichenko/consensus-lab/internal/kv"
)

// ErrNotLeader is returned when a write is proposed to a non-leader node.
var ErrNotLeader = errors.New("service: not leader")

// ErrCommitTimeout is returned when a write is accepted for replication but
// does not get committed/applied before the request deadline.
var ErrCommitTimeout = errors.New("service: write not committed before deadline")

// Logger is a minimal structured logger interface, compatible with slog.Logger.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
}

// Metrics captures service-level metric sinks used by KV.
type Metrics interface {
	ObserveKVWaitAppliedDuration(nodeID string, d time.Duration, ok bool)
	ObserveKVStartToApplyDuration(nodeID string, d time.Duration)
	ObserveKVApplyToWakeDuration(nodeID string, d time.Duration)
	AddKVWaitAppliedWakeups(nodeID string, n int)
	IncKVWaitAppliedCall(nodeID string, ok bool)
	IncKVProposalResult(nodeID, result string)
	ObserveKVSnapshotDuration(nodeID string, d time.Duration)
	ObserveKVSnapshotBytes(nodeID string, n int)
	IncKVSnapshot(nodeID, result string)
}

type noopMetrics struct{}

func (noopMetrics) ObserveKVWaitAppliedDuration(string, time.Duration, bool) {}
func (noopMetrics) ObserveKVStartToApplyDuration(string, time.Duration)      {}
func (noopMetrics) ObserveKVApplyToWakeDuration(string, time.Duration)       {}
func (noopMetrics) AddKVWaitAppliedWakeups(string, int)                      {}
func (noopMetrics) IncKVWaitAppliedCall(string, bool)                        {}
func (noopMetrics) IncKVProposalResult(string, string)                       {}
func (noopMetrics) ObserveKVSnapshotDuration(string, time.Duration)          {}
func (noopMetrics) ObserveKVSnapshotBytes(string, int)                       {}
func (noopMetrics) IncKVSnapshot(string, string)                             {}

// KV is the application service that bridges the KV store and consensus layer.
type KV struct {
	consensus consensus.Consensus
	store     *kv.Store
	logger    Logger
	tracer    oteltrace.Tracer
	metrics   Metrics
	nodeID    string
	mu        sync.Mutex

	// SnapshotEvery enables periodic snapshots after this many applied commands.
	// Zero disables service-level snapshot triggering.
	SnapshotEvery uint64

	lastAppliedIndex int64
	appliedSinceSnap uint64
	applyNotifyCh    chan struct{}
	appliedAtByIndex map[int64]time.Time
}

// NewKV creates a KV service backed by the provided consensus engine and store.
func NewKV(c consensus.Consensus, store *kv.Store, logger Logger, tracer oteltrace.Tracer, metrics Metrics, nodeID string) *KV {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	svc := &KV{
		consensus:        c,
		store:            store,
		logger:           logger,
		tracer:           tracer,
		metrics:          metrics,
		nodeID:           nodeID,
		applyNotifyCh:    make(chan struct{}, 1),
		appliedAtByIndex: make(map[int64]time.Time),
	}
	return svc
}

func (s *KV) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := s.tracer.Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return ctx, span
}

func kvSpanRecordError(span oteltrace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

func kvAppliedSinceSnapshotAttr(v uint64) attribute.KeyValue {
	if v > math.MaxInt64 {
		return attribute.Int64("kv.applied_since_snapshot", math.MaxInt64)
	}
	return attribute.Int64("kv.applied_since_snapshot", int64(v))
}

// Get returns a value from the local KV state machine.
func (s *KV) Get(key string) (string, bool) {
	_, span := s.startSpan(context.Background(), "kv.service.Get", attribute.String("kv.key", key))
	defer span.End()
	return s.store.Get(key)
}

// Put proposes a replicated write through consensus.
func (s *KV) Put(ctx context.Context, key, value string) (int64, error) {
	ctx, span := s.startSpan(
		ctx,
		"kv.service.Put",
		attribute.String("kv.key", key),
		attribute.Int("kv.value.bytes", len(value)),
	)
	defer span.End()
	s.logger.Debug("proposing put", "key", key)
	index, err := s.startCommand(ctx, kv.Command{
		Type:  kv.PutCmd,
		Key:   key,
		Value: value,
	})
	if err != nil {
		kvSpanRecordError(span, err)
		return 0, err
	}
	span.SetAttributes(attribute.Int64("raft.log.index", index))
	return index, nil
}

// Delete proposes a replicated delete through consensus.
func (s *KV) Delete(ctx context.Context, key string) (int64, error) {
	ctx, span := s.startSpan(ctx, "kv.service.Delete", attribute.String("kv.key", key))
	defer span.End()
	s.logger.Debug("proposing delete", "key", key)
	index, err := s.startCommand(ctx, kv.Command{
		Type: kv.DeleteCmd,
		Key:  key,
	})
	if err != nil {
		kvSpanRecordError(span, err)
		return 0, err
	}
	span.SetAttributes(attribute.Int64("raft.log.index", index))
	return index, nil
}

// IsLeader reports whether the underlying consensus node is currently leader.
func (s *KV) IsLeader() bool {
	return s.consensus.IsLeader()
}

// RunApplyLoop applies consensus messages to the KV store until ctx is canceled
// or a handler returns an error.
func (s *KV) RunApplyLoop(ctx context.Context) error {
	ch := s.consensus.ApplyCh()
	if ch == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			if err := s.handleApply(ctx, msg); err != nil {
				return err
			}
		}
	}
}

func (s *KV) handleApply(ctx context.Context, msg consensus.ApplyMsg) error {
	if msg.SnapshotValid {
		ctx, span := s.startSpan(
			ctx,
			"kv.service.handleApplySnapshot",
			attribute.Int64("raft.snapshot.index", msg.SnapshotIndex),
			attribute.Int("kv.snapshot.bytes", len(msg.Snapshot)),
		)
		defer span.End()

		s.logger.Debug("restoring state from snapshot",
			"snapshot_index", msg.SnapshotIndex,
		)
		if err := s.store.RestoreSnapshot(ctx, msg.Snapshot); err != nil {
			kvSpanRecordError(span, err)
			return err
		}
		s.mu.Lock()
		s.lastAppliedIndex = msg.SnapshotIndex
		s.appliedSinceSnap = 0
		s.mu.Unlock()
		s.notifyApply()
		s.logger.Debug("snapshot restored",
			"snapshot_index", msg.SnapshotIndex,
		)
		return nil
	}

	if !msg.CommandValid {
		return nil
	}

	ctx, span := s.startSpan(
		ctx,
		"kv.service.handleApplyCommand",
		attribute.Int64("raft.log.index", msg.CommandIndex),
		attribute.Int("kv.command.bytes", len(msg.Command)),
	)
	defer span.End()

	if err := s.store.Apply(ctx, msg.Command); err != nil {
		kvSpanRecordError(span, err)
		return err
	}

	s.mu.Lock()
	s.lastAppliedIndex = msg.CommandIndex
	s.appliedSinceSnap++
	s.appliedAtByIndex[msg.CommandIndex] = time.Now()
	// Best-effort cleanup for timed-out waiters: keep only a recent tail of applied timestamps.
	const appliedAtRetention = int64(4096)
	if cutoff := msg.CommandIndex - appliedAtRetention; cutoff > 0 {
		delete(s.appliedAtByIndex, cutoff)
	}
	appliedSinceSnap := s.appliedSinceSnap
	s.mu.Unlock()
	s.notifyApply()

	s.logger.Debug("command applied",
		"index", msg.CommandIndex,
		"applied_since_snap", appliedSinceSnap,
	)
	span.SetAttributes(kvAppliedSinceSnapshotAttr(appliedSinceSnap))

	if s.SnapshotEvery > 0 && appliedSinceSnap >= s.SnapshotEvery {
		if err := s.snapshot(ctx); err != nil {
			kvSpanRecordError(span, err)
			return err
		}
	}

	return nil
}

func (s *KV) snapshot(ctx context.Context) error {
	ctx, span := s.startSpan(ctx, "kv.service.snapshot")
	defer span.End()
	start := time.Now()

	s.mu.Lock()
	lastAppliedIndex := s.lastAppliedIndex
	appliedSinceSnap := s.appliedSinceSnap
	s.mu.Unlock()
	span.SetAttributes(
		attribute.Int64("raft.log.index", lastAppliedIndex),
		kvAppliedSinceSnapshotAttr(appliedSinceSnap),
	)

	s.logger.Debug("triggering snapshot",
		"last_applied_index", lastAppliedIndex,
		"applied_since_snap", appliedSinceSnap,
	)
	data, err := s.store.Snapshot(ctx)
	if err != nil {
		s.metrics.IncKVSnapshot(s.nodeID, "store_error")
		kvSpanRecordError(span, err)
		return err
	}
	span.SetAttributes(attribute.Int("kv.snapshot.bytes", len(data)))
	s.metrics.ObserveKVSnapshotBytes(s.nodeID, len(data))
	if err := s.consensus.Snapshot(lastAppliedIndex, data); err != nil {
		s.metrics.IncKVSnapshot(s.nodeID, "consensus_error")
		kvSpanRecordError(span, err)
		return err
	}
	s.metrics.ObserveKVSnapshotDuration(s.nodeID, time.Since(start))
	s.metrics.IncKVSnapshot(s.nodeID, "ok")
	s.mu.Lock()
	s.appliedSinceSnap = 0
	s.mu.Unlock()
	return nil
}

func (s *KV) startCommand(ctx context.Context, cmd kv.Command) (int64, error) {
	ctx, span := s.startSpan(
		ctx,
		"kv.service.startCommand",
		attribute.String("kv.command.type", string(cmd.Type)),
		attribute.String("kv.key", cmd.Key),
	)
	defer span.End()

	raw, err := json.Marshal(cmd)
	if err != nil {
		kvSpanRecordError(span, err)
		return 0, err
	}
	span.SetAttributes(attribute.Int("kv.command.bytes", len(raw)))

	index, isLeader := s.consensus.StartCommand(raw)
	if !isLeader {
		s.metrics.IncKVProposalResult(s.nodeID, "not_leader")
		kvSpanRecordError(span, ErrNotLeader)
		return 0, ErrNotLeader
	}
	s.metrics.IncKVProposalResult(s.nodeID, "accepted")
	span.SetAttributes(attribute.Int64("raft.log.index", index))
	s.logger.Debug("command accepted by consensus",
		"index", index,
		"type", cmd.Type,
		"key", cmd.Key,
	)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.waitApplied(ctx, index); err != nil {
		kvSpanRecordError(span, err)
		return 0, err
	}
	return index, nil
}

func (s *KV) waitApplied(ctx context.Context, index int64) error {
	ctx, span := s.startSpan(ctx, "kv.service.waitApplied", attribute.Int64("raft.log.index", index))
	defer span.End()
	start := time.Now()
	wakeups := 0

	for {
		s.mu.Lock()
		applied := s.lastAppliedIndex
		appliedAt := s.appliedAtByIndex[index]
		s.mu.Unlock()
		span.SetAttributes(attribute.Int64("kv.last_applied_index", applied))
		if applied >= index {
			span.SetAttributes(attribute.Bool("kv.wait_applied.done", true))
			total := time.Since(start)
			s.metrics.ObserveKVWaitAppliedDuration(s.nodeID, total, true)
			s.metrics.AddKVWaitAppliedWakeups(s.nodeID, wakeups)
			s.metrics.IncKVWaitAppliedCall(s.nodeID, true)
			if !appliedAt.IsZero() {
				now := time.Now()
				if !appliedAt.Before(start) {
					s.metrics.ObserveKVStartToApplyDuration(s.nodeID, appliedAt.Sub(start))
				}
				if !now.Before(appliedAt) {
					s.metrics.ObserveKVApplyToWakeDuration(s.nodeID, now.Sub(appliedAt))
				}
				s.mu.Lock()
				delete(s.appliedAtByIndex, index)
				s.mu.Unlock()
			}
			return nil
		}
		select {
		case <-ctx.Done():
			kvSpanRecordError(span, ErrCommitTimeout)
			s.metrics.ObserveKVWaitAppliedDuration(s.nodeID, time.Since(start), false)
			s.metrics.AddKVWaitAppliedWakeups(s.nodeID, wakeups)
			s.metrics.IncKVWaitAppliedCall(s.nodeID, false)
			s.metrics.IncKVProposalResult(s.nodeID, "commit_timeout")
			return ErrCommitTimeout
		case <-s.applyNotifyCh:
			wakeups++
		}
	}
}

func (s *KV) notifyApply() {
	select {
	case s.applyNotifyCh <- struct{}{}:
	default:
	}
}
