#!/usr/bin/env bash
# pgbench wrapper — G5 benchmark skeleton. env-driven, graceful skip.
# Refs: ROADMAP G5 §183 / docs/perf/baseline.md / ADR-0015.
set -euo pipefail

PGBENCH_HOST="${PGBENCH_HOST:-127.0.0.1}"
PGBENCH_PORT="${PGBENCH_PORT:-5432}"
PGBENCH_USER="${PGBENCH_USER:-postgres}"
PGBENCH_DB="${PGBENCH_DB:-postgres}"
PGBENCH_SCALE="${PGBENCH_SCALE:-10}"
PGBENCH_CLIENTS="${PGBENCH_CLIENTS:-10}"
PGBENCH_THREADS="${PGBENCH_THREADS:-4}"
DURATION_S="${DURATION_S:-60}"
PGBENCH_MODE="${PGBENCH_MODE:-tpcb-like}"   # tpcb-like | select-only | simple-update
OUTPUT_DIR="${OUTPUT_DIR:-./bench-results}"

log() { printf '[pgbench] %s\n' "$*" >&2; }

if ! command -v pgbench >/dev/null 2>&1; then
  log "WARN: pgbench binary not found — graceful skip (install postgresql-client)."
  exit 0
fi

mkdir -p "${OUTPUT_DIR}"
ts="$(date -u +%Y%m%dT%H%M%SZ)"
out="${OUTPUT_DIR}/pgbench-${PGBENCH_MODE}-s${PGBENCH_SCALE}-c${PGBENCH_CLIENTS}-${ts}.log"

log "host=${PGBENCH_HOST}:${PGBENCH_PORT} db=${PGBENCH_DB} scale=${PGBENCH_SCALE}"
log "mode=${PGBENCH_MODE} clients=${PGBENCH_CLIENTS} threads=${PGBENCH_THREADS} duration=${DURATION_S}s"
log "output=${out}"

# init (idempotent: -i 는 기존 데이터 drop+recreate)
if [[ "${PGBENCH_SKIP_INIT:-0}" != "1" ]]; then
  log "init: pgbench -i -s ${PGBENCH_SCALE}"
  pgbench -h "${PGBENCH_HOST}" -p "${PGBENCH_PORT}" -U "${PGBENCH_USER}" \
    -i -s "${PGBENCH_SCALE}" "${PGBENCH_DB}" 2>&1 | tee -a "${out}"
fi

# run
mode_flag=""
case "${PGBENCH_MODE}" in
  select-only) mode_flag="-S" ;;
  simple-update) mode_flag="-N" ;;
  tpcb-like) mode_flag="" ;;
  *) log "ERROR: unknown PGBENCH_MODE=${PGBENCH_MODE}"; exit 2 ;;
esac

log "run: pgbench ${mode_flag} -c ${PGBENCH_CLIENTS} -j ${PGBENCH_THREADS} -T ${DURATION_S}"
pgbench -h "${PGBENCH_HOST}" -p "${PGBENCH_PORT}" -U "${PGBENCH_USER}" \
  ${mode_flag} \
  -c "${PGBENCH_CLIENTS}" -j "${PGBENCH_THREADS}" -T "${DURATION_S}" \
  --progress=10 \
  "${PGBENCH_DB}" 2>&1 | tee -a "${out}"

log "DONE: results saved to ${out}"
