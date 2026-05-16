# Isolation Matrix — Cross-Shard Transactions

> postgres-operator G5 Distributed SQL 의 격리수준 (isolation level) × 트랜잭션 scope 매트릭스. *진본 보장* 과 *가짜 보장* 의 경계를 명시.

- 상태: G5 Gate sub-task (D.10.3, ROADMAP L182)
- 정합: ADR-0015 (2PC primary + saga deferred), RFC-0005 (분산 트랜잭션 명세, Draft)
- 범위: P6 GA (single-shard `{RC, RR, SER}` + distributed `{RC + 2PC}`)

## 1. 개요

postgres-operator 는 self-built sharding (RFC-0002 `ShardRange` CRD + RFC-0004 pg-router) 위에서 cross-shard write 의 원자성을 PG native `PREPARE TRANSACTION` 기반 2PC 로 제공한다 (ADR-0015). 그러나 *원자성* 과 *격리* 는 분리된 보장이며, 다음 원칙을 따른다:

- **단일 shard tx**: PG 표준 격리수준이 *그대로* 적용된다.
- **Cross-shard 2PC**: 원자성은 보장되나, *격리* 는 PG predicate locking 의 분산 한계로 인해 SERIALIZABLE 의 일부 anomaly 방지가 *깨진다*.
- **Cross-shard saga (deferred)**: 격리 보장 *없음*. eventual consistency.

본 문서는 application 개발자가 *어떤 isolation 을 신뢰해도 되는가* 를 결정할 수 있도록 anomaly 차단 매트릭스를 노출한다.

## 2. Isolation × Scope 매트릭스

Anomaly 약어: **D**=Dirty Read, **N**=Non-repeatable Read, **P**=Phantom Read, **S**=Serialization Anomaly (write skew 등). `차단` = 발생 방지, `허용` = 발생 가능, `가짜` = 단일 shard 만 차단 / cross-shard 에서는 허용 (의존 금지).

| Isolation Level | 단일 shard | Cross-shard 2PC | Cross-shard saga (deferred) |
|---|---|---|---|
| **READ UNCOMMITTED** [^1] | D 허용 / N 허용 / P 허용 / S 허용 | 동일 (D 차단 — PG 동작) | 격리 없음 |
| **READ COMMITTED** (default) | D 차단 / N 허용 / P 허용 / S 허용 | D 차단 / N 허용 / P 허용 / S 허용 ✅ 진본 | 격리 없음 |
| **REPEATABLE READ** | D 차단 / N 차단 / P 차단 [^2] / S 허용 | D 차단 / N 차단 (shard 내 snapshot) / **P 가짜** (cross-shard) / S 허용 | 격리 없음 |
| **SERIALIZABLE** | D 차단 / N 차단 / P 차단 / S 차단 (SSI) | D 차단 / N 차단 (shard 내) / **P 가짜** / **S 가짜** | 격리 없음 |

[^1]: Postgres 는 READ UNCOMMITTED 를 READ COMMITTED 와 동일하게 처리 (실제 dirty read 불가능). PG docs §13.2.
[^2]: PG REPEATABLE READ 는 SQL 표준보다 강력 — phantom 까지 차단 (snapshot isolation). PG docs §13.2.2.

### 알려진 제약 (가짜 보장의 근거)

- **REPEATABLE READ cross-shard**: 각 shard 의 snapshot 은 *독립적으로* 잡힌다. 코디네이터가 *글로벌 snapshot* 을 발급하지 않는다 (P6 GA 범위 외). 따라서 shard A 의 `SELECT` 와 shard B 의 `SELECT` 사이에 *외부* commit 이 끼어들면 cross-shard 결과는 non-repeatable 또는 phantom 을 노출할 수 있다.
- **SERIALIZABLE cross-shard**: PG SSI (Serializable Snapshot Isolation) 의 predicate locking 은 *단일 노드* 내에서만 유효하다. 다중 shard 의 SIREAD lock 을 종합 판정하는 분산 SSI 는 P6 GA 범위 외 — 후속 RFC (v2.0+) 에서 별도 채택.
- **Write skew (S anomaly)**: 두 shard 에 걸친 read-then-write 패턴은 cross-shard SERIALIZABLE 에서도 차단되지 않는다.

## 3. 단일 shard

routing 결과 *단일 shard* 로 해소되는 트랜잭션 (90%+ 케이스, P-D §D.10.1) 은 pg-router 가 `BEGIN/COMMIT` 을 그대로 forward 한다. PG 표준 격리수준이 그대로 적용되며 *zero overhead* (ADR-0015 Decision §2). default 는 `READ COMMITTED`. application 이 명시적으로 `SET TRANSACTION ISOLATION LEVEL ...` 을 호출하면 해당 shard 의 PG 인스턴스가 직접 처리한다.

