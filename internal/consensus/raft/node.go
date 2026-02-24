// Package raft contains the consensus backbone for the consensus-kv store.
//
// It implements leader election, log replication, commit/apply flow, and
// storage-backed state recovery. The KV store layer sits on top via the
// consensus.Consensus interface: it submits serialized operations through
// StartCommand(cmd) and applies committed entries received from ApplyCh().
//
// Transport wiring is intentionally kept outside this package.
package raft

import (
	"context"
	"sync"
	"time"

	"github.com/i-melnichenko/consensus-lab/internal/consensus"
)

// Node is a single Raft replica that manages elections, replication, and apply.
type Node struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup

	id      string
	peers   map[string]PeerClient
	storage Storage

	role Role

	currentTerm int64
	votedFor    string
	degraded    bool

	log []LogEntry

	// snapshotIndex / snapshotTerm track the last log entry covered by the current snapshot.
	// All log entries in n.log follow snapshotIndex: n.log[i] is at Raft index snapshotIndex+i+1.
	snapshotIndex int64
	snapshotTerm  int64
	// snapshot holds the current snapshot (including data) for leader-side InstallSnapshot sending.
	snapshot *Snapshot
	// pendingSnapshot is set when a snapshot needs to be delivered to the apply channel.
	pendingSnapshot *Snapshot

	commitIndex   int64
	lastApplied   int64
	lastAppliedAt time.Time

	// config is the active cluster configuration (source of quorum).
	// In the current implementation it is static at runtime and restored from
	// persisted hard state / snapshots.
	config ClusterConfig

	nextIndex         map[string]int64
	matchIndex        map[string]int64
	replicateInFlight map[string]bool
	replicatePending  map[string]bool

	electionTimeoutResetCh chan struct{}
	applyNotifyCh          chan struct{}
	replicateNotifyCh      chan struct{}

	applyCh chan consensus.ApplyMsg
	logger  Logger

	newTimer          timerFactory
	newTicker         tickerFactory
	electionTimeoutFn electionTimeoutFunc
	heartbeatInterval time.Duration
}

// NewNode creates a Raft node and restores persisted state from storage.
//
// The peers map must contain remote peers only (do not include the node itself).
// If self is present, it is ignored during normalization.
// Logger is required; pass a slog-compatible logger implementation.
func NewNode(
	id string,
	peers map[string]PeerClient,
	applyCh chan consensus.ApplyMsg,
	storage Storage,
	logger Logger,
) (*Node, error) {
	if storage == nil {
		return nil, ErrNilStorage
	}
	if logger == nil {
		return nil, ErrNilLogger
	}

	normalizedPeers := normalizePeers(id, peers)

	n := &Node{
		id:                     id,
		peers:                  normalizedPeers,
		storage:                storage,
		role:                   Follower,
		log:                    make([]LogEntry, 0),
		nextIndex:              make(map[string]int64),
		matchIndex:             make(map[string]int64),
		replicateInFlight:      make(map[string]bool),
		replicatePending:       make(map[string]bool),
		electionTimeoutResetCh: make(chan struct{}, 1),
		applyNotifyCh:          make(chan struct{}, 1),
		replicateNotifyCh:      make(chan struct{}, 1),
		applyCh:                applyCh,
		logger:                 logger,
		newTimer:               defaultTimerFactory,
		newTicker:              defaultTickerFactory,
		electionTimeoutFn:      randomElectionTimeout,
		heartbeatInterval:      50 * time.Millisecond,
	}

	ps, err := storage.Load()
	if err != nil {
		return nil, err
	}
	n.currentTerm = ps.CurrentTerm
	n.votedFor = ps.VotedFor
	n.commitIndex = ps.CommitIndex

	// Restore config: prefer persisted config; fall back to id + peers.
	if len(ps.Config.Members) > 0 {
		n.config = ps.Config
	} else {
		members := make([]string, 0, 1+len(normalizedPeers))
		members = append(members, id)
		for peerID := range normalizedPeers {
			members = append(members, peerID)
		}
		n.config = ClusterConfig{Members: members}
	}

	if ps.Snapshot != nil {
		n.snapshotIndex = ps.Snapshot.LastIncludedIndex
		n.snapshotTerm = ps.Snapshot.LastIncludedTerm
		snap := *ps.Snapshot
		n.snapshot = &snap
		// Deliver restored local snapshot to the application on startup so the
		// KV state machine rebuilds its in-memory state before applying new log entries.
		n.pendingSnapshot = &snap
		n.commitIndex = ps.Snapshot.LastIncludedIndex
		n.lastApplied = ps.Snapshot.LastIncludedIndex
		// Restore config from snapshot if it carries one.
		if len(ps.Snapshot.Config.Members) > 0 {
			n.config = ps.Snapshot.Config
		}
	}

	// Stored log entries start at LogBase+1.
	// If LogBase < snapshotIndex (crash during compaction), leading entries are stale.
	staleCount := n.snapshotIndex - ps.LogBase
	if staleCount < 0 {
		staleCount = 0
	}
	if staleCount > int64(len(ps.Log)) {
		staleCount = int64(len(ps.Log))
	}
	n.log = cloneLogEntries(ps.Log[staleCount:])

	// commitIndex is persistent so non-snapshotted committed entries can be replayed
	// into the in-memory KV state machine after restart. Clamp for safety when older
	// on-disk formats or partial writes are encountered.
	if n.commitIndex < n.snapshotIndex {
		n.commitIndex = n.snapshotIndex
	}
	if last := n.lastLogIndexLocked(); n.commitIndex > last {
		n.commitIndex = last
	}

	return n, nil
}

