# Raft

This document explains **how Raft consensus works** and maps each logical part of the protocol to the current implementation in this repository.

The focus is on protocol behavior (elections, replication, commit, safety). Code references are included only to show where each step is implemented.

## What Raft Solves

Raft keeps a replicated log consistent across multiple nodes so they can apply the same commands in the same order, even when:

- nodes crash and restart
- messages are delayed or lost
- network partitions occur
- leaders change over time

Raft achieves this by using a single leader per term and majority-based decisions.

## Core Model

### Roles

A node is always in one of three roles:

- **Follower**: passive; responds to leader/candidate RPCs
- **Candidate**: starts an election after timeout
- **Leader**: accepts writes and replicates log entries

### Terms

Time is divided into logical **terms**.

- terms increase monotonically
- each election happens in a term
- observing a higher term forces a node to step down to follower

This prevents stale leaders from continuing to make progress.

## Leader Election (Protocol Logic)

### Election timeout

A follower expects periodic leader communication (heartbeats / append RPCs). If it does not receive it before the election timeout, it starts an election.

### Candidate behavior

When starting an election, a node:

1. increments its current term
2. votes for itself
3. sends `RequestVote` to peers
4. becomes leader if it gets a majority
5. retries later if votes split / timeout occurs

### Voting safety rule

A voter grants at most one vote per term and only to a candidate whose log is at least as up-to-date as the voter's log.

This is a critical safety rule: it helps ensure a new leader contains all committed entries.

### Where this is implemented

- role loops: `internal/consensus/raft/election.go`
- follower logic: `runFollower`
- candidate logic: `runCandidate`
- vote RPC handler: `internal/consensus/raft/rpc_handlers.go` (`HandleRequestVote`)

## Log Replication (Protocol Logic)

Clients send writes to the leader.

The leader:

1. appends a new log entry locally
2. sends `AppendEntries` RPCs to followers
3. tracks which followers replicated which entries
4. marks entries committed after replication on a majority
5. applies committed entries to the state machine

Followers do not accept direct writes; they replicate the leader's log.

### Heartbeats

`AppendEntries` is also used as a heartbeat when there are no new entries.

Heartbeats:

- prove the leader is alive
- propagate `leader_commit` so followers can apply committed entries

### Where this is implemented

- leader replication loop: `internal/consensus/raft/replication.go` (`runLeader`)
- append RPC handler: `internal/consensus/raft/rpc_handlers.go` (`HandleAppendEntries`)
- client write entrypoint: `internal/consensus/raft/consensus.go` (`StartCommand`)

## Log Consistency and Conflict Resolution

Each log entry has:

- an **index**
- a **term**

When the leader sends `AppendEntries`, it includes the previous log index/term (`prev_log_index`, `prev_log_term`).
A follower accepts new entries only if its log matches that previous entry.

If the logs do not match:

- the follower rejects the append
- the leader backs up and retries earlier
- once a common prefix is found, the follower overwrites the conflicting suffix and appends the leader's entries

This guarantees eventual convergence of follower logs to the leader's log.

### Where this is implemented

- append validation + conflict response: `internal/consensus/raft/rpc_handlers.go` (`HandleAppendEntries`)
- leader-side retry/backoff behavior: `internal/consensus/raft/replication.go` (`runLeader`)

## Commit vs Apply

These are different steps.

- **Replicated**: entry is stored on some nodes
- **Committed**: leader knows the entry is safely stored on a majority (and commit rules are satisfied)
- **Applied**: entry is executed by the state machine

Followers may have replicated entries that are not yet committed.

Only committed entries are applied.

### Where this is implemented

- commit advancement happens in leader replication flow: `internal/consensus/raft/replication.go`
- application to state machine is handled by the apply loop: `internal/consensus/raft/apply.go` (`runApplyLoop`)

## Safety (Why Committed Entries Are Not Lost)

Raft's main safety properties come from combining:

- majority elections (majorities overlap)
- vote restriction by log freshness
- log matching checks in `AppendEntries`
- term ordering (step down on higher term)

High-level result:

- committed entries are preserved across leader changes
- all nodes apply the same committed commands in the same order

## Failures and Partitions

