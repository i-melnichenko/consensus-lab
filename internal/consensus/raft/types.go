package raft

import "errors"

// Role is the current Raft role of a node.
type Role int

// Node roles in the Raft state machine.
const (
	Follower Role = iota
	Candidate
	Leader
)

// NodeStatus reports operational health of the node runtime.
type NodeStatus string

// Runtime health states exposed by Status.
const (
	NodeStatusHealthy  NodeStatus = "healthy"
	NodeStatusDegraded NodeStatus = "degraded"
)

// EntryType identifies the kind of Raft log entry payload.
type EntryType uint8

// Supported Raft log entry types.
const (
	EntryCommand EntryType = 0 // backward-compat zero value
)

// ClusterConfig holds the set of member IDs for quorum calculation.
type ClusterConfig struct {
	Members []string `json:"members"` // all member IDs including self
}

// LogEntry is a single entry in the Raft replicated log.
type LogEntry struct {
	Term    int64
	Type    EntryType // currently only EntryCommand is used
	Command []byte
}

// AppendEntriesRequest is sent by the leader for replication and heartbeats.
type AppendEntriesRequest struct {
	Term         int64
	LeaderID     string
	PrevLogIndex int64
	PrevLogTerm  int64
	Entries      []LogEntry
	LeaderCommit int64
}

// AppendEntriesResponse is returned by followers for AppendEntries.
type AppendEntriesResponse struct {
	Term          int64
	Success       bool
	ConflictTerm  int64
	ConflictIndex int64
}

// HardState stores persistent Raft metadata required across restarts.
type HardState struct {
	CurrentTerm int64         `json:"current_term"`
	VotedFor    string        `json:"voted_for"`
	CommitIndex int64         `json:"commit_index"`
	Config      ClusterConfig `json:"config"`
}

// RequestVoteRequest is sent by candidates during leader election.
type RequestVoteRequest struct {
	Term         int64
	CandidateID  string
	LastLogIndex int64
	LastLogTerm  int64
}

// RequestVoteResponse is returned by peers in response to RequestVote.
type RequestVoteResponse struct {
	Term        int64
	VoteGranted bool
}

// Snapshot holds the application state at a particular log index.
type Snapshot struct {
	LastIncludedIndex int64         `json:"last_included_index"`
	LastIncludedTerm  int64         `json:"last_included_term"`
	Config            ClusterConfig `json:"config"`
	Data              []byte        `json:"data"`
}

// InstallSnapshotRequest is sent by the leader to bring a lagging follower
// up to date when the required log entries have already been compacted.
type InstallSnapshotRequest struct {
	Term              int64
	LeaderID          string
	LastIncludedIndex int64
	LastIncludedTerm  int64
	Config            ClusterConfig
	Data              []byte
}

// InstallSnapshotResponse acknowledges snapshot installation.
type InstallSnapshotResponse struct {
	Term int64
}

// ErrNilStorage is returned when NewNode is called with a nil Storage.
var ErrNilStorage = errors.New("raft: nil storage")

// ErrNilLogger is returned when NewNode is called with a nil logger.
var ErrNilLogger = errors.New("raft: nil logger")

// ErrNodeDegraded is returned when the node stopped progressing after a fatal background error.
var ErrNodeDegraded = errors.New("raft: node degraded")
