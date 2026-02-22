package raftgrpc

import (
	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

// --- RequestVote ---

func requestVoteRequestFromPB(pb *raftpb.RequestVoteRequest) *raft.RequestVoteRequest {
	return &raft.RequestVoteRequest{
		Term:         pb.Term,
		CandidateID:  pb.CandidateId,
		LastLogIndex: pb.LastLogIndex,
		LastLogTerm:  pb.LastLogTerm,
	}
}

func requestVoteRequestToPB(r *raft.RequestVoteRequest) *raftpb.RequestVoteRequest {
	return &raftpb.RequestVoteRequest{
		Term:         r.Term,
		CandidateId:  r.CandidateID,
		LastLogIndex: r.LastLogIndex,
		LastLogTerm:  r.LastLogTerm,
	}
}

func requestVoteResponseFromPB(pb *raftpb.RequestVoteResponse) *raft.RequestVoteResponse {
	return &raft.RequestVoteResponse{
		Term:        pb.Term,
		VoteGranted: pb.VoteGranted,
	}
}

func requestVoteResponseToPB(r *raft.RequestVoteResponse) *raftpb.RequestVoteResponse {
	return &raftpb.RequestVoteResponse{
		Term:        r.Term,
		VoteGranted: r.VoteGranted,
	}
}

// --- AppendEntries ---

func appendEntriesRequestFromPB(pb *raftpb.AppendEntriesRequest) *raft.AppendEntriesRequest {
	entries := make([]raft.LogEntry, len(pb.Entries))
	for i, e := range pb.Entries {
		entries[i] = raft.LogEntry{
			Term:    e.Term,
			Command: e.Command,
		}
	}
	return &raft.AppendEntriesRequest{
		Term:         pb.Term,
		LeaderID:     pb.LeaderId,
		PrevLogIndex: pb.PrevLogIndex,
		PrevLogTerm:  pb.PrevLogTerm,
		Entries:      entries,
		LeaderCommit: pb.LeaderCommit,
	}
}

func appendEntriesRequestToPB(r *raft.AppendEntriesRequest) *raftpb.AppendEntriesRequest {
	entries := make([]*raftpb.LogEntry, len(r.Entries))
	for i, e := range r.Entries {
		entries[i] = &raftpb.LogEntry{
			Term:    e.Term,
			Command: e.Command,
		}
	}
	return &raftpb.AppendEntriesRequest{
		Term:         r.Term,
		LeaderId:     r.LeaderID,
		PrevLogIndex: r.PrevLogIndex,
		PrevLogTerm:  r.PrevLogTerm,
		Entries:      entries,
		LeaderCommit: r.LeaderCommit,
	}
}

func appendEntriesResponseFromPB(pb *raftpb.AppendEntriesResponse) *raft.AppendEntriesResponse {
	return &raft.AppendEntriesResponse{
		Term:          pb.Term,
		Success:       pb.Success,
		ConflictTerm:  pb.ConflictTerm,
		ConflictIndex: pb.ConflictIndex,
	}
}

func appendEntriesResponseToPB(r *raft.AppendEntriesResponse) *raftpb.AppendEntriesResponse {
	return &raftpb.AppendEntriesResponse{
		Term:          r.Term,
		Success:       r.Success,
		ConflictTerm:  r.ConflictTerm,
		ConflictIndex: r.ConflictIndex,
	}
}

// --- InstallSnapshot ---

func clusterConfigFromPB(pb *raftpb.ClusterConfig) raft.ClusterConfig {
	if pb == nil {
		return raft.ClusterConfig{}
	}
	return raft.ClusterConfig{Members: append([]string(nil), pb.Members...)}
}

func clusterConfigToPB(cfg raft.ClusterConfig) *raftpb.ClusterConfig {
	return &raftpb.ClusterConfig{
		Members: append([]string(nil), cfg.Members...),
	}
}

func installSnapshotRequestFromPB(pb *raftpb.InstallSnapshotRequest) *raft.InstallSnapshotRequest {
	return &raft.InstallSnapshotRequest{
		Term:              pb.Term,
		LeaderID:          pb.LeaderId,
		LastIncludedIndex: pb.LastIncludedIndex,
		LastIncludedTerm:  pb.LastIncludedTerm,
		Config:            clusterConfigFromPB(pb.Config),
		Data:              append([]byte(nil), pb.Data...),
	}
}

func installSnapshotRequestToPB(r *raft.InstallSnapshotRequest) *raftpb.InstallSnapshotRequest {
	return &raftpb.InstallSnapshotRequest{
		Term:              r.Term,
		LeaderId:          r.LeaderID,
		LastIncludedIndex: r.LastIncludedIndex,
		LastIncludedTerm:  r.LastIncludedTerm,
		Config:            clusterConfigToPB(r.Config),
		Data:              append([]byte(nil), r.Data...),
	}
}

func installSnapshotResponseFromPB(pb *raftpb.InstallSnapshotResponse) *raft.InstallSnapshotResponse {
	return &raft.InstallSnapshotResponse{Term: pb.Term}
}

func installSnapshotResponseToPB(r *raft.InstallSnapshotResponse) *raftpb.InstallSnapshotResponse {
	return &raftpb.InstallSnapshotResponse{Term: r.Term}
}
