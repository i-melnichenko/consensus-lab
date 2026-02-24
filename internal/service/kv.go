// Package service contains application services exposed via transports.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

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

// KV is the application service that bridges the KV store and consensus layer.
type KV struct {
	consensus consensus.Consensus
	store     *kv.Store
	logger    Logger
	mu        sync.Mutex

	// SnapshotEvery enables periodic snapshots after this many applied commands.
	// Zero disables service-level snapshot triggering.
	SnapshotEvery uint64

	lastAppliedIndex int64
	appliedSinceSnap uint64
	applyNotifyCh    chan struct{}
}

// NewKV creates a KV service backed by the provided consensus engine and store.
func NewKV(c consensus.Consensus, store *kv.Store, logger Logger) *KV {
	return &KV{
		consensus:     c,
		store:         store,
		logger:        logger,
		applyNotifyCh: make(chan struct{}, 1),
	}
}

// Get returns a value from the local KV state machine.
func (s *KV) Get(key string) (string, bool) {
	return s.store.Get(key)
}

// Put proposes a replicated write through consensus.
func (s *KV) Put(ctx context.Context, key, value string) (int64, error) {
	s.logger.Debug("proposing put", "key", key)
	return s.startCommand(ctx, kv.Command{
		Type:  kv.PutCmd,
		Key:   key,
		Value: value,
	})
}

// Delete proposes a replicated delete through consensus.
func (s *KV) Delete(ctx context.Context, key string) (int64, error) {
	s.logger.Debug("proposing delete", "key", key)
	return s.startCommand(ctx, kv.Command{
		Type: kv.DeleteCmd,
		Key:  key,
	})
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
			if err := s.handleApply(msg); err != nil {
				return err
			}
		}
	}
}

func (s *KV) handleApply(msg consensus.ApplyMsg) error {
	if msg.SnapshotValid {
		s.logger.Debug("restoring state from snapshot",
			"snapshot_index", msg.SnapshotIndex,
		)
		if err := s.store.RestoreSnapshot(msg.Snapshot); err != nil {
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

	if err := s.store.Apply(msg.Command); err != nil {
		return err
	}

	s.mu.Lock()
	s.lastAppliedIndex = msg.CommandIndex
	s.appliedSinceSnap++
	appliedSinceSnap := s.appliedSinceSnap
	s.mu.Unlock()
	s.notifyApply()

	s.logger.Debug("command applied",
		"index", msg.CommandIndex,
		"applied_since_snap", appliedSinceSnap,
	)

	if s.SnapshotEvery > 0 && appliedSinceSnap >= s.SnapshotEvery {
		if err := s.snapshot(); err != nil {
			return err
		}
	}

	return nil
}

func (s *KV) snapshot() error {
	s.mu.Lock()
	lastAppliedIndex := s.lastAppliedIndex
	appliedSinceSnap := s.appliedSinceSnap
	s.mu.Unlock()

	s.logger.Debug("triggering snapshot",
		"last_applied_index", lastAppliedIndex,
		"applied_since_snap", appliedSinceSnap,
	)
	data, err := s.store.Snapshot()
	if err != nil {
		return err
	}
	if err := s.consensus.Snapshot(lastAppliedIndex, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.appliedSinceSnap = 0
	s.mu.Unlock()
	return nil
}

func (s *KV) startCommand(ctx context.Context, cmd kv.Command) (int64, error) {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return 0, err
	}

	index, isLeader := s.consensus.StartCommand(raw)
	if !isLeader {
		return 0, ErrNotLeader
	}
	s.logger.Debug("command accepted by consensus",
		"index", index,
		"type", cmd.Type,
		"key", cmd.Key,
	)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.waitApplied(ctx, index); err != nil {
		return 0, err
	}
	return index, nil
}

func (s *KV) waitApplied(ctx context.Context, index int64) error {
	for {
		s.mu.Lock()
		applied := s.lastAppliedIndex
		s.mu.Unlock()
		if applied >= index {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrCommitTimeout
		case <-s.applyNotifyCh:
		}
	}
}

func (s *KV) notifyApply() {
	select {
	case s.applyNotifyCh <- struct{}{}:
	default:
	}
}
