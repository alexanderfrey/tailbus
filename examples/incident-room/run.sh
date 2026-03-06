#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18643"
COORD_HEALTH=":18681"
COORD_DATA="/tmp/incidentroom-coord"
LOG_DIR="/tmp/incidentroom-logs"
GO_TMPDIR="${TMPDIR:-/tmp}"

# name:listen_port:metrics_port
NODES="
support-node:19643:19291
ops-node:19644:19292
finance-node:19645:19293
comms-node:19646:19294
"

# node:script
AGENTS="
support-node:support_triage.py
support-node:orchestrator.py
ops-node:logs_agent.py
ops-node:metrics_agent.py
ops-node:release_agent.py
finance-node:billing_agent.py
comms-node:status_agent.py
"

DIM="\033[2m"
BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
CYAN="\033[36m"
RESET="\033[0m"

say()  { echo -e "  ${DIM}run.sh${RESET}  $*"; }
good() { echo -e "  ${DIM}run.sh${RESET}  ${GREEN}✓${RESET} $*"; }
fail() { echo -e "  ${DIM}run.sh${RESET}  ${RED}✗${RESET} $*"; exit 1; }

kill_pid_list() {
    local sig="$1"
    shift || true
    if [ "$#" -gt 0 ]; then
        kill "-${sig}" "$@" 2>/dev/null || true
    fi
}

wait_for_pids_to_exit() {
    local deadline_secs="${1:-3}"
    shift || true
    local end=$((SECONDS + deadline_secs))
    while [ "$#" -gt 0 ] && [ "$SECONDS" -lt "$end" ]; do
        local remaining=()
        local pid
        for pid in "$@"; do
            if kill -0 "$pid" 2>/dev/null; then
                remaining+=("$pid")
            fi
        done
        if [ "${#remaining[@]}" -eq 0 ]; then
            return 0
        fi
        sleep 0.2
        set -- "${remaining[@]}"
    done
    return 1
}

kill_listener_on_port() {
    local port="$1"
    local pids=()
    while IFS= read -r pid; do
        [ -n "$pid" ] && pids+=("$pid")
    done < <(lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)
    if [ "${#pids[@]}" -eq 0 ]; then
        return 0
    fi
    kill_pid_list TERM "${pids[@]}"
    if ! wait_for_pids_to_exit 3 "${pids[@]}"; then
        kill_pid_list KILL "${pids[@]}"
    fi
}

kill_processes_matching() {
    local pattern="$1"
    local pids=()
    while IFS= read -r pid; do
        [ -n "$pid" ] && pids+=("$pid")
    done < <(pgrep -f "$pattern" 2>/dev/null || true)
    if [ "${#pids[@]}" -eq 0 ]; then
        return 0
    fi
    kill_pid_list TERM "${pids[@]}"
    if ! wait_for_pids_to_exit 3 "${pids[@]}"; then
        kill_pid_list KILL "${pids[@]}"
    fi
}

stop_all() {
    say "stopping all incident-room processes..."

    for entry in $AGENTS; do
        local node script
        IFS=: read -r node script <<< "$entry"
        local sock="/tmp/incidentroom-${node}.sock"
        kill_processes_matching "TAILBUS_SOCKET=${sock}"
        kill_processes_matching "${SCRIPT_DIR}/${script}"
    done

    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"
    done

    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18643
    kill_listener_on_port 18681

    sleep 1
    rm -f /tmp/incidentroom-*.sock
    rm -f \
        "${GO_TMPDIR}/tailbusd-support-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-ops-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-finance-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-comms-node.coord-fp"
    good "stopped"
}

fire_incident() {
    local incident="$1"
    local sock="/tmp/incidentroom-support-node.sock"
    local payload

    if [ ! -S "$sock" ]; then
        fail "support node not running (no socket at $sock)"
    fi

    payload="$(python3 -c 'import json,sys; print(json.dumps({"command":"report_incident","arguments":{"incident":sys.argv[1]}}))' "$incident")"
    say "firing incident..."
    echo ""
    tailbus -socket "$sock" fire support-triage "$payload"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching incident-room logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/agent-*.log 2>/dev/null
}

