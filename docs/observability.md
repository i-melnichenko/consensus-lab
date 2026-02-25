# Observability in `consensus-lab`

This document describes the observability setup in the project:

- OpenTelemetry traces (Jaeger)
- runtime/GC metrics (`/metrics`, Prometheus, Grafana)
- local Docker Compose monitoring stack wiring

It focuses on where telemetry is emitted, how to interpret it, and how to use it to debug Raft and KV behavior.

## Related docs

- [`benchmark.md`](benchmark.md) — benchmark methodology, units, and tuning variables
- [`client.md`](client.md) — CLI client commands (including batch modes used by latency benchmarks)

## Overview

Tracing is enabled in the node process via `internal/app` and exported to Jaeger using OTLP/gRPC.

The current tracing setup covers:

- gRPC server request handling (all services) via `otelgrpc`
- KV service layer (`internal/service/kv`)
- KV store internals (`internal/kv/store.go`)
- KV transport server adapter (`internal/transport/grpc/kv`)
- Raft transport client adapter (`internal/transport/grpc/raft`)
- Raft transport server adapter (`internal/transport/grpc/raft`)
- Raft node RPC handlers and outbound replication/election RPC calls (`internal/consensus/raft`)
- Raft apply loop (snapshot apply and log entry apply)
- Child spans for persistence operations in traced Raft paths (`SaveHardState`, `AppendLog`, `TruncateLog`, `SaveSnapshot`)

## Quick reference

