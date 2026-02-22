package raft

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNode_runCandidate(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) *Node
		check func(t *testing.T, n *Node)
	}{
		{
			name: "becomes leader after majority votes",
			setup: func(t *testing.T) *Node {
				t.Helper()

				ctrl := gomock.NewController(t)
				t.Cleanup(ctrl.Finish)

				peer := NewMockPeerClient(ctrl)
				peer.EXPECT().
					RequestVote(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, req *RequestVoteRequest) (*RequestVoteResponse, error) {
						if req.Term != 3 {
							t.Fatalf("expected request term=3, got %d", req.Term)
						}
						if req.CandidateID != "n1" {
							t.Fatalf("expected candidateID=n1, got %q", req.CandidateID)
						}
						if req.LastLogIndex != 2 {
							t.Fatalf("expected lastLogIndex=2, got %d", req.LastLogIndex)
						}
						if req.LastLogTerm != 2 {
							t.Fatalf("expected lastLogTerm=2, got %d", req.LastLogTerm)
						}
						return &RequestVoteResponse{Term: req.Term, VoteGranted: true}, nil
					}).
					Times(1)

				n := newTestNode("n1", map[string]PeerClient{
					"n2": peer,
				}, make(chan consensus.ApplyMsg))
				n.role = Candidate
				n.currentTerm = 2
				n.log = []LogEntry{{Term: 1}, {Term: 2}}
				return n
			},
			check: func(t *testing.T, n *Node) {
				t.Helper()

				if n.role != Leader {
					t.Fatalf("expected role %v, got %v", Leader, n.role)
				}
				if n.currentTerm != 3 {
					t.Fatalf("expected currentTerm=3, got %d", n.currentTerm)
				}
				if n.votedFor != "n1" {
					t.Fatalf("expected votedFor=n1, got %q", n.votedFor)
				}
				if got := n.nextIndex["n1"]; got != 3 {
					t.Fatalf("expected nextIndex[n1]=3, got %d", got)
				}
				if got := n.nextIndex["n2"]; got != 3 {
					t.Fatalf("expected nextIndex[n2]=3, got %d", got)
				}
				if got := n.matchIndex["n1"]; got != 0 {
					t.Fatalf("expected matchIndex[n1]=0, got %d", got)
				}
				if got := n.matchIndex["n2"]; got != 0 {
					t.Fatalf("expected matchIndex[n2]=0, got %d", got)
				}
			},
		},
		{
			name: "steps down on higher term response",
			setup: func(t *testing.T) *Node {
				t.Helper()

				ctrl := gomock.NewController(t)
				t.Cleanup(ctrl.Finish)

				peer := NewMockPeerClient(ctrl)
				peer.EXPECT().
					RequestVote(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, req *RequestVoteRequest) (*RequestVoteResponse, error) {
						return &RequestVoteResponse{Term: req.Term + 2, VoteGranted: false}, nil
					}).
					Times(1)

				n := newTestNode("n1", map[string]PeerClient{
					"n2": peer,
				}, make(chan consensus.ApplyMsg))
				n.role = Candidate
				n.currentTerm = 4
				n.votedFor = "old"
				return n
			},
			check: func(t *testing.T, n *Node) {
				t.Helper()

				if n.role != Follower {
					t.Fatalf("expected role %v, got %v", Follower, n.role)
				}
				if n.currentTerm != 7 {
					t.Fatalf("expected currentTerm=7, got %d", n.currentTerm)
				}
				if n.votedFor != "" {
					t.Fatalf("expected votedFor reset, got %q", n.votedFor)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			n := tt.setup(t)
			n.runCandidate(ctx)
			tt.check(t, n)
		})
	}
}

func TestNode_runFollower_BecomesCandidateOnTimeout(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg))
	n.role = Follower

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runFollower(ctx)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("follower did not time out")
	}

	n.mu.Lock()
	role := n.role
	n.mu.Unlock()
	if role != Candidate {
		t.Fatalf("expected role %v, got %v", Candidate, role)
	}
}