## 4. Cross-shard 2PC

다중 shard 에 걸친 트랜잭션은 pg-router (코디네이터 leader, ADR-0015 D.2.2 Lease election 통합) 가 다음 phase 로 진행한다:

1. **Begin** — 각 shard 에 `BEGIN` 발송 + tx log entry 생성 (etcd lease 보호)
2. **Prepare** — 모든 shard 에 `PREPARE TRANSACTION '<gid>'`
3. **Commit/Rollback** — 모든 shard `COMMIT PREPARED` 또는 `ROLLBACK PREPARED`
4. **Recover** — 코디네이터 fail-over 시 tx log replay + `pg_prepared_xacts` reconcile

이 모델은 *원자성* 을 보장한다. 그러나 *격리* 에 대해서는:

- `READ COMMITTED` 는 *진본 보장* — 각 shard 가 committed snapshot 만 노출하므로 cross-shard 에서도 동일 의미.
- `REPEATABLE READ` / `SERIALIZABLE` 의 phantom / write-skew 차단은 *가짜* — §2 알려진 제약 참조.

P6 GA 의 distributed isolation 공식 지원 범위 = **`READ COMMITTED` + 2PC** 만. 다른 조합은 *application 책임* (가짜 안전 의존 금지).

## 5. Cross-shard saga (deferred)

ADR-0015 의 saga 모델은 P6 GA 범위 *외* — 사용자 명시 `CALL begin_saga(...)` 시점에만 활성. saga 는 compensating action 기반 eventual consistency 패턴:

- 각 step 은 *독립 트랜잭션* 으로 commit (격리 경계 분리)
- 실패 시 *역순* compensating action 으로 보상 (rollback 아님)
- 중간 상태가 *외부에 노출* 가능 — isolation 보장 *없음*

saga 사용 시 application 은 *명시적* idempotency + compensating logic 을 설계해야 한다. 자동 합성 대상 아님.

## 6. 권장 사항 (application)

| 패턴 | 권장 |
|---|---|
| 90%+ single-shard read/write | default `READ COMMITTED` 유지 — 추가 비용 0 |
| Single-shard 강한 일관성 | `REPEATABLE READ` 또는 `SERIALIZABLE` 자유 사용 |
| Cross-shard write atomicity | 2PC + `READ COMMITTED` + explicit retry on serialization failure |
| **Cross-shard 강한 격리** | **`SERIALIZABLE` 가짜 안전 의존 금지** — application 측 advisory lock 또는 단일 shard 재설계 |
| Long-running multi-step business workflow | saga (명시 opt-in) + compensating action |

### Explicit retry 패턴 (cross-shard 2PC)

`PREPARE TRANSACTION` 단계에서 shard 가 `serialization_failure` (SQLSTATE `40001`) 또는 `deadlock_detected` (`40P01`) 를 반환하면 코디네이터가 *전체* 트랜잭션을 abort 한다. application 은 SQLSTATE `40001`/`40P01` 를 잡아 *전체 트랜잭션* 을 재실행해야 한다 (PG docs §13.3 권장 패턴과 동일).

## 7. P6 GA 이후 (Non-goals)

본 문서 범위 외 (별 RFC 후보):

- 분산 SSI (cross-shard SERIALIZABLE 진본 보장) — v2.0+
- 글로벌 snapshot (cross-shard REPEATABLE READ 진본 보장) — v2.0+
- saga 자동 합성 (사용자 명시 없이 SQL 으로부터 derive)
- read-only cross-shard snapshot (analytics workload 용)

각 항목은 *실험 + benchmark* 후 별 ADR/RFC 로 채택. 본 문서는 *현재 보장 경계* 만 봉인.

## 8. 참조

- ADR-0015 — 분산 트랜잭션 (2PC primary + saga deferred): [`../kb/adr/0015-distributed-tx.md`](../kb/adr/0015-distributed-tx.md)
- RFC-0005 — Distributed transactions (2PC + saga model, Draft 2026-05-02)
- RFC-0002 — `ShardRange` CRD
- RFC-0004 — pg-router
- Postgres docs — [Transaction Isolation (§13.2)](https://www.postgresql.org/docs/current/transaction-iso.html)
- Postgres docs — [Two-Phase Commit (`PREPARE TRANSACTION`)](https://www.postgresql.org/docs/current/sql-prepare-transaction.html)
- Postgres docs — [Serializable Snapshot Isolation (§13.2.3)](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-SERIALIZABLE)
- ROADMAP L182 — G5 Distributed SQL isolation matrix sub-task
- Plan: `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md` §D.10.3
