#!/usr/bin/env bash
# =============================================================================
# consensus-lab benchmark script
# Measures: write latency, read latency, throughput, leader failover time,
#           stale read detection, slow-follower recovery
#
# Prerequisites:
#   docker compose up --build   (cluster running on :8081 :8082 :8083 :8084 :8085)
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "$REPO_ROOT"

NODES="${NODES:-}"
TIMEOUT="5s"
RESULTS_DIR="./.results/benchmark"
mkdir -p "$RESULTS_DIR"
# Extra pauses help observe state transitions in the admin dashboard.
# Set to 0 to disable (e.g. BENCH_OBSERVE_SLEEP=0 ./benchmark.sh).
BENCH_OBSERVE_SLEEP="${BENCH_OBSERVE_SLEEP:-2}"
RUN_ID="${RUN_ID:-$(date +%s)}"
NODE_ADDRS=()
LEADER_HINT_ADDR=""

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
fail() { echo -e "${RED}  ✗${NC} $*"; }
sep()  { echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }
progress() { printf "\r\033[2K%s" "$*"; }
observe_sleep() {
    local reason="${1:-state change}"
    local seconds="${2:-$BENCH_OBSERVE_SLEEP}"
    if [[ "$seconds" == "0" ]]; then
        return 0
    fi
    log "  pause ${seconds}s (${reason})"
    sleep "$seconds"
}

init_node_addrs() {
    IFS=',' read -r -a NODE_ADDRS <<<"$NODES"
}

port_from_addr() {
    local addr="$1"
    echo "${addr##*:}"
}

# Build client binary ONCE — avoids ~600ms go run compilation overhead per call
CLIENT_BIN="$RESULTS_DIR/client"
log "Building client binary..."
go build -o "$CLIENT_BIN" ./cmd/client
ok "client binary → $CLIENT_BIN"