func TestNode_runFollower_ResetsTimeoutOnSignal(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg))
	n.role = Follower

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runFollower(ctx)
	}()

	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.After(220 * time.Millisecond)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			n.resetElectionTimeout()
		case <-done:
			t.Fatal("follower exited despite timeout resets")
		}
	}

	n.mu.Lock()
	role := n.role
	n.mu.Unlock()
	if role != Follower {
		t.Fatalf("expected role %v while resets are arriving, got %v", Follower, role)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runFollower did not stop after context cancellation")
	}
}

func TestNode_runCandidate_WinsDespiteDroppedVoteRPC(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	droppedPeer := NewMockPeerClient(ctrl)
	droppedPeer.EXPECT().
		RequestVote(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("dropped rpc")).
		AnyTimes()

	grantPeer := NewMockPeerClient(ctrl)
	grantPeer.EXPECT().
		RequestVote(gomock.Any(), gomock.Any()).
		Return(&RequestVoteResponse{Term: 2, VoteGranted: true}, nil).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{
		"n2": droppedPeer,
		"n3": grantPeer,
	}, make(chan consensus.ApplyMsg))
	n.role = Candidate
	n.currentTerm = 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n.runCandidate(ctx)

	if n.role != Leader {
		t.Fatalf("expected role %v, got %v", Leader, n.role)
	}
	if n.currentTerm != 2 {
		t.Fatalf("expected currentTerm=2, got %d", n.currentTerm)
	}
}

func TestNode_runCandidate_StopsOnContextCancellationWithSlowPeers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	slowPeer1 := NewMockPeerClient(ctrl)
	slowPeer1.EXPECT().
		RequestVote(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *RequestVoteRequest) (*RequestVoteResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}).
		Times(1)

	slowPeer2 := NewMockPeerClient(ctrl)
	slowPeer2.EXPECT().
		RequestVote(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *RequestVoteRequest) (*RequestVoteResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}).
		Times(1)

	n := newTestNode("n1", map[string]PeerClient{
		"n2": slowPeer1,
		"n3": slowPeer2,
	}, make(chan consensus.ApplyMsg))
	n.role = Candidate
	n.currentTerm = 5

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	n.runCandidate(ctx)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("runCandidate did not stop promptly after context cancellation")
	}

	if n.role != Candidate {
		t.Fatalf("expected role to remain %v, got %v", Candidate, n.role)
	}
	if n.currentTerm != 6 {
		t.Fatalf("expected currentTerm=6 after election start, got %d", n.currentTerm)
	}
}

func TestNode_runFollower_BecomesCandidateOnTimeout_DeterministicTimer(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg))
	n.role = Follower

	tf := newFakeTimerFactory()
	ft := tf.AddTimer()
	n.newTimer = tf.NewTimer
	n.electionTimeoutFn = func() time.Duration { return 123 * time.Millisecond }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runFollower(ctx)
	}()

	ft.Fire()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("follower did not exit after fake timer fired")
	}

	n.mu.Lock()
	role := n.role
	n.mu.Unlock()
	if role != Candidate {
		t.Fatalf("expected role %v, got %v", Candidate, role)
	}

	if got := tf.CreatedDurations(); len(got) != 1 || got[0] != 123*time.Millisecond {
		t.Fatalf("unexpected timer durations: %v", got)
	}
}

func TestNode_runFollower_ResetsTimeoutOnSignal_DeterministicTimer(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg))
	n.role = Follower

	tf := newFakeTimerFactory()
	ft := tf.AddTimer()
	n.newTimer = tf.NewTimer

	timeoutCalls := 0
	n.electionTimeoutFn = func() time.Duration {
		timeoutCalls++
		return time.Duration(100+timeoutCalls) * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runFollower(ctx)
	}()

	n.resetElectionTimeout()
	n.resetElectionTimeout()

	// Allow runFollower to process reset signals.
	time.Sleep(10 * time.Millisecond)

	// Reset signals are coalesced via a buffered channel, so multiple back-to-back
	// calls may collapse into a single observed reset.
	if got := ft.ResetCount(); got < 1 {
		t.Fatalf("expected at least 1 timer reset, got %d", got)
	}

	n.mu.Lock()
	role := n.role
	n.mu.Unlock()
	if role != Follower {
		t.Fatalf("expected role %v before timeout fires, got %v", Follower, role)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runFollower did not stop after cancellation")
	}
}
