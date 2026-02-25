# CLI Client

`cmd/client` is a gRPC CLI for KV operations and the live admin dashboard.

## Addressing and Routing

`--addr` accepts a comma-separated list of nodes.

Routing behavior:

- `get` — picks a random node (distributes reads across replicas)
- `put` / `delete` — tries nodes until the leader accepts the write
- `admin` — polls each admin endpoint and renders a live table (refresh ~500ms)

Write semantics:

- `put` / `delete` are acknowledged only after the command is committed and applied
- if the cluster cannot reach quorum (or does not commit in time), the request fails (usually timeout)

## Commands

### Basic KV Commands

```bash
# point at the whole cluster — leader discovery is automatic
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 put foo bar
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 get foo
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 delete foo

# single node still works
go run ./cmd/client --addr localhost:8081 get foo
```

### Batch KV Commands

Batch modes reuse one long-lived client/connection and are used by latency benchmarks.

- `put-batch --in <file|->` — TSV input (`key<TAB>value`)
- `get-batch --in <file|->` — one key per line
- `delete-batch --in <file|->` — one key per line

Examples:

```bash
printf 'k1\tv1\nk2\tv2\n' | go run ./cmd/client --addr localhost:8081,localhost:8082 put-batch --in -
printf 'k1\nk2\n' | go run ./cmd/client --addr localhost:8081,localhost:8082 get-batch --in -
printf 'k1\nk2\n' | go run ./cmd/client --addr localhost:8081,localhost:8082 delete-batch --in -
```

Batch output is TSV per operation (status, sequence number, latency, and command-specific fields), suitable for parsing in scripts.

## Admin Dashboard

```bash
# live admin dashboard (same shared gRPC ports)
go run ./cmd/client --addr localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085 admin

# same via Makefile
make admin
NODES=localhost:8081,localhost:8082,localhost:8083 make admin
```

## grpcurl Examples

```bash
grpcurl -plaintext localhost:8081 list
grpcurl -plaintext -d '{}' localhost:8081 admin.v1.AdminService/GetNodeInfo
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `localhost:8080` | Comma-separated gRPC addresses for the selected mode (KV or Admin) |
| `--timeout` | `5s` | Request timeout (for writes, includes waiting for commit/apply) |
