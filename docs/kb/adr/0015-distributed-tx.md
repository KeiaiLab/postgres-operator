# ADR-0015: 분산 트랜잭션 — 2PC primary + saga deferred

- Date: 2026-05-16
- Status: Accepted
- Authors: @phil

## Context

postgres-operator 의 G5 Gate (분산 SQL) 는 cross-shard write 의 *원자성* 보장을 요구한다. self-built sharding 계층 (RFC-0002 ShardRange / RFC-0004 pg-router) 위에서 다중 shard 에 걸친 트랜잭션은 다음 조건을 만족해야 한다:

- ACID 의 *Atomicity + Consistency* 가 cross-shard 경계를 넘어서도 유지
- 사용자 가시 의미가 PG 단일 노드 트랜잭션과 *동등* (Scenario 2 in RFC-0005 §2.2)
- 코디네이터 장애 시 *blocking PREPARED* 트랜잭션이 무한 잔존하지 않음
- 90% 케이스 (single-shard) 는 *zero overhead* — plain `BEGIN/COMMIT` forwarding

RFC-0005 (Distributed transactions — 2PC + saga model, Draft 2026-05-02) 가 본 결정의 상위 명세이며, 본 ADR 은 *구현 선택* 을 봉인한다. P-D §D.10.2 의 "2PC / saga 선택" gate 를 통과시키며, 후속 sub-task (D.10.1 scatter-gather, D.10.3 isolation matrix, D.10.4 benchmarks) 의 입력이 된다.

## Decision

**Primary 모델 = 2PC** — PG native `PREPARE TRANSACTION` / `COMMIT PREPARED` 위에서 pg-router 가 코디네이터 역할. 다음 핵심 파라미터:

- 코디네이터: pg-router 프로세스. 다중 인스턴스 중 *leader* 1개가 진행 중 분산 tx 의 코디네이터 (operator leader 의 Lease election → D.2.2 통합)
- 트랜잭션 로그: operator leader pod 가 *etcd lease + tx log* 보유. 코디네이터 fail-over 시 in-doubt tx 복구 경로의 sole source.
- 격리수준 GA 범위 (P6): single-shard `{RC, RR, SER}` / distributed `{RC + 2PC}`. distributed SERIALIZABLE 은 v2.0+ 별 RFC.
- in-doubt tx recovery: 신규 코디네이터가 부팅 시 tx log replay → 각 shard `pg_prepared_xacts` 와 reconcile → `COMMIT PREPARED` 또는 `ROLLBACK PREPARED` 결정 후 실행.

**Saga 모델 = deferred opt-in** — 사용자 명시 선언 (`CALL begin_saga(...)`) 한정. 일반 SQL 자동 합성 대상 아님. P6 GA 범위 외, 후속 phase 에서 별 ADR 로 채택.

skeleton 패키지: `internal/tx/` — `Coordinator` 인터페이스 + `TwoPhaseCommit` impl stub + in-doubt recovery hook 인터페이스. 본 ADR 시점 구현은 `ErrNotImplemented` sentinel — 각 phase (Begin/Prepare/Commit/Rollback/Recover) 의 실 구현은 후속 sub-task.

## Consequences

**긍정**:
- PG native `PREPARE TRANSACTION` 사용 → 외부 분산 tx 미들웨어 의존 0 (ADR-0001 self-built 원칙 정합)
- ACID cross-shard 보장 → 사용자 기대 (관계형 의미론) 일치
- single-shard 90% 케이스는 plain forwarding → overhead 0
- 사용자 명시 saga 추가는 후방 호환 (단순 새 SQL command 추가)

**부정 / trade-off**:
- 코디네이터 SPOF → Lease election (D.2.2) + tx log 영속화 의무. *본 ADR 단독으로 production-ready 아님* — D.2.2 통합 후만 GA.
- in-doubt PREPARED tx 가 backend 의 `pg_xact` pressure 유발 가능 → recovery path 의 latency budget 명시 필요 (별 ADR 후보)
- distributed deadlock 감지 부재 → 사용자 timeout 의무 (`statement_timeout`)
- distributed SERIALIZABLE 미지원 → 사용자가 cross-shard RR/SER 시도 시 명확한 error 반환 의무 (D.10.3 isolation matrix)

**후속 작업**:
- D.10.1 scatter-gather: 본 `Coordinator` 인터페이스를 query path 가 호출
- D.10.3 isolation matrix: 본 결정의 GA 범위 표 문서화
- D.10.4 benchmarks: 2PC overhead 측정 (sysbench / pgbench cross-shard 변형)
- D.2.2 Lease election: 코디네이터 fail-over 통합

## Alternatives Considered

### Pure saga (compensating actions only)

**기각**. 임의 SQL 에 대한 compensating action 을 *generic SQL router* 가 자동 합성하는 것은 일반적으로 불가능 (e.g., `UPDATE balance = balance - 100` 의 compensation 은 단순 `+ 100` 이 아닐 수 있음 — 중간에 다른 트랜잭션이 봤다면 의미가 변함). 사용자 정의 compensation 만 가능 → 일반 cross-shard write 의 대안 아님.

### Eventual consistency (no atomicity guarantee)

**기각**. ADR-0001 의 "PG-compatible distributed SQL" 미션과 충돌. 관계형 사용자 기대 (BEGIN/COMMIT 의 의미) 위반. NoSQL 영역으로 분류되는 trade-off — 본 repo 범위 외.

### Single-shard transactions only (block cross-shard writes)

**기각**. 5% 케이스 (cross-shard money transfer / multi-tenant migration) 가 사용자 실 워크로드. 명시적으로 *지원 불가* 라고 선언하는 것은 사용자 가치 표면 축소.

### 3PC (3-phase commit)

**기각**. 코디네이터 SPOF 완화 효과는 *비동기 네트워크 가정* 에서만 유효. PG 환경 (TCP + K8s) 은 동기 가정 — 3PC 의 추가 phase 가 latency 만 증가, blocking 회피 효과 미미. 학술적 매력 > 실용 가치.

## Refs

- RFC-0005 distributed-transactions (Draft) — `docs/rfcs/0005-distributed-transactions.md`
- Plan §D.10.2 — `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md`
- ROADMAP G5 — `ROADMAP.md` L181
- Future integration: D.2.2 (Lease election), D.10.1 (scatter-gather), D.10.3 (isolation matrix), D.10.4 (benchmarks)
