#!/usr/bin/env bash
# =============================================================================
# consensus-lab fault tolerance scenarios
# Exercises node failures/restarts and validates write/read behavior.
#
# Prerequisites:
#   docker compose up --build   (cluster running)
# =============================================================================

set -euo pipefail

NODES="localhost:8081,localhost:8082,localhost:8083,localhost:8084,localhost:8085"
ALL_KV_PORTS=(8081 8082 8083 8084 8085)
RESULTS_DIR="./.bench-results"
mkdir -p "$RESULTS_DIR"

TIMEOUT="${TIMEOUT:-5s}"
OBSERVE_SLEEP="${OBSERVE_SLEEP:-2}"
RUN_ID="${RUN_ID:-$(date +%s)}"
FT_BULK_WRITES="${FT_BULK_WRITES:-25}"
RESULT_FILE="$RESULTS_DIR/fault_tolerance.txt"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  !${NC} $*"; }
fail() { echo -e "${RED}  ✗${NC} $*"; }
sep()  { echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }

observe_sleep() {
    local reason="${1:-state change}"
    local seconds="${2:-$OBSERVE_SLEEP}"
    if [[ "$seconds" == "0" ]]; then
        return 0
    fi
    log "  pause ${seconds}s (${reason})"
    sleep "$seconds"
}

CLIENT_BIN="$RESULTS_DIR/client"

build_client() {
    log "Building client binary..."
    go build -o "$CLIENT_BIN" ./cmd/client
    ok "client binary → $CLIENT_BIN"
}

run_client() {
    "$CLIENT_BIN" --addr "$NODES" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_addrs() {
    local addrs="$1"; shift
    "$CLIENT_BIN" --addr "$addrs" --timeout "$TIMEOUT" "$@" 2>/dev/null
}

run_client_node() {
    local addr="$1"; shift
    "$CLIENT_BIN" --addr "$addr" --timeout 2s "$@" 2>/dev/null
}

retry_put() {
    local addrs="$1"; shift
    local key="$1"; shift
    local value="$1"; shift
    local attempts="${1:-10}"
    local sleep_s="${2:-0.2}"

    local i
    for i in $(seq 1 "$attempts"); do
        if run_client_addrs "$addrs" put "$key" "$value" > /dev/null 2>&1; then
            return 0
        fi
        sleep "$sleep_s"
    done
    return 1
}

leader_port() {
    local port
    for port in "${ALL_KV_PORTS[@]}"; do
        printf "\r  probing leader :%s" "$port" >&2
        if run_client_node "localhost:$port" put "ft:probe:${RUN_ID}" "x" > /dev/null 2>&1; then
            echo >&2
            echo "$port"
            return 0
        fi
    done
    echo >&2
    return 1
}

container_for_port() {
    case "$1" in
        8081) echo "node-1" ;;
        8082) echo "node-2" ;;
        8083) echo "node-3" ;;
        8084) echo "node-4" ;;
        8085) echo "node-5" ;;
        *) return 1 ;;
    esac
}

port_for_container() {
    case "$1" in
        node-1) echo "8081" ;;
        node-2) echo "8082" ;;
        node-3) echo "8083" ;;
        node-4) echo "8084" ;;
        node-5) echo "8085" ;;
        *) return 1 ;;
    esac
}

cluster_write_ok() {
    local key="$1"
    local value="$2"
    run_client put "$key" "$value" > /dev/null 2>&1
}

cluster_write_fail_expected() {
    local key="$1"
    local value="$2"
    if run_client put "$key" "$value" > /dev/null 2>&1; then
        return 1
    fi
    return 0
}

bulk_write_check() {
    local prefix="$1"
    local count="$2"
    local addrs="${3:-$NODES}"

    local ok_count=0
    local i
    for i in $(seq 1 "$count"); do
        printf "\r  bulk writes: %d/%d (%s:%d)" "$i" "$count" "$prefix" "$i"
        if run_client_addrs "$addrs" put "${prefix}:$i" "v-$i" > /dev/null 2>&1; then
            (( ok_count++ )) || true
        fi
    done
    echo

    [[ "$ok_count" -eq "$count" ]]
}

cleanup_resume_all() {
    docker compose start node-1 node-2 node-3 node-4 node-5 > /dev/null 2>&1 || true
}

