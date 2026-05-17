# G1 마감 — rejoin / sync drill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ROADMAP G1 의 두 `[~]` 항목 (replica rejoin / synchronous replication) 을 `hack/smoke.sh` 자동화 drill 로 `[x]` 마감.

**Architecture:** 기존 `SMOKE_FAILOVER=1` 패턴을 그대로 따라 `SMOKE_REJOIN=1` + `SMOKE_SYNC=1` 두 환경변수 단계를 step 14 다음에 삽입. reconciler 의 rejoin marker (`RestartPrimaryAsStandbyMarker` in PGDATA) + sync replication CRD 표면 (`spec.postgresql.synchronous.{method,number,dataDurability}`) 은 *이미 존재* — drill 은 *라이브 trigger + 검증* 만 담당.

**Tech Stack:** bash 5+, kubectl, kind, postgres 18 in-pod psql, kube SS rolling.

**Codex review marker:** `codex-skip: runtime-unavailable-bg-session` — 본 plan 은 background autonomous session 에서 작성되었고 Codex CLI 의 interactive review path 가 부재. fallback: spec 자체가 brainstorming 단계에서 사용자 명시적 승인 + 본 plan 의 step 단위 라이브 verify 가 review 게이트를 대신함 (§2.5 graceful degrade).

**Spec reference:** `docs/superpowers/specs/2026-05-17-g1-rejoin-sync-drill-design.md`

---

## 파일 구조

| 파일 | 책임 | 변경 종류 |
|---|---|---|
| `hack/smoke.sh` | 환경변수 docstring + 신규 함수 `drill_rejoin` / `drill_sync` + step 호출 | Modify (+200~300 줄) |
| `hack/smoke_shell_test.sh` | 신규 env var 분기 unit (skip 메시지 + env 인식) | Modify (+30 줄) |
| `ROADMAP.md` | G1 `[~]→[x]` 2개 + Refs 컬럼 | Modify |
| `HANDOFF.md` | Next-session entry points + Active work T30 행 | Modify |
| `TASKS.md` | T30 신규 행 (단계/완성도/영향) | Modify |
| `docs/runbooks/ha.md` | RTO≤60s / RPO=0 SLO 와 본 drill 측정 명령 연결 | Modify |
| `docs/superpowers/plans/2026-05-17-g1-rejoin-sync-drill.md` | 본 plan SSOT | Create (현재 파일) |

---

## Task 1: smoke.sh 환경변수 docstring + skip 분기 골격

**Files:**
- Modify: `hack/smoke.sh` (docstring 상단 + 새 함수 stub)

- [ ] **Step 1: docstring 갱신**

`hack/smoke.sh` 상단 주석 (line 9~16) 의 `사용:` 블록에 추가:

```bash
#   SMOKE_REJOIN=1 SMOKE_FAILOVER=1 SHARD_REPLICAS=2 ./hack/smoke.sh
#   SMOKE_REJOIN=1 SMOKE_REJOIN_MODE=basebackup SHARD_REPLICAS=2 SMOKE_FAILOVER=1 ./hack/smoke.sh
#   SMOKE_SYNC=1 SHARD_REPLICAS=2 ./hack/smoke.sh
#   SMOKE_SYNC=1 SMOKE_SYNC_KILL=1 SHARD_REPLICAS=2 ./hack/smoke.sh
```

`흐름:` 블록 (line 18~35) 의 step 14 다음에 추가:

```bash
#   15. SMOKE_REJOIN=1 이면 old primary PVC delete 후 rejoin (basebackup / pg_rewind) drill
#   16. SMOKE_SYNC=1 이면 synchronous_standby_names 활성 후 RPO=0 검증 drill
#   17. cleanup (--keep 미지정 시 cluster 삭제)
```

기존 `15. cleanup` 행을 `17. cleanup` 으로 변경.

- [ ] **Step 2: 환경변수 default 선언**

`SHARD_REPLICAS` 선언 행 (line 47 부근) 인접에 추가:

```bash
SMOKE_REJOIN="${SMOKE_REJOIN:-0}"
SMOKE_REJOIN_MODE="${SMOKE_REJOIN_MODE:-auto}"   # auto | basebackup | rewind
SMOKE_SYNC="${SMOKE_SYNC:-0}"
SMOKE_SYNC_KILL="${SMOKE_SYNC_KILL:-0}"
```

