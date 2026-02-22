package raft

import (
	"fmt"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

// StartCommand appends a new command to the leader log.
// It implements consensus.Consensus.
func (n *Node) StartCommand(cmd []byte) (index int64, isLeader bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded || n.role != Leader {
		n.logger.Debug("StartCommand rejected: not leader",
			"node_id", n.id,
			"role", n.role,
			"degraded", n.degraded,
		)
		return 0, false
	}

	entry := LogEntry{
		Term:    n.currentTerm,
		Type:    EntryCommand,
		Command: append([]byte(nil), cmd...),
	}
	prevLogLen := len(n.log)
	n.log = append(n.log, entry)
	if err := n.persistAppendLogLocked([]LogEntry{entry}); err != nil {
		n.log = n.log[:prevLogLen]
		return 0, false
	}

	index = n.lastLogIndexLocked()
	n.matchIndex[n.id] = index
	n.nextIndex[n.id] = index + 1

	n.logger.Debug("command appended to leader log",
		"node_id", n.id,
		"index", index,
		"term", n.currentTerm,
	)

	if n.advanceCommitIndexLocked() {
		n.notifyApply()
	}

	n.notifyReplicate()
	return index, true
}

// ApplyCh returns the channel used to deliver committed entries and snapshots.
func (n *Node) ApplyCh() <-chan consensus.ApplyMsg {
	return n.applyCh
}

// IsLeader reports whether the node currently believes it is the leader.
func (n *Node) IsLeader() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role == Leader
}

// Stop implements consensus.Consensus.
func (n *Node) Stop() {
	if n.cancel != nil {
		n.cancel()
	}
	for _, peerClient := range n.peers {
		_ = peerClient.Close()
	}

	n.wg.Wait()
}

// Snapshot compacts the Raft log up to and including index.
// data is the serialized application state at that point.
// Called by the KV layer after applying entries to free log space.
// Implements consensus.Consensus.
func (n *Node) Snapshot(index int64, data []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.logger.Debug("taking snapshot",
		"node_id", n.id,
		"index", index,
		"current_snapshot_index", n.snapshotIndex,
	)

	if index <= n.snapshotIndex {
		return nil // already compacted at or beyond this index
	}
	if index > n.lastLogIndexLocked() {
		return fmt.Errorf("raft: snapshot index %d beyond last log index %d", index, n.lastLogIndexLocked())
	}

	term := n.entryAtLocked(index).Term

	snap := Snapshot{
		LastIncludedIndex: index,
		LastIncludedTerm:  term,
		Config:            n.config,
		Data:              append([]byte(nil), data...),
	}

	if err := n.persistSnapshotLocked(snap); err != nil {
		return err
	}

	// Compact in-memory log: discard entries up to and including index.
	cutIdx := index - n.snapshotIndex
	n.log = n.log[cutIdx:]
	n.snapshotIndex = snap.LastIncludedIndex
	n.snapshotTerm = snap.LastIncludedTerm
	n.snapshot = &snap

	// Replace stored log with the remaining entries.
	// Non-fatal: on restart, stale entries are trimmed using LogBase from the snapshot.
	_ = n.persistCompactLogLocked()

	return nil
}
