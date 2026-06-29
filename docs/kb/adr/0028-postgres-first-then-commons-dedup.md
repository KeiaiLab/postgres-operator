# ADR-0028: PostgreSQL 우선 구현 → commons 중복 제거는 후행 일괄

- **Date**: 2026-06-26
- **Status**: Proposed
- **Authors**: @eightynine01
- **Refs**: ADR-0001 (self-built distributed SQL), ADR-0008 (keiailab-commons adoption), ADR-0020 (commons pkg/pvc+topology 채택), ADR-0027 (resharding identity)

## Context

KeiaiLab 워크스페이스(`E:\keiailab`)에는 4개의 독립 산출물이 공존한다:

| 산출물 | 성격 | 성숙도(2026-06) |
|---|---|---|
| `mongodb-operator` | DB operator | v1.13.2 (성숙, ReplicaSet GA / Sharded beta) |
| `postgres-operator` | DB operator | v0.4.0-beta.9 (G1 HA ~ G5 distributed SQL 진행 중) |
| `valkey-operator` | DB operator | 활발 (Cluster native) |
| `keiailab-commons` | **공용 라이브러리** (operator 아님) | 안정 (`pkg/{security,version,labels,networkpolicy,finalizer,status,topology,...}`) |

**본질적 차이**: MongoDB·Valkey 는 분산(HA·샤딩·라우팅·rebalancing)이 *DB engine native* 라 operator 가 얇은 오케스트레이터다. PostgreSQL 은 native 분산이 없어 라우터(`internal/router`)·샤딩 메타·online resharding(`ShardSplitJob`)·cross-shard 트랜잭션·자동 failover 를 *operator 가 직접 구현* 한다(ADR-0001). 따라서 mongodb-operator 를 그대로 모방할 수 없고, postgres-operator 가 구현 난이도·시장 가치 모두에서 화력 집중 대상이다.

mongodb-operator 의 `internal/topology` 는 *순수 의사결정 함수* 로 분리된 검증된 안전장치를 보유한다:

- `PreflightTopologyChange` — drain(shard 제거) 전 안전검증(잔여 chunk/db, balancer 상태, drain timeout) → `Blockers`/`Warnings`.
- `ComputeMigrationThrottle` — 부하 기반 backpressure(동시성·폴링 간격).
- `PlanBalancerControl` / `PlanZonePlacement` — rebalancer·zone 배치 계획(DryRun advisory).

postgres-operator 도 `internal/controller/shardsplit/steps.go` 에 cutover 상태기계(`RollbackAllowed`/`ValidateTransition`/`IsTerminal`)를 자체 보유한다. 두 operator 가 *동형(同型)의 의사결정 로직* 을 각자 구현 중이며, 향후 postgres G4(online resharding) 가 mongo 와 같은 drain/throttle/상태기계 패턴을 필요로 한다.

선택지를 검토했다:

- **C안 — 단일 multi-DB operator 로 통합**: 기각. 성숙도가 극단적으로 다른 산출물(mongo v1.13 GA vs pg v0.4 beta)을 묶으면 가장 미성숙한 것이 릴리스를 발목 잡는다. 코드 격리·RBAC 최소권한·선택 설치(OLM)·장애 차단을 모두 잃는다. 업계 관례(CNPG / Zalando / Percona)도 DB 별 독립 operator.
- **A안 — 검증된 공통 패턴을 commons 로 승격**: 채택하되 *시점* 이 문제. mongo 의 안전장치를 지금 commons 로 끌어올리면 안정 산출물(mongo)을 건드려야 하고, 2번째 소비자(postgres G4)가 아직 미완이라 추측 추상화 위험이 있다.
- **B안 — postgres 먼저 구현**: 채택. 가치·난이도가 거기 몰려 있다.

핵심 제약: 중복 제거는 **commons + (mongo 또는 postgres)** 최소 2개 repo 를 동시에 건드린다. 산발적으로 하면 cross-repo 버전 skew 와 회귀 위험이 누적된다.

## Decision

**postgres-operator 를 먼저 독립적으로 완성하고, 중복 제거(commons 승격)는 후행 단계에서 cross-repo 일괄로 수행한다.** 4개 산출물은 통합하지 않고 독립 repo 로 유지하며, 공유는 오직 `keiailab-commons` 를 단방향 의존으로 경유한다.

### 1. 의존·독립성 불변식

