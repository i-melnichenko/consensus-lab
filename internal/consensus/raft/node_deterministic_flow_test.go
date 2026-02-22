package raft

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNode_run_DeterministicFollowerTimeoutToLeaderAndFirstHeartbeat(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	heartbeatCalled := make(chan struct{}, 1)

	peer := NewMockPeerClient(ctrl)
	peer.EXPECT().
		RequestVote(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *RequestVoteRequest) (*RequestVoteResponse, error) {
			if req.CandidateID != "n1" {
				t.Fatalf("expected candidateID=n1, got %q", req.CandidateID)
			}
			if req.Term != 1 {
				t.Fatalf("expected election term=1, got %d", req.Term)
			}
			return &RequestVoteResponse{Term: req.Term, VoteGranted: true}, nil
		}).
		Times(1)

	peer.EXPECT().
		AppendEntries(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *AppendEntriesRequest) (*AppendEntriesResponse, error) {
			if req.Term != 1 {
				t.Fatalf("expected heartbeat term=1, got %d", req.Term)
			}
			if req.LeaderID != "n1" {
				t.Fatalf("expected leaderID=n1, got %q", req.LeaderID)
			}
			if len(req.Entries) != 0 {
				t.Fatalf("expected heartbeat AppendEntries, got %d entries", len(req.Entries))
			}
			select {
			case heartbeatCalled <- struct{}{}:
			default:
			}
			return &AppendEntriesResponse{Term: 1, Success: true}, nil
		}).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{"n2": peer}, make(chan consensus.ApplyMsg, 1))
	n.role = Follower
	n.currentTerm = 0

	timerFactory := newFakeTimerFactory()
	followerTimer := timerFactory.AddTimer()
	_ = timerFactory.AddTimer() // candidate election timeout timer; not fired in this scenario.
	n.newTimer = timerFactory.NewTimer
	n.electionTimeoutFn = func() time.Duration { return 111 * time.Millisecond }

	tickerFactory := newFakeTickerFactory()
	leaderTicker := tickerFactory.AddTicker()
	n.newTicker = tickerFactory.NewTicker
	n.heartbeatInterval = 222 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.run(ctx)
	}()

	followerTimer.Fire()

	waitForCondition(t, 200*time.Millisecond, func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.role == Leader && n.currentTerm == 1
	}, "node did not become leader after deterministic follower timeout + election")

	leaderTicker.Fire()

	select {
	case <-heartbeatCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("leader did not send first heartbeat after deterministic ticker fire")
	}

	if got := timerFactory.CreatedDurations(); len(got) < 2 {
		t.Fatalf("expected follower+candidate timers to be created, got %v", got)
	}
	if got := tickerFactory.CreatedDurations(); len(got) != 1 || got[0] != 222*time.Millisecond {
		t.Fatalf("unexpected heartbeat ticker durations: %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("node run loop did not stop after cancellation")
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	if cond() {
		return
	}
	t.Fatal(msg)
}
