package raft

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNodeWithJSONStorage_PersistsAndLoadsState(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "raft")
	storage := NewJSONStorage(dir)

	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), storage, slog.Default(), testTracer, testMetrics)
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	n.currentTerm = 3
	n.role = Leader
	n.mu.Lock()
	if err := n.persistHardStateLocked(); err != nil {
		n.mu.Unlock()
		t.Fatalf("persistHardStateLocked() error = %v", err)
	}
	n.mu.Unlock()

	if _, isLeader := n.StartCommand([]byte("cmd-1")); !isLeader {
		t.Fatalf("expected leader")
	}

	_, err = n.HandleRequestVote(context.Background(), &RequestVoteRequest{
		Term:         4,
		CandidateID:  "n2",
		LastLogIndex: 1,
		LastLogTerm:  3,
	})
	if err != nil {
		t.Fatalf("HandleRequestVote() error = %v", err)
	}

	restored, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), storage, slog.Default(), testTracer, testMetrics)
	if err != nil {
		t.Fatalf("restore NewNode() error = %v", err)
	}

	if restored.currentTerm != 4 {
		t.Fatalf("expected currentTerm=4, got %d", restored.currentTerm)
	}
	if restored.votedFor != "n2" {
		t.Fatalf("expected votedFor=n2, got %q", restored.votedFor)
	}
	if len(restored.log) != 1 {
		t.Fatalf("expected log len=1, got %d", len(restored.log))
	}
	if restored.commitIndex != 1 {
		t.Fatalf("expected commitIndex=1, got %d", restored.commitIndex)
	}
	if restored.log[0].Term != 3 {
		t.Fatalf("expected log term=3, got %d", restored.log[0].Term)
	}
	if got := string(restored.log[0].Command); got != "cmd-1" {
		t.Fatalf("expected command=cmd-1, got %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		restored.runApplyLoop(ctx)
	}()
	restored.notifyApply()

	msg := waitApplyMsg(t, restored.applyCh)
	if !msg.CommandValid || msg.CommandIndex != 1 || string(msg.Command) != "cmd-1" {
		t.Fatalf("unexpected apply msg after restore: %+v", msg)
	}
	cancel()
	<-done
}
