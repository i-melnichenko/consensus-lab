package raft

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func TestNode_Start_ReturnsNotLeaderWithoutAppending(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1))
	n.role = Follower
	n.currentTerm = 4

	index, isLeader := n.StartCommand([]byte("cmd"))

	if isLeader {
		t.Fatalf("expected isLeader=false")
	}
	if index != 0 {
		t.Fatalf("expected index=0, got %d", index)
	}
	if len(n.log) != 0 {
		t.Fatalf("expected log to stay empty, got len=%d", len(n.log))
	}
}

func TestNode_Start_RejectsWhenDegraded(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 4
	n.degraded = true

	index, isLeader := n.StartCommand([]byte("cmd"))

	if isLeader {
		t.Fatalf("expected isLeader=false when degraded")
	}
	if index != 0 {
		t.Fatalf("expected index=0, got %d", index)
	}
	if len(n.log) != 0 {
		t.Fatalf("expected log to remain empty, got len=%d", len(n.log))
	}
}

func TestNode_Start_AppendsEntryAndTriggersReplication(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1))
	n.role = Leader
	n.currentTerm = 7

	index, isLeader := n.StartCommand([]byte("set x=1"))

	if !isLeader {
		t.Fatalf("expected isLeader=true")
	}
	if index != 1 {
		t.Fatalf("expected index=1, got %d", index)
	}
	if len(n.log) != 1 {
		t.Fatalf("expected log len=1, got %d", len(n.log))
	}
	if got := n.log[0].Term; got != 7 {
		t.Fatalf("expected log term=7, got %d", got)
	}
	if got := string(n.log[0].Command); got != "set x=1" {
		t.Fatalf("expected command copied, got %q", got)
	}
	if got := n.matchIndex["n1"]; got != 1 {
		t.Fatalf("expected self matchIndex=1, got %d", got)
	}
	if got := n.nextIndex["n1"]; got != 2 {
		t.Fatalf("expected self nextIndex=2, got %d", got)
	}

	select {
	case <-n.replicateNotifyCh:
	default:
		t.Fatalf("expected replication notification")
	}
}

func TestNode_Start_SingleNodeLeaderCommitsAndApplies(t *testing.T) {
	applyCh := make(chan consensus.ApplyMsg, 1)
	n := newTestNode("n1", map[string]PeerClient{}, applyCh)
	n.role = Leader
	n.currentTerm = 2

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runApplyLoop(ctx)
	}()

	index, isLeader := n.StartCommand([]byte("cmd"))
	if !isLeader || index != 1 {
		t.Fatalf("unexpected Start result: index=%d isLeader=%v", index, isLeader)
	}

	msg := waitApplyMsg(t, applyCh)
	if !msg.CommandValid || msg.CommandIndex != 1 || string(msg.Command) != "cmd" || msg.SnapshotValid {
		t.Fatalf("unexpected apply msg: %+v", msg)
	}

	n.mu.Lock()
	commitIndex := n.commitIndex
	lastApplied := n.lastApplied
	n.mu.Unlock()
	if commitIndex != 1 {
		t.Fatalf("expected commitIndex=1, got %d", commitIndex)
	}
	if lastApplied != 1 {
		t.Fatalf("expected lastApplied=1, got %d", lastApplied)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("apply loop did not stop")
	}
}

func TestNode_runApplyLoop_AppliesCommittedEntriesInOrder(t *testing.T) {
	applyCh := make(chan consensus.ApplyMsg, 4)
	n := newTestNode("n1", map[string]PeerClient{}, applyCh)
	n.log = []LogEntry{
		{Term: 1, Command: []byte("a")},
		{Term: 2, Command: []byte("b")},
	}
	n.commitIndex = 2

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.runApplyLoop(ctx)
	}()

	n.notifyApply()

	msg1 := waitApplyMsg(t, applyCh)
	msg2 := waitApplyMsg(t, applyCh)

	if !msg1.CommandValid || msg1.CommandIndex != 1 || string(msg1.Command) != "a" || msg1.SnapshotValid {
		t.Fatalf("unexpected first apply msg: %+v", msg1)
	}
	if !msg2.CommandValid || msg2.CommandIndex != 2 || string(msg2.Command) != "b" || msg2.SnapshotValid {
		t.Fatalf("unexpected second apply msg: %+v", msg2)
	}

	n.mu.Lock()
	lastApplied := n.lastApplied
	n.mu.Unlock()
	if lastApplied != 2 {
		t.Fatalf("expected lastApplied=2, got %d", lastApplied)
	}

	cancel()
	<-done
}

