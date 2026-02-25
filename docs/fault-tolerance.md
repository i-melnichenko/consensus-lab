# Fault Tolerance Scenarios

This project ships `scripts/fault_tolerance.sh` to exercise common failure/recovery scenarios against a local Docker cluster.

## Scenarios

- `baseline` — basic write/read sanity check
- `single_follower_stop` — stop one follower, verify writes still succeed (quorum preserved)
- `leader_failover` — stop leader, wait for election, verify writes resume
- `quorum_loss` — stop 3 nodes, verify writes fail without quorum
- `quorum_restore` — restart nodes, verify writes resume after quorum restoration

The script records per-scenario `PASS` / `FAIL` results and key timing details in a summary file.

## How to Run

```bash
make docker-up
make fault-test
```

Direct script invocation:

```bash
./scripts/fault_tolerance.sh --nodes "localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085"
```

Open the live admin dashboard in another terminal while the script runs:

```bash
make admin
```

## Results

- summary file: `.results/fault/fault_tolerance.txt`
- temporary client binary: `.results/fault/client`

## Useful Environment Variables

- `NODES` / `--nodes` — comma-separated node addresses
- `TIMEOUT` — client request timeout (default `5s`)
- `OBSERVE_SLEEP` — pauses between transitions for easier observation (default `2`)
- `FT_BULK_WRITES` — writes used in bulk availability checks (default `25`)
- `RUN_ID` — identifier embedded in generated keys (default current timestamp)

## Notes

- This is a scenario/correctness-oriented script, not a pure latency benchmark.
- It intentionally performs step-by-step operations (including retries and pauses) to preserve scenario semantics and make cluster state transitions observable.
