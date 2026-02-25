package raft

import "time"

// Metrics captures Raft-layer metric sinks used by the node implementation.
type Metrics interface {
	ObserveRaftAppendEntriesRPCDuration(nodeID, peerID string, heartbeat bool, d time.Duration)
	IncRaftAppendEntriesReject(nodeID, peerID string, heartbeat bool)
	IncRaftAppendEntriesRPCError(nodeID, peerID string, heartbeat bool, kind string)
	ObserveRaftInstallSnapshotRPCDuration(nodeID, peerID string, d time.Duration)
	ObserveRaftInstallSnapshotSendBytes(nodeID, peerID string, n int)
	IncRaftInstallSnapshotSend(nodeID, peerID, result string)
	IncRaftElectionStarted(nodeID string)
	IncRaftElectionWon(nodeID string)
	IncRaftElectionLost(nodeID, reason string)
	IncRaftStorageError(nodeID, op string)
	SetRaftApplyLag(nodeID string, lag int64)
	SetRaftIsLeader(nodeID string, isLeader bool)
	ObserveRaftStartToCommitDuration(nodeID string, d time.Duration)
	ObserveRaftCommitToApplyDuration(nodeID string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) ObserveRaftAppendEntriesRPCDuration(string, string, bool, time.Duration) {}
func (noopMetrics) IncRaftAppendEntriesReject(string, string, bool)                         {}
func (noopMetrics) IncRaftAppendEntriesRPCError(string, string, bool, string)               {}
func (noopMetrics) ObserveRaftInstallSnapshotRPCDuration(string, string, time.Duration)     {}
func (noopMetrics) ObserveRaftInstallSnapshotSendBytes(string, string, int)                 {}
func (noopMetrics) IncRaftInstallSnapshotSend(string, string, string)                       {}
func (noopMetrics) IncRaftElectionStarted(string)                                           {}
func (noopMetrics) IncRaftElectionWon(string)                                               {}
func (noopMetrics) IncRaftElectionLost(string, string)                                      {}
func (noopMetrics) IncRaftStorageError(string, string)                                      {}
func (noopMetrics) SetRaftApplyLag(string, int64)                                           {}
func (noopMetrics) SetRaftIsLeader(string, bool)                                            {}
func (noopMetrics) ObserveRaftStartToCommitDuration(string, time.Duration)                  {}
func (noopMetrics) ObserveRaftCommitToApplyDuration(string, time.Duration)                  {}
