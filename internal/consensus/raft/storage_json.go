package raft

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// JSONStorage persists Raft state as JSON files in a local directory.
type JSONStorage struct {
	dir string
}

// NewJSONStorage returns a file-backed Storage implementation rooted at dir.
func NewJSONStorage(dir string) Storage {
	return &JSONStorage{dir: dir}
}

// Load restores hard state, snapshot, and log from disk.
func (s *JSONStorage) Load() (*PersistentState, error) {
	hs, err := s.loadHardState()
	if err != nil {
		return nil, err
	}

	snap, err := s.loadSnapshot()
	if err != nil {
		return nil, err
	}

	logBase, logEntries, err := s.loadLog()
	if err != nil {
		return nil, err
	}

	return &PersistentState{
		HardState: hs,
		LogBase:   logBase,
		Snapshot:  snap,
		Log:       logEntries,
	}, nil
}

// SaveHardState persists the current hard state to disk.
func (s *JSONStorage) SaveHardState(state HardState) error {
	return writeJSONAtomically(s.hardStatePath(), state)
}

// AppendLog appends entries to the stored log.
func (s *JSONStorage) AppendLog(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	base, existing, err := s.loadLog()
	if err != nil {
		return err
	}
	existing = append(existing, cloneLogEntries(entries)...)
	return s.writeLog(base, existing)
}

// TruncateLog truncates the stored log to the first keepN entries.
func (s *JSONStorage) TruncateLog(keepN int64) error {
	base, existing, err := s.loadLog()
	if err != nil {
		return err
	}
	if keepN < 0 {
		keepN = 0
	}
	if keepN > int64(len(existing)) {
		keepN = int64(len(existing))
	}
	return s.writeLog(base, existing[:keepN])
}

// SetLog replaces the stored log and updates its compaction base index.
func (s *JSONStorage) SetLog(baseIndex int64, entries []LogEntry) error {
	return s.writeLog(baseIndex, entries)
}

// SaveSnapshot persists a snapshot to disk.
func (s *JSONStorage) SaveSnapshot(snap Snapshot) error {
	return writeJSONAtomically(s.snapshotPath(), snap)
}

// storedLog is the on-disk format for the log file.
// Storing base alongside entries makes log compaction crash-safe:
// if the process crashes after saving the snapshot but before replacing the log,
// NewNode can use base to discard stale leading entries on restart.
type storedLog struct {
	Base    int64      `json:"base"`
	Entries []LogEntry `json:"entries"`
}

func (s *JSONStorage) writeLog(base int64, entries []LogEntry) error {
	return writeJSONAtomically(s.logPath(), storedLog{Base: base, Entries: entries})
}

func (s *JSONStorage) hardStatePath() string { return filepath.Join(s.dir, "hard_state.json") }
func (s *JSONStorage) logPath() string       { return filepath.Join(s.dir, "log.json") }
func (s *JSONStorage) snapshotPath() string  { return filepath.Join(s.dir, "snapshot.json") }

func (s *JSONStorage) loadHardState() (HardState, error) {
	var hs HardState
	data, err := os.ReadFile(s.hardStatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return hs, nil
		}
		return hs, err
	}
	if len(data) == 0 {
		return hs, nil
	}
	if err := json.Unmarshal(data, &hs); err != nil {
		return hs, err
	}
	return hs, nil
}

func (s *JSONStorage) loadLog() (base int64, entries []LogEntry, err error) {
	data, err := os.ReadFile(s.logPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	if len(data) == 0 {
		return 0, nil, nil
	}

	// Support both old format (bare []LogEntry) and new format ({base, entries}).
	// Try new format first; fall back to old for backward compatibility.
	var sl storedLog
	if err := json.Unmarshal(data, &sl); err == nil && (sl.Entries != nil || sl.Base != 0) {
		return sl.Base, cloneLogEntries(sl.Entries), nil
	}

	var legacy []LogEntry
	if err := json.Unmarshal(data, &legacy); err != nil {
		return 0, nil, err
	}
	return 0, cloneLogEntries(legacy), nil
}

func (s *JSONStorage) loadSnapshot() (*Snapshot, error) {
	data, err := os.ReadFile(s.snapshotPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func writeJSONAtomically(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	//nolint:gosec // tmpName and path are derived from internal storage paths, not user input.
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	// Sync the parent directory so the rename itself is durable.
	//nolint:gosec // dir is derived from the configured storage directory under our control.
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = dirFile.Close() }()

	return dirFile.Sync()
}