run_client() {
    "$CLIENT_BIN" --addr "$NODES" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_raw() {
    "$CLIENT_BIN" --addr "$NODES" --timeout "$TIMEOUT" "$@"
}

run_client_node() {
    # Talk to a single specific node (no cluster routing)
    local addr="$1"; shift
    "$CLIENT_BIN" --addr "$addr" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_node_raw() {
    # Talk to a single specific node (no cluster routing), keep stderr visible
    local addr="$1"; shift
    "$CLIENT_BIN" --addr "$addr" --timeout "$TIMEOUT" "$@"
}

run_client_node_timeout() {
    # Talk to a single specific node with an explicit timeout
    local addr="$1"; shift
    local timeout="$1"; shift
    "$CLIENT_BIN" --addr "$addr" --timeout "$timeout" "$@" 2>/dev/null
}

run_client_timeout() {
    # Talk to the cluster with an explicit timeout
    local timeout="$1"; shift
    "$CLIENT_BIN" --addr "$NODES" --timeout "$timeout" "$@" 2>/dev/null
}

run_client_addrs() {
    # Talk to a specific subset of nodes (used for degraded-cluster benchmarks)
    local addrs="$1"; shift
    "$CLIENT_BIN" --addr "$addrs" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_addrs_timeout() {
    # Talk to a specific subset of nodes with an explicit timeout
    local addrs="$1"; shift
    local timeout="$1"; shift
    "$CLIENT_BIN" --addr "$addrs" --timeout "$timeout" "$@" 2>/dev/null
}

detect_leader_addr() {
    local addrs_csv="${1:-$NODES}"
    local addrs=()
    IFS=',' read -r -a addrs <<<"$addrs_csv"
    local addr
    for addr in "${addrs[@]}"; do
        if "$CLIENT_BIN" --addr "$addr" --timeout 2s put "bench:leader-probe:$RUN_ID" "x" > /dev/null 2>&1; then
            echo "$addr"
            return 0
        fi
    done
    return 1
}

refresh_leader_hint() {
    local addrs_csv="${1:-$NODES}"
    LEADER_HINT_ADDR="$(detect_leader_addr "$addrs_csv" || true)"
    [[ -n "$LEADER_HINT_ADDR" ]]
}

addr_in_csv() {
    local needle="$1"
    local addrs_csv="$2"
    local addrs=()
    IFS=',' read -r -a addrs <<<"$addrs_csv"
    local addr
    for addr in "${addrs[@]}"; do
        [[ "$addr" == "$needle" ]] && return 0
    done
    return 1
}

run_client_put_preferring_leader() {
    local addrs_csv="$1"; shift
    local key="$1"; shift
    local value="$1"; shift
    local timeout="${1:-$TIMEOUT}"

    if [[ -n "$LEADER_HINT_ADDR" ]] && addr_in_csv "$LEADER_HINT_ADDR" "$addrs_csv"; then
        if run_client_node_timeout "$LEADER_HINT_ADDR" "$timeout" put "$key" "$value" > /dev/null 2>&1; then
            return 0
        fi
    fi
    refresh_leader_hint "$addrs_csv" || true
    if [[ -n "$LEADER_HINT_ADDR" ]]; then
        if run_client_node_timeout "$LEADER_HINT_ADDR" "$timeout" put "$key" "$value" > /dev/null 2>&1; then
            return 0
        fi
    fi
    run_client_addrs_timeout "$addrs_csv" "$timeout" put "$key" "$value" > /dev/null 2>&1
}

retry_run_client_put() {
    local key="$1"
    local value="$2"
    local attempts="${3:-5}"
    local sleep_s="${4:-0.1}"

    local attempt
    for attempt in $(seq 1 "$attempts"); do
        if run_client_put_preferring_leader "$NODES" "$key" "$value" "$TIMEOUT"; then
            return 0
        fi
        sleep "$sleep_s"
    done

    fail "pre-populate put failed after ${attempts} attempts: key=$key"
    return 1
}

retry_run_client_addrs_put() {
    local addrs="$1"
    local timeout="$2"
    local key="$3"
    local value="$4"
    local attempts="${5:-5}"
    local sleep_s="${6:-0.1}"

    local attempt
    for attempt in $(seq 1 "$attempts"); do
        if run_client_put_preferring_leader "$addrs" "$key" "$value" "$timeout"; then
            return 0
        fi
        sleep "$sleep_s"
    done
    return 1
}

# ─── helper: measure single operation in ms + status ─────────────────────────
MEASURE_MS=0
MEASURE_OK=0
MEASURE_TIMEOUT=0
MEASURE_ERR=""

measure_op() {
    local start end out rc
    start=$(date +%s%N)
    out="$("$@" 2>&1)" || rc=$?
    rc="${rc:-0}"
    end=$(date +%s%N)
    MEASURE_MS=$(( (end - start) / 1000000 ))
    MEASURE_OK=0
    MEASURE_TIMEOUT=0
    MEASURE_ERR=""

    if [[ "$rc" -eq 0 ]]; then
        MEASURE_OK=1
        return 0
    fi

    MEASURE_ERR="$(echo "$out" | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g')"
    if [[ "$out" == *"DeadlineExceeded"* || "$out" == *"context deadline exceeded"* || "$out" == *"timed out"* ]]; then
        MEASURE_TIMEOUT=1
    fi
    return "$rc"
}

percentiles() {
    local n="$1"
    if [[ "$n" -le 0 ]]; then
        echo "  no successful samples"
        return 0
    fi
    sort -n | awk -v n="$n" '
    {
        a[NR] = $1
        sum += $1
    }
    END {
        p50 = a[int(n*0.50)+1]
        p95 = a[int(n*0.95)+1]
        p99 = a[int(n*0.99)+1]
        p999_idx = int(n*0.999)+1
        if (p999_idx < 1) p999_idx = 1
        if (p999_idx > n) p999_idx = n
        p999 = a[p999_idx]
        max = a[n]
        printf "  median=%dms  p95=%dms  p99=%dms  p99.9=%dms  max=%dms  avg=%.1fms\n", p50, p95, p99, p999, max, sum/n
    }'
}

# =============================================================================
# 1. WRITE LATENCY
# =============================================================================
bench_write_latency() {
    sep
    log "BENCHMARK 1 — Write Latency (sequential puts)"
    local N="${BENCH_WRITE_LATENCY_N:-1000}"
    local WARMUP="${BENCH_WRITE_LATENCY_WARMUP:-20}"
    local PIN_LEADER="${BENCH_WRITE_LATENCY_PIN_LEADER:-1}"
    local SLOW_MS="${BENCH_WRITE_LATENCY_SLOW_MS:-200}"
    local times=()
    local ok_count=0 fail_count=0 timeout_count=0
    local warmup_ok=0 warmup_fail=0
    local target_desc="cluster-routing"
    local leader_addr=""
    local slow_samples=()

    if [[ "${PIN_LEADER}" == "1" ]]; then
        leader_addr="$(detect_leader_addr || true)"
        if [[ -n "$leader_addr" ]]; then
            target_desc="leader-pinned($leader_addr)"
            log "  Write target: $target_desc"
        else
            log "  Write target: cluster-routing (leader detection failed)"
        fi
    else
        log "  Write target: $target_desc"
    fi

    if [[ "$WARMUP" -gt 0 ]]; then
        log "  Warmup: ${WARMUP} sequential puts (excluded from stats)"
        for i in $(seq 1 "$WARMUP"); do
            local key="bench:lat:warmup:$RUN_ID:$i"
            progress "  warmup: ${i}/${WARMUP} (put ${key})"
            if [[ -n "$leader_addr" ]]; then
                if run_client_node_raw "$leader_addr" put "$key" "value-$i" > /dev/null 2>&1; then
                    (( warmup_ok++ )) || true
                else
                    (( warmup_fail++ )) || true
                fi
            elif run_client_raw put "$key" "value-$i" > /dev/null 2>&1; then
                (( warmup_ok++ )) || true
            else
                (( warmup_fail++ )) || true
            fi
        done
        echo
        log "  Warmup done (ok=$warmup_ok failed=$warmup_fail)"
    fi

    for i in $(seq 1 $N); do
        local key="bench:lat:$RUN_ID:$i"
        progress "  progress: ${i}/${N} (put ${key})"
        if [[ -n "$leader_addr" ]]; then
            measure_op run_client_node_raw "$leader_addr" put "$key" "value-$i" || true
        else
            measure_op run_client_raw put "$key" "value-$i" || true
        fi
        if [[ "$MEASURE_OK" -eq 1 ]]; then
            times+=("$MEASURE_MS")
            (( ok_count++ )) || true
            if [[ "$MEASURE_MS" -ge "$SLOW_MS" ]]; then
                slow_samples+=("${i}:${MEASURE_MS}")
            fi
            progress "  progress: ${i}/${N} (ok last=${MEASURE_MS}ms)"
        else
            (( fail_count++ )) || true
            if [[ "$MEASURE_TIMEOUT" -eq 1 ]]; then
                (( timeout_count++ )) || true
                progress "  progress: ${i}/${N} (timeout ${MEASURE_MS}ms)"
            else
                progress "  progress: ${i}/${N} (err ${MEASURE_MS}ms)"
            fi
        fi
    done
    echo

    if [[ ${#times[@]} -gt 0 ]]; then
        printf '%s\n' "${times[@]}" | percentiles "${#times[@]}" | tee "$RESULTS_DIR/write_latency.txt"
    else
        echo "  no successful samples" | tee "$RESULTS_DIR/write_latency.txt"
    fi
    if [[ ${#slow_samples[@]} -gt 0 ]]; then
        printf "  slow_samples(>=%sms): %s\n" "$SLOW_MS" "${slow_samples[*]}" | tee -a "$RESULTS_DIR/write_latency.txt"
    else
        echo "  slow_samples(>=${SLOW_MS}ms): none" | tee -a "$RESULTS_DIR/write_latency.txt"
    fi
    echo "  config: measured_n=$N warmup=$WARMUP pin_leader=$PIN_LEADER target=$target_desc run_id=$RUN_ID" | tee -a "$RESULTS_DIR/write_latency.txt"
    echo "  total=$N  ok=$ok_count  failed=$fail_count  timeouts=$timeout_count" | tee -a "$RESULTS_DIR/write_latency.txt"
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
    local ok_count=0 fail_count=0 timeout_count=0

    log "  Pre-populating $N keys..."
    for i in $(seq 1 $N); do
        local key="bench:read:$i"
        progress "  pre-populate: ${i}/${N} (put ${key})"
        retry_run_client_put "$key" "value-$i"
        progress "  pre-populate: ${i}/${N} (done)"
    done
    echo

    for i in $(seq 1 $N); do
        local key="bench:read:$i"
        progress "  progress: ${i}/${N} (get ${key})"
        measure_op run_client_raw get "$key" || true
        if [[ "$MEASURE_OK" -eq 1 ]]; then
            times+=("$MEASURE_MS")
            (( ok_count++ )) || true
            progress "  progress: ${i}/${N} (ok last=${MEASURE_MS}ms)"
        else
            (( fail_count++ )) || true
            if [[ "$MEASURE_TIMEOUT" -eq 1 ]]; then
                (( timeout_count++ )) || true
                progress "  progress: ${i}/${N} (timeout ${MEASURE_MS}ms)"
            else
                progress "  progress: ${i}/${N} (err ${MEASURE_MS}ms)"
            fi
        fi
    done
    echo

    if [[ ${#times[@]} -gt 0 ]]; then
        printf '%s\n' "${times[@]}" | percentiles "${#times[@]}" | tee "$RESULTS_DIR/read_latency.txt"
    else
        echo "  no successful samples" | tee "$RESULTS_DIR/read_latency.txt"
    fi
    echo "  total=$N  ok=$ok_count  failed=$fail_count  timeouts=$timeout_count" | tee -a "$RESULTS_DIR/read_latency.txt"
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
    local throughput_leader_addr=""

    throughput_leader_addr="$(detect_leader_addr || true)"
    if [[ -n "$throughput_leader_addr" ]]; then
        log "  Throughput target: leader-pinned ($throughput_leader_addr)"
    else
        log "  Throughput target: cluster-routing (leader detection failed)"
    fi

    log "  Launching $CONCURRENCY workers × $BATCH writes..."
    start=$(date +%s%N)
    for w in $(seq 1 $CONCURRENCY); do
        (
            for i in $(seq 1 $BATCH); do
                printf "\r\033[2K  worker %d/%d (put %d/%d)" "$w" "$CONCURRENCY" "$i" "$BATCH" >&2
                if [[ -n "$throughput_leader_addr" ]]; then
                    run_client_node "$throughput_leader_addr" put "bench:thr:w${w}:$i" "v" > /dev/null
                else
                    run_client put "bench:thr:w${w}:$i" "v" > /dev/null
                fi
            done
        ) &
    done
    wait
    printf "\r\033[2K  workers complete\n" >&2
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
    local addr
    for addr in "${NODE_ADDRS[@]}"; do
        local port
        port=$(port_from_addr "$addr")
        progress "  probing leader candidate :${port}"
        if "$CLIENT_BIN" --addr "$addr" --timeout 2s put failover:probe x > /dev/null 2>&1; then
            leader_port=$port
            break
        fi
    done
    echo

    case $leader_port in
        8081) container="node-1" ;;
        8082) container="node-2" ;;
        8083) container="node-3" ;;
        8084) container="node-4" ;;
        8085) container="node-5" ;;
        *)    container="node-1"; leader_port=8081 ;;
    esac

    log "  Leader is $container (:$leader_port) — stopping it..."
    docker compose stop "$container" > /dev/null 2>&1
    observe_sleep "leader stopped (watch admin dashboard for unavailable node)"

    local start end elapsed
    start=$(date +%s%N)
    local recovered=false

    for attempt in $(seq 1 100); do
        sleep 0.1
        progress "  waiting for failover... attempt ${attempt}/100"
        if run_client put failover:after "yes" > /dev/null 2>&1; then
            end=$(date +%s%N)
            elapsed=$(( (end - start) / 1000000 ))
            echo
            ok "new leader elected in ~${elapsed}ms"
            recovered=true
            observe_sleep "new leader elected (watch role/leader updates)" 1
            break
        fi
    done
    if ! $recovered; then
        echo
    fi

    if ! $recovered; then
        fail "cluster did not recover within 10s"
        elapsed="-1"
    fi

    echo "  failover_ms=${elapsed}" | tee "$RESULTS_DIR/failover.txt"

    log "  Restarting $container..."
    docker compose start "$container" > /dev/null 2>&1
    observe_sleep "leader restarted (watch node recovery)"
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
    local read_timeout="1s"
    local write_timeout="2s"

    for i in $(seq 1 $N); do
        local key="bench:stale:${RUN_ID}:$i"
        local expected="fresh-$i"

        progress "  progress: ${i}/${N} (write)"
        local put_ok=0
        for attempt in $(seq 1 5); do
            if run_client_timeout "$write_timeout" put "$key" "$expected" > /dev/null 2>&1; then
                put_ok=1
                break
            fi
            sleep 0.1
        done
        if [[ $put_ok -eq 0 ]]; then
            fail "stale-read setup write failed for key=$key"
            continue
        fi

        local addr port
        for addr in "${NODE_ADDRS[@]}"; do
            port=$(port_from_addr "$addr")
            progress "  progress: ${i}/${N} (read :${port})"
            local actual
            actual=$(run_client_node_timeout "$addr" "$read_timeout" get "$key" 2>/dev/null || echo "")
            [[ "$actual" != "$expected" ]] && (( stale_count++ )) || true
        done

        progress "  progress: ${i}/${N} (done)"
    done
    echo

    local total_reads=$(( N * ${#NODE_ADDRS[@]} ))
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
    observe_sleep "node-3 paused (watch admin dashboard for failure)" 2

    # Verify node-3 is actually unreachable now
    if "$CLIENT_BIN" --addr "localhost:8083" --timeout 1s get probe > /dev/null 2>&1; then
        fail "node-3 still responding after pause — is it running via docker compose?"
        return 1
    fi
    ok "node-3 is paused and unreachable"

    local N=50
    local key_prefix="bench:recovery:${RUN_ID}"
    local active_addrs="localhost:8081,localhost:8082,localhost:8084,localhost:8085"
    local degraded_write_timeout="10s"
    log "  Writing $N entries while node-3 is paused..."
    local write_ok=0
    for i in $(seq 1 $N); do
        # Exclude paused node-3 from client routing so random first-hop timeouts
        # do not hide a healthy leader+follower quorum.
        if retry_run_client_addrs_put "$active_addrs" "$degraded_write_timeout" "${key_prefix}:$i" "v-$i" 5 0.2; then
            (( write_ok++ )) || true
        fi
        progress "  written: ${i}/${N} (ok=${write_ok})"
    done
    echo

    if [[ $write_ok -lt $N ]]; then
        fail "only $write_ok/$N writes succeeded while follower was paused (likely commit timeouts under degraded mode)"
        echo "  recovery_ms=-1  entries_backfilled=$N  writes_ok=$write_ok" \
            | tee "$RESULTS_DIR/slow_follower_recovery.txt"
        log "  Resuming node-3 before exit..."
        docker compose unpause node-3 || true
        observe_sleep "node-3 resumed after failed write phase" 1
        return 1
    fi
    ok "$N entries written while node-3 was paused"

    log "  Resuming node-3..."
    docker compose unpause node-3 || true
    observe_sleep "node-3 resumed (watch recovery/catch-up)" 1

    # Poll node-3 directly until it returns the last key
    local start end elapsed
    start=$(date +%s%N)
    local synced=0

    log "  Waiting for node-3 to catch up..."
    for attempt in $(seq 1 200); do
        sleep 0.05
        progress "  catch-up: attempt ${attempt}/200"
        local val
        val=$("$CLIENT_BIN" --addr "localhost:8083" --timeout 1s get "${key_prefix}:$N" 2>/dev/null || echo "")
        if [[ "$val" == *"= v-$N" ]]; then
            end=$(date +%s%N)
            elapsed=$(( (end - start) / 1000000 ))
            echo
            ok "node-3 fully re-synced in ~${elapsed}ms ($N entries backfilled)"
            synced=1
            break
        fi
        # Show progress every 20 attempts
        (( attempt % 20 == 0 )) && log "  still waiting... attempt $attempt, last val='$val'"
    done

    if [[ $synced -eq 0 ]]; then
        echo
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
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --nodes)
                NODES="${2:?--nodes requires value}"
                shift 2
                ;;
            *)
                break
                ;;
        esac
    done

    init_node_addrs
    if [[ ${#NODE_ADDRS[@]} -eq 0 ]]; then
        fail "no nodes provided (use --nodes or NODES=...)"
        exit 1
    fi

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
