package raft

import (
	"context"
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
