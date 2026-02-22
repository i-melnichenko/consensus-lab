// Package raftgrpc contains the Raft gRPC transport adapters.
package raftgrpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

// PeerClient implements raft.PeerClient over a gRPC connection.
type PeerClient struct {
	conn   *grpc.ClientConn
	client raftpb.RaftServiceClient
}

// Dial connects to a remote Raft peer and returns a PeerClient.
// The connection is established lazily on the first RPC call.
func Dial(target string, opts ...grpc.DialOption) (*PeerClient, error) {
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, err
	}
	return &PeerClient{
		conn:   conn,
		client: raftpb.NewRaftServiceClient(conn),
	}, nil
}

// RequestVote calls the remote Raft RequestVote RPC.
func (c *PeerClient) RequestVote(ctx context.Context, req *raft.RequestVoteRequest) (*raft.RequestVoteResponse, error) {
	pbResp, err := c.client.RequestVote(ctx, requestVoteRequestToPB(req))
	if err != nil {
		return nil, err
	}
	return requestVoteResponseFromPB(pbResp), nil
}

// AppendEntries calls the remote Raft AppendEntries RPC.
func (c *PeerClient) AppendEntries(ctx context.Context, req *raft.AppendEntriesRequest) (*raft.AppendEntriesResponse, error) {
	pbResp, err := c.client.AppendEntries(ctx, appendEntriesRequestToPB(req))
	if err != nil {
		return nil, err
	}
	return appendEntriesResponseFromPB(pbResp), nil
}

// Close closes the underlying gRPC connection to the peer.
func (c *PeerClient) Close() error {
	return c.conn.Close()
}

// InstallSnapshot calls the remote Raft InstallSnapshot RPC.
func (c *PeerClient) InstallSnapshot(ctx context.Context, req *raft.InstallSnapshotRequest) (*raft.InstallSnapshotResponse, error) {
	pbResp, err := c.client.InstallSnapshot(ctx, installSnapshotRequestToPB(req))
	if err != nil {
		return nil, err
	}
	return installSnapshotResponseFromPB(pbResp), nil
}
