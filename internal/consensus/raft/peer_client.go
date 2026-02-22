package raft

import "context"

//go:generate mockgen -source=$GOFILE -destination=mocks_test.go -package=$GOPACKAGE

// PeerClient is the transport client used by a node to call Raft RPCs on peers.
type PeerClient interface {
	RequestVote(
		ctx context.Context,
		req *RequestVoteRequest,
	) (*RequestVoteResponse, error)

	AppendEntries(
		ctx context.Context,
		req *AppendEntriesRequest,
	) (*AppendEntriesResponse, error)

	InstallSnapshot(
		ctx context.Context,
		req *InstallSnapshotRequest,
	) (*InstallSnapshotResponse, error)

	Close() error
}
