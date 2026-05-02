# RFC-0005: 분산 트랜잭션 — 2PC + saga 모델

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P6 (~v0.9.0)
- Supersedes: 없음 (신규)

## §1 Summary

자체 분산 SQL 의 트랜잭션 모델을 정의한다. **단일 shard transaction** 은 PG `BEGIN/COMMIT` 직접 forwarding 으로 오버헤드 0. **분산 transaction** 은 router 가 coordinator 가 되어 PG 내장 `PREPARE TRANSACTION` 기반 **2PC** 실행. coordinator 장애 대비 *operator leader pod* 가 etcd lease + transaction log 를 보유하여 recovery. 명시적 declaration 시 **saga** 모델 (사용자 정의 compensation hook) 도 지원. 격리 수준은 단일 shard {RC, RR, SER} 모두 / 분산 {RC + 2PC} 만 P6 GA — 분산 SERIALIZABLE 은 v2.0+ 검토.

## §2 Motivation

### §2.1 문제

분산 DB 의 원자성 보장은 **CAP 의 C** 영역. 2PC 는 known good 알고리즘이지만 다음 구현 함정:

- **coordinator SPOF**: coordinator 다운 시 PREPARED txn 영구 lock → backend pg_xact 압박.
- **참여자 실패**: PREPARE 후 응답 timeout → blocking 또는 unsafe abort.
- **recovery**: 새 coordinator 가 어떤 txn 이 prepared 인지 알아야.
- **deadlock**: 분산 lock graph 미가시 → 분산 deadlock detection 부재.
- **격리 수준**: SERIALIZABLE 분산은 SSI (Serializable Snapshot Isolation) 의 분산판 필요. 매우 복잡.

대부분의 OLTP workload 는 **READ COMMITTED + 단일 shard** 로 충분. 분산 txn 은 *드물게 발생*하는 cross-shard 작업 (예: 자금 이체, 멀티-tenant 데이터 마이그레이션) 만 허용해도 99% 케이스 cover.

### §2.2 사용자 시나리오

**시나리오 1: 단일 shard txn (90% 케이스)**
```sql
BEGIN;
  UPDATE users SET balance = balance - 100 WHERE id = 42;
  INSERT INTO transactions (...) VALUES (...);
COMMIT;
```
모든 row 가 동일 shard (`tenant_id` 기준) → router 가 BEGIN/COMMIT 를 직접 forwarding. PG 단일 노드 트랜잭션과 동일 의미론. 오버헤드 0.

**시나리오 2: 분산 2PC (5% 케이스)**
```sql
BEGIN;
  UPDATE accounts SET balance = balance - 100 WHERE id = 1;   -- shard-A
  UPDATE accounts SET balance = balance + 100 WHERE id = 999; -- shard-B
COMMIT;
```
router 가 두 shard 참여 감지 → 2PC. PREPARE 양쪽 → log 기록 → COMMIT PREPARED 양쪽.

**시나리오 3: saga (5% 케이스, 명시 declaration)**
```sql
-- 사용자 정의 saga 함수
CALL begin_saga('order_fulfillment');
CALL saga_step('reserve_inventory', 'release_inventory', $$...$$);
CALL saga_step('charge_card', 'refund_card', $$...$$);
CALL saga_step('ship_order', 'cancel_shipment', $$...$$);
CALL commit_saga();
```
실패 시 router 가 등록된 compensation 을 역순 실행. 비-원자적 (eventual consistency) 이지만 long-running 가능.

### §2.3 비목표

- 분산 SERIALIZABLE — P6 범위 외. v2.0+ 별도 RFC.
- cross-cluster transaction — 영구 미지원.
- 사용자 정의 isolation level — PG 표준만.

## §3 Design / Specification

### §3.1 transaction 분류 결정

router 의 분류 알고리즘:

```
on BEGIN:
  state = TxState{ shards: {}, mode: Pending }

on each statement:
  plan = planner.Plan(stmt)
  if plan.Type == SingleShard:
    state.shards.add(plan.Shard)
  elif plan.Type == Scatter:
    state.shards.addAll(plan.Shards)
  else:
    state.shards.addAll(allShards)   # 광범위 cross-shard

on COMMIT:
  if len(state.shards) == 1:
    forward COMMIT to that shard       # 단일 shard 경로
  elif len(state.shards) > 1:
    execute 2PC with state.shards      # 분산 경로
  else:
    no-op (empty tx)
```