func TestNode_Stop_UnblocksBlockedApplyLoop(t *testing.T) {
	applyCh := make(chan consensus.ApplyMsg) // unbuffered: apply loop may block on send
	n := newTestNode("n1", map[string]PeerClient{}, applyCh)
	n.role = Leader
	n.currentTerm = 1

	n.Run(context.Background())

	index, isLeader := n.StartCommand([]byte("cmd"))
	if !isLeader || index != 1 {
		t.Fatalf("unexpected Start result: index=%d isLeader=%v", index, isLeader)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Stop()
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not return while apply loop was blocked")
	}
}

func TestNewNode_NormalizesPeersByDroppingSelf(t *testing.T) {
	n, err := NewNode("n1", map[string]PeerClient{
		"n1": nil, // should be ignored
		"n2": nil,
		"n3": nil,
	}, make(chan consensus.ApplyMsg, 1), NewInMemoryStorage(), slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	if _, ok := n.peers["n1"]; ok {
		t.Fatalf("expected self peer to be removed during normalization")
	}
	if len(n.peers) != 2 {
		t.Fatalf("expected 2 remote peers after normalization, got %d", len(n.peers))
	}
	if got := n.quorumSize(); got != 2 {
		t.Fatalf("expected quorumSize=2 for 3-node cluster, got %d", got)
	}
}

func TestNewNode_ReturnsErrorOnNilLogger(t *testing.T) {
	_, err := NewNode(
		"n1",
		map[string]PeerClient{},
		make(chan consensus.ApplyMsg, 1),
		NewInMemoryStorage(),
		nil,
	)
	if !errors.Is(err, ErrNilLogger) {
		t.Fatalf("expected ErrNilLogger, got %v", err)
	}
}

func TestNode_Run_DoesNotStartWhenAlreadyDegraded(t *testing.T) {
	n := newTestNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1))
	n.mu.Lock()
	n.degraded = true
	n.mu.Unlock()

	n.Run(context.Background())

	// Run should return without starting background goroutines when already degraded.
	n.Stop()

	if n.Status() != NodeStatusDegraded {
		t.Fatalf("expected status=%q, got %q", NodeStatusDegraded, n.Status())
	}
}

type failingStorage struct {
	loadState        *PersistentState
	saveHardStateErr error
	appendLogErr     error
	truncateLogErr   error
}

type recordingLogger struct {
	mu   sync.Mutex
	logs []string
}

func (l *recordingLogger) append(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, fmt.Sprintf("%s %v", msg, args))
}

func (l *recordingLogger) Debug(msg string, args ...any) { l.append(msg, args...) }
func (l *recordingLogger) Info(msg string, args ...any)  { l.append(msg, args...) }
func (l *recordingLogger) Warn(msg string, args ...any)  { l.append(msg, args...) }
func (l *recordingLogger) Error(msg string, args ...any) { l.append(msg, args...) }

func (l *recordingLogger) Contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.logs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func (s *failingStorage) Load() (*PersistentState, error) {
	if s.loadState == nil {
		return &PersistentState{}, nil
	}

	return &PersistentState{
		HardState: s.loadState.HardState,
		Log:       cloneLogEntries(s.loadState.Log),
	}, nil
}

func (s *failingStorage) SaveHardState(_ HardState) error {
	return s.saveHardStateErr
}

func (s *failingStorage) AppendLog(_ []LogEntry) error {
	return s.appendLogErr
}

func (s *failingStorage) TruncateLog(_ int64) error {
	return s.truncateLogErr
}

func (s *failingStorage) SetLog(_ int64, _ []LogEntry) error { return nil }
func (s *failingStorage) SaveSnapshot(_ Snapshot) error      { return nil }

func TestNode_Start_RollsBackOnPersistAppendError(t *testing.T) {
	storageErr := errors.New("append failed")
	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), &failingStorage{
		appendLogErr: storageErr,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	n.role = Leader
	n.currentTerm = 3

	index, isLeader := n.StartCommand([]byte("cmd"))
	if isLeader {
		t.Fatalf("expected isLeader=false on append persist error")
	}
	if index != 0 {
		t.Fatalf("expected index=0, got %d", index)
	}
	if len(n.log) != 0 {
		t.Fatalf("expected log rollback, got len=%d", len(n.log))
	}
	if got := n.matchIndex["n1"]; got != 0 {
		t.Fatalf("expected self matchIndex unchanged, got %d", got)
	}
	if got := n.nextIndex["n1"]; got != 0 {
		t.Fatalf("expected self nextIndex unchanged, got %d", got)
	}

	select {
	case <-n.replicateNotifyCh:
		t.Fatalf("unexpected replicate notification on persist failure")
	default:
	}
}

