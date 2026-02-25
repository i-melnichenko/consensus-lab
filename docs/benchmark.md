# Benchmarking Notes

This project ships `scripts/benchmark.sh` for local cluster benchmarking.

## Benchmark List

- `write_latency` — sequential writes (`Put`) latency
- `read_latency` — follower-distributed reads (`Get`) latency
- `delete_latency` — sequential deletes (`Delete`) latency
- `write_throughput` — parallel write throughput
- `failover_time` — time to recover writes after leader stop
- `stale_reads` — stale follower read detection (correctness scenario)
- `slow_follower_recovery` — paused follower catch-up time after resume

## Methodology

Latency benchmarks use long-lived batch client modes to avoid measuring per-request CLI process startup and gRPC dial overhead:

- `write_latency` -> `put-batch`
- `read_latency` -> `get-batch`
- `delete_latency` -> `delete-batch`

This makes the reported numbers closer to steady-state RPC latency.

Scenario-style benchmarks (failover, stale reads, slow follower recovery) intentionally keep per-step CLI calls because they validate behavior and timing across cluster state transitions.

## Units

- `write_latency`: milliseconds (`ms`)
- `delete_latency`: milliseconds (`ms`)
- `read_latency`: microseconds (`us`) for better resolution on fast local reads

## Useful Environment Variables

### Common

- `NODES` / `--nodes` — comma-separated node addresses (for example `localhost:8081,...,localhost:8085`)
- `RUN_ID` — benchmark run identifier used in generated keys (default: current timestamp)
- `BENCH_OBSERVE_SLEEP` — pauses around scenario transitions for easier observation (default `2`)

### Latency Benchmarks

- `BENCH_WRITE_LATENCY_N` (default `1000`)
- `BENCH_WRITE_LATENCY_WARMUP` (default `20`)
- `BENCH_WRITE_LATENCY_PIN_LEADER` (default `1`)
- `BENCH_WRITE_LATENCY_SLOW_MS` (default `200`)
- `BENCH_READ_LATENCY_N` (default `1000`)
- `BENCH_READ_LATENCY_SLOW_US` (default `10000`)
- `BENCH_DELETE_LATENCY_N` (default `1000`)
- `BENCH_DELETE_LATENCY_SLOW_MS` (default `200`)

## Results

Benchmark outputs are written to `.results/benchmark/*.txt`.

- `write_latency.txt`, `delete_latency.txt` report latency in `ms`
- `read_latency.txt` reports latency in `us`
- latency benchmarks also include `slow_samples(...)` to quickly inspect tail outliers

## Expected Behavior Notes

- `stale_reads` may report a high stale rate (often `100%`) in this project because follower reads are served without `ReadIndex` / linearizable read coordination.
- Tail latency (`p95`/`p99`) is environment-dependent and may vary due to local CPU load, scheduler jitter, Docker VM/networking, and GC activity.

## Example

```bash
BENCH_READ_LATENCY_N=5000 BENCH_READ_LATENCY_SLOW_US=5000 make bench-read_latency
```

```bash
./scripts/benchmark.sh --nodes "localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085" write_latency
```