wait_nodes_down() {
    local timeout_s="${1:-15}"; shift
    local nodes=("$@")
    local deadline=$(( $(date +%s) + timeout_s ))

    while (( $(date +%s) <= deadline )); do
        local running
        running="$(docker compose ps --status running --services 2>/dev/null || true)"
        local all_down=true
        local n
        for n in "${nodes[@]}"; do
            if grep -qx "$n" <<<"$running"; then
                all_down=false
                break
            fi
        done
        if $all_down; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

wait_nodes_up() {
    local timeout_s="${1:-20}"; shift
    local nodes=("$@")
    local deadline=$(( $(date +%s) + timeout_s ))

    while (( $(date +%s) <= deadline )); do
        local running
        running="$(docker compose ps --status running --services 2>/dev/null || true)"
        local all_up=true
        local n
        for n in "${nodes[@]}"; do
            if ! grep -qx "$n" <<<"$running"; then
                all_up=false
                break
            fi
        done
        if $all_up; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

record_result() {
    local name="$1" status="$2" details="$3"
    printf "%s status=%s %s\n" "$name" "$status" "$details" | tee -a "$RESULT_FILE"
}

scenario_baseline() {
    sep
    log "SCENARIO 1 — Baseline read/write"
    local key="ft:baseline:${RUN_ID}"
    local value="ok-${RUN_ID}"

    if ! cluster_write_ok "$key" "$value"; then
        fail "baseline write failed"
        record_result "baseline" "FAIL" "write_failed=1"
        return 1
    fi

    local got
    got=$(run_client get "$key" || true)
    if [[ "$got" == *"= $value" ]]; then
        ok "baseline read/write ok"
        record_result "baseline" "PASS" "key=$key"
        observe_sleep "baseline stable state (observe admin dashboard)" 1
        return 0
    fi

    fail "baseline read mismatch: got='$got'"
    record_result "baseline" "FAIL" "read_mismatch=1"
    return 1
}

scenario_single_follower_stop() {
    sep
    log "SCENARIO 2 — Stop one follower, writes should continue"

    local leader
    leader=$(leader_port) || { fail "cannot detect leader"; record_result "single_follower_stop" "FAIL" "leader_detect=0"; return 1; }

    local stop_container="node-5"
    local stop_port="8085"
    if [[ "$leader" == "$stop_port" ]]; then
        stop_container="node-4"
        stop_port="8084"
    fi

    log "  Stopping follower candidate ${stop_container} (:${stop_port})..."
    docker compose stop "$stop_container" > /dev/null 2>&1
    if wait_nodes_down 15 "$stop_container"; then
        ok "${stop_container} is stopped"
    else
        fail "timed out waiting for ${stop_container} to stop"
        record_result "single_follower_stop" "FAIL" "stopped=$stop_container wait_down_timeout=1"
        return 1
    fi
    observe_sleep "follower stopped (watch admin dashboard)"

    local key="ft:follower-stop:${RUN_ID}"
    if cluster_write_ok "$key" "ok"; then
        ok "write succeeded with one follower down"
        if bulk_write_check "ft:follower-stop-bulk:${RUN_ID}" "$FT_BULK_WRITES"; then
            ok "bulk writes succeeded with one follower down (${FT_BULK_WRITES}/${FT_BULK_WRITES})"
            record_result "single_follower_stop" "PASS" "stopped=$stop_container bulk_writes=$FT_BULK_WRITES"
        else
            fail "bulk writes failed with one follower down"
            record_result "single_follower_stop" "FAIL" "stopped=$stop_container bulk_writes_failed=1"
            docker compose start "$stop_container" > /dev/null 2>&1 || true
            observe_sleep "follower restarted after failure" 1
            return 1
        fi
    else
        fail "write failed with one follower down"
        record_result "single_follower_stop" "FAIL" "stopped=$stop_container write_failed=1"
        docker compose start "$stop_container" > /dev/null 2>&1 || true
        observe_sleep "follower restarted after failure" 1
        return 1
    fi

    log "  Restarting ${stop_container}..."
    docker compose start "$stop_container" > /dev/null 2>&1
    if wait_nodes_up 20 "$stop_container"; then
        ok "${stop_container} is running"
    else
        fail "timed out waiting for ${stop_container} to start"
        record_result "single_follower_stop" "FAIL" "stopped=$stop_container wait_up_timeout=1"
        return 1
    fi
    observe_sleep "follower restarted (watch recovery)"
    ok "follower restored"
}

scenario_leader_failover() {
    sep
    log "SCENARIO 3 — Stop leader, failover, resume writes"

    local leader
    leader=$(leader_port) || { fail "cannot detect leader"; record_result "leader_failover" "FAIL" "leader_detect=0"; return 1; }
    local container
    container=$(container_for_port "$leader")

    log "  Stopping leader ${container} (:${leader})..."
    docker compose stop "$container" > /dev/null 2>&1
    if wait_nodes_down 15 "$container"; then
        ok "${container} is stopped"
    else
        fail "timed out waiting for ${container} to stop"
        record_result "leader_failover" "FAIL" "leader=$container wait_down_timeout=1"
        return 1
    fi
    observe_sleep "leader stopped (watch role changes)"

    local start end elapsed recovered=false
    start=$(date +%s%N)
    for attempt in $(seq 1 100); do
        printf "\r  waiting for failover... attempt %d/100" "$attempt"
        sleep 0.1
        if cluster_write_ok "ft:failover:${RUN_ID}" "ok"; then
            end=$(date +%s%N)
            elapsed=$(( (end - start) / 1000000 ))
            recovered=true
            echo
            ok "new leader elected in ~${elapsed}ms"
            if bulk_write_check "ft:failover-bulk:${RUN_ID}" "$FT_BULK_WRITES"; then
                ok "bulk writes succeeded after failover (${FT_BULK_WRITES}/${FT_BULK_WRITES})"
                record_result "leader_failover" "PASS" "leader=$container failover_ms=$elapsed bulk_writes=$FT_BULK_WRITES"
            else
                fail "bulk writes failed after failover"
                record_result "leader_failover" "FAIL" "leader=$container failover_ms=$elapsed bulk_writes_failed=1"
                recovered=false
            fi
            observe_sleep "new leader active (watch role/leader in admin)" 1
            break
        fi
    done
    if ! $recovered; then
        echo
        fail "cluster did not recover writes within 10s"
        record_result "leader_failover" "FAIL" "leader=$container failover_ms=-1"
        observe_sleep "failed failover state (inspect admin dashboard)" 2
    fi

    log "  Restarting ${container}..."
    docker compose start "$container" > /dev/null 2>&1
    if wait_nodes_up 20 "$container"; then
        ok "${container} is running"
    else
        fail "timed out waiting for ${container} to start"
        record_result "leader_failover" "FAIL" "leader=$container wait_up_timeout=1"
        return 1
    fi
    observe_sleep "leader restarted (watch rejoin)"
    ok "leader restored"

    $recovered
}

scenario_quorum_loss() {
    sep
    log "SCENARIO 4 — Lose quorum, writes must fail"

    # Stop any 3 nodes. Keep node-1/node-2 for easier recovery checks.
    local stopped=(node-3 node-4 node-5)
    log "  Stopping ${stopped[*]}..."
    docker compose stop "${stopped[@]}" > /dev/null 2>&1
    if wait_nodes_down 20 "${stopped[@]}"; then
        ok "target nodes are stopped"
    else
        fail "timed out waiting for target nodes to stop"
        record_result "quorum_loss" "FAIL" "stopped=${stopped[*]} wait_down_timeout=1"
        return 1
    fi
    observe_sleep "quorum lost (watch admin dashboard)"

    local key="ft:quorum-loss:${RUN_ID}"
    if cluster_write_fail_expected "$key" "must-fail"; then
        ok "write rejected without quorum (expected)"
        record_result "quorum_loss" "PASS" "stopped=${stopped[*]}"
        observe_sleep "quorum lost confirmed (observe unavailable/leader state)" 2
    else
        fail "write unexpectedly succeeded without quorum"
        record_result "quorum_loss" "FAIL" "stopped=${stopped[*]} unexpected_write_success=1"
        observe_sleep "unexpected quorum behavior (inspect admin dashboard)" 2
        docker compose start "${stopped[@]}" > /dev/null 2>&1 || true
        wait_nodes_up 20 "${stopped[@]}" || true
        observe_sleep "nodes restarted after quorum test" 1
        return 1
    fi

    log "  Restarting ${stopped[*]}..."
    docker compose start "${stopped[@]}" > /dev/null 2>&1
    if wait_nodes_up 20 "${stopped[@]}"; then
        ok "target nodes are running"
    else
        fail "timed out waiting for target nodes to start"
        record_result "quorum_restore" "FAIL" "wait_up_timeout=1"
        return 1
    fi
    observe_sleep "quorum restored (watch leader/health)"

    local recover_key="ft:quorum-restored:${RUN_ID}"
    if retry_put "$NODES" "$recover_key" "ok" 20 0.2; then
        if bulk_write_check "ft:quorum-restored-bulk:${RUN_ID}" "$FT_BULK_WRITES"; then
            ok "writes resumed after quorum restoration"
            record_result "quorum_restore" "PASS" "key=$recover_key bulk_writes=$FT_BULK_WRITES"
        else
            fail "single write recovered, but bulk writes failed after quorum restoration"
            record_result "quorum_restore" "FAIL" "key=$recover_key bulk_writes_failed=1"
            observe_sleep "quorum restored but bulk writes failing (inspect admin)" 2
            return 1
        fi
        observe_sleep "quorum restored and writes resumed (observe convergence)" 2
    else
        fail "writes did not resume after quorum restoration"
        record_result "quorum_restore" "FAIL" "key=$recover_key"
        observe_sleep "quorum restored but writes still failing (inspect admin)" 2
        return 1
    fi
}

print_summary() {
    sep
    log "FAULT TOLERANCE SUMMARY"
    sep
    cat "$RESULT_FILE" | sed 's/^/  /'
    sep
    echo "  Results saved in $RESULT_FILE"
}

main() {
    : > "$RESULT_FILE"
    trap cleanup_resume_all EXIT

    build_client
    scenario_baseline
    scenario_single_follower_stop
    scenario_leader_failover
    scenario_quorum_loss
    print_summary
}

main "$@"
