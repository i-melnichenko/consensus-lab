package raft

import "context"

// HandleRequestVote handles a Raft RequestVote RPC from a candidate.
func (n *Node) HandleRequestVote(
	_ context.Context,
	req *RequestVoteRequest,
) (*RequestVoteResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		return nil, ErrNodeDegraded
	}

	n.logger.Debug("received RequestVote",
		"node_id", n.id,
		"from", req.CandidateID,
		"candidate_term", req.Term,
		"current_term", n.currentTerm,
		"candidate_last_log_index", req.LastLogIndex,
		"candidate_last_log_term", req.LastLogTerm,
	)

	resp := &RequestVoteResponse{
		Term:        n.currentTerm,
		VoteGranted: false,
	}

	if req.Term < n.currentTerm {
		n.logger.Debug("rejected vote: stale term",
			"node_id", n.id,
			"from", req.CandidateID,
			"candidate_term", req.Term,
			"current_term", n.currentTerm,
		)
		return resp, nil
	}

	if req.Term > n.currentTerm {
		prevTerm := n.currentTerm
		prevVotedFor := n.votedFor
		prevRole := n.role
		n.currentTerm = req.Term
		n.votedFor = ""
		n.role = Follower
		if err := n.persistHardStateLocked(); err != nil {
			n.currentTerm = prevTerm
			n.votedFor = prevVotedFor
			n.role = prevRole
			return nil, err
		}
	}

	resp.Term = n.currentTerm

	lastTerm := n.lastLogTermLocked()
	lastIndex := n.lastLogIndexLocked()

	upToDate := req.LastLogTerm > lastTerm ||
		(req.LastLogTerm == lastTerm && req.LastLogIndex >= lastIndex)

	if (n.votedFor == "" || n.votedFor == req.CandidateID) && upToDate {
		prevVotedFor := n.votedFor
		n.votedFor = req.CandidateID
		if err := n.persistHardStateLocked(); err != nil {
			n.votedFor = prevVotedFor
			return nil, err
		}
		resp.VoteGranted = true
		n.resetElectionTimeout()
		n.logger.Debug("granted vote",
			"node_id", n.id,
			"to", req.CandidateID,
			"term", n.currentTerm,
		)
	} else {
		n.logger.Debug("denied vote",
			"node_id", n.id,
			"to", req.CandidateID,
			"term", n.currentTerm,
			"voted_for", n.votedFor,
			"up_to_date", upToDate,
		)
	}

	return resp, nil
}

// HandleAppendEntries handles a Raft AppendEntries RPC from the leader.
func (n *Node) HandleAppendEntries(
	_ context.Context,
	req *AppendEntriesRequest,
) (*AppendEntriesResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		return nil, ErrNodeDegraded
	}

	resp := &AppendEntriesResponse{
		Term:    n.currentTerm,
		Success: false,
	}

	if req.Term < n.currentTerm {
		return resp, nil
	}

	if req.Term > n.currentTerm {
		prevTerm := n.currentTerm
		prevVotedFor := n.votedFor
		n.currentTerm = req.Term
		n.votedFor = ""
		if err := n.persistHardStateLocked(); err != nil {
			n.currentTerm = prevTerm
			n.votedFor = prevVotedFor
			return nil, err
		}
	}

	n.role = Follower
	resp.Term = n.currentTerm
	n.resetElectionTimeout()

	// PrevLogIndex consistency check.
	if req.PrevLogIndex > n.lastLogIndexLocked() {
		n.logger.Debug("AppendEntries rejected: missing prev entry",
			"node_id", n.id,
			"leader", req.LeaderID,
			"prev_log_index", req.PrevLogIndex,
			"last_log_index", n.lastLogIndexLocked(),
		)
		resp.ConflictIndex = n.lastLogIndexLocked() + 1
		return resp, nil
	}

	if req.PrevLogIndex > n.snapshotIndex {
		// PrevLogIndex is in our non-compacted log range.
		prevTerm := n.entryAtLocked(req.PrevLogIndex).Term
		if prevTerm != req.PrevLogTerm {
			n.logger.Debug("AppendEntries rejected: term conflict at prev entry",
				"node_id", n.id,
				"leader", req.LeaderID,
				"prev_log_index", req.PrevLogIndex,
				"our_term", prevTerm,
				"leader_term", req.PrevLogTerm,
			)
			resp.ConflictTerm = prevTerm
			resp.ConflictIndex = n.firstIndexOfTermLocked(prevTerm)
			return resp, nil
		}
	} else if req.PrevLogIndex == n.snapshotIndex {
		// PrevLogIndex is exactly at our snapshot boundary.
		if req.PrevLogTerm != n.snapshotTerm {
			n.logger.Debug("AppendEntries rejected: snapshot boundary term mismatch",
				"node_id", n.id,
				"leader", req.LeaderID,
				"snapshot_index", n.snapshotIndex,
			)
			resp.ConflictIndex = n.snapshotIndex + 1
			return resp, nil
		}
	}
	// PrevLogIndex < snapshotIndex: covered by our committed snapshot — skip check.

	for i, entry := range req.Entries {
		index := req.PrevLogIndex + int64(i) + 1

		if index <= n.snapshotIndex {
			continue // entry already covered by our snapshot
		}

		if index > n.lastLogIndexLocked() {
			appendEntries := cloneLogEntries(req.Entries[i:])
			if err := n.persistAppendLogLocked(appendEntries); err != nil {
				return nil, err
			}
			n.log = append(n.log, appendEntries...)
			break
		}

		if n.entryAtLocked(index).Term == entry.Term {
			continue
		}

		n.logger.Debug("truncating conflicting log entries",
			"node_id", n.id,
			"from_index", index,
		)
		if err := n.persistTruncateLogLocked(index); err != nil {
			return nil, err
		}
		n.log = n.log[:index-n.snapshotIndex-1]
		appendEntries := cloneLogEntries(req.Entries[i:])
		if err := n.persistAppendLogLocked(appendEntries); err != nil {
			return nil, err
		}
		n.log = append(n.log, appendEntries...)
		break
	}

	if len(req.Entries) > 0 {
		n.logger.Debug("appended entries from leader",
			"node_id", n.id,
			"leader", req.LeaderID,
			"count", len(req.Entries),
			"last_index", n.lastLogIndexLocked(),
		)
	}

	if req.LeaderCommit > n.commitIndex {
		prevCommit := n.commitIndex
		lastIndex := n.lastLogIndexLocked()
		if req.LeaderCommit < lastIndex {
			n.commitIndex = req.LeaderCommit
		} else {
			n.commitIndex = lastIndex
		}
		n.logger.Debug("commit index updated by leader",
			"node_id", n.id,
			"prev_commit", prevCommit,
			"new_commit", n.commitIndex,
			"leader_commit", req.LeaderCommit,
		)
		if err := n.persistHardStateLocked(); err != nil {
			return nil, err
		}
		n.notifyApply()
	}

	resp.Success = true
	return resp, nil
}

