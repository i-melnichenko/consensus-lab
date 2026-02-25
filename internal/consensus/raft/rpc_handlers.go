package raft

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// HandleRequestVote handles a Raft RequestVote RPC from a candidate.
func (n *Node) HandleRequestVote(
	ctx context.Context,
	req *RequestVoteRequest,
) (*RequestVoteResponse, error) {
	_, span := n.startSpan(
		ctx,
		"raft.node.HandleRequestVote",
		attribute.String("raft.peer_id", req.CandidateID),
		attribute.Int64("raft.term", req.Term),
		attribute.Int64("raft.last_log_index", req.LastLogIndex),
		attribute.Int64("raft.last_log_term", req.LastLogTerm),
	)
	defer span.End()

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		spanRecordError(span, ErrNodeDegraded)
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
		span.SetAttributes(
			attribute.Int64("raft.response_term", resp.Term),
			attribute.Bool("raft.vote_granted", resp.VoteGranted),
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
		if err := n.tracePersistHardStateLocked(ctx, "request_vote_term_update"); err != nil {
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
		if err := n.tracePersistHardStateLocked(ctx, "request_vote_grant"); err != nil {
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

	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.vote_granted", resp.VoteGranted),
	)
	return resp, nil
}

// HandleAppendEntries handles a Raft AppendEntries RPC from the leader.
func (n *Node) HandleAppendEntries(
	ctx context.Context,
	req *AppendEntriesRequest,
) (*AppendEntriesResponse, error) {
	_, span := n.startSpan(
		ctx,
		"raft.node.HandleAppendEntries",
		attribute.String("raft.peer_id", req.LeaderID),
		attribute.Int64("raft.term", req.Term),
		attribute.Int64("raft.prev_log_index", req.PrevLogIndex),
		attribute.Int64("raft.prev_log_term", req.PrevLogTerm),
		attribute.Int("raft.entries_count", len(req.Entries)),
		attribute.Bool("raft.is_heartbeat", len(req.Entries) == 0),
		attribute.Int64("raft.leader_commit", req.LeaderCommit),
	)
	defer span.End()

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		spanRecordError(span, ErrNodeDegraded)
		return nil, ErrNodeDegraded
	}

	resp := &AppendEntriesResponse{
		Term:    n.currentTerm,
		Success: false,
	}

	if req.Term < n.currentTerm {
		span.SetAttributes(
			attribute.Int64("raft.response_term", resp.Term),
			attribute.Bool("raft.append.success", resp.Success),
		)
		return resp, nil
	}

	if req.Term > n.currentTerm {
		prevTerm := n.currentTerm
		prevVotedFor := n.votedFor
		n.currentTerm = req.Term
		n.votedFor = ""
		if err := n.tracePersistHardStateLocked(ctx, "append_entries_term_update"); err != nil {
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
		span.SetAttributes(
			attribute.Int64("raft.response_term", resp.Term),
			attribute.Bool("raft.append.success", resp.Success),
			attribute.Int64("raft.conflict_index", resp.ConflictIndex),
		)
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
			span.SetAttributes(
				attribute.Int64("raft.response_term", resp.Term),
				attribute.Bool("raft.append.success", resp.Success),
				attribute.Int64("raft.conflict_term", resp.ConflictTerm),
				attribute.Int64("raft.conflict_index", resp.ConflictIndex),
			)
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
			span.SetAttributes(
				attribute.Int64("raft.response_term", resp.Term),
				attribute.Bool("raft.append.success", resp.Success),
				attribute.Int64("raft.conflict_index", resp.ConflictIndex),
			)
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
			if err := n.tracePersistAppendLogLocked(ctx, appendEntries); err != nil {
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
		if err := n.tracePersistTruncateLogLocked(ctx, index); err != nil {
			return nil, err
		}
		n.log = n.log[:index-n.snapshotIndex-1]
		appendEntries := cloneLogEntries(req.Entries[i:])
		if err := n.tracePersistAppendLogLocked(ctx, appendEntries); err != nil {
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
		if err := n.tracePersistHardStateLocked(ctx, "append_entries_commit_update"); err != nil {
			return nil, err
		}
		n.recordCommitSeenRangeLocked(prevCommit, n.commitIndex, time.Now())
		n.metrics.SetRaftApplyLag(n.id, n.commitIndex-n.lastApplied)
		n.notifyApply()
	}

	resp.Success = true
	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.append.success", resp.Success),
	)
	return resp, nil
}

// HandleInstallSnapshot applies a snapshot sent by the leader.
// It steps down to follower, replaces log state, and notifies the apply loop.
func (n *Node) HandleInstallSnapshot(
	ctx context.Context,
	req *InstallSnapshotRequest,
) (*InstallSnapshotResponse, error) {
	_, span := n.startSpan(
		ctx,
		"raft.node.HandleInstallSnapshot",
		attribute.String("raft.peer_id", req.LeaderID),
		attribute.Int64("raft.term", req.Term),
		attribute.Int64("raft.snapshot.index", req.LastIncludedIndex),
		attribute.Int64("raft.snapshot.term", req.LastIncludedTerm),
		attribute.Int("raft.snapshot.bytes", len(req.Data)),
	)
	defer span.End()

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.degraded {
		spanRecordError(span, ErrNodeDegraded)
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
		span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
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
		if err := n.tracePersistHardStateLocked(ctx, "install_snapshot_term_update"); err != nil {
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
		span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
		return resp, nil
	case req.LastIncludedIndex == n.snapshotIndex && req.LastIncludedTerm == n.snapshotTerm:
		n.logger.Debug("InstallSnapshot ignored: already have same snapshot",
			"node_id", n.id,
			"snapshot_index", n.snapshotIndex,
			"snapshot_term", n.snapshotTerm,
		)
		span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
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

	if err := n.tracePersistSnapshotLocked(ctx, snap); err != nil {
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

	span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
	return resp, nil
}

func (n *Node) resetElectionTimeout() {
	select {
	case n.electionTimeoutResetCh <- struct{}{}:
	default:
	}
}
