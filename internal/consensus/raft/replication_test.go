package raft

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestNode_sendAppendEntries_ForcesBackoffWhenConflictHintMakesNoProgress(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 4}

	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{
			Term:          4,
			Success:       false,
			ConflictTerm:  0,
			ConflictIndex: 6, // same as current nextIndex: no progress without fallback
		}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg))
	n.role = Leader
	n.currentTerm = 4
	n.nextIndex["n2"] = 6

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.nextIndex["n2"]; got != 5 {
		t.Fatalf("expected nextIndex[n2]=5 after no-progress hint, got %d", got)
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

func TestNode_sendAppendEntries_IgnoresRPCErrorAndKeepsProgress(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}
	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(nil, errors.New("rpc dropped")).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.nextIndex["n2"] = 4
	n.matchIndex["n2"] = 2
	n.replicateInFlight["n2"] = true

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.nextIndex["n2"]; got != 4 {
		t.Fatalf("expected nextIndex to stay 4 on rpc error, got %d", got)
	}
	if got := n.matchIndex["n2"]; got != 2 {
		t.Fatalf("expected matchIndex to stay 2 on rpc error, got %d", got)
	}
	if n.replicateInFlight["n2"] {
		t.Fatalf("expected inflight flag to be cleared after rpc error")
	}
}

func TestNode_sendAppendEntries_IgnoresNilResponseAndKeepsProgress(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}
	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(nil, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 3
	n.nextIndex["n2"] = 5
	n.matchIndex["n2"] = 1
	n.replicateInFlight["n2"] = true

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if got := n.nextIndex["n2"]; got != 5 {
		t.Fatalf("expected nextIndex to stay 5 on nil response, got %d", got)
	}
	if got := n.matchIndex["n2"]; got != 1 {
		t.Fatalf("expected matchIndex to stay 1 on nil response, got %d", got)
	}
	if n.replicateInFlight["n2"] {
		t.Fatalf("expected inflight flag to be cleared after nil response")
	}
}

func TestNode_runLeader_SendsHeartbeatOnTicker_Deterministic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	heartbeatCalled := make(chan struct{}, 1)

	peer := NewMockPeerClient(ctrl)
	peer.EXPECT().
		AppendEntries(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *AppendEntriesRequest) (*AppendEntriesResponse, error) {
			if req.Term != 7 {
				t.Fatalf("expected term=7, got %d", req.Term)
			}
			if req.LeaderID != "n1" {
				t.Fatalf("expected leaderID=n1, got %q", req.LeaderID)
			}
			if len(req.Entries) != 0 {
				t.Fatalf("expected heartbeat with no entries, got %d entries", len(req.Entries))
			}
			select {
			case heartbeatCalled <- struct{}{}:
			default:
			}
			return &AppendEntriesResponse{Term: 7, Success: true}, nil
		}).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 7
	n.nextIndex["n2"] = 1

	tf := newFakeTickerFactory()
	tk := tf.AddTicker()
	n.newTicker = tf.NewTicker
	n.heartbeatInterval = 777 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runLeader(ctx)
	}()

	tk.Fire()

	select {
	case <-heartbeatCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("leader did not send heartbeat after fake ticker fire")
	}

	if got := tf.CreatedDurations(); len(got) != 1 || got[0] != 777*time.Millisecond {
		t.Fatalf("unexpected ticker durations: %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runLeader did not stop after cancellation")
	}
}
