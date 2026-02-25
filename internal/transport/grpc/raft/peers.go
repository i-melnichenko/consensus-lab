package raftgrpc

import (
	"fmt"

	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
)

// DialPeers dials all peers and returns a map of raft.PeerClient keyed by peer ID.
// On any dial failure the already-opened connections are closed and an error is returned.
func DialPeers(addresses map[string]string, tracer oteltrace.Tracer, opts ...grpc.DialOption) (map[string]raft.PeerClient, error) {
	peers := make(map[string]raft.PeerClient, len(addresses))
	for id, addr := range addresses {
		pc, err := Dial(addr, tracer, opts...)
		if err != nil {
			for _, p := range peers {
				_ = p.Close()
			}
			return nil, fmt.Errorf("dial peer %s at %s: %w", id, addr, err)
		}
		peers[id] = pc
	}
	return peers, nil
}
