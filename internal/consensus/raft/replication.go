package raft

import (
	"context"
)

func (n *Node) runLeader(ctx context.Context) {
	n.logger.Debug("became leader, starting replication loop",
		"node_id", n.id,
		"term", n.currentTerm,
	)

	ticker := n.newTicker(n.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-n.replicateNotifyCh:
		case <-ticker.C():
		}

		for peer, peerClient := range n.peers {
			// Check whether this peer needs a snapshot first.
			snapReq, ok := n.installSnapshotRequestForPeer(peer)
			if !ok {
				return // stepped down from leader
			}
			if snapReq != nil {
				go n.sendInstallSnapshot(ctx, peer, peerClient, snapReq)
				continue
			}

			req, ok := n.appendEntriesRequestForPeer(peer)
			if !ok {
				return
			}
			if req == nil {
				continue
			}

			go n.sendAppendEntries(ctx, peer, peerClient, req)
		}
	}
}

func (n *Node) notifyReplicate() {
	select {
	case n.replicateNotifyCh <- struct{}{}:
	default:
	}
}

func (n *Node) appendEntriesRequestForPeer(peerID string) (*AppendEntriesRequest, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.role != Leader {
		return nil, false
	}
	if n.replicateInFlight[peerID] {
		n.replicatePending[peerID] = true
		return nil, true
	}
	n.replicateInFlight[peerID] = true

	nextIndex := n.nextIndex[peerID]
	if nextIndex < 1 {
		nextIndex = 1
	}

	prevLogIndex := nextIndex - 1
	var prevLogTerm int64
	if prevLogIndex > n.snapshotIndex {
		prevLogTerm = n.entryAtLocked(prevLogIndex).Term
	} else if prevLogIndex == n.snapshotIndex {
		prevLogTerm = n.snapshotTerm
	}

	var entries []LogEntry
	lastLogIndex := n.lastLogIndexLocked()
	if nextIndex <= lastLogIndex {
		entries = append(entries, n.log[nextIndex-n.snapshotIndex-1:]...)
	}

	req := &AppendEntriesRequest{
		Term:         n.currentTerm,
		LeaderID:     n.id,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  prevLogTerm,
		Entries:      entries,
		LeaderCommit: n.commitIndex,
	}

	return req, true
}

func (n *Node) sendAppendEntries(
	ctx context.Context,
	peerID string,
	peerClient PeerClient,
	req *AppendEntriesRequest,
) {
	if len(req.Entries) > 0 {
		n.logger.Debug("sending AppendEntries",
			"node_id", n.id,
			"peer", peerID,
			"term", req.Term,
			"prev_log_index", req.PrevLogIndex,
			"entries", len(req.Entries),
			"leader_commit", req.LeaderCommit,
		)
	}

	defer func() {
		notifyReplicate := false
		n.mu.Lock()
		n.replicateInFlight[peerID] = false
		if n.replicatePending[peerID] {
			n.replicatePending[peerID] = false
			notifyReplicate = true
		}
		n.mu.Unlock()

		if notifyReplicate {
			n.notifyReplicate()
		}
	}()

	resp, err := peerClient.AppendEntries(ctx, req)
	if err != nil || resp == nil {
		if err != nil && len(req.Entries) > 0 {
			n.logger.Debug("AppendEntries RPC failed",
				"node_id", n.id,
				"peer", peerID,
				"error", err,
			)
		}
		return
	}

	var notifyApply bool
	var notifyReplicate bool

	n.mu.Lock()

	if resp.Term > n.currentTerm {
		n.logger.Debug("stepping down: higher term in AppendEntries response",
			"node_id", n.id,
			"current_term", n.currentTerm,
			"peer_term", resp.Term,
			"peer", peerID,
		)
		n.currentTerm = resp.Term
		n.votedFor = ""
		n.role = Follower
		if err := n.persistHardStateLocked(); err != nil {
			n.markDegradedLocked(err)
		}
		n.mu.Unlock()
		return
	}

	if n.role != Leader {
		n.mu.Unlock()
		return
	}

	// Ignore stale responses from an older leader term.
	if req.Term != n.currentTerm {
		n.mu.Unlock()
		return
	}

	if !resp.Success {
		prevNext := n.nextIndex[peerID]
		switch {
		case resp.ConflictTerm > 0:
			if idx := n.lastIndexOfTermLocked(resp.ConflictTerm); idx > 0 {
				n.nextIndex[peerID] = idx + 1
			} else if resp.ConflictIndex > 0 {
				n.nextIndex[peerID] = resp.ConflictIndex
			} else if n.nextIndex[peerID] > 1 {
				n.nextIndex[peerID]--
			}
		case resp.ConflictIndex > 0:
			n.nextIndex[peerID] = resp.ConflictIndex
		case n.nextIndex[peerID] > 1:
			n.nextIndex[peerID]--
		default:
			n.nextIndex[peerID] = 1
		}
		if n.nextIndex[peerID] < 1 {
			n.nextIndex[peerID] = 1
		}
		n.logger.Debug("AppendEntries rejected, backing off nextIndex",
			"node_id", n.id,
			"peer", peerID,
			"prev_next_index", prevNext,
			"new_next_index", n.nextIndex[peerID],
			"conflict_term", resp.ConflictTerm,
			"conflict_index", resp.ConflictIndex,
		)
		n.mu.Unlock()
		n.notifyReplicate()
		return
	}

	matchIndex := req.PrevLogIndex + int64(len(req.Entries))
	if matchIndex > n.matchIndex[peerID] {
		n.matchIndex[peerID] = matchIndex
	}
	if next := matchIndex + 1; next > n.nextIndex[peerID] {
		n.nextIndex[peerID] = next
	}

	if len(req.Entries) > 0 {
		n.logger.Debug("AppendEntries succeeded",
			"node_id", n.id,
			"peer", peerID,
			"match_index", n.matchIndex[peerID],
			"next_index", n.nextIndex[peerID],
		)
	}

	if n.advanceCommitIndexLocked() {
		notifyApply = true
	}
	if len(req.Entries) > 0 {
		notifyReplicate = true
	}
	n.mu.Unlock()

	if notifyApply {
		n.notifyApply()
	}
	if notifyReplicate {
		n.notifyReplicate()
	}
}

func (n *Node) advanceCommitIndexLocked() bool {
	majority := n.quorumSize()
	lastIndex := n.lastLogIndexLocked()

	for candidate := lastIndex; candidate > n.commitIndex; candidate-- {
		// Raft: leader commits by counting replicas only for entries from current term.
		if n.entryAtLocked(candidate).Term != n.currentTerm {
			continue
		}

		votes := 1 // leader itself
		for peerID := range n.peers {
			if n.matchIndex[peerID] >= candidate {
				votes++
			}
		}

		if votes >= majority {
			n.logger.Debug("commit index advanced",
				"node_id", n.id,
				"prev_commit_index", n.commitIndex,
				"new_commit_index", candidate,
				"term", n.currentTerm,
			)
			n.commitIndex = candidate
			if err := n.persistHardStateLocked(); err != nil {
				n.markDegradedLocked(err)
				return false
			}
			return true
		}
	}

	return false
}