Raft continues to make progress as long as a **majority** of nodes can communicate.

Examples:

- **Leader crashes**: followers elect a new leader after timeout
- **Follower crashes**: cluster still works if a majority remains
- **Network partition**: only the partition with a majority can elect/keep a leader
- **Delayed stale RPCs**: rejected by term checks

## Membership Model in This Project (Important)

This repository currently uses **static membership**.

- the cluster member set is fixed at startup / restored state
- runtime add/remove node reconfiguration is **not implemented**
- failed nodes are **not removed automatically**

Practical consequence:

- quorum is calculated from the active Raft membership config, not from "currently reachable nodes"
- if the config is `5` nodes, write quorum is `3`
- if only `2/5` nodes are alive, writes stop (reads may still be possible depending on endpoint)

This behavior is correct for Raft and preserves safety. Automatic removal of failed nodes without a proper reconfiguration protocol would be unsafe.

### Quorum examples (static membership)

- `1` node config → quorum `1` (writes require the only node)
- `3` node config → quorum `2`
- `5` node config → quorum `3`

If you want writes to continue with only 2 live nodes, the membership must be explicitly reconfigured beforehand (for example to a `3`-node cluster with quorum `2`). Doing this safely requires membership change support (joint consensus or another correct reconfiguration approach).

### Where this is implemented

- term checks and step-down behavior are enforced in RPC handlers and leader/candidate loops:
  - `internal/consensus/raft/rpc_handlers.go`
  - `internal/consensus/raft/election.go`
  - `internal/consensus/raft/replication.go`

## Snapshots and Log Compaction

Without compaction, the log grows indefinitely.

Raft supports snapshots so a node can:

- persist a compacted state machine snapshot
- record the last included log index/term
- discard older log entries already represented by the snapshot

If a follower falls too far behind and the leader no longer has the needed log prefix, the leader sends a snapshot instead of normal log entries.

### Where this is implemented

- local compaction entrypoint from state machine layer: `internal/consensus/raft/consensus.go` (`Snapshot`)
- snapshot install handler on follower: `internal/consensus/raft/rpc_handlers.go` (`HandleInstallSnapshot`)
- leader snapshot catch-up path: `internal/consensus/raft/replication.go`

## Node Lifecycle (Minimal Code Mapping)

This is not part of the Raft algorithm itself, but useful for orientation in this repo:

- node construction / state restore: `internal/consensus/raft/node.go` (`NewNode`)
- start background loops: `internal/consensus/raft/node.go` (`Run`)
- shutdown: `internal/consensus/raft/consensus.go` (`Stop`)

## Write Path (end-to-end)

A `Put` or `Delete` call flows through the following layers. Understanding this path helps map any latency or correctness issue to the right component.

```
Client gRPC call
  └─ kvgrpc.server.Put                  internal/transport/grpc/kv/server.go
       └─ kv.service.Put                internal/service/kv.go
            └─ kv.service.startCommand
                 └─ raft.Node.StartCommand   internal/consensus/raft/consensus.go
                      ├─ reject if not leader → ErrNotLeader returned to client
                      ├─ append entry to n.log + persist (storage.AppendLog)
                      ├─ advanceCommitIndexLocked  (1-node fast path: commit immediately)
                      └─ notifyReplicate()
                           │
                           └─ runLeader             internal/consensus/raft/replication.go
                                └─ sendAppendEntries (one goroutine per peer)
                                     ├─ raftgrpc.client.AppendEntries → follower
                                     │     └─ HandleAppendEntries     rpc_handlers.go
                                     │           ├─ validate term + log consistency
                                     │           └─ persist (AppendLog + SaveHardState)
                                     └─ handleAppendEntriesResponse
                                           └─ advanceCommitIndexLocked (majority reached)
                                                └─ notifyApply()
                                                     └─ runApplyLoop  internal/consensus/raft/apply.go
                                                          └─ applyCh ← ApplyMsg{CommandValid}
                                                               └─ kv.service.RunApplyLoop    internal/service/kv.go
                                                                    └─ kv.service.handleApplyCommand
                                                                         ├─ kv.store.Apply   internal/kv/store.go
                                                                         └─ notifyApply()
                                                                              └─ kv.service.waitApplied ← unblocks
                                                                                   └─ response to client
```

