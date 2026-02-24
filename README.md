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

## CLI Client

`--addr` accepts a comma-separated list of nodes. The client routes requests automatically:

- **get** — picks a random node (distributes reads across replicas)
- **put / delete** — tries all nodes until the leader accepts the write
- **admin** — polls each admin endpoint and renders a live table (refresh every 500ms)

Write semantics:

- `put` / `delete` are acknowledged only after the command is **committed and applied**
- if the cluster cannot reach quorum (or does not commit in time), the request fails by timeout

```bash
# point at the whole cluster — leader discovery is automatic
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 put foo bar
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 get foo
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 delete foo

# single node still works
go run ./cmd/client --addr localhost:8081 get foo

# live admin dashboard (same shared gRPC ports)
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 admin

# same via Makefile
make admin
NODES=localhost:8081,localhost:8082,localhost:8083 make admin

# inspect services with grpcurl (reflection enabled)
grpcurl -plaintext localhost:8081 list
grpcurl -plaintext -d '{}' localhost:8081 admin.v1.AdminService/GetNodeInfo
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `localhost:8080` | Comma-separated gRPC addresses for the selected mode (KV or Admin) |
| `--timeout` | `5s` | Request timeout (for writes, includes waiting for commit/apply) |

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
make bench-failover_time
make bench-stale_reads
make bench-slow_follower_recovery
```

The script measures:

- sequential write latency
- follower-distributed read latency
- parallel write throughput
- leader failover time
- stale follower reads (demonstrates non-linearizable follower reads without ReadIndex)
- slow follower pause/resume recovery time (log backfill)

Results are written to `.results/benchmark/*.txt`.

## Fault Tolerance Scenarios

Run scripted failure/recovery scenarios against the local Docker cluster:

```bash
make fault-test
```

Open the live admin dashboard in another terminal while running scenarios/benchmarks:

```bash
make admin
```

The script exercises:

- baseline read/write
- write availability with one follower down
- leader failover and write recovery
- quorum loss (writes must fail)
- quorum restoration (writes resume)

It also adds short pauses around stop/start transitions so changes are easier to observe in the admin dashboard.

Useful env vars:

- `OBSERVE_SLEEP` — pause duration in seconds between state transitions (default `2`)
- `FT_BULK_WRITES` — number of writes used in bulk checks (default `25`)

Implementation scripts are located in `scripts/`:

- `scripts/benchmark.sh`
- `scripts/fault_tolerance.sh`

Result directories:

- benchmarks: `.results/benchmark/`
- fault tolerance scenarios: `.results/fault/`

Notes:

- Numbers are highly environment-dependent (laptop model, Docker runtime, local CPU load, background processes).
- Tail latency (`p95`/`p99`) can vary noticeably between runs because of scheduler jitter and GC.
- Benchmarks include simple retries for some setup writes to reduce flaky failures during transient leader changes.

## Requirements

- Go 1.26+
- Docker + Docker Compose (for containerised cluster)
- [buf](https://buf.build) + protoc-gen-go + protoc-gen-go-grpc — only if regenerating proto (`buf generate`)
