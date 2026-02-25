package raft

import "time"

// recordStartSeenLocked marks leader-side command start time for a specific log index.
// Caller must hold n.mu.
func (n *Node) recordStartSeenLocked(index int64, now time.Time) {
	if index <= 0 {
		return
	}
	if n.startSeenAt == nil {
		n.startSeenAt = make(map[int64]time.Time)
	}
	if _, exists := n.startSeenAt[index]; !exists {
		n.startSeenAt[index] = now
	}
}

// observeStartToCommitRangeLocked records start->commit latency for entries newly
// covered by commitIndex. Caller must hold n.mu.
func (n *Node) observeStartToCommitRangeLocked(prevCommit, newCommit int64, now time.Time) {
	if newCommit <= prevCommit || len(n.startSeenAt) == 0 {
		return
	}
	for idx := prevCommit + 1; idx <= newCommit; idx++ {
		if ts, ok := n.startSeenAt[idx]; ok {
			if !ts.IsZero() && !now.Before(ts) {
				n.metrics.ObserveRaftStartToCommitDuration(n.id, now.Sub(ts))
			}
			delete(n.startSeenAt, idx)
		}
	}
}

// recordCommitSeenRangeLocked marks commit-observed time for entries newly covered
// by commitIndex. Caller must hold n.mu.
func (n *Node) recordCommitSeenRangeLocked(prevCommit, newCommit int64, now time.Time) {
	if newCommit <= prevCommit {
		return
	}
	if n.commitSeenAt == nil {
		n.commitSeenAt = make(map[int64]time.Time)
	}
	for idx := prevCommit + 1; idx <= newCommit; idx++ {
		if _, exists := n.commitSeenAt[idx]; !exists {
			n.commitSeenAt[idx] = now
		}
	}
}

// observeCommitToApplyLocked records commit->apply latency for an applied index and
// clears stale entries up to that index. Caller must hold n.mu.
func (n *Node) observeCommitToApplyLocked(appliedIndex int64, now time.Time) {
	if appliedIndex <= 0 || len(n.commitSeenAt) == 0 {
		return
	}
	if ts, ok := n.commitSeenAt[appliedIndex]; ok {
		if !ts.IsZero() && !now.Before(ts) {
			n.metrics.ObserveRaftCommitToApplyDuration(n.id, now.Sub(ts))
		}
	}
	for idx := range n.commitSeenAt {
		if idx <= appliedIndex {
			delete(n.commitSeenAt, idx)
		}
	}
	for idx := range n.startSeenAt {
		if idx <= appliedIndex {
			delete(n.startSeenAt, idx)
		}
	}
}
