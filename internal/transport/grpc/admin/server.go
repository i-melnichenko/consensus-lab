package admingrpc

import (
	"context"
	"math"
	"sort"

	"google.golang.org/protobuf/types/known/timestamppb"

	raftconsensus "github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	adminpb "github.com/i-melnichenko/consensus-lab/pkg/proto/adminv1"
)

// RaftInspector is the subset of *raft.Node required by the admin gRPC server.
// *raft.Node satisfies this interface.
type RaftInspector interface {
	AdminState() raftconsensus.AdminState
}

// Server implements adminpb.AdminServiceServer.
type Server struct {
	adminpb.UnimplementedAdminServiceServer

	nodeID        string
	consensusType string
	peerAddrs     map[string]string
	raft          RaftInspector
}

// NewServer creates an admin gRPC server adapter.
func NewServer(nodeID, consensusType string, peerAddrs map[string]string, raft RaftInspector) *Server {
	peerCopy := make(map[string]string, len(peerAddrs))
	for id, addr := range peerAddrs {
		peerCopy[id] = addr
	}
	return &Server{
		nodeID:        nodeID,
		consensusType: consensusType,
		peerAddrs:     peerCopy,
		raft:          raft,
	}
}

// GetNodeInfo returns administrative information about the current node.
func (s *Server) GetNodeInfo(_ context.Context, _ *adminpb.GetNodeInfoRequest) (*adminpb.GetNodeInfoResponse, error) {
	node := &adminpb.NodeInfo{
		NodeId:           s.nodeID,
		ConsensusType:    mapConsensusType(s.consensusType),
		Role:             adminpb.NodeRole_NODE_ROLE_UNSPECIFIED,
		Status:           adminpb.NodeStatus_NODE_STATUS_HEALTHY,
		Peers:            peerInfosFromMap(s.peerAddrs),
		ConsensusDetails: nil,
	}

	if s.raft != nil {
		rs := s.raft.AdminState()
		raftInfo := &adminpb.RaftNodeInfo{
			Term:              rs.Term,
			LeaderId:          rs.LeaderID,
			CommitIndex:       rs.CommitIndex,
			LastApplied:       rs.LastApplied,
			LastLogIndex:      rs.LastLogIndex,
			LastLogTerm:       rs.LastLogTerm,
			SnapshotLastIndex: rs.SnapshotLastIndex,
			SnapshotLastTerm:  rs.SnapshotLastTerm,
			SnapshotSizeBytes: rs.SnapshotSizeBytes,
			ClusterMembers:    append([]string(nil), rs.ClusterMembers...),
			QuorumSize:        safeInt32(rs.QuorumSize),
			Peers:             make([]*adminpb.RaftPeerInfo, 0, len(rs.Peers)),
		}
		if !rs.LastAppliedAt.IsZero() {
			raftInfo.LastAppliedAt = timestamppb.New(rs.LastAppliedAt)
		}
		for _, p := range rs.Peers {
			lag := rs.LastLogIndex - p.MatchIndex
			if lag < 0 {
				lag = 0
			}
			raftInfo.Peers = append(raftInfo.Peers, &adminpb.RaftPeerInfo{
				NodeId:     p.NodeID,
				Status:     adminpb.NodeStatus_NODE_STATUS_UNSPECIFIED,
				MatchIndex: p.MatchIndex,
				NextIndex:  p.NextIndex,
				Lag:        lag,
			})
		}

		node.Role = mapRaftRole(rs.Role)
		node.Status = mapRaftStatus(rs.Status)
		node.ConsensusDetails = &adminpb.NodeInfo_Raft{Raft: raftInfo}
	}

	return &adminpb.GetNodeInfoResponse{Node: node}, nil
}

func peerInfosFromMap(peerAddrs map[string]string) []*adminpb.PeerInfo {
	if len(peerAddrs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(peerAddrs))
	for id := range peerAddrs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]*adminpb.PeerInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, &adminpb.PeerInfo{
			NodeId:  id,
			Address: peerAddrs[id],
		})
	}
	return out
}

func mapConsensusType(v string) adminpb.ConsensusType {
	switch v {
	case "raft":
		return adminpb.ConsensusType_CONSENSUS_TYPE_RAFT
	default:
		return adminpb.ConsensusType_CONSENSUS_TYPE_UNSPECIFIED
	}
}

func mapRaftRole(v raftconsensus.Role) adminpb.NodeRole {
	switch v {
	case raftconsensus.Leader:
		return adminpb.NodeRole_NODE_ROLE_LEADER
	case raftconsensus.Follower:
		return adminpb.NodeRole_NODE_ROLE_FOLLOWER
	case raftconsensus.Candidate:
		return adminpb.NodeRole_NODE_ROLE_CANDIDATE
	default:
		return adminpb.NodeRole_NODE_ROLE_UNSPECIFIED
	}
}

func mapRaftStatus(v raftconsensus.NodeStatus) adminpb.NodeStatus {
	switch v {
	case raftconsensus.NodeStatusHealthy:
		return adminpb.NodeStatus_NODE_STATUS_HEALTHY
	case raftconsensus.NodeStatusDegraded:
		return adminpb.NodeStatus_NODE_STATUS_DEGRADED
	default:
		return adminpb.NodeStatus_NODE_STATUS_UNSPECIFIED
	}
}

func safeInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
