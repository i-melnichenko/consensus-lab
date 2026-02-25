// Package raftgrpc contains the Raft gRPC transport adapters.
package raftgrpc

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

// PeerClient implements raft.PeerClient over a gRPC connection.
type PeerClient struct {
	conn   *grpc.ClientConn
	client raftpb.RaftServiceClient
	tracer oteltrace.Tracer
	target string
}

// Dial connects to a remote Raft peer and returns a PeerClient.
// The connection is established lazily on the first RPC call.
func Dial(target string, tracer oteltrace.Tracer, opts ...grpc.DialOption) (*PeerClient, error) {
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, err
	}
	return &PeerClient{
		conn:   conn,
		client: raftpb.NewRaftServiceClient(conn),
		tracer: tracer,
		target: target,
	}, nil
}

// RequestVote calls the remote Raft RequestVote RPC.
func (c *PeerClient) RequestVote(ctx context.Context, req *raft.RequestVoteRequest) (*raft.RequestVoteResponse, error) {
	ctx, span := c.tracer.Start(
		ctx,
		"raftgrpc.client.RequestVote",
		oteltrace.WithAttributes(clientRequestVoteAttrs(c.target, req)...),
	)
	defer span.End()

	pbResp, err := c.client.RequestVote(ctx, requestVoteRequestToPB(req))
	if err != nil {
		recordSpanError(span, err)
		return nil, err
	}
	resp := requestVoteResponseFromPB(pbResp)
	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.vote_granted", resp.VoteGranted),
	)
	return resp, nil
}

// AppendEntries calls the remote Raft AppendEntries RPC.
func (c *PeerClient) AppendEntries(ctx context.Context, req *raft.AppendEntriesRequest) (*raft.AppendEntriesResponse, error) {
	ctx, span := c.tracer.Start(
		ctx,
		"raftgrpc.client.AppendEntries",
		oteltrace.WithAttributes(clientAppendEntriesAttrs(c.target, req)...),
	)
	defer span.End()

	pbResp, err := c.client.AppendEntries(ctx, appendEntriesRequestToPB(req))
	if err != nil {
		recordSpanError(span, err)
		return nil, err
	}
	resp := appendEntriesResponseFromPB(pbResp)
	span.SetAttributes(
		attribute.Int64("raft.response_term", resp.Term),
		attribute.Bool("raft.append.success", resp.Success),
		attribute.Int64("raft.conflict_term", resp.ConflictTerm),
		attribute.Int64("raft.conflict_index", resp.ConflictIndex),
	)
	return resp, nil
}

// Close closes the underlying gRPC connection to the peer.
func (c *PeerClient) Close() error {
	return c.conn.Close()
}

// InstallSnapshot calls the remote Raft InstallSnapshot RPC.
func (c *PeerClient) InstallSnapshot(ctx context.Context, req *raft.InstallSnapshotRequest) (*raft.InstallSnapshotResponse, error) {
	ctx, span := c.tracer.Start(
		ctx,
		"raftgrpc.client.InstallSnapshot",
		oteltrace.WithAttributes(clientInstallSnapshotAttrs(c.target, req)...),
	)
	defer span.End()

	pbResp, err := c.client.InstallSnapshot(ctx, installSnapshotRequestToPB(req))
	if err != nil {
		recordSpanError(span, err)
		return nil, err
	}
	resp := installSnapshotResponseFromPB(pbResp)
	span.SetAttributes(attribute.Int64("raft.response_term", resp.Term))
	return resp, nil
}
