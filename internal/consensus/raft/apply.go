package raft

import (
	"context"
	"time"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func (n *Node) notifyApply() {
	select {
	case n.applyNotifyCh <- struct{}{}:
	default:
	}
}

func (n *Node) runApplyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.applyNotifyCh:
		}

		// Drain any pending snapshot first — the KV store must replace its
		// state before we apply regular log entries that follow the snapshot.
		for {
			n.mu.Lock()
			snap := n.pendingSnapshot
			if snap == nil {
				n.mu.Unlock()
				break
			}
			n.pendingSnapshot = nil
			n.mu.Unlock()

			n.logger.Debug("applying snapshot to state machine",
				"node_id", n.id,
				"snapshot_index", snap.LastIncludedIndex,
				"snapshot_term", snap.LastIncludedTerm,
			)

			select {
			case <-ctx.Done():
				return
			case n.applyCh <- consensus.ApplyMsg{
				SnapshotValid: true,
				Snapshot:      append([]byte(nil), snap.Data...),
				SnapshotIndex: snap.LastIncludedIndex,
			}:
			}

			// Advance lastApplied to the snapshot index so the loop below
			// starts applying log entries from the right position.
			n.mu.Lock()
			if snap.LastIncludedIndex > n.lastApplied {
				n.lastApplied = snap.LastIncludedIndex
			}
			n.lastAppliedAt = time.Now()
			n.mu.Unlock()
		}

		// Apply committed log entries in order.
		for {
			n.mu.Lock()
			if n.lastApplied >= n.commitIndex {
				n.mu.Unlock()
				break
			}

			nextIndex := n.lastApplied + 1
			if nextIndex > n.lastLogIndexLocked() {
				n.mu.Unlock()
				break
			}

			entry := n.entryAtLocked(nextIndex)
			n.lastApplied = nextIndex
			n.mu.Unlock()

			n.logger.Debug("applying log entry",
				"node_id", n.id,
				"index", nextIndex,
				"term", entry.Term,
			)

			select {
			case <-ctx.Done():
				return
			case n.applyCh <- consensus.ApplyMsg{
				CommandValid: true,
				Command:      append([]byte(nil), entry.Command...),
				CommandIndex: nextIndex,
			}:
				n.mu.Lock()
				n.lastAppliedAt = time.Now()
				n.mu.Unlock()
			}
		}
	}
}
