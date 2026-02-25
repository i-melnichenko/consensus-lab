# consensus-lab

Raft-backed distributed KV store in Go. A `cmd/node` process exposes three gRPC APIs on a single gRPC port:

- KV API (`kv.v1.KVService`) — client reads/writes
- Admin API (`admin.v1.AdminService`) — node/cluster diagnostics (`GetNodeInfo`)
- Raft API (`raft.v1.RaftService`) — consensus peer traffic

## Implementation Status

### Raft ✓

| Component | Status |
|---|---|
| Leader election | ✓ |
| Log replication | ✓ |
| Commit by majority | ✓ |
| Apply loop | ✓ |
| Persistence (`InMemoryStorage`, `JSONStorage`) | ✓ |
| Degraded-mode safety | ✓ |
| gRPC transport (`Server`, `PeerClient`) | ✓ |
| Snapshots / InstallSnapshot | ✓ |
| Membership changes | — |
| Linearizable reads | — |

## Documentation

- [Raft implementation](docs/consensus/raft.md) — protocol logic (elections, replication, commit, safety) with code mapping
- [Observability](docs/observability.md) — tracing, metrics, Jaeger/Prometheus/Grafana, pprof, dashboard usage
- [CLI client](docs/client.md) — KV/Admin commands, routing semantics, batch modes, examples
- [Benchmarking](docs/benchmark.md) — benchmark list, methodology, units, tuning vars
- [Fault tolerance scenarios](docs/fault-tolerance.md) — failure/recovery scripts, scenarios, env vars, results

## Quick Start (Docker)

Run a local Raft cluster with a single command:

```bash
make docker-up
```

Docker Compose starts 5 nodes. Each node exposes a single host gRPC port (shared by KV/Admin/Raft):

| Node   | Shared gRPC |
|--------|-------------|
| node-1 | `:8081` |
| node-2 | `:8082` |
| node-3 | `:8083` |
| node-4 | `:8084` |
| node-5 | `:8085` |

It also starts the local observability stack:

- Jaeger UI: `http://localhost:16686`
- Prometheus UI: `http://localhost:9090`
- Grafana UI: `http://localhost:3000` (default `admin` / `admin`)

## CLI Client

CLI usage, routing semantics, batch commands (`put-batch` / `get-batch` / `delete-batch`) and admin dashboard examples are documented in [`docs/client.md`](docs/client.md).

```bash
# quick examples
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 put foo bar
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 get foo
make admin
```

## Run Node Manually

Environment variables:

| Variable | Description |
|----------|-------------|
| `APP_NODE_ID` | Local node ID |
| `APP_CONSENSUS_TYPE` | must be `raft` |
| `APP_LOG_LEVEL` | `debug\|info\|warn\|error` |
| `APP_GRPC_ADDR` | Shared gRPC listen address for KV/Admin/Raft |
| `APP_DATA_DIR` | Local Raft storage directory |
| `APP_PEERS` | Comma-separated peers (`id=host:port` or `host:port`). The node's own ID is ignored, so the full cluster list can be used on every node. |
| `APP_SNAPSHOT_EVERY` | Trigger a snapshot after this many applied commands. `0` disables (default). |
| `APP_METRICS_ADDR` | HTTP metrics listen address (Prometheus `/metrics`). Empty disables. |
| `APP_TRACING_ENABLED` | Enable OpenTelemetry tracing export (`true` / `false`). |
| `APP_TRACING_ENDPOINT` | OTLP/gRPC endpoint for trace export (for example `jaeger:4317`). |
| `APP_TRACING_SERVICE_NAME` | Trace service name reported to the exporter (for example `consensus-node`). |
| `APP_PPROF_ADDR` | HTTP `pprof` listen address. Empty disables (recommended by default). |

3-node cluster example:

```bash
# All three nodes share the same APP_PEERS list — the node removes itself automatically.

# terminal 1
APP_NODE_ID=node-1 APP_GRPC_ADDR=:8081 \
APP_DATA_DIR=./var/node-1 \
APP_PEERS=node-1=:8081,node-2=:8082,node-3=:8083 \
go run ./cmd/node

# terminal 2
APP_NODE_ID=node-2 APP_GRPC_ADDR=:8082 \
APP_DATA_DIR=./var/node-2 \
APP_PEERS=node-1=:8081,node-2=:8082,node-3=:8083 \
go run ./cmd/node

# terminal 3
APP_NODE_ID=node-3 APP_GRPC_ADDR=:8083 \
APP_DATA_DIR=./var/node-3 \
APP_PEERS=node-1=:8081,node-2=:8082,node-3=:8083 \
go run ./cmd/node
```

Note: cluster membership is static. Runtime add/remove peers is not implemented.

## Build & Test

```bash
go build ./...
go test ./...
```

## Benchmarking

Run the local benchmark suite against the local Docker cluster:

```bash
make docker-up
make bench
```

Run a single benchmark:

```bash
make bench-write_latency
make bench-read_latency
make bench-delete_latency
make bench-failover_time
make bench-stale_reads
make bench-slow_follower_recovery
```

Results are written to `.results/benchmark/*.txt`.

Benchmark details are documented in [`docs/benchmark.md`](docs/benchmark.md).

## Fault Tolerance Scenarios

Run scripted failure/recovery scenarios against the local Docker cluster:

```bash
make fault-test
```

Open the live admin dashboard in another terminal while running scenarios/benchmarks:

```bash
make admin
```

Fault tolerance scenario details (scenario list, semantics, env vars, results) are documented in [`docs/fault-tolerance.md`](docs/fault-tolerance.md).

Implementation scripts are located in `scripts/`:

- `scripts/benchmark.sh`
- `scripts/fault_tolerance.sh`

Result directories:

- benchmarks: `.results/benchmark/`
- fault tolerance scenarios: `.results/fault/`

Notes:

- Numbers are highly environment-dependent (laptop model, Docker runtime, local CPU load, background processes).
- Tail latency (`p95`/`p99`) can vary noticeably between runs because of scheduler jitter and GC.

## Requirements

- Go 1.26+
- Docker + Docker Compose (for containerised cluster)
- [buf](https://buf.build) + protoc-gen-go + protoc-gen-go-grpc — only if regenerating proto (`buf generate`)
