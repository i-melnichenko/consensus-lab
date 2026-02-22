package raft

import "context"

// applySnapshotLocked updates node state to reflect the installed snapshot.
// Caller must hold n.mu.
func (n *Node) applySnapshotLocked(snap Snapshot) {
	// Keep log entries that come after the snapshot, discard the rest.
	lastLogIdx := n.lastLogIndexLocked()
	if snap.LastIncludedIndex < lastLogIdx {
		cutIdx := snap.LastIncludedIndex - n.snapshotIndex
		if cutIdx > 0 {
			n.log = n.log[cutIdx:]
		}
	} else {
		n.log = nil
	}

	n.snapshotIndex = snap.LastIncludedIndex
	n.snapshotTerm = snap.LastIncludedTerm
	n.snapshot = &snap

	if n.commitIndex < snap.LastIncludedIndex {
		n.commitIndex = snap.LastIncludedIndex
	}

	if len(snap.Config.Members) > 0 {
		n.config = snap.Config
		_ = n.persistHardStateLocked()
	}

	// Mark snapshot as pending for the apply loop. lastApplied is updated after
	// the apply loop sends the snapshot message to applyCh.
	if snap.LastIncludedIndex > n.lastApplied {
		n.pendingSnapshot = &snap
	}

	// Best-effort: compact the stored log. On failure, NewNode trims on restart.
	_ = n.persistCompactLogLocked()
}

// installSnapshotRequestForPeer checks whether peerID needs a snapshot instead of AppendEntries.
//
// Returns:
//   - (*InstallSnapshotRequest, false) when not leader → caller must stop
//   - (nil, true)                      when in-flight or no snapshot needed → proceed to AppendEntries
//   - (*InstallSnapshotRequest, true)  when snapshot must be sent
func (n *Node) installSnapshotRequestForPeer(peerID string) (*InstallSnapshotRequest, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.role != Leader {
		return nil, false
	}

	// No snapshot available, or peer already has the snapshot.
	if n.snapshotIndex == 0 || n.nextIndex[peerID] > n.snapshotIndex {
		return nil, true // proceed to AppendEntries
	}

	// Another request is already in flight for this peer.
	if n.replicateInFlight[peerID] {
		n.replicatePending[peerID] = true
		return nil, true // skip this round
	}
	n.replicateInFlight[peerID] = true

	return &InstallSnapshotRequest{
		Term:              n.currentTerm,
		LeaderID:          n.id,
		LastIncludedIndex: n.snapshot.LastIncludedIndex,
		LastIncludedTerm:  n.snapshot.LastIncludedTerm,
		Config:            n.snapshot.Config,
		Data:              append([]byte(nil), n.snapshot.Data...),
	}, true
}

// sendInstallSnapshot delivers a snapshot to a lagging follower and updates
// leader replication progress on success.
func (n *Node) sendInstallSnapshot(
	ctx context.Context,
	peerID string,
	peerClient PeerClient,
	req *InstallSnapshotRequest,
) {
	n.logger.Debug("sending InstallSnapshot",
		"node_id", n.id,
		"peer", peerID,
		"term", req.Term,
		"snapshot_index", req.LastIncludedIndex,
		"snapshot_term", req.LastIncludedTerm,
	)

	defer func() {
		n.mu.Lock()
		n.replicateInFlight[peerID] = false
		pending := n.replicatePending[peerID]
		n.replicatePending[peerID] = false
		n.mu.Unlock()

		if pending {
			n.notifyReplicate()
		}
	}()

	resp, err := peerClient.InstallSnapshot(ctx, req)
	if err != nil || resp == nil {
		if err != nil {
			n.logger.Debug("InstallSnapshot RPC failed",
				"node_id", n.id,
				"peer", peerID,
				"error", err,
				"snapshot_index", req.LastIncludedIndex,
			)
		}
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if resp.Term > n.currentTerm {
		n.currentTerm = resp.Term
		n.votedFor = ""
		n.role = Follower
		if err := n.persistHardStateLocked(); err != nil {
			n.markDegradedLocked(err)
		}
		return
	}

	if n.role != Leader || req.Term != n.currentTerm {
		return
	}

	n.logger.Debug("InstallSnapshot succeeded",
		"node_id", n.id,
		"peer", peerID,
		"snapshot_index", req.LastIncludedIndex,
		"snapshot_term", req.LastIncludedTerm,
		"peer_term", resp.Term,
	)

	// Advance peer progress past the snapshot.
	if req.LastIncludedIndex > n.matchIndex[peerID] {
		n.matchIndex[peerID] = req.LastIncludedIndex
	}
	if next := req.LastIncludedIndex + 1; next > n.nextIndex[peerID] {
		n.nextIndex[peerID] = next
	}

	// Immediately continue with normal AppendEntries to send any log entries after the snapshot.
	n.notifyReplicate()
}
