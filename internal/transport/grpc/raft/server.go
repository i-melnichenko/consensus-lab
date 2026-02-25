package raftgrpc

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

// Handler is the subset of *raft.Node required by the gRPC server.
// *raft.Node satisfies this interface.
type Handler interface {
	HandleRequestVote(ctx context.Context, req *raft.RequestVoteRequest) (*raft.RequestVoteResponse, error)
	HandleAppendEntries(ctx context.Context, req *raft.AppendEntriesRequest) (*raft.AppendEntriesResponse, error)
	HandleInstallSnapshot(ctx context.Context, req *raft.InstallSnapshotRequest) (*raft.InstallSnapshotResponse, error)
}

// Server implements raftpb.RaftServiceServer by delegating RPCs to a Raft node.
type Server struct {
	raftpb.UnimplementedRaftServiceServer
	handler Handler
	tracer  oteltrace.Tracer
}

// NewServer creates a Raft gRPC server adapter for the provided handler.
func NewServer(handler Handler, tracer oteltrace.Tracer) *Server {
	return &Server{handler: handler, tracer: tracer}
}

// RequestVote handles a Raft RequestVote RPC.
func (s *Server) RequestVote(ctx context.Context, pbReq *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	ctx, span := s.tracer.Start(ctx, "raftgrpc.server.RequestVote", oteltrace.WithAttributes(serverRequestVoteAttrs(pbReq)...))
	defer span.End()

	resp, err := s.handler.HandleRequestVote(ctx, requestVoteRequestFromPB(pbReq))
	if err != nil {
		recordSpanError(span, err)
		return nil, toGRPCStatus(err)
	}
	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.vote_granted", resp.VoteGranted),
	)
	return requestVoteResponseToPB(resp), nil
}

// AppendEntries handles a Raft AppendEntries RPC.
func (s *Server) AppendEntries(ctx context.Context, pbReq *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	ctx, span := s.tracer.Start(ctx, "raftgrpc.server.AppendEntries", oteltrace.WithAttributes(serverAppendEntriesAttrs(pbReq)...))
	defer span.End()

	resp, err := s.handler.HandleAppendEntries(ctx, appendEntriesRequestFromPB(pbReq))
	if err != nil {
		recordSpanError(span, err)
		return nil, toGRPCStatus(err)
	}
	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.append.success", resp.Success),
		attribute.Int64("raft.conflict_term", resp.ConflictTerm),
		attribute.Int64("raft.conflict_index", resp.ConflictIndex),
	)
	return appendEntriesResponseToPB(resp), nil
}

// InstallSnapshot handles a Raft InstallSnapshot RPC.
func (s *Server) InstallSnapshot(ctx context.Context, pbReq *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	ctx, span := s.tracer.Start(ctx, "raftgrpc.server.InstallSnapshot", oteltrace.WithAttributes(serverInstallSnapshotAttrs(pbReq)...))
	defer span.End()

	resp, err := s.handler.HandleInstallSnapshot(ctx, installSnapshotRequestFromPB(pbReq))
	if err != nil {
		recordSpanError(span, err)
		return nil, toGRPCStatus(err)
	}
	span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
	return installSnapshotResponseToPB(resp), nil
}

func toGRPCStatus(err error) error {
	if errors.Is(err, raft.ErrNodeDegraded) {
		return status.Error(codes.Unavailable, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
