package raft

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNode_sendAppendEntries_UpdatesProgressOnSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{
		Term:         3,
		LeaderID:     "n1",
		PrevLogIndex: 1,
		PrevLogTerm:  1,
		Entries:      []LogEntry{{Term: 2}, {Term: 3}},
		LeaderCommit: 0,
	}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 3, Success: true}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg))
	n.role = Leader
	n.currentTerm = 3
	n.nextIndex["n2"] = 2
	n.matchIndex["n2"] = 0

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.matchIndex["n2"]; got != 3 {
		t.Fatalf("expected matchIndex[n2]=3, got %d", got)
	}
	if got := n.nextIndex["n2"]; got != 4 {
		t.Fatalf("expected nextIndex[n2]=4, got %d", got)
	}
}

func TestNode_sendAppendEntries_StepsDownOnHigherTerm(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 5, Success: false}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg))
	n.role = Leader
	n.currentTerm = 3
	n.votedFor = "n1"

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if n.role != Follower {
		t.Fatalf("expected role %v, got %v", Follower, n.role)
	}
	if n.currentTerm != 5 {
		t.Fatalf("expected currentTerm=5, got %d", n.currentTerm)
	}
	if n.votedFor != "" {
		t.Fatalf("expected votedFor reset, got %q", n.votedFor)
	}
}

func TestNode_sendAppendEntries_DecrementsNextIndexOnConflict(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 3, Success: false}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg))
	n.role = Leader
	n.currentTerm = 3
	n.nextIndex["n2"] = 4

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.nextIndex["n2"]; got != 3 {
		t.Fatalf("expected nextIndex[n2]=3 after conflict, got %d", got)
	}

	select {
	case <-n.replicateNotifyCh:
	default:
		t.Fatalf("expected replicate notification after conflict")
	}
}

func TestNode_sendAppendEntries_UsesConflictHintsForBacktracking(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 4}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{
			Term:          4,
			Success:       false,
			ConflictTerm:  2,
			ConflictIndex: 2,
		}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg))
	n.role = Leader
	n.currentTerm = 4
	n.log = []LogEntry{{Term: 1}, {Term: 2}, {Term: 2}, {Term: 4}}
	n.nextIndex["n2"] = 5

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	// Leader has conflict term=2 up to index 3, so nextIndex should jump to 4.
	if got := n.nextIndex["n2"]; got != 4 {
		t.Fatalf("expected nextIndex[n2]=4 from conflict hints, got %d", got)
	}
}

func TestNode_sendAppendEntries_AdvancesCommitIndexOnMajority(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{
		Term:         3,
		LeaderID:     "n1",
		PrevLogIndex: 1,
		PrevLogTerm:  1,
		Entries:      []LogEntry{{Term: 3}},
	}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 3, Success: true}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{
		"n2": peer,
		"n3": NewMockPeerClient(ctrl),
	}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.log = []LogEntry{{Term: 1}, {Term: 3}}
	n.nextIndex["n2"] = 2
	n.matchIndex["n2"] = 0
	n.matchIndex["n3"] = 0
	n.commitIndex = 0

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.commitIndex; got != 2 {
		t.Fatalf("expected commitIndex=2 after majority replication, got %d", got)
	}

	select {
	case <-n.replicateNotifyCh:
	default:
		t.Fatalf("expected replicate notification after successful append with entries")
	}
}

func TestNode_advanceCommitIndexLocked_DoesNotCommitOldTermByCounting(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{
		"n2": nil,
		"n3": nil,
	}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.log = []LogEntry{
		{Term: 1},
		{Term: 2},
	}
	n.matchIndex["n1"] = 2
	n.matchIndex["n2"] = 2
	n.matchIndex["n3"] = 0
	n.commitIndex = 0

	n.mu.Lock()
	advanced := n.advanceCommitIndexLocked()
	commitIndex := n.commitIndex
	n.mu.Unlock()

	if advanced {
		t.Fatalf("expected no commit advancement for old-term entries")
	}
	if commitIndex != 0 {
		t.Fatalf("expected commitIndex=0, got %d", commitIndex)
	}
}

func TestNode_appendEntriesRequestForPeer_SingleflightMarksPending(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{"n2": nil}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.nextIndex["n2"] = 1

	req1, ok := n.appendEntriesRequestForPeer("n2")
	if !ok || req1 == nil {
		t.Fatalf("expected first request to be created")
	}

	req2, ok := n.appendEntriesRequestForPeer("n2")
	if !ok {
		t.Fatalf("expected second call to keep leader loop running")
	}
	if req2 != nil {
		t.Fatalf("expected second request to be skipped while inflight")
	}

	n.mu.Lock()
	inflight := n.replicateInFlight["n2"]
	pending := n.replicatePending["n2"]
	n.mu.Unlock()
	if !inflight {
		t.Fatalf("expected inflight flag to be set")
	}
	if !pending {
		t.Fatalf("expected pending flag to be set after skipped launch")
	}
}

func TestNode_sendAppendEntries_ClearsSingleflightAndReNotifiesWhenPending(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}
	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 3, Success: true}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.replicateInFlight["n2"] = true
	n.replicatePending["n2"] = true

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	n.mu.Lock()
	inflight := n.replicateInFlight["n2"]
	pending := n.replicatePending["n2"]
	n.mu.Unlock()
	if inflight {
		t.Fatalf("expected inflight flag to be cleared")
	}
	if pending {
		t.Fatalf("expected pending flag to be cleared")
	}

	select {
	case <-n.replicateNotifyCh:
	default:
		t.Fatalf("expected replicate notification when pending work existed")
	}
}
