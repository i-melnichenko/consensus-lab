package raft

import (
	"log/slog"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

func newTestNode(
	id string,
	peers map[string]PeerClient,
	applyCh chan consensus.ApplyMsg,
) *Node {
	n, err := NewNode(id, peers, applyCh, NewInMemoryStorage(), slog.Default(), testTracer, testMetrics)
	if err != nil {
		panic(err)
	}
	return n
}

func newNodeFromStorage(id string, storage Storage) (*Node, error) {
	return NewNode(id, map[string]PeerClient{}, nil, storage, slog.Default(), testTracer, testMetrics)
}
