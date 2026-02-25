package raftgrpc

import (
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/i-melnichenko/consensus-lab/internal/consensus/raft"
	raftpb "github.com/i-melnichenko/consensus-lab/pkg/proto/raftv1"
)

func recordSpanError(span oteltrace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

func clientRequestVoteAttrs(target string, req *raft.RequestVoteRequest) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("raft.peer.target", target),
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.candidate_id", req.CandidateID),
		attribute.Int64("raft.last_log_index", req.LastLogIndex),
		attribute.Int64("raft.last_log_term", req.LastLogTerm),
	}
}

func clientAppendEntriesAttrs(target string, req *raft.AppendEntriesRequest) []attribute.KeyValue {
	isHeartbeat := len(req.Entries) == 0
	return []attribute.KeyValue{
		attribute.String("raft.peer.target", target),
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.leader_id", req.LeaderID),
		attribute.Int64("raft.prev_log_index", req.PrevLogIndex),
		attribute.Int64("raft.prev_log_term", req.PrevLogTerm),
		attribute.Int("raft.entries_count", len(req.Entries)),
		attribute.Bool("raft.is_heartbeat", isHeartbeat),
		attribute.Int64("raft.leader_commit", req.LeaderCommit),
	}
}

func clientInstallSnapshotAttrs(target string, req *raft.InstallSnapshotRequest) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("raft.peer.target", target),
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.leader_id", req.LeaderID),
		attribute.Int64("raft.snapshot.index", req.LastIncludedIndex),
		attribute.Int64("raft.snapshot.term", req.LastIncludedTerm),
		attribute.Int("raft.snapshot.bytes", len(req.Data)),
	}
}

func serverRequestVoteAttrs(req *raftpb.RequestVoteRequest) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.candidate_id", req.CandidateId),
		attribute.Int64("raft.last_log_index", req.LastLogIndex),
		attribute.Int64("raft.last_log_term", req.LastLogTerm),
	}
}

func serverAppendEntriesAttrs(req *raftpb.AppendEntriesRequest) []attribute.KeyValue {
	isHeartbeat := len(req.Entries) == 0
	return []attribute.KeyValue{
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.leader_id", req.LeaderId),
		attribute.Int64("raft.prev_log_index", req.PrevLogIndex),
		attribute.Int64("raft.prev_log_term", req.PrevLogTerm),
		attribute.Int("raft.entries_count", len(req.Entries)),
		attribute.Bool("raft.is_heartbeat", isHeartbeat),
		attribute.Int64("raft.leader_commit", req.LeaderCommit),
	}
}

func serverInstallSnapshotAttrs(req *raftpb.InstallSnapshotRequest) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64("raft.term", req.Term),
		attribute.String("raft.leader_id", req.LeaderId),
		attribute.Int64("raft.snapshot.index", req.LastIncludedIndex),
		attribute.Int64("raft.snapshot.term", req.LastIncludedTerm),
		attribute.Int("raft.snapshot.bytes", len(req.Data)),
	}
}
