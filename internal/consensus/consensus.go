// Package consensus defines the minimal interface between the replicated state
// machine and a consensus implementation.
package consensus

import "context"

// Consensus is the interface implemented by the active consensus engine (Raft).
type Consensus interface {
	Run(ctx context.Context)
	StartCommand(cmd []byte) (index int64, isLeader bool)
	ApplyCh() <-chan ApplyMsg
	IsLeader() bool
	Snapshot(index int64, data []byte) error
	Stop()
}

// ApplyMsg is delivered by the consensus layer to the state machine.
type ApplyMsg struct {
	CommandValid bool
	Command      []byte
	CommandIndex int64

	SnapshotValid bool
	Snapshot      []byte
	SnapshotIndex int64
}
