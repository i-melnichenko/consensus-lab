// Package kv implements the in-memory key-value state machine applied by Raft.
package kv

// CommandType identifies a KV operation encoded in the Raft log.
type CommandType string

// Supported KV commands.
const (
	PutCmd    CommandType = "put"
	DeleteCmd CommandType = "delete"
)

// Command is the serialized operation applied to the KV store.
type Command struct {
	Type  CommandType `json:"type"`
	Key   string      `json:"key"`
	Value string      `json:"value,omitempty"`
}
