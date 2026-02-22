package raft

import "sync"

// InMemoryStorage keeps persistent Raft state in memory for tests/dev usage.
type InMemoryStorage struct {
	mu      sync.Mutex
	hard    HardState
	logBase int64
	log     []LogEntry
	snap    *Snapshot
}

// NewInMemoryStorage returns an in-memory Storage implementation.
func NewInMemoryStorage() Storage {
	return &InMemoryStorage{}
}

// Load returns a copy of the current in-memory persisted state.
func (s *InMemoryStorage) Load() (*PersistentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var snap *Snapshot
	if s.snap != nil {
		cp := *s.snap
		cp.Data = append([]byte(nil), s.snap.Data...)
		snap = &cp
	}

	return &PersistentState{
		HardState: s.hard,
		LogBase:   s.logBase,
		Snapshot:  snap,
		Log:       cloneLogEntries(s.log),
	}, nil
}

// SaveHardState stores the latest hard state.
func (s *InMemoryStorage) SaveHardState(state HardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hard = state
	return nil
}

// AppendLog appends entries to the in-memory log.
func (s *InMemoryStorage) AppendLog(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, cloneLogEntries(entries)...)
	return nil
}

// TruncateLog truncates the in-memory log to the first keepN entries.
func (s *InMemoryStorage) TruncateLog(keepN int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keepN < 0 {
		keepN = 0
	}
	if keepN > int64(len(s.log)) {
		keepN = int64(len(s.log))
	}
	s.log = s.log[:keepN]
	return nil
}

// SetLog replaces the in-memory log and stores the compaction base index.
func (s *InMemoryStorage) SetLog(baseIndex int64, entries []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logBase = baseIndex
	s.log = cloneLogEntries(entries)
	return nil
}

// SaveSnapshot stores a copy of the snapshot in memory.
func (s *InMemoryStorage) SaveSnapshot(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := snap
	cp.Data = append([]byte(nil), snap.Data...)
	s.snap = &cp
	return nil
}