func TestNode_HandleRequestVote_ReturnsErrorOnPersistHardStateFailure(t *testing.T) {
	storageErr := errors.New("save hard state failed")
	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), &failingStorage{
		saveHardStateErr: storageErr,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	n.currentTerm = 1

	resp, err := n.HandleRequestVote(context.Background(), &RequestVoteRequest{
		Term:         2,
		CandidateID:  "n2",
		LastLogIndex: 0,
		LastLogTerm:  0,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if resp != nil {
		t.Fatalf("expected nil response on persist error")
	}
	if !errors.Is(err, storageErr) {
		t.Fatalf("expected storage error, got %v", err)
	}
	if n.currentTerm != 1 {
		t.Fatalf("expected currentTerm rollback to 1, got %d", n.currentTerm)
	}
	if n.votedFor != "" {
		t.Fatalf("expected votedFor rollback, got %q", n.votedFor)
	}
}

func TestNode_HandleAppendEntries_ReturnsErrorOnPersistAppendFailure(t *testing.T) {
	storageErr := errors.New("append failed")
	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), &failingStorage{
		appendLogErr: storageErr,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	n.currentTerm = 2

	resp, err := n.HandleAppendEntries(context.Background(), &AppendEntriesRequest{
		Term:         2,
		LeaderID:     "n2",
		PrevLogIndex: 0,
		PrevLogTerm:  0,
		Entries:      []LogEntry{{Term: 2, Command: []byte("x")}},
		LeaderCommit: 0,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if resp != nil {
		t.Fatalf("expected nil response on persist error")
	}
	if !errors.Is(err, storageErr) {
		t.Fatalf("expected storage error, got %v", err)
	}
	if len(n.log) != 0 {
		t.Fatalf("expected in-memory log unchanged, got len=%d", len(n.log))
	}
}

func TestNode_sendAppendEntries_StepDownPersistFailureSetsDegradedStatus(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	storageErr := errors.New("save hard state failed")
	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), &failingStorage{
		saveHardStateErr: storageErr,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	n.role = Leader
	n.currentTerm = 3

	peer := NewMockPeerClient(ctrl)
	req := &AppendEntriesRequest{Term: 3}
	peer.EXPECT().
		AppendEntries(gomock.Any(), req).
		Return(&AppendEntriesResponse{Term: 4, Success: false}, nil).
		Times(1)

	n.sendAppendEntries(context.Background(), "n2", peer, req)

	if n.role != Follower {
		t.Fatalf("expected role %v, got %v", Follower, n.role)
	}
	if n.currentTerm != 4 {
		t.Fatalf("expected currentTerm=4, got %d", n.currentTerm)
	}
	if n.Status() != NodeStatusDegraded {
		t.Fatalf("expected status=%q, got %q", NodeStatusDegraded, n.Status())
	}
}

func TestNode_runCandidate_PersistFailureSetsDegradedStatus(t *testing.T) {
	storageErr := errors.New("save hard state failed")
	recLogger := &recordingLogger{}
	n, err := NewNode("n1", map[string]PeerClient{}, make(chan consensus.ApplyMsg, 1), &failingStorage{
		saveHardStateErr: storageErr,
	}, recLogger)
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	n.role = Candidate
	n.currentTerm = 5

	n.runCandidate(context.Background())

	if n.role != Follower {
		t.Fatalf("expected role %v, got %v", Follower, n.role)
	}
	if n.currentTerm != 5 {
		t.Fatalf("expected currentTerm rollback to 5, got %d", n.currentTerm)
	}
	if n.votedFor != "" {
		t.Fatalf("expected votedFor rollback, got %q", n.votedFor)
	}
	if n.Status() != NodeStatusDegraded {
		t.Fatalf("expected status=%q, got %q", NodeStatusDegraded, n.Status())
	}
	if !recLogger.Contains("raft node degraded due to persistence error") {
		t.Fatalf("expected degraded log message")
	}
}

func waitApplyMsg(t *testing.T, ch <-chan consensus.ApplyMsg) consensus.ApplyMsg {
	t.Helper()

	select {
	case msg := <-ch:
		return msg
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for apply msg")
		return consensus.ApplyMsg{}
	}
}