Key points:

- The client blocks in `waitApplied` until `lastAppliedIndex >= log index` of its command.
- Replication to followers happens in parallel goroutines; the leader commits once a majority responds.
- `applyCh` is the boundary between Raft and the KV state machine. Entries are guaranteed committed before entering this channel.
- On a single-node cluster, `advanceCommitIndexLocked` commits immediately in `StartCommand`, before the replication loop runs.

The read path (`Get`) does not go through Raft at all — it reads directly from the local `kv.Store` state machine without contacting the leader. This means follower reads can be stale.

## Testing

Raft tests live in `internal/consensus/raft/` as `package raft` (white-box tests — same package as the implementation).

### Core test helpers

**`newTestNode(id, peers, applyCh)`** — the standard way to create a test node. Uses `InMemoryStorage`, `slog.Default()`, a no-op tracer, and no-op metrics:

```go
applyCh := make(chan consensus.ApplyMsg, 16)
n := newTestNode("n1", map[string]PeerClient{"n2": peer}, applyCh)
```

**`newNodeFromStorage(id, storage)`** — creates a node from a custom `Storage` implementation, used in persistence and restart tests.

**`waitApplyMsg(t, ch)`** — waits up to 200 ms for an `ApplyMsg` on `applyCh`. Calls `t.Fatal` on timeout:

```go
msg := waitApplyMsg(t, applyCh)
if !msg.CommandValid { t.Fatal("expected command") }
```

### Mocking peers

`MockPeerClient` (generated by gomock, in `mocks_test.go`) implements the `PeerClient` interface:

```go
ctrl := gomock.NewController(t)
t.Cleanup(ctrl.Finish)
peer := NewMockPeerClient(ctrl)

peer.EXPECT().
    AppendEntries(gomock.Any(), gomock.Any()).
    Return(&AppendEntriesResponse{Term: 1, Success: true}, nil)

n := newTestNode("n1", map[string]PeerClient{"n2": peer}, applyCh)
```

### Injecting storage errors

`failingStorage` (in `node_test.go`) lets you make specific storage operations return errors:

```go
n, err := NewNode("n1", map[string]PeerClient{}, applyCh, &failingStorage{
    saveHardStateErr: errors.New("disk full"),
}, slog.Default(), testTracer, testMetrics)
```

Fields: `loadState *PersistentState`, `saveHardStateErr`, `appendLogErr`, `truncateLogErr`.

### Deterministic time control

`fakeTimerFactory` and `fakeTickerFactory` (in `test_time_harness_test.go`) allow tests to control election timeouts and heartbeat ticks without sleeping:

```go
tf := newFakeTimerFactory()
timer := tf.AddTimer()          // register a timer to be returned by the next NewTimer call
n.newTimer = tf.NewTimer        // inject into node

timer.Fire()                    // trigger the election timeout
```

`fakeTimer.Fire()` sends to the timer's channel; `fakeTimer.ResetCount()` checks how many times it was reset.

### No-op observability in tests

Tests use shared no-op stubs declared in `test_tracing_test.go`:

```go
var (
    testTracer  = noop.NewTracerProvider().Tracer("test/internal/consensus/raft")
    testMetrics = noopMetrics{}
)
```

Pass these to `NewNode` directly when not using `newTestNode`.

## Practical Reading Order (Code)

If you want to understand the implementation while keeping the protocol logic in mind, read in this order:

1. `internal/consensus/raft/election.go` (roles and elections)
2. `internal/consensus/raft/rpc_handlers.go` (`RequestVote`, `AppendEntries`, `InstallSnapshot` semantics)
3. `internal/consensus/raft/replication.go` (leader replication and commit progression)
4. `internal/consensus/raft/apply.go` (apply committed entries / snapshots)
5. `internal/consensus/raft/consensus.go` (external API surface: start command, snapshot, stop)

## Summary

In this project, as in Raft generally, correctness comes from protocol rules (terms, majority voting, log matching, commit discipline). The code is organized around those same responsibilities: election loops, RPC handlers, leader replication, and apply/snapshot paths.
