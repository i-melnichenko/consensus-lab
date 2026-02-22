package raft

// Storage persists Raft hard state, log entries, and snapshots.
// All methods must be safe for concurrent use.
type Storage interface {
	// Load restores the full persistent state on startup.
	Load() (*PersistentState, error)

	// SaveHardState durably writes currentTerm and votedFor.
	SaveHardState(state HardState) error

	// AppendLog appends new entries to the end of the stored log.
	AppendLog(entries []LogEntry) error

	// TruncateLog keeps the first keepN entries of the stored log and discards the rest.
	// Used when a follower detects a conflicting suffix and must roll back.
	TruncateLog(keepN int64) error

	// SetLog atomically replaces the entire stored log.
	// baseIndex is the snapshotIndex at the time of compaction; entries are those
	// that follow the snapshot. Used during log compaction after a snapshot is taken.
	SetLog(baseIndex int64, entries []LogEntry) error

	// SaveSnapshot durably writes a snapshot.
	SaveSnapshot(snap Snapshot) error
}

// PersistentState is returned by Storage.Load.
type PersistentState struct {
	HardState

	// LogBase is the snapshotIndex when the stored log was last compacted.
	// Stored log entries represent Raft indices LogBase+1, LogBase+2, ...
	LogBase int64 `json:"log_base"`

	Snapshot *Snapshot  `json:"snapshot,omitempty"`
	Log      []LogEntry `json:"log"`
}
