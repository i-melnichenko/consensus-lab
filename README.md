# consensus-lab

Raft-backed distributed KV store in Go. A `cmd/node` process exposes three gRPC APIs:

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

## Quick Start (Docker)

Run a local Raft cluster with a single command:

```bash
docker compose up --build
```

Docker Compose starts 5 nodes. Host ports:

| Node   | KV gRPC | Admin gRPC |
|--------|---------|------------|
| node-1 | `:8081` | `:8091` |
| node-2 | `:8082` | `:8092` |
| node-3 | `:8083` | `:8093` |
| node-4 | `:8084` | `:8094` |
| node-5 | `:8085` | `:8095` |

## CLI Client

`--addr` accepts a comma-separated list of nodes. The client routes requests automatically:

- **get** — picks a random node (distributes reads across replicas)
- **put / delete** — tries all nodes until the leader accepts the write
- **admin** — polls each admin endpoint and renders a live table (refresh every 1s)

```bash
# point at the whole cluster — leader discovery is automatic
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083 put foo bar
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083 get foo
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083 delete foo

# single node still works
go run ./cmd/client --addr localhost:8081 get foo

# live admin dashboard (use Admin gRPC ports)
go run ./cmd/client --addr localhost:8091,localhost:8092,localhost:8093 admin
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `localhost:8080` | Comma-separated gRPC addresses for the selected mode (KV or Admin) |
| `--timeout` | `5s` | Request timeout |

## Run Node Manually

Environment variables:

| Variable | Description |
|----------|-------------|
| `APP_NODE_ID` | Local node ID |
| `APP_CONSENSUS_TYPE` | must be `raft` |
| `APP_LOG_LEVEL` | `debug\|info\|warn\|error` |
| `APP_KV_GRPC_ADDR` | KV gRPC listen address |
| `APP_ADMIN_GRPC_ADDR` | Admin gRPC listen address |
| `APP_CONSENSUS_GRPC_ADDR` | Raft gRPC listen address |
| `APP_DATA_DIR` | Local Raft storage directory |
| `APP_PEERS` | Comma-separated peers (`id=host:port` or `host:port`). The node's own ID is ignored, so the full cluster list can be used on every node. |
| `APP_SNAPSHOT_EVERY` | Trigger a snapshot after this many applied commands. `0` disables (default). |

3-node cluster example:

```bash
# All three nodes share the same APP_PEERS list — the node removes itself automatically.

# terminal 1
APP_NODE_ID=node-1 APP_KV_GRPC_ADDR=:8081 APP_ADMIN_GRPC_ADDR=:8091 APP_CONSENSUS_GRPC_ADDR=:9091 \
APP_DATA_DIR=./var/node-1 \
APP_PEERS=node-1=:9091,node-2=:9092,node-3=:9093 \
go run ./cmd/node

# terminal 2
APP_NODE_ID=node-2 APP_KV_GRPC_ADDR=:8082 APP_ADMIN_GRPC_ADDR=:8092 APP_CONSENSUS_GRPC_ADDR=:9092 \
APP_DATA_DIR=./var/node-2 \
APP_PEERS=node-1=:9091,node-2=:9092,node-3=:9093 \
go run ./cmd/node

# terminal 3
APP_NODE_ID=node-3 APP_KV_GRPC_ADDR=:8083 APP_ADMIN_GRPC_ADDR=:8093 APP_CONSENSUS_GRPC_ADDR=:9093 \
APP_DATA_DIR=./var/node-3 \
APP_PEERS=node-1=:9091,node-2=:9092,node-3=:9093 \
go run ./cmd/node
```

Note: cluster membership is static. Runtime add/remove peers is not implemented.

### Fault tolerance

A 3-node cluster tolerates **1 node failure**. With only 1 node remaining the
cluster stops accepting writes (Raft requires a majority quorum of 2).

| Nodes | Quorum | Failures tolerated |
|-------|--------|--------------------|
| 1     | 1      | 0                  |
| 3     | 2      | **1**              |
| 5     | 3      | 2                  |

## Build & Test

```bash
go build ./...
go test ./...
```

## Benchmarking

Run the local benchmark suite against the local Docker cluster:

```bash
docker compose up --build
./benchmark.sh
```

Run a single benchmark:

```bash
./benchmark.sh write_latency
./benchmark.sh read_latency
./benchmark.sh failover_time
./benchmark.sh stale_reads
./benchmark.sh slow_follower_recovery
```

The script measures:

- sequential write latency
- follower-distributed read latency
- parallel write throughput
- leader failover time
- stale follower reads (demonstrates non-linearizable follower reads without ReadIndex)
- slow follower pause/resume recovery time (log backfill)

Results are written to `.bench-results/*.txt`.

Notes:

- Numbers are highly environment-dependent (laptop model, Docker runtime, local CPU load, background processes).
- Tail latency (`p95`/`p99`) can vary noticeably between runs because of scheduler jitter and GC.
- Benchmarks include simple retries for some setup writes to reduce flaky failures during transient leader changes.

## Requirements

- Go 1.26+
- Docker + Docker Compose (for containerised cluster)
- [buf](https://buf.build) + protoc-gen-go + protoc-gen-go-grpc — only if regenerating proto (`buf generate`)
