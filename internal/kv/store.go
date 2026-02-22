package kv

import (
	"encoding/json"
	"sync"
)

// Store is an in-memory key-value state machine.
type Store struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewStore creates an empty KV store.
func NewStore() *Store {
	return &Store{
		data: make(map[string]string),
	}
}

// Get returns the current value for key, if present.
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	val, ok := s.data[key]
	return val, ok
}

// Apply decodes and applies a serialized KV command.
func (s *Store) Apply(raw []byte) error {
	var cmd Command

	if err := json.Unmarshal(raw, &cmd); err != nil {
		return err
	}

	switch cmd.Type {
	case PutCmd:
		s.applyPut(cmd.Key, cmd.Value)
	case DeleteCmd:
		s.applyDelete(cmd.Key)
	}

	return nil
}

// Snapshot returns a serialized copy of the current KV state.
func (s *Store) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Copy to avoid exposing internal map during marshaling.
	cp := make(map[string]string, len(s.data))
	for k, v := range s.data {
		cp[k] = v
	}
	return json.Marshal(cp)
}

// RestoreSnapshot replaces the current state with the provided snapshot bytes.
// Empty snapshot bytes reset the store to an empty state.
func (s *Store) RestoreSnapshot(raw []byte) error {
	if len(raw) == 0 {
		s.mu.Lock()
		s.data = make(map[string]string)
		s.mu.Unlock()
		return nil
	}

	var restored map[string]string
	if err := json.Unmarshal(raw, &restored); err != nil {
		return err
	}
	if restored == nil {
		restored = make(map[string]string)
	}

	s.mu.Lock()
	s.data = restored
	s.mu.Unlock()
	return nil
}

func (s *Store) applyPut(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[key] = value
}

func (s *Store) applyDelete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data, key)
}
