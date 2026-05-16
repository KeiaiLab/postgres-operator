#!/usr/bin/env bash
# sysbench OLTP wrapper — G5 benchmark skeleton. env-driven, graceful skip.
# Modes: oltp_read_write / oltp_read_only / oltp_point_select / etc.
# Refs: ROADMAP G5 §183 / docs/perf/baseline.md / docs/sharding/SHARDING.md.
set -euo pipefail

SYSBENCH_HOST="${SYSBENCH_HOST:-127.0.0.1}"
SYSBENCH_PORT="${SYSBENCH_PORT:-5432}"
SYSBENCH_USER="${SYSBENCH_USER:-postgres}"
SYSBENCH_PASSWORD="${SYSBENCH_PASSWORD:-}"
SYSBENCH_DB="${SYSBENCH_DB:-sbtest}"
SYSBENCH_TABLES="${SYSBENCH_TABLES:-10}"
SYSBENCH_TABLE_SIZE="${SYSBENCH_TABLE_SIZE:-100000}"
SYSBENCH_THREADS="${SYSBENCH_THREADS:-8}"
DURATION_S="${DURATION_S:-60}"
SYSBENCH_MODE="${SYSBENCH_MODE:-oltp_read_write}"
OUTPUT_DIR="${OUTPUT_DIR:-./bench-results}"

log() { printf '[sysbench] %s\n' "$*" >&2; }

if ! command -v sysbench >/dev/null 2>&1; then
  log "WARN: sysbench binary not found — graceful skip (install sysbench)."
  exit 0
fi

mkdir -p "${OUTPUT_DIR}"
ts="$(date -u +%Y%m%dT%H%M%SZ)"
out="${OUTPUT_DIR}/sysbench-${SYSBENCH_MODE}-t${SYSBENCH_THREADS}-${ts}.log"

common_args=(
  --db-driver=pgsql
  --pgsql-host="${SYSBENCH_HOST}"
  --pgsql-port="${SYSBENCH_PORT}"
  --pgsql-user="${SYSBENCH_USER}"
  --pgsql-db="${SYSBENCH_DB}"
  --tables="${SYSBENCH_TABLES}"
  --table-size="${SYSBENCH_TABLE_SIZE}"
  --threads="${SYSBENCH_THREADS}"
)
if [[ -n "${SYSBENCH_PASSWORD}" ]]; then
  common_args+=(--pgsql-password="${SYSBENCH_PASSWORD}")
fi

log "host=${SYSBENCH_HOST}:${SYSBENCH_PORT} db=${SYSBENCH_DB}"
log "mode=${SYSBENCH_MODE} threads=${SYSBENCH_THREADS} tables=${SYSBENCH_TABLES} size=${SYSBENCH_TABLE_SIZE}"
log "output=${out}"

if [[ "${SYSBENCH_SKIP_PREPARE:-0}" != "1" ]]; then
  log "prepare"
  sysbench "${SYSBENCH_MODE}" "${common_args[@]}" prepare 2>&1 | tee -a "${out}"
fi

log "run: --time=${DURATION_S}"
sysbench "${SYSBENCH_MODE}" "${common_args[@]}" \
  --time="${DURATION_S}" \
  --report-interval=10 \
  run 2>&1 | tee -a "${out}"

if [[ "${SYSBENCH_KEEP_DATA:-0}" != "1" ]]; then
  log "cleanup"
  sysbench "${SYSBENCH_MODE}" "${common_args[@]}" cleanup 2>&1 | tee -a "${out}" || true
fi

log "DONE: results saved to ${out}"
