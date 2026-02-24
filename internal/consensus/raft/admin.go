package raft

import (
	"sort"
	"time"
)

// AdminPeerState is a point-in-time snapshot of leader-side replication progress for a peer.
type AdminPeerState struct {
	NodeID     string
	MatchIndex int64
	NextIndex  int64
}

// AdminState is a point-in-time snapshot of Raft runtime state for admin APIs.
type AdminState struct {
	NodeID            string
	LeaderID          string
	Role              Role
	Status            NodeStatus
	Term              int64
	CommitIndex       int64
	LastApplied       int64
	LastAppliedAt     time.Time
	LastLogIndex      int64
	LastLogTerm       int64
	SnapshotLastIndex int64
	SnapshotLastTerm  int64
	SnapshotSizeBytes int64
	ClusterMembers    []string
	QuorumSize        int
	Peers             []AdminPeerState
}

// AdminState returns a read-only snapshot of Raft state for admin/diagnostic APIs.
func (n *Node) AdminState() AdminState {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := AdminState{
		NodeID:            n.id,
		Role:              n.role,
		Term:              n.currentTerm,
		CommitIndex:       n.commitIndex,
		LastApplied:       n.lastApplied,
		LastAppliedAt:     n.lastAppliedAt,
		LastLogIndex:      n.lastLogIndexLocked(),
		LastLogTerm:       n.lastLogTermLocked(),
		SnapshotLastIndex: n.snapshotIndex,
		SnapshotLastTerm:  n.snapshotTerm,
		QuorumSize:        n.quorumSize(),
	}
	if n.degraded {
		out.Status = NodeStatusDegraded
	} else {
		out.Status = NodeStatusHealthy
	}
	if n.role == Leader {
		out.LeaderID = n.id
	}
	if n.snapshot != nil {
		out.SnapshotSizeBytes = int64(len(n.snapshot.Data))
	}
	if len(n.config.Members) > 0 {
		out.ClusterMembers = append([]string(nil), n.config.Members...)
		sort.Strings(out.ClusterMembers)
	}

	peerIDs := make([]string, 0, len(n.peers))
	for peerID := range n.peers {
		peerIDs = append(peerIDs, peerID)
	}
	sort.Strings(peerIDs)

	out.Peers = make([]AdminPeerState, 0, len(peerIDs))
	for _, peerID := range peerIDs {
		out.Peers = append(out.Peers, AdminPeerState{
			NodeID:     peerID,
			MatchIndex: n.matchIndex[peerID],
			NextIndex:  n.nextIndex[peerID],
		})
	}

	return out
}
