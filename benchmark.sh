#!/usr/bin/env bash
# =============================================================================
# consensus-lab benchmark script
# Measures: write latency, read latency, throughput, leader failover time,
#           stale read detection, slow-follower recovery
#
# Prerequisites:
#   docker compose up --build   (cluster running on :8081 :8082 :8083)
# =============================================================================

set -euo pipefail

NODES="localhost:8081,localhost:8082,localhost:8083"
TIMEOUT="5s"
RESULTS_DIR="./.bench-results"
mkdir -p "$RESULTS_DIR"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
fail() { echo -e "${RED}  ✗${NC} $*"; }
sep()  { echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }

# Build client binary ONCE — avoids ~600ms go run compilation overhead per call
CLIENT_BIN="$RESULTS_DIR/client"
log "Building client binary..."
go build -o "$CLIENT_BIN" ./cmd/client
ok "client binary → $CLIENT_BIN"

run_client() {
    "$CLIENT_BIN" --addr "$NODES" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_node() {
    # Talk to a single specific node (no cluster routing)
    local addr="$1"; shift
    "$CLIENT_BIN" --addr "$addr" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_addrs() {
    # Talk to a specific subset of nodes (used for degraded-cluster benchmarks)
    local addrs="$1"; shift
    "$CLIENT_BIN" --addr "$addrs" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

retry_run_client_put() {
    local key="$1"
    local value="$2"
    local attempts="${3:-5}"
    local sleep_s="${4:-0.1}"

    local attempt
    for attempt in $(seq 1 "$attempts"); do
        if run_client put "$key" "$value" > /dev/null 2>&1; then
            return 0
        fi
        sleep "$sleep_s"
    done

    fail "pre-populate put failed after ${attempts} attempts: key=$key"
    return 1
}

# ─── helper: measure single operation in ms ──────────────────────────────────
measure_ms() {
    local start end
    start=$(date +%s%N)
    "$@" > /dev/null 2>&1
    end=$(date +%s%N)
    echo $(( (end - start) / 1000000 ))
}

percentiles() {
    local n="$1"
    sort -n | awk -v n="$n" '
    {
        a[NR] = $1
        sum += $1
    }
    END {
        p50 = a[int(n*0.50)+1]
        p95 = a[int(n*0.95)+1]
        p99 = a[int(n*0.99)+1]
        printf "  median=%dms  p95=%dms  p99=%dms  avg=%.1fms\n", p50, p95, p99, sum/n
    }'
}

# =============================================================================
# 1. WRITE LATENCY
# =============================================================================
bench_write_latency() {
    sep
    log "BENCHMARK 1 — Write Latency (sequential puts)"
    local N=100
    local times=()

    for i in $(seq 1 $N); do
        local ms
        ms=$(measure_ms run_client put "bench:lat:$i" "value-$i")
        times+=("$ms")
        printf "\r  progress: %d/%d" "$i" "$N"
    done
    echo

    printf '%s\n' "${times[@]}" | percentiles $N | tee "$RESULTS_DIR/write_latency.txt"
    ok "done — results in $RESULTS_DIR/write_latency.txt"
}

# =============================================================================
# 2. READ LATENCY
# =============================================================================
bench_read_latency() {
    sep
    log "BENCHMARK 2 — Read Latency (follower-distributed gets)"
    local N=100
    local times=()

    log "  Pre-populating $N keys..."
    for i in $(seq 1 $N); do
        retry_run_client_put "bench:read:$i" "value-$i"
        printf "\r  pre-populate: %d/%d" "$i" "$N"
    done
    echo

    for i in $(seq 1 $N); do
        local ms
        ms=$(measure_ms run_client get "bench:read:$i")
        times+=("$ms")
        printf "\r  progress: %d/%d" "$i" "$N"
    done
    echo

    printf '%s\n' "${times[@]}" | percentiles $N | tee "$RESULTS_DIR/read_latency.txt"
    ok "done — results in $RESULTS_DIR/read_latency.txt"
}

# =============================================================================
# 3. WRITE THROUGHPUT
# =============================================================================
bench_write_throughput() {
    sep
    log "BENCHMARK 3 — Write Throughput (parallel puts)"
    local N=200
    local CONCURRENCY=10
    local BATCH=$(( N / CONCURRENCY ))
    local start end total_ms ops_per_sec

    start=$(date +%s%N)
    for w in $(seq 1 $CONCURRENCY); do
        (
            for i in $(seq 1 $BATCH); do
                run_client put "bench:thr:w${w}:$i" "v" > /dev/null
            done
        ) &
    done
    wait
    end=$(date +%s%N)

    total_ms=$(( (end - start) / 1000000 ))
    ops_per_sec=$(echo "scale=1; $N * 1000 / $total_ms" | bc)

    echo "  total=$N writes  time=${total_ms}ms  throughput=${ops_per_sec} ops/sec" \
        | tee "$RESULTS_DIR/write_throughput.txt"
    ok "done"
}

# =============================================================================
# 4. LEADER FAILOVER TIME
# =============================================================================
bench_failover_time() {
    sep
    log "BENCHMARK 4 — Leader Failover Time"

    run_client put failover:canary before > /dev/null
    ok "cluster healthy before test"

    local leader_port="" container=""
    for port in 8081 8082 8083; do
        if "$CLIENT_BIN" --addr "localhost:$port" --timeout 2s put failover:probe x > /dev/null 2>&1; then
            leader_port=$port
            break
        fi
    done

    case $leader_port in
        8081) container="node-1" ;;
        8082) container="node-2" ;;
        8083) container="node-3" ;;
        *)    container="node-1"; leader_port=8081 ;;
    esac

    log "  Leader is $container (:$leader_port) — stopping it..."
    docker compose stop "$container" > /dev/null 2>&1

    local start end elapsed
    start=$(date +%s%N)
    local recovered=false

    for attempt in $(seq 1 100); do
        sleep 0.1
        if run_client put failover:after "yes" > /dev/null 2>&1; then
            end=$(date +%s%N)
            elapsed=$(( (end - start) / 1000000 ))
            ok "new leader elected in ~${elapsed}ms"
            recovered=true
            break
        fi
    done

    if ! $recovered; then
        fail "cluster did not recover within 10s"
        elapsed="-1"
    fi

    echo "  failover_ms=${elapsed}" | tee "$RESULTS_DIR/failover.txt"

    log "  Restarting $container..."
    docker compose start "$container" > /dev/null 2>&1
    sleep 1
    ok "cluster restored"
}

