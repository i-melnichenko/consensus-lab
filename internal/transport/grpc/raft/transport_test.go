package raftgrpc_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftgrpc "github.com/i-melnichenko/consensus-lab/internal/transport/grpc/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

const bufSize = 1 << 20 // 1 MB

// startServer spins up an in-process gRPC server backed by handler.
// Returns a connected PeerClient and a cleanup function.
func startServer(t *testing.T, handler raftgrpc.Handler) (*raftgrpc.PeerClient, func()) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	raftpb.RegisterRaftServiceServer(srv, raftgrpc.NewServer(handler))
	go func() { _ = srv.Serve(lis) }()

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	pc, err := raftgrpc.Dial("passthrough:///bufconn", dialOpts...)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	cleanup := func() {
		_ = pc.Close()
		srv.GracefulStop()
	}
	return pc, cleanup
}

// stubHandler is a test double for raft.Node.
type stubHandler struct {
	requestVoteResp   *raft.RequestVoteResponse
	requestVoteErr    error
	appendEntriesResp *raft.AppendEntriesResponse
	appendEntriesErr  error
	installSnapResp   *raft.InstallSnapshotResponse
	installSnapErr    error

	lastRequestVote   *raft.RequestVoteRequest
	lastAppendEntries *raft.AppendEntriesRequest
	lastInstallSnap   *raft.InstallSnapshotRequest
}

func (s *stubHandler) HandleRequestVote(_ context.Context, req *raft.RequestVoteRequest) (*raft.RequestVoteResponse, error) {
	s.lastRequestVote = req
	return s.requestVoteResp, s.requestVoteErr
}

func (s *stubHandler) HandleAppendEntries(_ context.Context, req *raft.AppendEntriesRequest) (*raft.AppendEntriesResponse, error) {
	s.lastAppendEntries = req
	return s.appendEntriesResp, s.appendEntriesErr
}

func (s *stubHandler) HandleInstallSnapshot(_ context.Context, req *raft.InstallSnapshotRequest) (*raft.InstallSnapshotResponse, error) {
	s.lastInstallSnap = req
	return s.installSnapResp, s.installSnapErr
}

func TestRequestVote_GrantedRoundTrip(t *testing.T) {
	handler := &stubHandler{
		requestVoteResp: &raft.RequestVoteResponse{Term: 3, VoteGranted: true},
	}
	pc, cleanup := startServer(t, handler)
	defer cleanup()

	req := &raft.RequestVoteRequest{
		Term:         3,
		CandidateID:  "n1",
		LastLogIndex: 5,
		LastLogTerm:  2,
	}
	resp, err := pc.RequestVote(context.Background(), req)
	if err != nil {
		t.Fatalf("RequestVote: %v", err)
	}

	if !resp.VoteGranted {
		t.Error("expected VoteGranted=true")
	}
	if resp.Term != 3 {
		t.Errorf("expected Term=3, got %d", resp.Term)
	}

	// verify the handler received the correct fields
	got := handler.lastRequestVote
	if got.CandidateID != "n1" {
		t.Errorf("CandidateID: want n1, got %s", got.CandidateID)
	}
	if got.LastLogIndex != 5 {
		t.Errorf("LastLogIndex: want 5, got %d", got.LastLogIndex)
	}
}

func TestAppendEntries_RoundTrip(t *testing.T) {
	handler := &stubHandler{
		appendEntriesResp: &raft.AppendEntriesResponse{Term: 2, Success: true},
	}
	pc, cleanup := startServer(t, handler)
	defer cleanup()

	req := &raft.AppendEntriesRequest{
		Term:         2,
		LeaderID:     "n0",
		PrevLogIndex: 3,
		PrevLogTerm:  1,
		LeaderCommit: 3,
		Entries: []raft.LogEntry{
			{Term: 2, Command: []byte(`{"type":"put","key":"x","value":"1"}`)},
		},
	}
	resp, err := pc.AppendEntries(context.Background(), req)
	if err != nil {
		t.Fatalf("AppendEntries: %v", err)
	}

	if !resp.Success {
		t.Error("expected Success=true")
	}

	got := handler.lastAppendEntries
	if got.LeaderID != "n0" {
		t.Errorf("LeaderID: want n0, got %s", got.LeaderID)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("Entries: want 1, got %d", len(got.Entries))
	}
	if string(got.Entries[0].Command) != `{"type":"put","key":"x","value":"1"}` {
		t.Errorf("Command mismatch: %s", got.Entries[0].Command)
	}
}

func TestRequestVote_NodeDegraded(t *testing.T) {
	handler := &stubHandler{
		requestVoteErr: raft.ErrNodeDegraded,
	}
	pc, cleanup := startServer(t, handler)
	defer cleanup()

	_, err := pc.RequestVote(context.Background(), &raft.RequestVoteRequest{Term: 1, CandidateID: "n2"})
	if err == nil {
		t.Fatal("expected error for degraded node")
	}
}

func TestInstallSnapshot_RoundTrip(t *testing.T) {
	handler := &stubHandler{
		installSnapResp: &raft.InstallSnapshotResponse{Term: 7},
	}
	pc, cleanup := startServer(t, handler)
	defer cleanup()

	req := &raft.InstallSnapshotRequest{
		Term:              7,
		LeaderID:          "n0",
		LastIncludedIndex: 42,
		LastIncludedTerm:  6,
		Config:            raft.ClusterConfig{Members: []string{"n0", "n1", "n2"}},
		Data:              []byte(`{"k":"v"}`),
	}

	resp, err := pc.InstallSnapshot(context.Background(), req)
	if err != nil {
		t.Fatalf("InstallSnapshot: %v", err)
	}
	if resp.Term != 7 {
		t.Fatalf("Term: want 7, got %d", resp.Term)
	}

	got := handler.lastInstallSnap
	if got == nil {
		t.Fatal("expected handler to receive InstallSnapshot request")
	}
	if got.LeaderID != "n0" {
		t.Errorf("LeaderID: want n0, got %s", got.LeaderID)
	}
	if got.LastIncludedIndex != 42 {
		t.Errorf("LastIncludedIndex: want 42, got %d", got.LastIncludedIndex)
	}
	if got.LastIncludedTerm != 6 {
		t.Errorf("LastIncludedTerm: want 6, got %d", got.LastIncludedTerm)
	}
	if len(got.Config.Members) != 3 {
		t.Fatalf("Config.Members: want 3, got %d", len(got.Config.Members))
	}
	if string(got.Data) != `{"k":"v"}` {
		t.Errorf("Data mismatch: %s", got.Data)
	}
}