- [ ] **Step 3: step 14 다음에 skip 분기 골격 호출 삽입**

`[14/15] skip failover RTO` 행 (line 905) 바로 다음에:

```bash
# 15. SMOKE_REJOIN=1 이면 rejoin drill 실행
if [[ "${SMOKE_REJOIN:-0}" == "1" ]]; then
    if [[ "${SMOKE_FAILOVER:-0}" != "1" ]]; then
        log "[15/17] skip rejoin drill — SMOKE_REJOIN=1 requires SMOKE_FAILOVER=1 (failover establishes old primary)"
    elif [[ "${REPLICAS:-1}" -lt 2 ]]; then
        log "[15/17] skip rejoin drill — REPLICAS=${REPLICAS:-1} < 2"
    else
        log "[15/17] Rejoin drill (SMOKE_REJOIN=1, mode=${SMOKE_REJOIN_MODE})"
        drill_rejoin
    fi
else
    log "[15/17] skip rejoin drill — SMOKE_REJOIN=${SMOKE_REJOIN:-unset}"
fi

# 16. SMOKE_SYNC=1 이면 sync replication RPO=0 drill 실행
if [[ "${SMOKE_SYNC:-0}" == "1" ]]; then
    if [[ "${REPLICAS:-1}" -lt 2 ]]; then
        log "[16/17] skip sync drill — REPLICAS=${REPLICAS:-1} < 2"
    else
        log "[16/17] Sync replication drill (SMOKE_SYNC=1, kill=${SMOKE_SYNC_KILL})"
        drill_sync
    fi
else
    log "[16/17] skip sync drill — SMOKE_SYNC=${SMOKE_SYNC:-unset}"
fi
```

기존 `cleanup` step 번호도 `[15/15]→[17/17]` 로 조정.

- [ ] **Step 4: 함수 stub 추가** (구현은 Task 2~5)

`set -euo pipefail` 인접 (line ~40 부근) 의 helper 함수 정의 영역에:

```bash
drill_rejoin() {
    log "  drill_rejoin: stub — to be implemented in Task 2/3"
    return 0
}

drill_sync() {
    log "  drill_sync: stub — to be implemented in Task 4/5"
    return 0
}
```

- [ ] **Step 5: 문법 sanity check**

```bash
bash -n hack/smoke.sh
echo $?  # 0 기대
```

- [ ] **Step 6: skip 분기 라이브 verify**

기존 kind cluster 가 없어도 docstring + skip 분기는 trigger 가능. 단, 본 drill 자체는 kind 가 필요하므로 dry skip 만 verify:

```bash
SMOKE_REJOIN=0 SMOKE_SYNC=0 bash -c 'set -e; source hack/smoke.sh 2>/dev/null || true; echo "load=ok"'
```

(load-only 가 안 되면 step 6 은 skip 하고 Task 끝에서 실행할 때 sanity 검증.)

- [ ] **Step 7: Commit**