start_all() {
    command -v tailbus-coord >/dev/null || fail "tailbus-coord not found in PATH"
    command -v tailbusd >/dev/null || fail "tailbusd not found in PATH"
    command -v tailbus >/dev/null || fail "tailbus not found in PATH"
    command -v python3 >/dev/null || fail "python3 not found in PATH"
    command -v curl >/dev/null || fail "curl not found in PATH"

    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA" "${SCRIPT_DIR}/output"

    echo ""
    echo -e "  ${BOLD}Incident Room — Flagship Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    say "starting coord on ${CYAN}${COORD_ADDR}${RESET}..."
    tailbus-coord \
        -listen "$COORD_ADDR" \
        -data-dir "$COORD_DATA" \
        -health-addr "$COORD_HEALTH" \
        > "$LOG_DIR/coord.log" 2>&1 &

    local retries=0
    while ! curl -sf "http://127.0.0.1${COORD_HEALTH}/healthz" >/dev/null 2>&1; do
        retries=$((retries + 1))
        if [ $retries -gt 30 ]; then
            fail "coord didn't start — check $LOG_DIR/coord.log"
        fi
        sleep 0.2
    done
    good "coord ready"

    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        local sock="/tmp/incidentroom-${name}.sock"

        say "starting daemon ${CYAN}${name}${RESET} on :${listen_port}..."
        tailbusd \
            -node-id "$name" \
            -coord "$COORD_ADDR" \
            -advertise "127.0.0.1:${listen_port}" \
            -listen ":${listen_port}" \
            -socket "$sock" \
            -metrics ":${metrics_port}" \
            > "$LOG_DIR/daemon-${name}.log" 2>&1 &

        retries=0
        while [ ! -S "$sock" ]; do
            retries=$((retries + 1))
            if [ $retries -gt 30 ]; then
                fail "daemon ${name} didn't start — check $LOG_DIR/daemon-${name}.log"
            fi
            sleep 0.2
        done
        good "daemon ${BOLD}${name}${RESET} ready"
    done

    echo ""
    say "starting agents..."
    for entry in $AGENTS; do
        local node script
        IFS=: read -r node script <<< "$entry"
        local sock="/tmp/incidentroom-${node}.sock"
        local name="${script%.py}"

        TAILBUS_SOCKET="$sock" OUTPUT_DIR="${SCRIPT_DIR}/output" python3 "${SCRIPT_DIR}/${script}" \
            > "$LOG_DIR/agent-${name}.log" 2>&1 &
        good "agent ${BOLD}${name}${RESET} on ${node}"
    done

    sleep 1

    echo ""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 4 daemons + 7 agents"
    echo ""
    echo -e "  ${BOLD}Dashboard:${RESET}"
    echo -e "    tailbus -socket /tmp/incidentroom-support-node.sock dashboard"
    echo ""
    echo -e "  ${BOLD}Inspect mesh capabilities:${RESET}"
    echo -e "    tailbus -socket /tmp/incidentroom-support-node.sock list --verbose"
    echo -e "    tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities ops.logs.search"
    echo ""
    echo -e "  ${BOLD}Open an incident:${RESET}"
    echo -e "    ./run.sh fire \"EU customers cannot complete checkout\""
    echo -e "    ./run.sh fire \"Subscriptions are paid but not activating\""
    echo ""
    echo -e "  ${BOLD}Watch logs:${RESET}"
    echo -e "    ./run.sh logs"
    echo ""
    echo -e "  ${BOLD}Stop everything:${RESET}"
    echo -e "    ./run.sh stop"
    echo ""
}

case "${1:-start}" in
    stop)
        stop_all
        ;;
    fire)
        shift
        [ $# -ge 1 ] || fail "usage: ./run.sh fire \"incident text\""
        fire_incident "$*"
        ;;
    logs)
        watch_logs
        ;;
    start)
        start_all
        ;;
    *)
        echo "Usage: ./run.sh [start|stop|fire|logs]"
        exit 1
        ;;
esac
