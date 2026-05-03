#!/usr/bin/env bash
# fake-postgres: a stub binary used by supervise package unit tests.
#
# Behavior:
#   - On SIGTERM (smart shutdown) → exit 0 after $TERM_DELAY seconds.
#   - On SIGINT  (immediate)     → exit 0 after $INT_DELAY seconds.
#   - On SIGHUP  (reload)        → reset reload counter, prints "RELOADED" to stderr.
#   - $FAIL_ON_START=1           → exit 1 immediately (Start error path test).
#   - $EXIT_AFTER=<sec>          → self-exit after sleep (ExitCh signal test).

set -uo pipefail

if [ "${FAIL_ON_START:-0}" = "1" ]; then
    printf 'FAKE_POSTGRES: failing on start (FAIL_ON_START=1)\n' >&2
    exit 1
fi

reload_count=0

# trap 등록을 PID 출력 전에 — Start() 직후 Stop()/Reload() 가 SIGTERM/SIGHUP 을
# 보낼 때 trap 이 이미 active 임을 보장 (race 회피).
trap 'printf "FAKE_POSTGRES: SIGTERM\n" >&2; sleep "${TERM_DELAY:-0}"; exit 0' TERM
trap 'printf "FAKE_POSTGRES: SIGINT\n"  >&2; sleep "${INT_DELAY:-0}";  exit 0' INT
trap 'reload_count=$((reload_count+1)); printf "FAKE_POSTGRES: RELOADED (count=%d)\n" "$reload_count" >&2' HUP

printf 'FAKE_POSTGRES_PID=%d\n' "$$" >&2

if [ -n "${EXIT_AFTER:-}" ]; then
    sleep "${EXIT_AFTER}"
    exit 0
fi

# Default: sleep forever waiting for signals.
while true; do
    sleep 1 &
    wait $!
done