// HandleInstallSnapshot applies a snapshot sent by the leader.
// It steps down to follower, replaces log state, and notifies the apply loop.
func (n *Node) HandleInstallSnapshot(
	_ context.Context,
	req *InstallSnapshotRequest,
) (*InstallSnapshotResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		return nil, ErrNodeDegraded
	}

	n.logger.Debug("received InstallSnapshot",
		"node_id", n.id,
		"leader", req.LeaderID,
		"snapshot_index", req.LastIncludedIndex,
		"snapshot_term", req.LastIncludedTerm,
	)

	resp := &InstallSnapshotResponse{Term: n.currentTerm}

	if req.Term < n.currentTerm {
		n.logger.Debug("InstallSnapshot rejected: stale term",
			"node_id", n.id,
			"req_term", req.Term,
			"current_term", n.currentTerm,
		)
		return resp, nil
	}

	if req.Term > n.currentTerm {
		prevTerm := n.currentTerm
		prevVoted := n.votedFor
		n.currentTerm = req.Term
		n.votedFor = ""
		if err := n.persistHardStateLocked(); err != nil {
			n.currentTerm = prevTerm
			n.votedFor = prevVoted
			return nil, err
		}
	}

	n.role = Follower
	n.resetElectionTimeout()
	resp.Term = n.currentTerm

	switch {
	case req.LastIncludedIndex < n.snapshotIndex:
		n.logger.Debug("InstallSnapshot ignored: already have newer snapshot",
			"node_id", n.id,
			"our_snapshot_index", n.snapshotIndex,
			"our_snapshot_term", n.snapshotTerm,
			"req_snapshot_index", req.LastIncludedIndex,
			"req_snapshot_term", req.LastIncludedTerm,
		)
		return resp, nil
	case req.LastIncludedIndex == n.snapshotIndex && req.LastIncludedTerm == n.snapshotTerm:
		n.logger.Debug("InstallSnapshot ignored: already have same snapshot",
			"node_id", n.id,
			"snapshot_index", n.snapshotIndex,
			"snapshot_term", n.snapshotTerm,
		)
		return resp, nil
	case req.LastIncludedIndex == n.snapshotIndex && req.LastIncludedTerm != n.snapshotTerm:
		n.logger.Debug("InstallSnapshot replacing snapshot at same index due to term mismatch",
			"node_id", n.id,
			"snapshot_index", n.snapshotIndex,
			"our_snapshot_term", n.snapshotTerm,
			"req_snapshot_term", req.LastIncludedTerm,
		)
	}

	snap := Snapshot{
		LastIncludedIndex: req.LastIncludedIndex,
		LastIncludedTerm:  req.LastIncludedTerm,
		Config:            req.Config,
		Data:              append([]byte(nil), req.Data...),
	}

	if err := n.persistSnapshotLocked(snap); err != nil {
		return nil, err
	}

	n.logger.Debug("installing snapshot from leader",
		"node_id", n.id,
		"leader", req.LeaderID,
		"snapshot_index", snap.LastIncludedIndex,
		"snapshot_term", snap.LastIncludedTerm,
	)
	n.applySnapshotLocked(snap)
	n.notifyApply()

	return resp, nil
}

func (n *Node) resetElectionTimeout() {
	select {
	case n.electionTimeoutResetCh <- struct{}{}:
	default:
	}
}
