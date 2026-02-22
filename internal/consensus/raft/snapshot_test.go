package raft

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

// --- Snapshot (log compaction) ---

func TestSnapshot_CompactsLog(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, nil)
	n.role = Leader
	n.currentTerm = 1
	n.log = []LogEntry{
		{Term: 1, Command: []byte("a")},
		{Term: 1, Command: []byte("b")},
		{Term: 1, Command: []byte("c")},
	}
	n.commitIndex = 3
	n.lastApplied = 3

	if err := n.Snapshot(2, []byte("snap-data")); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.snapshotIndex != 2 {
		t.Errorf("snapshotIndex: want 2, got %d", n.snapshotIndex)
	}
	if n.snapshotTerm != 1 {
		t.Errorf("snapshotTerm: want 1, got %d", n.snapshotTerm)
	}
	// Only the third entry (index 3) should remain.
	if len(n.log) != 1 {
		t.Fatalf("log len: want 1, got %d", len(n.log))
	}
	if string(n.log[0].Command) != "c" {
		t.Errorf("remaining entry: want 'c', got %q", n.log[0].Command)
	}
	// lastLogIndexLocked should still be 3.
	if got := n.lastLogIndexLocked(); got != 3 {
		t.Errorf("lastLogIndex: want 3, got %d", got)
	}
}

func TestSnapshot_IgnoresStaleIndex(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, nil)
	n.snapshotIndex = 5
	n.snapshotTerm = 2

	// Trying to snapshot at an index already covered → no-op.
	if err := n.Snapshot(3, []byte("old")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.snapshotIndex != 5 {
		t.Errorf("snapshotIndex should be unchanged, got %d", n.snapshotIndex)
	}
}

func TestSnapshot_RejectsIndexBeyondLog(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, nil)
	n.role = Leader
	n.log = []LogEntry{{Term: 1, Command: []byte("x")}}

	if err := n.Snapshot(99, []byte("data")); err == nil {
		t.Fatal("expected error for index beyond log")
	}
}

// --- HandleInstallSnapshot ---

func TestHandleInstallSnapshot_AppliesSnapshot(t *testing.T) {
	applyCh := make(chan consensus.ApplyMsg, 2)
	n := newTestNode("n1", map[string]PeerClient{}, applyCh)
	n.currentTerm = 1
	n.log = []LogEntry{
		{Term: 1, Command: []byte("a")},
		{Term: 1, Command: []byte("b")},
	}

	resp, err := n.HandleInstallSnapshot(context.Background(), &InstallSnapshotRequest{
		Term:              1,
		LeaderID:          "n0",
		LastIncludedIndex: 2,
		LastIncludedTerm:  1,
		Data:              []byte("snapshot-data"),
	})
	if err != nil {
		t.Fatalf("HandleInstallSnapshot() error = %v", err)
	}
	if resp.Term != 1 {
		t.Errorf("resp.Term: want 1, got %d", resp.Term)
	}

	n.mu.Lock()
	idx := n.snapshotIndex
	term := n.snapshotTerm
	commitIdx := n.commitIndex
	n.mu.Unlock()

	if idx != 2 {
		t.Errorf("snapshotIndex: want 2, got %d", idx)
	}
	if term != 1 {
		t.Errorf("snapshotTerm: want 1, got %d", term)
	}
	if commitIdx != 2 {
		t.Errorf("commitIndex: want 2, got %d", commitIdx)
	}
}