첫 statement 가 단일 shard 라도 두 번째가 다른 shard → 자동 escalation 2PC. application 은 이 사실을 인지 X.

### §3.2 2PC protocol

```
[Phase 1: PREPARE]
  router → shard-A: PREPARE TRANSACTION 'tx-<uuid>-A'
  router → shard-B: PREPARE TRANSACTION 'tx-<uuid>-B'
  (parallel, timeout 5s default)

  [모두 OK]
    operator leader 의 etcd 에 commit log 기록:
      key:   /dtxn/<cluster>/tx-<uuid>
      value: { state: Prepared, shards: [A, B], at: <time> }
      lease: 1h (clean-up 시 자동 만료)

  [하나라도 fail/timeout]
    router → 모든 shard: ROLLBACK PREPARED 'tx-<uuid>-X'
    응답 client: ROLLBACK (40001 retry-able)

[Phase 2: COMMIT]
  router → shard-A: COMMIT PREPARED 'tx-<uuid>-A'
  router → shard-B: COMMIT PREPARED 'tx-<uuid>-B'
  (parallel)

  operator leader 의 etcd 갱신:
      state: Committed, completedAt: <time>

  응답 client: COMMIT
```

**uuid 명명 규칙**: `tx-<cluster>-<routerPodName>-<uuidv4>`. shard 별로 suffix `-<shardName>` 추가하여 PG 의 prepared transaction name 충돌 방지.

### §3.3 transaction log (etcd)

operator leader pod 가 etcd 에 append-only log 기록:

```
key prefix: /dtxn/<cluster>/

key:   /dtxn/foo/tx-<uuid>
value: protobuf-encoded TxRecord {
  uuid: "..."
  router_pod: "foo-router-7d8b9c-x4k2p"
  shards: ["shard-A", "shard-B"]
  state: STATE_PREPARED | STATE_COMMITTED | STATE_ABORTED
  prepared_at: <ts>
  committed_at: <ts>
  decision: COMMIT | ABORT
}
lease: 1h (Committed/Aborted 후 갱신, Prepared 는 짧은 lease 로 leak 방지)
```

operator leader 는 K8s lease (`coordination.k8s.io/v1`) 사용 — election 은 RFC 0003 (election interface) 동결된 구현 활용.

### §3.4 recovery (router crash)

```go
// 새 router pod startup
func (r *Router) recover(ctx context.Context) error {
    // 1. etcd 에서 자기 podName 의 PREPARED txn 조회
    records, err := r.etcd.GetPreparedByRouter(ctx, r.podName)
    // 2. 각 record 별로 결정
    for _, rec := range records {
        if rec.Decision == COMMIT {
            r.commitPrepared(ctx, rec)   // resume commit phase
        } else if rec.Decision == ABORT {
            r.rollbackPrepared(ctx, rec)
        } else {
            // Phase 1 도중 crash — 모든 shard 에 ROLLBACK PREPARED 안전하게 전송
            r.rollbackPrepared(ctx, rec)
        }
    }
    return nil
}
```

router 가 영영 돌아오지 않는 경우 (Pod 삭제) operator leader 의 *garbage collector* 가 1h lease 만료 후 PREPARED 를 ROLLBACK 처리. PG 의 `pg_prepared_xacts` 와 etcd log 를 cross-check.

### §3.5 saga 모델

명시적 declaration. router 가 PG extension 또는 magic SQL function 으로 인식 (P6 구현 결정):

옵션 A — annotation 기반 (preferred):
```sql
/*+ saga(name=order_fulfillment) */ BEGIN;
  /*+ saga_step(forward=$$INSERT INTO orders ...$$,
                compensate=$$DELETE FROM orders WHERE id=...$$) */
  ...
COMMIT;
```

옵션 B — `CALL` 함수 기반 (PG 11+):
```sql
CALL pgr.saga_begin('order_fulfillment');
CALL pgr.saga_step('reserve', $$forward sql$$, $$compensate sql$$);
CALL pgr.saga_commit();
```