// Run starts the Raft background loops and returns immediately.
func (n *Node) Run(ctx context.Context) {
	n.mu.Lock()
	if n.degraded {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()

	ctx, n.cancel = context.WithCancel(ctx)
	n.wg.Add(1)

	go func() {
		defer n.wg.Done()
		n.run(ctx)
	}()

	if n.applyCh != nil {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			n.runApplyLoop(ctx)
		}()
		n.notifyApply()
	}
}

func (n *Node) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n.mu.Lock()
		if n.degraded {
			n.mu.Unlock()
			return
		}
		role := n.role
		n.mu.Unlock()

		switch role {
		case Follower:
			n.runFollower(ctx)
		case Candidate:
			n.runCandidate(ctx)
		case Leader:
			n.runLeader(ctx)
		}
	}
}

// normalizePeers returns a copy of peers without selfID.
func normalizePeers(selfID string, peers map[string]PeerClient) map[string]PeerClient {
	if len(peers) == 0 {
		return map[string]PeerClient{}
	}

	normalized := make(map[string]PeerClient, len(peers))
	for id, client := range peers {
		if id == selfID {
			continue
		}
		normalized[id] = client
	}
	return normalized
}

// quorumSize returns the majority quorum based on the active cluster config.
func (n *Node) quorumSize() int {
	return len(n.config.Members)/2 + 1
}

// lastLogIndexLocked returns the last Raft log index (1..N, or 0 for empty log).
// Caller must hold n.mu.
func (n *Node) lastLogIndexLocked() int64 {
	return n.snapshotIndex + int64(len(n.log))
}

// lastLogTermLocked returns the term of the last log entry.
// Returns snapshotTerm if the log is empty (all entries were compacted).
// Caller must hold n.mu.
func (n *Node) lastLogTermLocked() int64 {
	if len(n.log) == 0 {
		return n.snapshotTerm
	}
	return n.log[len(n.log)-1].Term
}

// entryAtLocked returns the log entry at a given Raft index.
// Caller must hold n.mu. Panics if raftIndex is out of the non-compacted range.
func (n *Node) entryAtLocked(raftIndex int64) LogEntry {
	return n.log[raftIndex-n.snapshotIndex-1]
}

// firstIndexOfTermLocked returns the first Raft index containing term, or 0 if absent.
// Caller must hold n.mu.
func (n *Node) firstIndexOfTermLocked(term int64) int64 {
	for i, entry := range n.log {
		if entry.Term == term {
			return n.snapshotIndex + int64(i+1)
		}
	}
	return 0
}

// lastIndexOfTermLocked returns the last Raft index containing term, or 0 if absent.
// Caller must hold n.mu.
func (n *Node) lastIndexOfTermLocked(term int64) int64 {
	for i := len(n.log) - 1; i >= 0; i-- {
		if n.log[i].Term == term {
			return n.snapshotIndex + int64(i+1)
		}
	}
	return 0
}

func (n *Node) electionTimeoutResetSignal() <-chan struct{} {
	return n.electionTimeoutResetCh
}

func (n *Node) markDegradedLocked(err error) {
	if err == nil || n.degraded {
		return
	}
	n.degraded = true
	if n.logger != nil {
		n.logger.Error(
			"raft node degraded due to persistence error",
			"node_id", n.id,
			"error", err,
		)
	}
}

// Status reports runtime node health.
//
// A degraded node encountered a critical persistence error in a background path
// (for example election/replication processing), logs the error, and stops the
// main role loop from making further progress.
func (n *Node) Status() NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.degraded {
		return NodeStatusDegraded
	}
	return NodeStatusHealthy
}

func (n *Node) persistHardStateLocked() error {
	if n.storage == nil {
		return nil
	}
	return n.storage.SaveHardState(HardState{
		CurrentTerm: n.currentTerm,
		VotedFor:    n.votedFor,
		CommitIndex: n.commitIndex,
		Config:      n.config,
	})
}

func (n *Node) persistAppendLogLocked(entries []LogEntry) error {
	if n.storage == nil {
		return nil
	}
	return n.storage.AppendLog(entries)
}

// persistTruncateLogLocked keeps entries with Raft index in (snapshotIndex, fromIndex).
// Caller must hold n.mu.
func (n *Node) persistTruncateLogLocked(fromIndex int64) error {
	if n.storage == nil {
		return nil
	}
	keepN := fromIndex - n.snapshotIndex - 1
	if keepN < 0 {
		keepN = 0
	}
	return n.storage.TruncateLog(keepN)
}

func (n *Node) persistSnapshotLocked(snap Snapshot) error {
	if n.storage == nil {
		return nil
	}
	return n.storage.SaveSnapshot(snap)
}

// persistCompactLogLocked atomically replaces the stored log with the post-snapshot entries.
// Must be called after n.snapshotIndex has been updated. Caller must hold n.mu.
func (n *Node) persistCompactLogLocked() error {
	if n.storage == nil {
		return nil
	}
	return n.storage.SetLog(n.snapshotIndex, n.log)
}

func cloneLogEntries(src []LogEntry) []LogEntry {
	if len(src) == 0 {
		return nil
	}

	dst := make([]LogEntry, len(src))
	for i, entry := range src {
		dst[i] = LogEntry{
			Term:    entry.Term,
			Type:    entry.Type,
			Command: append([]byte(nil), entry.Command...),
		}
	}

	return dst
}
