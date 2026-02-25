package kv

import (
	"context"
	"encoding/json"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Store is an in-memory key-value state machine.
type Store struct {
	mu     sync.RWMutex
	data   map[string]string
	tracer oteltrace.Tracer
}

// NewStore creates an empty KV store.
func NewStore(tracer oteltrace.Tracer) *Store {
	return &Store{
		data:   make(map[string]string),
		tracer: tracer,
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
func (s *Store) Apply(ctx context.Context, raw []byte) error {
	_, span := s.tracer.Start(ctx, "kv.store.Apply", oteltrace.WithAttributes(attribute.Int("kv.command.bytes", len(raw))))
	defer span.End()

	var cmd Command

	if err := json.Unmarshal(raw, &cmd); err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return err
	}
	span.SetAttributes(
		attribute.String("kv.command.type", string(cmd.Type)),
		attribute.String("kv.key", cmd.Key),
		attribute.Int("kv.value.bytes", len(cmd.Value)),
	)

	switch cmd.Type {
	case PutCmd:
		s.applyPut(cmd.Key, cmd.Value)
	case DeleteCmd:
		s.applyDelete(cmd.Key)
	}

	return nil
}

// Snapshot returns a serialized copy of the current KV state.
func (s *Store) Snapshot(ctx context.Context) ([]byte, error) {
	_, span := s.tracer.Start(ctx, "kv.store.Snapshot")
	defer span.End()

	s.mu.RLock()
	defer s.mu.RUnlock()
	span.SetAttributes(attribute.Int("kv.store.items", len(s.data)))

	// Copy to avoid exposing internal map during marshaling.
	cp := make(map[string]string, len(s.data))
	for k, v := range s.data {
		cp[k] = v
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(attribute.Int("kv.snapshot.bytes", len(raw)))
	return raw, nil
}

// RestoreSnapshot replaces the current state with the provided snapshot bytes.
// Empty snapshot bytes reset the store to an empty state.
func (s *Store) RestoreSnapshot(ctx context.Context, raw []byte) error {
	_, span := s.tracer.Start(ctx, "kv.store.RestoreSnapshot", oteltrace.WithAttributes(attribute.Int("kv.snapshot.bytes", len(raw))))
	defer span.End()

	if len(raw) == 0 {
		s.mu.Lock()
		s.data = make(map[string]string)
		s.mu.Unlock()
		span.SetAttributes(attribute.Int("kv.store.items", 0))
		return nil
	}

	var restored map[string]string
	if err := json.Unmarshal(raw, &restored); err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return err
	}
	if restored == nil {
		restored = make(map[string]string)
	}

	s.mu.Lock()
	s.data = restored
	s.mu.Unlock()
	span.SetAttributes(attribute.Int("kv.store.items", len(restored)))
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