| Symptom | Spans to check | Metrics to check | Section |
|---------|---------------|-----------------|---------|
| No leader / frequent elections | `raft.node.sendRequestVote`, `raft.node.HandleRequestVote` | `election_started_total`, `election_lost_total{reason}` | [Leader election issues](#1-leader-election-issues) |
| Write fails with `not_leader` | `kv.service.startCommand` | `kv_proposal_total{result="not_leader"}` | [Leader election issues](#1-leader-election-issues) |
| Writes stall / commit not advancing | `raft.node.sendAppendEntries`, `raft.storage.AppendLog` | `raft_apply_lag`, `raft_start_to_commit_duration_seconds` | [Replication stalls](#2-replication-stalls--follower-lag) |
| One follower permanently behind | `raftgrpc.client.AppendEntries`, `raft.node.HandleAppendEntries` | `appendentries_reject_total`, `installsnapshot_send_total` | [Replication stalls](#2-replication-stalls--follower-lag) |
| Write accepted but slow response | `kv.service.waitApplied`, `raft.node.applyEntry` | `kv_wait_applied_duration_seconds{result="ok"}`, `kv_start_to_apply_duration_seconds` | [Commit vs apply](#4-commit-vs-apply-visibility) |
| Write timeouts on client | `kv.service.waitApplied` | `kv_wait_applied_calls_total{result="timeout"}` | [Commit vs apply](#4-commit-vs-apply-visibility) |
| Repeated snapshot sends to same peer | `raft.node.sendInstallSnapshot`, `raft.storage.SaveSnapshot` | `installsnapshot_send_total`, `installsnapshot_rpc_duration_seconds` | [Snapshot churn](#3-snapshot-churn-or-snapshot-failures) |
| Heartbeat spikes, no write issue | `raftgrpc.client.AppendEntries` (`raft.is_heartbeat=true`) | `appendentries_rpc_duration_seconds{heartbeat="true"}` | [Heartbeat spikes](#interpreting-heartbeat-spikes-vs-write-replication) |

## How to run locally

`docker-compose.yml` includes a Jaeger all-in-one service.

- Jaeger UI: `http://localhost:16686`
- OTLP/gRPC endpoint (used by nodes): `jaeger:4317`
- Prometheus UI: `http://localhost:9090`
- Grafana UI: `http://localhost:3000` (default `admin` / `admin`)

Prometheus scrapes node metrics inside the Docker Compose network (`node-*:9090`); node metrics ports are not exposed on the host.
Grafana provisions the Prometheus datasource and a dashboard named `Consensus Lab Observability` automatically.
The dashboard `Instance` variable supports multi-select and `All`, so you can compare multiple nodes at once.

Node tracing env vars (configured in compose):

- `APP_TRACING_ENABLED=true`
- `APP_TRACING_ENDPOINT=jaeger:4317`
- `APP_TRACING_SERVICE_NAME=consensus-node`
- `APP_METRICS_ADDR=:9090` (HTTP `/metrics`; empty disables)
- `APP_PPROF_ADDR=` (HTTP `pprof`; empty disables, recommended default)

## pprof (memory / CPU debugging)

The node can expose Go `net/http/pprof` endpoints via `APP_PPROF_ADDR` (disabled by default).

Example (enable on one node only):

- `APP_PPROF_ADDR=:6060`

Common endpoints:

- `/debug/pprof/heap`
- `/debug/pprof/allocs`
- `/debug/pprof/goroutine`
- `/debug/pprof/profile` (CPU profile, default 30s)
- `/debug/pprof/trace`

Examples:

```bash
# heap profile from a node (if pprof port is reachable)
go tool pprof "http://localhost:6060/debug/pprof/heap"

# 15-second CPU profile
go tool pprof "http://localhost:6060/debug/pprof/profile?seconds=15"
```

For local Docker debugging, prefer enabling and exposing pprof only temporarily on the node you are investigating.

## Benchmark correlation workflow

When investigating latency tails:

1. Run a benchmark from [`benchmark.md`](benchmark.md) (for example `write_latency`).
2. Open Grafana (`Consensus Lab Observability`) and select the benchmark time window.
3. Compare benchmark percentiles with:
   - `KV waitApplied Duration`
   - `Raft Start->Commit Duration`
   - `Raft Commit->Apply Duration`
   - `AppendEntries RPC Duration` (heartbeat vs replication / by peer)
4. Use Jaeger traces to inspect individual tail samples and confirm whether the latency is in:
   - transport / gRPC
   - Raft replication / commit path
   - apply path
   - KV state machine handling

This is especially important because benchmark methodology matters: latency benchmarks use batch client modes to avoid per-request CLI startup and dial overhead.

## Trace flow (Raft RPC path)

For a Raft RPC (for example `AppendEntries`), a typical trace may include:

1. `raft.node.sendAppendEntries` (Raft leader logic)
   - may include child spans like `raft.node.handleAppendEntriesResponse`
2. `raftgrpc.client.AppendEntries` (Raft gRPC client adapter)
3. `otelgrpc` server span (gRPC server instrumentation)
4. `raftgrpc.server.AppendEntries` (Raft gRPC server adapter)
5. `raft.node.HandleAppendEntries` (Raft follower logic)
6. Child persistence spans (when applicable), e.g. `raft.storage.AppendLog`

This split makes it easier to see whether latency is caused by:

- leader-side scheduling / Raft logic
- transport / gRPC overhead
- follower-side handler logic
- persistence operations

For a client write (`Put`/`Delete`), a typical trace may include:

1. `otelgrpc` server span for the KV gRPC request
2. `kvgrpc.server.Put` / `kvgrpc.server.Delete`
3. `kv.service.Put` / `kv.service.Delete`
4. `kv.service.startCommand`
5. Raft replication path (`raft.node.sendAppendEntries`, follower-side Raft spans)
6. `raft.node.applyEntry` (Raft delivery to `applyCh`)
7. `kv.service.handleApplyCommand` (KV service applies command to state machine)
8. `kv.service.waitApplied` completes on the original request path

## Span inventory

### KV service spans

File: `internal/service/kv.go`

Tracer source:

- Passed as constructor parameters to `service.NewKV(...)` from the composition layer (`cmd/node`):
  - tracer
  - metrics sink interface
- Expects a non-nil tracer from the composition layer (`cmd/node`); tests should pass a no-op tracer

Spans:

- `kv.service.Get`
- `kv.service.Put`
- `kv.service.Delete`
- `kv.service.startCommand`
- `kv.service.waitApplied`
- `kv.service.handleApplyCommand`
- `kv.service.handleApplySnapshot`
- `kv.service.snapshot`

Purpose:

- Trace service-level read/write operations (`Get`, `Put`, `Delete`)
- Measure write latency across proposal and wait-for-apply (`startCommand` + `waitApplied`)
- Trace application of committed Raft messages into the KV state machine
- Trace service-triggered snapshot creation and handoff to consensus

Useful for:

- debugging slow writes where Raft replication succeeds but request still waits
- separating Raft apply-channel delivery time from KV state machine apply time
- understanding snapshot trigger frequency from `SnapshotEvery`

### KV store spans

File: `internal/kv/store.go`

Tracer source:

- Passed as a constructor parameter to `kv.NewStore(...)` from the composition layer (`cmd/node`)
- Expects a non-nil tracer from the composition layer (`cmd/node`)

Spans:

- `kv.store.Apply`
- `kv.store.Snapshot`
- `kv.store.RestoreSnapshot`

Purpose:

- Trace internal KV state machine work (decode/apply/serialize/restore)
- Attach store-level attributes (`kv.store.items`, `kv.command.type`, `kv.key`, payload sizes)
- Record JSON decode/encode errors

Useful for:

- distinguishing service-layer overhead from actual store work
- debugging slow snapshot serialization/restoration
- debugging malformed command payloads reaching the state machine

### App-level setup

File: `internal/app/tracing.go`

Purpose:

- Initializes OTLP trace exporter
- Sets resource attributes:
  - `service.name`
  - `service.instance.id`
  - `consensus.type`
- Registers global tracer provider and propagator

This is infrastructure setup (not a business span source by itself).

Tracer injection note:

- `cmd/node` injects tracers into five components:
  - `internal/service/kv` service (`service.NewKV(..., tracer, metrics)`)
  - `internal/transport/grpc/kv` server adapter (`kvgrpc.NewServer(..., tracer)`)
  - `internal/transport/grpc/raft` client adapter (`raftgrpc.DialPeers(..., tracer, ...)`)
  - `internal/transport/grpc/raft` server adapter (`raftgrpc.NewServer(..., tracer)`)
  - `internal/consensus/raft` node (`raft.NewNode(..., tracer, metrics)`)

### gRPC server instrumentation

File: `internal/app/app.go`

Instrumentation:

- `grpc.StatsHandler(otelgrpc.NewServerHandler())`

Purpose:

- Creates server-side gRPC spans for incoming requests
- Provides protocol-level visibility (request lifecycle in gRPC server)

This applies to all registered gRPC services, including KV/Admin/Raft.

### KV transport server adapter spans

File: `internal/transport/grpc/kv/server.go`

Spans:

- `kvgrpc.server.Get`
- `kvgrpc.server.Put`
- `kvgrpc.server.Delete`

Purpose:

- Time adapter-level handling for KV RPCs before/after delegation to `internal/service/kv`
- Attach request/response attributes (`kv.key`, `kv.value.bytes`, `kv.found`, `raft.log.index`)
- Record service errors before conversion to gRPC status

Useful for:

- separating transport adapter overhead from `internal/service/kv` execution time
- verifying KV error mapping behavior (`not leader`, `commit timeout`, internal errors)

### Raft transport client adapter spans

File: `internal/transport/grpc/raft/client.go`

Spans:

- `raftgrpc.client.RequestVote`
- `raftgrpc.client.AppendEntries`
- `raftgrpc.client.InstallSnapshot`

Purpose:

- Time outbound Raft gRPC calls to remote peers
- Attach request/response attributes (`raft.peer.target`, term, indexes, conflicts, snapshot metadata)
- Record transport/RPC errors before they bubble into Raft node logic

Useful for:

- separating leader/candidate-side logic time from transport/RPC time
- diagnosing peer connectivity or RPC latency issues
- correlating outbound requests with follower-side handling spans
- filtering heartbeats vs actual replication using `raft.is_heartbeat`

### Raft transport server adapter spans

File: `internal/transport/grpc/raft/server.go`

Spans:

- `raftgrpc.server.RequestVote`
- `raftgrpc.server.AppendEntries`
- `raftgrpc.server.InstallSnapshot`

Purpose:

- Time adapter-level request conversion + delegation to Raft node handler
- Use an injected tracer (DI) from the node composition layer instead of creating a global tracer in the package
- Record request and response metadata
- Record handler errors before conversion to gRPC status

Useful for:

- understanding overhead and error mapping at transport boundary
- verifying what request payload reached the Raft handler

### Raft node RPC handler spans (inbound Raft logic)

File: `internal/consensus/raft/rpc_handlers.go`

Tracer source:

- Passed as constructor parameters to `raft.NewNode(...)` from the composition layer (`cmd/node`):
  - tracer
  - metrics sink interface
- `internal/consensus/raft` does not create a global package tracer anymore

Spans:

- `raft.node.HandleRequestVote`
- `raft.node.HandleAppendEntries`
- `raft.node.HandleInstallSnapshot`

Purpose:

- Trace the actual follower-side Raft logic execution
- Capture decisions and results:
  - vote granted / denied
  - append success / conflicts (`conflict_term`, `conflict_index`)
  - snapshot acceptance/rejection
- Record internal persistence errors and degraded node errors

Useful for:

- election debugging (why votes are denied)
- replication conflict debugging (why follower rejects append)
- snapshot behavior debugging (stale/duplicate/new snapshot handling)

### Raft outbound election/replication/snapshot spans

Files:

- `internal/consensus/raft/election.go`
- `internal/consensus/raft/replication.go`
- `internal/consensus/raft/snapshot.go`

Spans:

- `raft.node.sendRequestVote`
- `raft.node.sendAppendEntries`
- `raft.node.sendInstallSnapshot`
- `raft.node.handleAppendEntriesResponse` (child span inside `raft.node.sendAppendEntries`)

Purpose:

- Trace leader/candidate-side Raft workflow before/after transport RPC
- Show leader-side AppendEntries response-processing time after the transport call returns
- Attach peer-specific context (`raft.peer_id`, term, indexes, entry count, snapshot metadata)
- Record RPC errors and response summaries

Useful for:

- tracing election attempts across peers
- seeing retry/backoff behavior in append replication
- understanding when and how snapshots are sent to lagging followers

### Raft apply loop spans

File: `internal/consensus/raft/apply.go`

Spans:

- `raft.node.applySnapshot`
- `raft.node.applyEntry`

Purpose:

- Time the delivery of snapshot/log entries from Raft node to the state machine apply channel (`applyCh`)
- Record payload size and indices
- Mark successful channel send with `raft.apply.sent=true`

Useful for:

- identifying delays between commit and state machine delivery
- distinguishing replication success from application progress

Note:

- These spans measure Raft-side delivery to `applyCh`, not the full downstream KV state machine execution time.

### Child persistence spans (Raft storage operations)

File: `internal/consensus/raft/tracing.go`

Spans:

- `raft.storage.SaveHardState`
- `raft.storage.AppendLog`
- `raft.storage.TruncateLog`
- `raft.storage.SaveSnapshot`

Purpose:

- Provide child spans inside traced Raft flows for persistence latency
- Record operation-specific attributes:
  - `raft.persist.reason`
  - `raft.entries_count`
  - `raft.from_index`
  - snapshot index/term/size
- Record storage errors

Useful for:

- separating CPU/logic time from storage I/O time
- finding slow persistence operations behind Raft stalls or degraded nodes

Current coverage note:

- Child persistence spans are added in the primary traced Raft paths (RPC handlers, election/replication/snapshot send/step-down flows).
- Some background/best-effort persistence calls without context are not yet wrapped as child spans.
- `internal/consensus/raft` and `internal/service/kv` constructors expect a non-nil tracer (tests should pass a no-op tracer).

## Key attributes used

The tracing implementation uses a consistent subset of attributes (not all spans use all attributes):

- `raft.node_id`
- `raft.peer_id`
- `raft.peer.target`
- `raft.term`
- `raft.response_term`
- `raft.vote_granted`
- `raft.append.success`
- `raft.conflict_term`
- `raft.conflict_index`
- `raft.prev_log_index`
- `raft.prev_log_term`
- `raft.entries_count`
- `raft.is_heartbeat`
- `raft.leader_commit`
- `raft.snapshot.index`
- `raft.snapshot.term`
- `raft.snapshot.bytes`
- `raft.apply.sent`
- `raft.persist.reason`
- `kv.key`
- `kv.command.type`
- `kv.command.bytes`
- `kv.value.bytes`
- `kv.found`
- `kv.snapshot.bytes`
- `kv.store.items`
- `kv.applied_since_snapshot`
- `kv.last_applied_index`
- `kv.wait_applied.done`

## What these traces help diagnose

### 1. Leader election issues

Symptoms:

- no leader elected
- frequent leadership changes

Use spans:

- `raft.node.sendRequestVote`
- `raftgrpc.client.RequestVote`
- `raftgrpc.server.RequestVote`
- `raft.node.HandleRequestVote`

Look for:

- RPC errors / timeouts
- `vote_granted=false`
- higher `response_term` values causing step-down

### 2. Replication stalls / follower lag

Symptoms:

- commits not advancing
- one follower permanently behind

Use spans:

- `raft.node.sendAppendEntries`
- `raftgrpc.client.AppendEntries`
- `raftgrpc.server.AppendEntries`
- `raft.node.HandleAppendEntries`
- `raft.storage.AppendLog` / `raft.storage.TruncateLog` / `raft.storage.SaveHardState`

Look for:

- `append.success=false`
- repeated `conflict_index` / `conflict_term`
- slow storage child spans on follower
- `raft.is_heartbeat=true` spikes: usually transport/runtime jitter, not write replication work
- `raft.is_heartbeat=false` spikes: replication of real writes (more relevant for client write latency)

### 3. Snapshot churn or snapshot failures

Symptoms:

- repeated snapshot sends
- lagging follower never catches up

Use spans:

- `raft.node.sendInstallSnapshot`
- `raftgrpc.client.InstallSnapshot`
- `raftgrpc.server.InstallSnapshot`
- `raft.node.HandleInstallSnapshot`
- `raft.storage.SaveSnapshot`

Look for:

- repeated snapshot sends to same peer
- large `raft.snapshot.bytes`
- storage errors in `SaveSnapshot`

### 4. Commit vs apply visibility

Symptoms:

- entries replicate, but state machine appears slow

Use spans:

- `kv.service.Put` / `kv.service.Delete`
- `kv.service.startCommand`
- `kv.service.waitApplied`
- `raft.node.sendAppendEntries` and handler spans (replication success)
- `raft.node.applyEntry` / `raft.node.applySnapshot` (delivery to apply channel)
- `kv.service.handleApplyCommand` / `kv.service.handleApplySnapshot` (KV service apply)

Look for:

- long gap between replication success and `raft.node.applyEntry`
- long gap between `raft.node.applyEntry` and `kv.service.handleApplyCommand`
- long `kv.service.waitApplied` even when apply spans exist (notification / progress tracking issue)
- missing apply spans when commits should advance (indicates upstream issue)

## Runtime/GC metrics for trace correlation

The node process exposes a Prometheus `/metrics` endpoint (configured via `APP_METRICS_ADDR`, default `:9090`).
In local Docker Compose runs, Prometheus scrapes these endpoints over the internal network.

These metrics are especially useful when Jaeger shows synchronous `AppendEntries` spikes across many peers (for example `raft.is_heartbeat=true` spans around `40-50ms` at the same time):

- `go_goroutines`
- `go_gc_duration_seconds` / `go_gc_duration_seconds_count` (GC duration summary + count; names depend on collector/runtime version)
- `go_sched_latencies_seconds` / `go_sched_latencies_seconds_bucket` (scheduler latency histogram; may be unavailable depending on runtime/collector version)
- `process_cpu_seconds_total`

Use them to correlate:

- heartbeat spikes with scheduler latency spikes (`go_sched_latencies_seconds*`, if available)
- tail write latency windows with GC duration spikes (`go_gc_duration_seconds`)
- runtime pressure with goroutine growth and CPU time rate (`go_goroutines`, `process_cpu_seconds_total`)

## Application metrics (custom)

The node also exposes application-level Prometheus metrics for correlating traces with Raft/KV behavior:

- `consensuslab_kv_wait_applied_duration_seconds_bucket`
  - histogram for `kv.service.waitApplied` duration
  - labels: `node_id`, `result` (`ok` / `timeout`)
- `consensuslab_kv_start_to_apply_duration_seconds_bucket`
  - histogram for time from entering `waitApplied` to command apply in KV state machine
  - label: `node_id`
- `consensuslab_kv_apply_to_waiter_wakeup_duration_seconds_bucket`
  - histogram for time from KV apply to waiter completion
  - label: `node_id`
- `consensuslab_kv_wait_applied_wakeups_total`
  - counter for total apply-notify wakeups observed by `waitApplied` calls
  - label: `node_id`
- `consensuslab_kv_wait_applied_calls_total`
  - counter for `waitApplied` calls
  - labels: `node_id`, `result` (`ok` / `timeout`)
- `consensuslab_kv_proposal_total`
  - counter for proposal outcomes at KV service layer
  - labels: `node_id`, `result` (`accepted`, `not_leader`, `commit_timeout`)
- `consensuslab_raft_appendentries_rpc_duration_seconds_bucket`
  - histogram for outbound AppendEntries RPC duration
  - labels: `node_id`, `peer_id`, `heartbeat`
- `consensuslab_raft_appendentries_reject_total`
  - counter for AppendEntries rejections
  - labels: `node_id`, `peer_id`, `heartbeat`
- `consensuslab_raft_appendentries_rpc_error_total`
  - counter for outbound AppendEntries RPC errors
  - labels: `node_id`, `peer_id`, `heartbeat`, `kind`
- `consensuslab_raft_storage_error_total`
  - counter for Raft storage operation errors
  - labels: `node_id`, `op`
- `consensuslab_raft_apply_lag`
  - gauge of `commitIndex - lastApplied`
  - label: `node_id`
- `consensuslab_raft_is_leader`
  - gauge (`0`/`1`) indicating node leadership state
  - label: `node_id`
- `consensuslab_raft_start_to_commit_duration_seconds_bucket`
  - histogram for time from `StartCommand` acceptance on leader to commit
  - label: `node_id`
- `consensuslab_raft_commit_to_apply_duration_seconds_bucket`
  - histogram for time from commitIndex advance to apply
  - label: `node_id`
- `consensuslab_raft_election_started_total`
  - counter for election attempts started
  - label: `node_id`
- `consensuslab_raft_election_won_total`
  - counter for elections won
  - label: `node_id`
- `consensuslab_raft_election_lost_total`
  - counter for lost/aborted elections
  - labels: `node_id`, `reason`
- `consensuslab_kv_snapshot_total`
  - counter for KV snapshot attempts/results
  - labels: `node_id`, `result`
- `consensuslab_kv_snapshot_duration_seconds_bucket`
  - histogram for KV snapshot build duration
  - label: `node_id`
- `consensuslab_kv_snapshot_bytes_bucket`
  - histogram for KV snapshot size in bytes
  - label: `node_id`
- `consensuslab_raft_installsnapshot_send_total`
  - counter for outbound `InstallSnapshot` sends
  - labels: `node_id`, `peer_id`, `result`
- `consensuslab_raft_installsnapshot_send_bytes_bucket`
  - histogram for outbound snapshot payload size
  - labels: `node_id`, `peer_id`
- `consensuslab_raft_installsnapshot_rpc_duration_seconds_bucket`
  - histogram for outbound `InstallSnapshot` RPC duration
  - labels: `node_id`, `peer_id`

The provisioned Grafana dashboard includes panels for these metrics:

- `Raft Leadership State (0/1)`
- `Raft Leaders Selected`
- `Error Rates (RPC / Proposal / Storage)`
- `GC Duration Quantiles`
- `KV waitApplied Duration (p95/p99)`
- `KV Start->Apply Duration (p95/p99)`
- `KV Apply->Waiter Wakeup (p95/p99)`
- `KV waitApplied Wakeups per Call (avg)`
- `AppendEntries RPC Duration p95 (heartbeat vs replication)`
- `AppendEntries RPC p95 by peer_id (heartbeat=false)`
- `AppendEntries Reject Rate`
- `Raft Apply Lag`
- `Raft Start->Commit Duration (p95/p99)`
- `Raft Commit->Apply Duration (p95/p99)`
- `Election Rates (started / won)`
- `Election Lost Rate by Reason`
- `Snapshot Frequency (KV / InstallSnapshot)`
- `Snapshot Size / Duration p95`

Panel note:

- `KV waitApplied Duration (p95/p99)` on the dashboard is filtered to `result="ok"`, and timeout rate is shown as a separate series/panel signal to avoid mixing successful requests with timeouts.

## Current limitations

- No dedicated spans around every storage method in every code path (some calls lack contextual parent span)
- KV store tracing now covers apply/snapshot/restore, but does not yet break them into finer-grained child spans (for example decode vs map mutation vs marshal)
- No explicit sampling configuration (uses SDK defaults)
- Background Raft loop traces (`runLeader`, `runFollower`, `runCandidate` descendants) are often root/orphan traces by design because they are not initiated from a client request context; correlation is done via metrics and Raft attributes (for example `raft.log.index`, peer IDs, terms)

## Interpreting heartbeat spikes vs write replication

`AppendEntries` spans can represent two different things:

- heartbeat RPCs (`raft.entries_count=0`, `raft.is_heartbeat=true`)
- actual log replication (`raft.entries_count>0`, `raft.is_heartbeat=false`)

When investigating client write latency (`Put`/`Delete`) tails:

- Prefer looking at `raft.is_heartbeat=false` spans first.
- Heartbeat spikes (for example many peers showing ~40-50ms at the same time) often indicate transport/runtime scheduling jitter, Docker/network overhead, or host pauses rather than slow Raft handler logic.
- Use `raft.node.sendAppendEntries` plus transport spans (`raftgrpc.client.AppendEntries`, `otelgrpc`) to separate transport/runtime time from `raft.node.handleAppendEntriesResponse` (leader-side response handling / commit progression logic).
