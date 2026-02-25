package raft

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNode_HandleAppendEntries_HeartbeatOnEmptyLog(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 2
	n.role = Candidate

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{
		Term:         2,
		LeaderID:     "n2",
		PrevLogIndex: 0,
		PrevLogTerm:  0,
		Entries:      nil,
		LeaderCommit: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected heartbeat on empty log to succeed")
	}
	if resp.Term != 2 {
		t.Fatalf("expected resp.Term=2, got %d", resp.Term)
	}
	if n.role != Follower {
		t.Fatalf("expected node to become follower, got %v", n.role)
	}
}

func TestNode_HandleRequestVote_ReturnsErrNodeDegraded(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.degraded = true

	resp, err := n.HandleRequestVote(context.Background(), &RequestVoteRequest{})
	if !errors.Is(err, ErrNodeDegraded) {
		t.Fatalf("expected ErrNodeDegraded, got %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response on degraded node")
	}
}

func TestNode_HandleAppendEntries_ReturnsErrNodeDegraded(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.degraded = true

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{})
	if !errors.Is(err, ErrNodeDegraded) {
		t.Fatalf("expected ErrNodeDegraded, got %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response on degraded node")
	}
}

func TestNode_HandleRequestVote_EmptyLogCandidateIsUpToDate(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 3

	resp, err := n.HandleRequestVote(context.Background(), &RequestVoteRequest{
		Term:         3,
		CandidateID:  "n2",
		LastLogIndex: 0,
		LastLogTerm:  0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.VoteGranted {
		t.Fatalf("expected vote to be granted for empty up-to-date candidate log")
	}
	if n.votedFor != "n2" {
		t.Fatalf("expected votedFor=n2, got %q", n.votedFor)
	}
}

func TestNode_HandleRequestVote_RejectsOutdatedLog(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 3
	n.log = []LogEntry{
		{Term: 1},
		{Term: 3},
	}

	resp, err := n.HandleRequestVote(context.Background(), &RequestVoteRequest{
		Term:         3,
		CandidateID:  "n2",
		LastLogIndex: 1,
		LastLogTerm:  1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.VoteGranted {
		t.Fatalf("expected vote to be rejected for outdated candidate log")
	}
}

func TestNode_HandleAppendEntries_ReturnsConflictIndexWhenPrevTooHigh(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 4
	n.log = []LogEntry{{Term: 1}, {Term: 2}}

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{
		Term:         4,
		LeaderID:     "n2",
		PrevLogIndex: 5,
		PrevLogTerm:  4,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected append to fail when PrevLogIndex is too high")
	}
	if resp.ConflictIndex != 3 {
		t.Fatalf("expected conflictIndex=3, got %d", resp.ConflictIndex)
	}
	if resp.ConflictTerm != 0 {
		t.Fatalf("expected conflictTerm=0, got %d", resp.ConflictTerm)
	}
}

func TestNode_HandleAppendEntries_ReturnsConflictTermAndFirstIndexOnMismatch(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 4
	n.log = []LogEntry{
		{Term: 1},
		{Term: 2},
		{Term: 2},
		{Term: 3},
	}

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{
		Term:         4,
		LeaderID:     "n2",
		PrevLogIndex: 3,
		PrevLogTerm:  9, // mismatch with follower term=2 at index 3
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected append to fail on term mismatch")
	}
	if resp.ConflictTerm != 2 {
		t.Fatalf("expected conflictTerm=2, got %d", resp.ConflictTerm)
	}
	if resp.ConflictIndex != 2 {
		t.Fatalf("expected conflictIndex=2, got %d", resp.ConflictIndex)
	}
}

func TestNode_HandleAppendEntries_UpdatesCommitIndexAndNotifiesApply(t *testing.T) {
	n, _ := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	n.currentTerm = 5
	n.log = []LogEntry{
		{Term: 4},
		{Term: 5},
	}

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{
		Term:         5,
		LeaderID:     "n2",
		PrevLogIndex: 2,
		PrevLogTerm:  5,
		LeaderCommit: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected append to succeed")
	}

	n.mu.Lock()
	commitIndex := n.commitIndex
	n.mu.Unlock()
	if commitIndex != 2 {
		t.Fatalf("expected commitIndex=2, got %d", commitIndex)
	}

	select {
	case <-n.applyNotifyCh:
	default:
		t.Fatalf("expected apply notification")
	}
}