**실행 의미**:
- forward step 들은 *순차 commit* (각 step 은 일반 (단일 또는 분산) txn).
- 실패 step 은 등록된 compensation 을 *역순* 실행.
- compensation 자체는 idempotent 책임 *application 측*.
- saga 상태는 etcd `/saga/<cluster>/<saga_id>` 에 기록 — recovery 가능.

**격리**: saga 는 ACID 의 I 를 포기 (eventual consistency). cross-step 사이 다른 transaction 이 row 를 볼 수 있음. application 이 idempotency token / status column 으로 보강.

### §3.6 격리 수준 매트릭스

| 트랜잭션 유형 | READ COMMITTED | REPEATABLE READ | SERIALIZABLE |
|---|---|---|---|
| 단일 shard | ✓ (PG 그대로) | ✓ (PG 그대로) | ✓ (PG 그대로) |
| 분산 2PC | ✓ (P6 GA) | ⚠ (best-effort, anomaly 가능) | ✗ (v2.0+) |
| saga | — (원자성 X, 격리 X) | — | — |

분산 RR 의 anomaly: 한 shard 의 snapshot 이 2PC commit 시점에 align 되지 않을 수 있음. *non-monotonic read* 발생 가능. application 권고: 분산 트랜잭션은 RC 만 사용.

분산 SERIALIZABLE 부재 사유: SSI 의 분산판 (e.g. CockroachDB 패턴) 은 *예측 트래킹 + 분산 abort* 필요. 구현 비용 거대 + BUSL 패턴 회피 차원에서 P6 미포함.

### §3.7 deadlock detection

분산 lock graph 의 cycle 은 *현재 미처리*. PG shard 별 `deadlock_timeout` (default 1s) 으로 single-shard deadlock 만 감지. 분산 deadlock 은:

- 각 shard 의 `lock_timeout` (default 0 → 30s 권장 설정) 으로 *eventual* 해소.
- 발생 시 application 측 retry 로 처리.

장기적으로 (P7+) 분산 wait-for graph 도입 검토.

### §3.8 메트릭 / 관찰성

```
postgresql_dtxn_total{cluster, type=single|distributed|saga, status=commit|abort}  counter
postgresql_dtxn_prepare_duration_seconds{cluster}    histogram
postgresql_dtxn_commit_duration_seconds{cluster}     histogram
postgresql_dtxn_in_flight{cluster, state}             gauge
postgresql_saga_step_duration_seconds{cluster, saga_name, step}  histogram
postgresql_saga_compensation_total{cluster, saga_name}            counter
```

OpenTelemetry: 1 분산 txn = 1 root span + 1 child span per shard + commit/prepare events.

### §3.9 client-side 가이드

application 이 알아야 할 사항:

1. **분산 txn 은 retry 가능** (40001 SQLSTATE). exponential backoff + jitter 권장.
2. **분산 deadlock 은 lock_timeout 으로 해소** — application 은 30s 이상 hang 가정 X.
3. **saga 는 명시 declaration 필요** — 자동 추론 X. 비즈니스 로직 책임.
4. **isolation level**: `SET TRANSACTION ISOLATION LEVEL READ COMMITTED` 권장 (분산 시).

## §4 Drawbacks / Trade-offs

- **2PC 의 blocking nature**: prepare 후 coordinator 다운 시 shard 의 prepared txn 이 lock 보유 → 1h lease 만료 까지 영향. 완화: lease 짧게 (5min) + leader 알림.
- **etcd 부하**: 분산 txn 마다 2 회 etcd write (prepare + commit/abort). 초당 1k 분산 txn 은 etcd 의 write QPS 한계 (3k~5k) 의 ~40%. 초당 10k 분산 txn 은 부적합 → 단일 shard 설계 권장.
- **saga 는 ACID 부재**: 사용자 책임 큼. 잘못 쓰면 data corruption 가능. 완화: 문서 + e2e 예제 강화.
- **분산 SERIALIZABLE 부재**: 일부 application 은 SI 만으로 부족. 본 operator 의 target workload 가 OLTP CRUD 중심임을 명시.