# =============================================================================
# 5. STALE READ DETECTION
# =============================================================================
bench_stale_reads() {
    sep
    log "BENCHMARK 5 — Stale Read Detection"
    local N=50
    local stale_count=0

    for i in $(seq 1 $N); do
        local key="bench:stale:$i"
        local expected="fresh-$i"

        retry_run_client_put "$key" "$expected"

        for port in 8081 8082 8083; do
            local actual
            actual=$(run_client_node "localhost:$port" get "$key" 2>/dev/null || echo "")
            [[ "$actual" != "$expected" ]] && (( stale_count++ )) || true
        done

        printf "\r  progress: %d/%d" "$i" "$N"
    done
    echo

    local total_reads=$(( N * 3 ))
    local stale_pct
    stale_pct=$(echo "scale=1; $stale_count * 100 / $total_reads" | bc)

    echo "  total_reads=$total_reads  stale=$stale_count  stale_rate=${stale_pct}%" \
        | tee "$RESULTS_DIR/stale_reads.txt"

    if [[ $stale_count -gt 0 ]]; then
        echo "  → Follower reads return stale data — ReadIndex required for linearizability"
    else
        ok "no stale reads detected (followers caught up within RTT)"
    fi
}

# =============================================================================
# 6. SLOW FOLLOWER RECOVERY
# =============================================================================
bench_slow_follower_recovery() {
    sep
    log "BENCHMARK 6 — Slow Follower Recovery (log backfill)"

    # Pause node-3 — || true prevents set -e from killing the script
    log "  Pausing node-3..."
    docker compose pause node-3 || true
    sleep 0.5  # give docker a moment to confirm the pause

    # Verify node-3 is actually unreachable now
    if "$CLIENT_BIN" --addr "localhost:8083" --timeout 1s get probe > /dev/null 2>&1; then
        fail "node-3 still responding after pause — is it running via docker compose?"
        return 1
    fi
    ok "node-3 is paused and unreachable"

    local N=50
    log "  Writing $N entries while node-3 is paused..."
    local write_ok=0
    for i in $(seq 1 $N); do
        # Exclude paused node-3 from client routing so random first-hop timeouts
        # do not hide a healthy leader+follower quorum.
        if run_client_addrs "localhost:8081,localhost:8082" put "bench:recovery:$i" "v-$i" > /dev/null 2>&1; then
            (( write_ok++ )) || true
        fi
        printf "\r  written: %d/%d (ok: %d)" "$i" "$N" "$write_ok"
    done
    echo

    if [[ $write_ok -lt $N ]]; then
        fail "only $write_ok/$N writes succeeded — cluster may be unhealthy"
    else
        ok "$N entries written to leader + node-2"
    fi

    log "  Resuming node-3..."
    docker compose unpause node-3 || true
    sleep 0.2

    # Poll node-3 directly until it returns the last key
    local start end elapsed
    start=$(date +%s%N)
    local synced=0

    log "  Waiting for node-3 to catch up..."
    for attempt in $(seq 1 200); do
        sleep 0.05
        local val
        val=$("$CLIENT_BIN" --addr "localhost:8083" --timeout 1s get "bench:recovery:$N" 2>/dev/null || echo "")
        if [[ "$val" == *"= v-$N" ]]; then
            end=$(date +%s%N)
            elapsed=$(( (end - start) / 1000000 ))
            ok "node-3 fully re-synced in ~${elapsed}ms ($N entries backfilled)"
            synced=1
            break
        fi
        # Show progress every 20 attempts
        (( attempt % 20 == 0 )) && log "  still waiting... attempt $attempt, last val='$val'"
    done

    if [[ $synced -eq 0 ]]; then
        fail "node-3 did not re-sync within 10s"
        elapsed="-1"
    fi

    echo "  recovery_ms=${elapsed}  entries_backfilled=$N" \
        | tee "$RESULTS_DIR/slow_follower_recovery.txt"
}

# =============================================================================
# SUMMARY
# =============================================================================
print_summary() {
    sep
    log "RESULTS SUMMARY"
    sep
    for f in "$RESULTS_DIR"/*.txt; do
        local name
        name=$(basename "$f" .txt)
        echo -e "  ${CYAN}${name}${NC}:"
        cat "$f" | sed 's/^/    /'
    done
    sep
    echo "  Full results saved in $RESULTS_DIR/"
}

# =============================================================================
# MAIN
# Usage:
#   ./benchmark.sh                   # run all
#   ./benchmark.sh write_latency     # run one benchmark by name
# =============================================================================
main() {
    if [[ $# -eq 0 ]]; then
        bench_write_latency
        bench_read_latency
        bench_write_throughput
        bench_failover_time
        bench_stale_reads
        bench_slow_follower_recovery
        print_summary
    else
        "bench_$1"
    fi
}

main "$@"