func TestHandleInstallSnapshot_IgnoresStaleTerm(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, nil)
	n.currentTerm = 5

	resp, err := n.HandleInstallSnapshot(context.Background(), &InstallSnapshotRequest{
		Term:              3,
		LeaderID:          "n0",
		LastIncludedIndex: 10,
		LastIncludedTerm:  3,
		Data:              nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Term != 5 {
		t.Errorf("resp.Term: want 5, got %d", resp.Term)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.snapshotIndex != 0 {
		t.Errorf("snapshotIndex should be 0, got %d", n.snapshotIndex)
	}
}

func TestHandleInstallSnapshot_IgnoresOlderSnapshot(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, nil)
	n.currentTerm = 2
	n.snapshotIndex = 10
	n.snapshotTerm = 2

	resp, err := n.HandleInstallSnapshot(context.Background(), &InstallSnapshotRequest{
		Term:              2,
		LeaderID:          "n0",
		LastIncludedIndex: 5, // older than our snapshotIndex=10
		LastIncludedTerm:  1,
		Data:              []byte("old"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Term != 2 {
		t.Errorf("resp.Term: want 2, got %d", resp.Term)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.snapshotIndex != 10 {
		t.Errorf("snapshotIndex should remain 10, got %d", n.snapshotIndex)
	}
}

// --- Apply loop: snapshot message delivery ---

func TestApplyLoop_DeliversSnapshotBeforeLogEntries(t *testing.T) {
	applyCh := make(chan consensus.ApplyMsg, 4)
	n := newTestNode("n1", map[string]PeerClient{}, applyCh)

	// Simulate: snapshot at index 2, one log entry at index 3.
	n.snapshotIndex = 2
	n.snapshotTerm = 1
	snap := &Snapshot{LastIncludedIndex: 2, LastIncludedTerm: 1, Data: []byte("state")}
	n.snapshot = snap
	n.pendingSnapshot = snap
	n.lastApplied = 0 // will be advanced after snapshot delivered
	n.log = []LogEntry{{Term: 1, Command: []byte("d")}}
	n.commitIndex = 3

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runApplyLoop(ctx)
	}()

	n.notifyApply()

	// First message: snapshot.
	snapMsg := waitApplyMsg(t, applyCh)
	if !snapMsg.SnapshotValid || snapMsg.CommandValid {
		t.Fatalf("expected snapshot message first, got %+v", snapMsg)
	}
	if snapMsg.SnapshotIndex != 2 {
		t.Errorf("snapshot msg SnapshotIndex: want 2, got %d", snapMsg.SnapshotIndex)
	}
	if string(snapMsg.Snapshot) != "state" {
		t.Errorf("snapshot data: want 'state', got %q", snapMsg.Snapshot)
	}

	// Second message: log entry at index 3.
	entryMsg := waitApplyMsg(t, applyCh)
	if entryMsg.SnapshotValid {
		t.Fatal("expected regular log entry, got snapshot")
	}
	if !entryMsg.CommandValid {
		t.Fatal("expected regular log entry, got invalid apply message")
	}
	if entryMsg.CommandIndex != 3 {
		t.Errorf("entry index: want 3, got %d", entryMsg.CommandIndex)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("apply loop did not stop")
	}
}

// --- NewNode snapshot restore ---

func TestNewNode_RestoresSnapshotState(t *testing.T) {
	storage := NewInMemoryStorage().(*InMemoryStorage)

	snap := Snapshot{LastIncludedIndex: 5, LastIncludedTerm: 2, Data: []byte("restored")}
	_ = storage.SaveSnapshot(snap)
	_ = storage.SetLog(5, []LogEntry{{Term: 2, Command: []byte("e6")}})

	n, err := newNodeFromStorage("n1", storage)
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.snapshotIndex != 5 {
		t.Errorf("snapshotIndex: want 5, got %d", n.snapshotIndex)
	}
	if n.snapshotTerm != 2 {
		t.Errorf("snapshotTerm: want 2, got %d", n.snapshotTerm)
	}
	if n.pendingSnapshot == nil {
		t.Fatal("pendingSnapshot: expected restored snapshot to be queued for apply loop")
	}
	if n.commitIndex != 5 {
		t.Errorf("commitIndex: want 5, got %d", n.commitIndex)
	}
	if n.lastApplied != 5 {
		t.Errorf("lastApplied: want 5, got %d", n.lastApplied)
	}
	if len(n.log) != 1 {
		t.Fatalf("log len: want 1, got %d", len(n.log))
	}
	if string(n.log[0].Command) != "e6" {
		t.Errorf("log[0].Command: want 'e6', got %q", n.log[0].Command)
	}
	// lastLogIndex must account for snapshot offset.
	if got := n.lastLogIndexLocked(); got != 6 {
		t.Errorf("lastLogIndex: want 6, got %d", got)
	}
}

func TestNewNode_RestoredSnapshot_IsDeliveredToApplyLoopOnStartup(t *testing.T) {
	storage := NewInMemoryStorage().(*InMemoryStorage)
	snap := Snapshot{LastIncludedIndex: 3, LastIncludedTerm: 1, Data: []byte("state")}
	_ = storage.SaveSnapshot(snap)

	applyCh := make(chan consensus.ApplyMsg, 1)
	n, err := NewNode("n1", map[string]PeerClient{}, applyCh, storage, slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runApplyLoop(ctx)
	}()

	n.notifyApply()

	msg := waitApplyMsg(t, applyCh)
	if !msg.SnapshotValid || msg.CommandValid {
		t.Fatalf("expected startup snapshot ApplyMsg, got %+v", msg)
	}
	if msg.SnapshotIndex != 3 {
		t.Fatalf("SnapshotIndex: want 3, got %d", msg.SnapshotIndex)
	}
	if string(msg.Snapshot) != "state" {
		t.Fatalf("Snapshot data: want %q, got %q", "state", msg.Snapshot)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("apply loop did not stop")
	}
}

func TestNewNode_TrimsStaleLogOnCrashRecovery(t *testing.T) {
	// Simulate crash: snapshot at 3 saved, but log still has all entries from base 0.
	storage := NewInMemoryStorage().(*InMemoryStorage)

	snap := Snapshot{LastIncludedIndex: 3, LastIncludedTerm: 1, Data: []byte("s")}
	_ = storage.SaveSnapshot(snap)
	// Log base=0 means crash happened before SetLog was called.
	_ = storage.AppendLog([]LogEntry{
		{Term: 1, Command: []byte("e1")},
		{Term: 1, Command: []byte("e2")},
		{Term: 1, Command: []byte("e3")},
		{Term: 1, Command: []byte("e4")},
	})

	n, err := newNodeFromStorage("n1", storage)
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// Entries 1-3 are stale; only e4 at index 4 should remain.
	if len(n.log) != 1 {
		t.Fatalf("log len: want 1, got %d — stale entries not trimmed", len(n.log))
	}
	if string(n.log[0].Command) != "e4" {
		t.Errorf("remaining entry: want 'e4', got %q", n.log[0].Command)
	}
	if got := n.lastLogIndexLocked(); got != 4 {
		t.Errorf("lastLogIndex: want 4, got %d", got)
	}
}