## §5 Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **Spanner-style TrueTime + MVCC** | 시계 동기화 (atomic clock) 필요, K8s 환경 부적합 |
| **CockroachDB SSI 패턴 차용** | BUSL 라이선스 위험, 코드 차용 0 정책 |
| **단일 shard 만 (분산 txn 거부)** | 사용성 ↓, 자금 이체 등 표준 use-case 미지원 |
| **HLC + 분산 commit time** | 복잡도 ↑, P6 범위 초과. v2.0 후보 |
| **외부 dtm 라이브러리 (e.g. dtm-labs/dtm)** | Apache 2.0 호환이지만 Go service 추가 운영 부담, K8s native 정책과 align X |

## §6 Open Questions

1. saga 의 declaration 문법 — annotation 기반 vs CALL 함수 기반 — 둘 중 하나 결정 필요. P6 구현 시 spike 후 결정.
2. PREPARED txn 의 lease 길이 — 5min vs 1h trade-off (짧으면 false abort 위험, 길면 lock 보유 확장).
3. 분산 deadlock detection P7+ 도입 시 알고리즘 — Chandy-Misra-Haas? edge chasing? 별도 RFC.
4. saga 의 nested 지원 — saga 안에 saga? 첫 버전은 *flat 만* 지원, nested 는 v2.0+.

## §7 Implementation Plan

### P6 (~v0.9.0)

#### P6-T1: 단일 shard txn (사실은 P2 시점에 이미 동작, 본 RFC 는 의미론 명문화만)
- [ ] 단일 shard 경로 e2e 테스트 (jepsen-style consistency).

#### P6-T2: 2PC 기본
- [ ] `internal/dtxn/coordinator.go` — 2PC state machine.
- [ ] `internal/dtxn/log/etcd.go` — etcd transaction log.
- [ ] router 의 BEGIN/COMMIT 경로 통합.
- [ ] e2e: 2-shard transfer, coordinator kill (router pod delete) → recovery 후 atomic.

#### P6-T3: recovery
- [ ] router startup recovery (PREPARED txn 처리).
- [ ] operator leader 의 garbage collector (lease 만료 prepared 정리).
- [ ] e2e: chaos-mesh 로 router/leader 동시 kill 시나리오.

#### P6-T4: saga
- [ ] saga DSL 결정 (annotation vs CALL).
- [ ] `internal/dtxn/saga/executor.go` — 순차 forward, 역순 compensate.
- [ ] e2e: 3-step saga, 2 단계 실패 → 1 단계 compensation 정확.

### 검증 명령

```bash
go test ./internal/dtxn/...                          # 단위 (state machine)
go test ./internal/dtxn/log/etcd/...                 # etcd integration (testcontainers)
make test-e2e PILLAR=p6 -- --focus="2PC"
make test-e2e PILLAR=p6 -- --focus="saga"
make test-jepsen PILLAR=p6                           # 일관성 (Linearizability for single, RC for distributed)
make test-chaos PILLAR=p6 -- --kill=router
make test-chaos PILLAR=p6 -- --kill=operator-leader
```

성공 기준:
- 단일 shard: linearizable (PG 단일 노드 의미론 동일).
- 분산 2PC: atomicity 보장 (chaos test 100 회 중 inconsistent 0 건).
- saga: forward 일부 실패 시 compensation 100% 실행 + idempotency 보장.

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §3.4
- PostgreSQL Two-Phase Commit: https://www.postgresql.org/docs/18/sql-prepare-transaction.html
- 2PC original: Gray, Jim. *Notes on Database Operating Systems* (1978)
- Saga: Garcia-Molina & Salem, *Sagas* (SIGMOD 1987)
- Spanner (참조 only): https://research.google/pubs/pub39966/
- CockroachDB SSI (참조 only, 코드 차용 0): https://www.cockroachlabs.com/docs/stable/architecture/transaction-layer.html
- etcd lease: https://etcd.io/docs/v3.5/learning/api/#lease-api
- RFC 0003: ShardSplitJob (cutover 도 일종의 분산 atomic 작업)
- RFC 0004: pg-router (coordinator 위치)
- ADR 0003: License policy (no AGPL/BUSL)
