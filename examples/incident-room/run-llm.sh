#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export INCIDENT_ROOM_VARIANT=llm

exec "${SCRIPT_DIR}/run.sh" "$@"