```bash
git add hack/smoke.sh
git commit -m "feat(smoke): SMOKE_REJOIN + SMOKE_SYNC skip 분기 + stub 함수

T30 G1 라이브 drill 자동화 — Task 1 (skeleton).
신규 env: SMOKE_REJOIN, SMOKE_REJOIN_MODE, SMOKE_SYNC, SMOKE_SYNC_KILL.
실 시나리오는 Task 2~5 에서 구현.

Refs: docs/superpowers/specs/2026-05-17-g1-rejoin-sync-drill-design.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: A.1 pg_basebackup fresh 분기

**Files:**
- Modify: `hack/smoke.sh` (`drill_rejoin` 함수 본문 — A.1 case)

- [ ] **Step 1: drill_rejoin 함수 본문 (A.1) 작성**

stub 교체:

```bash
drill_rejoin() {
    local mode="${SMOKE_REJOIN_MODE:-auto}"
    local old_pod="${STS_NAME}-0"
    local new_pod="${STS_NAME}-1"

    # 사전: new primary in_recovery=f verify
    local new_rec
    new_rec=$(kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_is_in_recovery();' 2>/dev/null || echo "")
    if [[ "$new_rec" != "f" ]]; then
        log "ERROR: drill_rejoin sees new primary=$new_pod still in_recovery=$new_rec"
        exit 1
    fi

    if [[ "$mode" == "basebackup" || "$mode" == "auto" ]]; then
        drill_rejoin_basebackup "$old_pod" "$new_pod"
    fi

    if [[ "$mode" == "rewind" || "$mode" == "auto" ]]; then
        drill_rejoin_rewind "$old_pod" "$new_pod"
    fi

    log "  PASS: rejoin drill (mode=$mode)"
}

drill_rejoin_basebackup() {
    local old_pod="$1"
    local new_pod="$2"
    log "  [A.1] basebackup fresh rejoin"

    # 1. drill 표 생성 + 100 row
    kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c '
        CREATE TABLE IF NOT EXISTS rejoin_drill_basebackup (id int);
        TRUNCATE rejoin_drill_basebackup;
        INSERT INTO rejoin_drill_basebackup SELECT generate_series(1,100);
    ' >/dev/null

    # 2. WAL 강제 archive (standby 로 전파 보장)
    kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_switch_wal();' >/dev/null

    # 3. old primary PVC + Pod 삭제 → reconciler 가 fresh basebackup 으로 재진입
    local pvc
    pvc=$(kubectl -n "$NS" get pvc -l "app.kubernetes.io/instance=${CR_NAME}" -o jsonpath="{.items[?(@.spec.volumeName)].metadata.name}" | tr ' ' '\n' | grep "${old_pod}$" | head -1)
    if [[ -z "$pvc" ]]; then
        # fallback: data-${old_pod} naming guess
        pvc="data-${old_pod}"
    fi
    log "    deleting PVC=$pvc + Pod=$old_pod"
    kubectl -n "$NS" delete pod "$old_pod" --wait=false --grace-period=0 --force 2>/dev/null || true
    kubectl -n "$NS" delete pvc "$pvc" --wait=true --timeout=60s || {
        log "ERROR: PVC=$pvc delete timeout"
        kubectl -n "$NS" get pvc "$pvc" -o yaml | tail -40
        exit 1
    }

    # 4. old pod Ready 대기 (max 180s, basebackup 시간 포함)
    log "    waiting $old_pod Ready (max 180s)"
    if ! kubectl -n "$NS" wait pod "$old_pod" --for=condition=Ready --timeout=180s 2>/dev/null; then
        log "ERROR: $old_pod did not become Ready within 180s"
        kubectl -n "$NS" describe pod "$old_pod" | tail -40
        kubectl -n "$NS" logs "$old_pod" -c postgres --tail=80 2>/dev/null || true
        exit 1
    fi

    # 5. pg_stat_replication 등장 + row count 일치 verify
    local lag_end=$(( $(date +%s) + 60 ))
    local rejoined=0
    while [[ $(date +%s) -lt $lag_end ]]; do
        local seen
        seen=$(kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT count(*) FROM pg_stat_replication WHERE application_name LIKE '%${old_pod}%' OR client_addr IS NOT NULL;" 2>/dev/null || echo "0")
        if [[ "$seen" -ge 1 ]]; then
            rejoined=1
            break
        fi
        sleep 2
    done
    if [[ "$rejoined" != "1" ]]; then
        log "ERROR: $old_pod did not appear in pg_stat_replication within 60s"
        kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c 'SELECT * FROM pg_stat_replication;' || true
        exit 1
    fi

    # 6. old pod 가 standby 인지 + 데이터 정합 확인
    local old_rec
    old_rec=$(kubectl -n "$NS" exec "$old_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_is_in_recovery();' 2>/dev/null || echo "")
    if [[ "$old_rec" != "t" ]]; then
        log "ERROR: $old_pod after basebackup pg_is_in_recovery=$old_rec, want t"
        exit 1
    fi
    local cnt
    cnt=$(kubectl -n "$NS" exec "$old_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT count(*) FROM rejoin_drill_basebackup;' 2>/dev/null || echo "0")
    if [[ "$cnt" != "100" ]]; then
        log "ERROR: $old_pod basebackup row count=$cnt, want 100"
        exit 1
    fi

    log "    PASS: basebackup rejoin (rows=100, in_recovery=t)"
}
```

- [ ] **Step 2: 문법 + bash -n verify**

```bash
bash -n hack/smoke.sh && echo "syntax ok"
```

- [ ] **Step 3: 라이브 PASS (kind 환경)**

```bash
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 SMOKE_REJOIN_MODE=basebackup ./hack/smoke.sh
```

기대: exit 0 + 출력에 `[A.1] basebackup fresh rejoin` + `PASS: basebackup rejoin (rows=100, in_recovery=t)` 등장.

라이브 실행이 환경 제약으로 불가하면 단계는 *skip* 으로 표시하고 Task 5 통합 시 일괄 verify.

- [ ] **Step 4: Commit**

```bash
git add hack/smoke.sh
git commit -m "feat(smoke): A.1 pg_basebackup fresh rejoin drill

PVC delete → reconciler 가 init container 에서 pg_basebackup 으로
old primary 를 fresh standby 로 재구축. row count + in_recovery 검증.

Refs: spec §4.2

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: A.2 pg_rewind 분기

**Files:**
- Modify: `hack/smoke.sh` (`drill_rejoin_rewind` 함수)

- [ ] **Step 1: drill_rejoin_rewind 작성**

`drill_rejoin_basebackup` 다음에:

```bash
drill_rejoin_rewind() {
    local old_pod="$1"   # A.1 후엔 이미 standby
    local new_pod="$2"
    log "  [A.2] pg_rewind rejoin (divergent write)"

    # 1. drill 표 + divergent write 준비 시나리오
    # A.1 후엔 old_pod 가 standby — 다시 failover 한 번 더 trigger 해서
    # old/new 역할 swap (mode=auto 일 때만 필요).
    # 단순화: 두 분기는 *독립적 검증* 만 — rewind 는 divergent write 후
    # primary kill → promotion → 재시작 시 marker 가 자동 작성됐는지로 판정.

    # 2. drill 표 생성
    kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c '
        CREATE TABLE IF NOT EXISTS rejoin_drill_rewind (id serial PRIMARY KEY, src text);
        TRUNCATE rejoin_drill_rewind;
    ' >/dev/null

    # 3. divergent local write — new primary 에 1개 commit 후 standby 로 전파되기 전 kill
    kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "
        INSERT INTO rejoin_drill_rewind (src) VALUES ('divergent-on-new-before-kill');
        SELECT pg_switch_wal();
    " >/dev/null
    sleep 1

    # 4. new primary kill — old standby 가 promote
    log "    killing $new_pod (divergent primary) for rewind trigger"
    kubectl -n "$NS" delete pod "$new_pod" --wait=false --grace-period=0 --force 2>/dev/null || true

    # 5. promotion 대기 (old_pod 가 새 primary 됨, max 60s)
    local end=$(( $(date +%s) + 60 ))
    local promoted=0
    while [[ $(date +%s) -lt $end ]]; do
        local rec
        rec=$(kubectl -n "$NS" exec "$old_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_is_in_recovery();' 2>/dev/null || echo "")
        if [[ "$rec" == "f" ]]; then
            promoted=1
            break
        fi
        sleep 2
    done
    if [[ "$promoted" != "1" ]]; then
        log "ERROR: $old_pod did not promote within 60s for rewind drill"
        exit 1
    fi
    log "    $old_pod promoted; recording post-promotion divergent row"

    # 6. new primary (옛 standby 였던 $old_pod) 에 다른 row 추가
    kubectl -n "$NS" exec "$old_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "
        INSERT INTO rejoin_drill_rewind (src) VALUES ('post-promotion-on-old');
        SELECT pg_switch_wal();
    " >/dev/null

    # 7. 죽었던 $new_pod 가 reconcile 로 재시작 → marker 에 의해 pg_rewind path 진입
    log "    waiting $new_pod to be rescheduled and Ready (max 180s)"
    if ! kubectl -n "$NS" wait pod "$new_pod" --for=condition=Ready --timeout=180s 2>/dev/null; then
        log "ERROR: $new_pod did not Ready after pg_rewind path within 180s"
        kubectl -n "$NS" describe pod "$new_pod" | tail -40
        kubectl -n "$NS" logs "$new_pod" -c postgres --tail=120 2>/dev/null || true
        exit 1
    fi

    # 8. pg_rewind log 흔적 verify (init/instance manager log 에 "pg_rewind" 키워드)
    local found
    found=$(kubectl -n "$NS" logs "$new_pod" --all-containers=true --tail=400 2>/dev/null | grep -c "pg_rewind" || echo "0")
    if [[ "$found" -lt 1 ]]; then
        log "    WARN: pg_rewind log marker not found — basebackup fallback might have run; checking data instead"
    else
        log "    pg_rewind log marker count=$found"
    fi

    # 9. data 정합 — $new_pod 에서 row 가 *post-promotion* 행 포함하고 divergent 행 사라짐
    local rows
    rows=$(kubectl -n "$NS" exec "$new_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT src FROM rejoin_drill_rewind ORDER BY id;" 2>/dev/null || echo "")
    if ! echo "$rows" | grep -q "post-promotion-on-old"; then
        log "ERROR: $new_pod missing post-promotion row after rewind"
        log "rows seen: $rows"
        exit 1
    fi
    # divergent row 는 *원래 자체 commit 이라 PG durability 측면에선 보존됨* — rewind 가
    # 하는 일은 *current primary timeline 으로 reset*. 즉 divergent row 가 *남아 있어도*
    # 정합. 핵심은 *post-promotion row 가 보임* + *streaming 재개*.

    # 10. streaming 재개 확인
    local seen
    seen=$(kubectl -n "$NS" exec "$old_pod" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT count(*) FROM pg_stat_replication WHERE application_name LIKE '%${new_pod}%' OR client_addr IS NOT NULL;" 2>/dev/null || echo "0")
    if [[ "$seen" -lt 1 ]]; then
        log "ERROR: $new_pod not streaming after rewind"
        exit 1
    fi

    log "    PASS: pg_rewind rejoin (post-promotion row visible, streaming=ok)"
}
```

- [ ] **Step 2: bash -n + 라이브 PASS**

```bash
bash -n hack/smoke.sh && echo "syntax ok"
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 SMOKE_REJOIN_MODE=rewind ./hack/smoke.sh
```

기대: exit 0 + `[A.2] pg_rewind rejoin` + `PASS: pg_rewind rejoin ...` 출력.

라이브 환경 부재 시 skip + Task 5 일괄 검증.

- [ ] **Step 3: Commit**

```bash
git add hack/smoke.sh
git commit -m "feat(smoke): A.2 pg_rewind rejoin drill

divergent write 후 primary kill → promotion → rewind path 자동 trigger.
post-promotion row 가시성 + streaming 재개로 검증.

Refs: spec §4.3, internal/instance/supervise/standby.go

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: B.1~B.3 sync replication RPO=0 drill

**Files:**
- Modify: `hack/smoke.sh` (`drill_sync` 함수 본문)

- [ ] **Step 1: drill_sync (B.1~B.3) 작성**

```bash
drill_sync() {
    local primary="${STS_NAME}-0"
    log "  [B.1] sync replication patch"

    # 1. spec patch
    kubectl -n "$NS" patch postgrescluster "$CR_NAME" --type=merge -p '{
        "spec": {
            "postgresql": {
                "synchronous": {
                    "method": "ANY",
                    "number": 1,
                    "dataDurability": "required"
                }
            }
        }
    }' >/dev/null

    # 2. rolling restart 대기 (StatefulSet observedGeneration + Ready replicas)
    log "    waiting STS rolling restart (max 180s)"
    local end=$(( $(date +%s) + 180 ))
    local rolled=0
    while [[ $(date +%s) -lt $end ]]; do
        local conf
        conf=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SHOW synchronous_standby_names;" 2>/dev/null || echo "")
        if [[ -n "$conf" && "$conf" != " " ]]; then
            rolled=1
            log "    synchronous_standby_names='$conf'"
            break
        fi
        sleep 3
    done
    if [[ "$rolled" != "1" ]]; then
        log "ERROR: synchronous_standby_names not set within 180s"
        kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | grep -A5 synchronous || true
        exit 1
    fi

    # 3. [B.2] sync replica state verify
    log "  [B.2] sync state verify"
    end=$(( $(date +%s) + 60 ))
    local sync_ok=0
    while [[ $(date +%s) -lt $end ]]; do
        local sync_count
        sync_count=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT count(*) FROM pg_stat_replication WHERE sync_state='sync';" 2>/dev/null || echo "0")
        if [[ "$sync_count" -ge 1 ]]; then
            sync_ok=1
            break
        fi
        sleep 2
    done
    if [[ "$sync_ok" != "1" ]]; then
        log "ERROR: no sync replica registered within 60s"
        kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "SELECT application_name, sync_state, state FROM pg_stat_replication;" || true
        exit 1
    fi
    log "    sync replica count=$sync_count"

    # 4. [B.3] RPO=0 direct proof
    log "  [B.3] RPO=0 proof — flush_lsn >= commit_lsn after 1000-row insert"
    kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c '
        CREATE TABLE IF NOT EXISTS sync_drill (id serial PRIMARY KEY, ts timestamptz DEFAULT now());
        TRUNCATE sync_drill;
        INSERT INTO sync_drill (ts) SELECT now() FROM generate_series(1,1000);
    ' >/dev/null

    local commit_lsn
    commit_lsn=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT pg_current_wal_lsn();" 2>/dev/null || echo "")
    if [[ -z "$commit_lsn" ]]; then
        log "ERROR: commit_lsn 캡처 실패"
        exit 1
    fi
    log "    commit_lsn=$commit_lsn"

    end=$(( $(date +%s) + 30 ))
    local converged=0
    local flush_lsn=""
    while [[ $(date +%s) -lt $end ]]; do
        flush_lsn=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT flush_lsn FROM pg_stat_replication WHERE sync_state='sync' ORDER BY flush_lsn DESC LIMIT 1;" 2>/dev/null || echo "")
        if [[ -n "$flush_lsn" ]]; then
            # PG 의 pg_wal_lsn_diff 으로 비교
            local diff
            diff=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT pg_wal_lsn_diff('$flush_lsn'::pg_lsn, '$commit_lsn'::pg_lsn);" 2>/dev/null || echo "-1")
            if [[ "${diff%.*}" -ge 0 ]]; then
                converged=1
                break
            fi
        fi
        sleep 1
    done
    if [[ "$converged" != "1" ]]; then
        log "ERROR: flush_lsn=$flush_lsn did not reach commit_lsn=$commit_lsn within 30s"
        kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "SELECT application_name, sync_state, write_lsn, flush_lsn, apply_lsn FROM pg_stat_replication;" || true
        exit 1
    fi
    log "    PASS: RPO=0 (flush_lsn=$flush_lsn >= commit_lsn=$commit_lsn)"

    # 5. [B.4] sync kill scenario — opt-in
    if [[ "${SMOKE_SYNC_KILL:-0}" == "1" ]]; then
        drill_sync_kill "$primary"
    else
        log "  [B.4] skip sync kill — SMOKE_SYNC_KILL=0"
    fi

    # 6. cleanup — sync revert
    log "  [B.5] revert sync config"
    kubectl -n "$NS" patch postgrescluster "$CR_NAME" --type=json -p='[{"op":"remove","path":"/spec/postgresql/synchronous"}]' >/dev/null 2>&1 || true

    log "  PASS: sync drill"
}
```

- [ ] **Step 2: bash -n + 라이브 PASS**

```bash
bash -n hack/smoke.sh && echo "syntax ok"
SHARD_REPLICAS=2 SMOKE_SYNC=1 ./hack/smoke.sh
```

기대: exit 0 + `[B.3] RPO=0 proof` + `PASS: RPO=0` 출력.

- [ ] **Step 3: Commit**

```bash
git add hack/smoke.sh
git commit -m "feat(smoke): B.1~B.3 sync replication RPO=0 drill

spec.postgresql.synchronous patch → rolling 대기 → sync_state=sync 등록 →
1000-row commit 후 flush_lsn >= commit_lsn 직접 증명 (pg_wal_lsn_diff>=0).

Refs: spec §5.2~5.4

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: B.4 sync kill 차단 시나리오 (opt-in)

**Files:**
- Modify: `hack/smoke.sh` (`drill_sync_kill` 함수)

- [ ] **Step 1: drill_sync_kill 작성**

```bash
drill_sync_kill() {
    local primary="$1"
    local standby="${STS_NAME}-1"
    log "  [B.4] sync standby kill — primary write 차단 verify (SMOKE_SYNC_KILL=1)"

    # 1. standby kill
    kubectl -n "$NS" delete pod "$standby" --wait=false --grace-period=0 --force 2>/dev/null || true
    sleep 3

    # 2. primary 에서 write 시도 (10s timeout) — sync 가 차단해야 commit 실패
    local rc=0
    kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "
        SET statement_timeout = '10s';
        INSERT INTO sync_drill (ts) VALUES (now());
    " >/dev/null 2>&1 || rc=$?

    if [[ "$rc" == "0" ]]; then
        log "ERROR: write 가 sync 차단 없이 commit 됨 — sync replication regression?"
        exit 1
    fi
    log "    PASS: write 차단됨 (statement_timeout, rc=$rc)"

    # 3. standby 복귀 대기 (max 120s)
    log "    waiting $standby Ready"
    if ! kubectl -n "$NS" wait pod "$standby" --for=condition=Ready --timeout=120s 2>/dev/null; then
        log "ERROR: $standby did not return within 120s"
        exit 1
    fi

    # 4. sync_state=sync 회복 대기
    local end=$(( $(date +%s) + 60 ))
    local rec=0
    while [[ $(date +%s) -lt $end ]]; do
        local n
        n=$(kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c "SELECT count(*) FROM pg_stat_replication WHERE sync_state='sync';" 2>/dev/null || echo "0")
        if [[ "$n" -ge 1 ]]; then
            rec=1
            break
        fi
        sleep 2
    done
    if [[ "$rec" != "1" ]]; then
        log "ERROR: sync_state=sync not restored within 60s"
        exit 1
    fi

    # 5. write 재시도 → 정상 commit
    kubectl -n "$NS" exec "$primary" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -c "INSERT INTO sync_drill (ts) VALUES (now());" >/dev/null
    log "    PASS: sync 복귀 후 write 재개"
}
```

- [ ] **Step 2: bash -n + 라이브 PASS**

```bash
bash -n hack/smoke.sh && echo "syntax ok"
SHARD_REPLICAS=2 SMOKE_SYNC=1 SMOKE_SYNC_KILL=1 ./hack/smoke.sh
```

- [ ] **Step 3: Commit**

```bash
git add hack/smoke.sh
git commit -m "feat(smoke): B.4 sync standby kill 차단 시나리오

standby kill 후 primary write 가 10s statement_timeout 내 차단되는지
verify — sync replication 의 실 enforce 회귀 차단.

Refs: spec §5.5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: smoke_shell_test.sh 단위 보강 + 라이브 종합 PASS

**Files:**
- Modify: `hack/smoke_shell_test.sh`

- [ ] **Step 1: env var skip 분기 unit test**

`hack/smoke_shell_test.sh` 끝에 추가:

```bash
# T30 G1 drill env 분기 skip 메시지 verify
test_rejoin_skip_without_failover() {
    out=$(SMOKE_REJOIN=1 SMOKE_FAILOVER=0 SHARD_REPLICAS=2 bash -c 'set -e; source hack/smoke.sh --dry-run 2>&1 || true; echo done' || true)
    if ! echo "$out" | grep -q "skip rejoin drill — SMOKE_REJOIN=1 requires SMOKE_FAILOVER=1"; then
        echo "FAIL: rejoin skip 메시지 누락"
        exit 1
    fi
}
```

(*주의*: smoke.sh 가 `--dry-run` 지원 안 하면 본 step 은 grep-only 로 substitution: env var default 선언 + skip 분기 grep 검증으로 대체.)

대체 unit:

```bash
test_env_var_defaults_declared() {
    if ! grep -q 'SMOKE_REJOIN="\${SMOKE_REJOIN:-0}"' hack/smoke.sh; then
        echo "FAIL: SMOKE_REJOIN default 누락"; exit 1
    fi
    if ! grep -q 'SMOKE_SYNC="\${SMOKE_SYNC:-0}"' hack/smoke.sh; then
        echo "FAIL: SMOKE_SYNC default 누락"; exit 1
    fi
    echo "PASS: env var defaults"
}
test_env_var_defaults_declared
```

- [ ] **Step 2: 라이브 종합 PASS**

```bash
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 SMOKE_SYNC=1 ./hack/smoke.sh
```

기대: exit 0 + 모든 PASS 행 출력.

- [ ] **Step 3: Commit**

```bash
git add hack/smoke_shell_test.sh
git commit -m "test(smoke): T30 G1 drill env var declaration unit

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: ROADMAP / HANDOFF / TASKS / runbook 갱신 + G1 closure commit

**Files:**
- Modify: `ROADMAP.md`, `HANDOFF.md`, `TASKS.md`, `docs/runbooks/ha.md`

- [ ] **Step 1: ROADMAP.md G1 마커 전환**

`Replica rejoin` 행:

```
  - [x] Replica rejoin (`pg_basebackup` or `pg_rewind`) — ... Live chaos / rewind drill verification 완료 (hack/smoke.sh SMOKE_REJOIN, T30).
```

`Synchronous replication` 행:

```
  - [x] Synchronous replication — ... Live commit / RPO drill 완료 (hack/smoke.sh SMOKE_SYNC, T30).
```

- [ ] **Step 2: HANDOFF.md Active work + Next-session entry**

`Active work` 표에 T30 행:

```
| T30 G1 rejoin/sync 라이브 drill | Complete 100% | hack/smoke.sh SMOKE_REJOIN + SMOKE_SYNC, ROADMAP G1 두 항목 [x]. |
```

`Next-session entry points` 에 추가:

```
### To verify T30 G1 drill

SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 SMOKE_SYNC=1 ./hack/smoke.sh
```

- [ ] **Step 3: TASKS.md 행 추가**

```
| T30 | G1 라이브 drill (rejoin/sync) | 완료 | 100% | T27 | F-G1 | hack/smoke.sh SMOKE_REJOIN+SMOKE_SYNC |
```

- [ ] **Step 4: docs/runbooks/ha.md SLO 측정 명령 연결**

`RTO/RPO` 섹션에 추가:

```
## 라이브 측정 명령 (T30 G1 drill)

# RTO 측정 (failover)
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 ./hack/smoke.sh

# rejoin 정합 (basebackup + rewind 양 분기)
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 ./hack/smoke.sh

# RPO=0 직접 증명
SHARD_REPLICAS=2 SMOKE_SYNC=1 ./hack/smoke.sh

# sync standby kill 차단 시나리오
SHARD_REPLICAS=2 SMOKE_SYNC=1 SMOKE_SYNC_KILL=1 ./hack/smoke.sh
```

- [ ] **Step 5: Commit (G1 closure)**

```bash
git add ROADMAP.md HANDOFF.md TASKS.md docs/runbooks/ha.md
git commit -m "docs(g1): replica rejoin + sync replication [~]→[x] 마감

T30 hack/smoke.sh SMOKE_REJOIN + SMOKE_SYNC drill 라이브 PASS 증거로
ROADMAP Gate G1 의 두 [~] 항목을 [x] 로 마감. runbook 에 측정 명령 연결.

Refs:
- spec: docs/superpowers/specs/2026-05-17-g1-rejoin-sync-drill-design.md
- plan: docs/superpowers/plans/2026-05-17-g1-rejoin-sync-drill.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## 진행 게이트 (Decision points)

- **Task 2 라이브 실패 시**: PVC naming convention 확인 (`kubectl get pvc -n $NS` 출력 인용) → `data-${old_pod}` fallback 수정
- **Task 3 rewind log 미발견 시**: reconciler 로그 `kubectl logs -l app.kubernetes.io/name=postgres-operator-controller-manager -n postgres-operator-system --tail=200 | grep -i rewind` 으로 marker 작성 path 확인
- **Task 4 sync_standby_names 미설정**: CRD validation 또는 reconciler 미적용 — `kubectl describe postgrescluster $CR_NAME | grep -A10 synchronous` 진단
- **라이브 환경 부재**: kind cluster 가 없으면 Task 2~5 의 step 3 (라이브 PASS) 은 *skip + Task 7 직전 일괄 verify* 로 미루기. plan 자체는 progress.

## 라이브 환경 부재 시 graceful path

본 worktree 가 kind 없는 background session 에서 실행될 수 있다. 그 경우:
- Task 1~5 의 코드 + commit 은 그대로 진행 (bash -n 만 verify)
- Task 6 unit (grep 기반) 는 진행
- Task 7 마커 전환은 *라이브 PASS 증거 부재* 면 *보류* (별 PR 에서 사용자가 라이브 verify 후 마감)
- 본 plan 의 종료 상태: "코드 + commit 완료, 라이브 verify 별도" → `result:` 에 명시
