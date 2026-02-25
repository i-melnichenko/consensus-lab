package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ConsensusType selects the consensus implementation used by the node.
type ConsensusType string

// Supported consensus engine types.
const (
	ConsensusTypeRaft ConsensusType = "raft"
)

// Config contains runtime settings for a node process.
type Config struct {
	NodeID        string
	ConsensusType ConsensusType
	LogLevel      string

	TracingEnabled     bool
	TracingEndpoint    string
	TracingServiceName string

	GRPCAddr    string
	MetricsAddr string
	PprofAddr   string
	DataDir     string

	PeerAddrs []string

	// SnapshotEvery triggers a snapshot after this many applied commands.
	// Zero disables automatic snapshots.
	SnapshotEvery uint64
}

// DefaultConfig returns a local-development configuration.
func DefaultConfig() Config {
	return Config{
		NodeID:             "node-1",
		ConsensusType:      ConsensusTypeRaft,
		LogLevel:           "info",
		TracingServiceName: "consensus-node",
		GRPCAddr:           ":8080",
		MetricsAddr:        ":9090",
		DataDir:            "./var/node-1",
	}
}

// LoadConfigFromEnv loads config from environment variables.
//
// Supported vars:
// - APP_NODE_ID
// - APP_CONSENSUS_TYPE (must be "raft")
// - APP_LOG_LEVEL (debug|info|warn|error)
// - APP_TRACING_ENABLED (bool)
// - APP_TRACING_ENDPOINT (OTLP gRPC endpoint, e.g. "jaeger:4317")
// - APP_TRACING_SERVICE_NAME
// - APP_GRPC_ADDR
// - APP_METRICS_ADDR (HTTP /metrics endpoint; empty disables)
// - APP_PPROF_ADDR (HTTP net/http/pprof endpoint; empty disables)
// - APP_DATA_DIR
// - APP_PEERS (comma-separated addresses)
// - APP_SNAPSHOT_EVERY (uint, 0 = disabled)
func LoadConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()

	if v := strings.TrimSpace(os.Getenv("APP_NODE_ID")); v != "" {
		cfg.NodeID = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_CONSENSUS_TYPE")); v != "" {
		cfg.ConsensusType = ConsensusType(v)
	}
	if v := strings.TrimSpace(os.Getenv("APP_LOG_LEVEL")); v != "" {
		cfg.LogLevel = strings.ToLower(v)
	}
	if v := strings.TrimSpace(os.Getenv("APP_TRACING_ENABLED")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("app: invalid APP_TRACING_ENABLED %q: %w", v, err)
		}
		cfg.TracingEnabled = b
	}
	if v := strings.TrimSpace(os.Getenv("APP_TRACING_ENDPOINT")); v != "" {
		cfg.TracingEndpoint = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_TRACING_SERVICE_NAME")); v != "" {
		cfg.TracingServiceName = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_GRPC_ADDR")); v != "" {
		cfg.GRPCAddr = v
	}
	if v, ok := os.LookupEnv("APP_METRICS_ADDR"); ok {
		cfg.MetricsAddr = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("APP_PPROF_ADDR"); ok {
		cfg.PprofAddr = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(os.Getenv("APP_DATA_DIR")); v != "" {
		cfg.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_PEERS")); v != "" {
		cfg.PeerAddrs = splitCSV(v)
	}
	if v := strings.TrimSpace(os.Getenv("APP_SNAPSHOT_EVERY")); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("app: invalid APP_SNAPSHOT_EVERY %q: %w", v, err)
		}
		cfg.SnapshotEvery = n
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that required settings are present and supported.
func (c Config) Validate() error {
	if strings.TrimSpace(c.NodeID) == "" {
		return fmt.Errorf("app: node id is required")
	}
	switch c.ConsensusType {
	case ConsensusTypeRaft:
	default:
		return fmt.Errorf("app: unsupported consensus type %q", c.ConsensusType)
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("app: unsupported log level %q", c.LogLevel)
	}
	if strings.TrimSpace(c.GRPCAddr) == "" {
		return fmt.Errorf("app: grpc addr is required")
	}
	// MetricsAddr may be empty to disable the metrics endpoint.
	// PprofAddr may be empty to disable the pprof endpoint.
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("app: data dir is required")
	}
	if c.TracingEnabled {
		if strings.TrimSpace(c.TracingEndpoint) == "" {
			return fmt.Errorf("app: tracing endpoint is required when tracing is enabled")
		}
		if strings.TrimSpace(c.TracingServiceName) == "" {
			return fmt.Errorf("app: tracing service name is required when tracing is enabled")
		}
	}
	return nil
}

// PeerAddrMap parses PeerAddrs into a map of peer-id -> address.
// Each entry is either "host:port" (peer ID equals address) or "peer-id=host:port".
func (c Config) PeerAddrMap() (map[string]string, error) {
	out := make(map[string]string, len(c.PeerAddrs))
	for _, raw := range c.PeerAddrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		id := raw
		addr := raw
		if left, right, ok := strings.Cut(raw, "="); ok {
			id = strings.TrimSpace(left)
			addr = strings.TrimSpace(right)
		}

		if id == "" || addr == "" {
			return nil, fmt.Errorf("app: invalid peer entry %q", raw)
		}
		if _, exists := out[id]; exists {
			return nil, fmt.Errorf("app: duplicate peer id %q", id)
		}
		out[id] = addr
	}
	return out, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