- `keiailab-commons` 는 **어떤 operator 도 import 하지 않는다**(순환 금지).
- operator 끼리 **서로 import 하지 않는다**(독립성). 공유는 commons 경유만.
- commons 는 **semver**, 각 operator 는 `go.mod` 에서 특정 버전 **pin**. breaking change = commons 메이저 +1 → operator 는 각자 편할 때 마이그레이션. 즉 *코드 공유가 릴리스를 묶지 않는다*(C안의 단점 회피).

### 2. 단계 순서

1. **Phase 1 — postgres 우선 (B)**: mongodb-operator 는 **건드리지 않는다**(freeze). postgres 가 필요한 drain preflight / throttle / cutover 상태기계 등을 *우선 `internal/` 에 자체 구현* 한다. 추상화를 미리 commons 로 빼지 않는다(Rule of Three 미충족).
   - G1 마무리(`failover_chaos` 실패 건 안정화) → G3 샤딩 → **G4 online resharding**(drain/throttle/상태기계 본진) → G5 distributed SQL(`internal/tx` 2PC 재구현).
2. **Phase 2 — 중복 제거 일괄 (A, 후행)**: postgres 가 mongo 와 동형 로직을 *실제로 2번째 소비* 하게 된 시점에, 순수 커널을 commons 로 **한 번에** 승격한다. commons 릴리스 → mongo·postgres 가 각자 어댑터로 채택. 이때만 mongo repo 를 건드린다.

### 3. "commons 로 승격" 판정 3-test (Phase 2 게이트)

하나라도 NO 면 operator 에 잔류:

1. **엔진 비종속인가** — 타입에 chunk/shard/mongos/WAL 이 박혀 있으면 NO. 범용어(`RemainingUnits` 등)로 추상화 가능할 때만 OK.
2. **순수 함수인가** — K8s client·DB 커넥션·CRD 를 만지면 NO. 입력→출력만이면 OK.
3. **2번째 소비자가 실재하는가 (Rule of Three)** — 추측 추상화 금지. Phase 1 이 끝나야 충족.

### 4. 어댑터 패턴 (독립성 보존 장치)

commons 는 범용 타입만 알고, 도메인 변환은 각 operator 가 수행한다:

```
// commons/pkg/topology — 순수 함수 + 범용 타입
type DrainState struct { RemainingUnits, InflightMoves int; DrainTimeout *time.Duration; RebalancerOn bool }
func DrainPreflight(s DrainState) Verdict

// mongodb-operator: chunks/dbs → units 어댑터
// postgres-operator: pending rows / ranges → units 어댑터
```

### 5. 승격 후보 (Phase 2 작업 목록, 확정 아님)

| 출처 | 무엇 | commons 위치(안) |
|---|---|---|
| mongo `topology.PreflightTopologyChange` | drain 안전검증 | `pkg/topology.DrainPreflight` |
| mongo `topology.ComputeMigrationThrottle` | 부하 기반 backpressure | `pkg/topology.Throttle` |
| mongo `topology.PlanBalancerControl` | rebalancer enable/disable 계획 | `pkg/topology.RebalancePlan` |
| pg `shardsplit/steps.go` 상태기계 | cutover 전이 가드 | `pkg/statemachine` |

### 6. commons 에 넣지 않는 것 (경계 = 독립성의 전부)

DB wire protocol(mongos 명령 / libpq forwarding), 2PC 코디네이터(PG `PREPARE TRANSACTION` orphan GC 는 PG 고유), CRD 타입, reconcile I/O. 이를 commons 로 올리면 사실상 C안(통합)이 되므로 금지.

## Consequences

**긍정**:

- postgres 가 mongo 안정성에 발목 잡히지 않고 독립 속도로 전진.
- 중복 제거를 1회 cross-repo 트랜잭션으로 묶어 버전 skew·회귀 위험 최소화.
- mongo·valkey 는 "유지 모드"로 freeze → 안정 산출물 무위험.
- Rule of Three 충족 후 추상화 → over-engineering 회피.

**부정 / 트레이드오프**:

- Phase 1 동안 postgres `internal/` 에 mongo 와 의도적으로 *중복된* 로직이 일시 존재(기술 부채를 명시적으로 수용, Phase 2 에서 상환).
- Phase 2 는 최소 2 repo 동시 변경 → 별도 조율 ADR(commons + 채택 operator 각 1건) 필요.
- commons 승격 시 mongo 어댑터 작성 = 그 시점에 한해 mongo freeze 해제(검증된 회귀 가드 단위 테스트로 보호).

**후속**:

- Phase 2 착수 시 commons 측 ADR + mongo·postgres 채택 ADR 신규 발행(ADR-0020 패턴 정합).
- 본 ADR 은 `docs/ROADMAP.md` Gate 진행과 연동(G4 가 Phase 2 trigger).
