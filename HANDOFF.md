# HANDOFF — postgresql-operator

> 다음 세션이 *컨버세이션 컨텍스트 없이* 재개 가능해야 한다. 시작 의식: 본 파일 → `TASKS.md` → 마지막 commit log 순서로 읽는다.

## 현재 상태 (2026-05-03)

- **HEAD**: T15 — `refactor(instance)!: election lease 명명 규약 shard ordinal 모델로 마이그레이션` (F02 진입 정리)
- **HEAD~1**: F01b — `feat(controller): RFC 0001 spec 기반 reconcile 본체 + envtest 재작성`
- **HEAD~2**: F01a — `feat!(api): RFC 0001 PostgresCluster CRD v2 schema 실장 (F01a — types/webhook only)`
- **브랜치**: main
- **현재 phase**: **P1 진행 중**. F01a + F01b + T15 완료. F02 ~ F05 대기.
- **검증 결과 (T15)**: `make lint` 0 issues / `make test` 모든 패키지 PASS (election cov 97.5%, controller cov 65.9%, sharding cov 100%).

## 본 세션 (F01b) 의사결정 기록

1. **2026-05-03**: helper 시그니처는 호출자 결정형 유지 (`buildConfigMap(cluster, name, role, shardOrdinal, reg)` 등). 응집형 (`buildShardConfigMap(cluster, ordinal, reg)`) 도 후보였으나 plan 의 "5개 helper 시그니처 통일" 명시에 따라 §3 Surgical 우선 — pool string → shardOrdinal int32 만 적용하고 함수 갯수/이름은 보존.
2. **2026-05-03**: `SelectorLabels(cluster, role, shardOrdinal int32)` 의 ordinal=-1 sentinel 로 router 의 "shard 차원 부재" 표현. 별도 `RouterSelectorLabels()` 분리 회피 (§2 Simplicity — 단일 사용 코드에 추상화 금지).
3. **2026-05-03**: envtest 의 STS/Deployment controller 부재 ↔ `Status.ReadyReplicas` 자동 진행 불가. `markSTSReady` 헬퍼로 mock + spec annotation bump 로 reconcile re-trigger. 이는 envtest 의 표준 패턴이며 실 클러스터에서는 STS controller 가 자동 처리.
4. **2026-05-03**: cascade-delete envtest 는 GC controller 부재로 *직접 삭제 관측 불가* — 대신 OwnerReference (Controller=true, BlockOwnerDeletion=true, UID 일치) 부착 자체를 검증. K8s GC 의 cascade 동작은 본 메타데이터를 단일 진실로 사용하므로 이 검증이 cascade GC 의 *전제 조건* 을 보장한다.
5. **2026-05-03**: `r.upsert` 직후 같은 reconcile 내에서 `r.Get(STS)` 시 cache propagation 지연으로 NotFound 가 잠깐 나타날 수 있다 → graceful fallback (readiness=false 로 단순화, 다음 reconcile 에 진짜 status 관측). 동일 패턴을 router Deployment 에도 적용.
6. **2026-05-03**: Reconcile cyclomatic complexity 가 31 (>30) → status 갱신부를 `applyClusterConditions` 헬퍼로 분리. 단일 책임 + 테스트 가능성 향상.
7. **2026-05-03**: `internal/plugin/sharding/api.go` Name() doc comment 의 `PostgresClusterSpec.Sharding.Backend 와 일치` → `PostgresClusterSpec.ShardingMode 가 "native" 일 때 활성화` 로 정정. 새 spec 에 sharding 필드 부재.

## 다음 단계 (F02 본체 진입)

T15 (election 인터페이스 마이그레이션) 완료 — F02 본체 진입 가능.

**F02 — instance manager 본체 — postgres 프로세스 supervise + promote/demote 실장**

진입점 (별도 plan 사이클 권장 — T2~T3 분량):
1. `internal/instance/supervise/` 신규 패키지 — `Supervisor` struct + `os/exec.CommandContext` 로 postgres 자식 프로세스 lifecycle 관리 (start/stop/reload/signal forwarding).
2. `cmd/instance/main.go` 의 election callback (`OnPromote`/`OnDemote`) 안에 `pg_ctl promote` / `pg_ctl demote` 호출 wiring.
3. Readiness probe HTTP endpoint (`pg_isready` wrapping) + WAL lag 측정 (`pg_stat_replication`).
4. RFC 0003 (election / fencing 인터페이스) 의 active logic 은 F03 에서 — F02 는 receiver 측만.
5. `Status.Shards[].Primary.Endpoint` 갱신 — sidecar patch vs controller active probe 결정 (별도 ADR).

## 후속 정리 작업 (F02 이후, 별도 PR)

- `docs/roadmap.md` 새 8-Phase (P0~P7) 본문 재작성 — 현재 deprecated stub.
- `docs/concepts/`, `docs/how-to/`, `docs/reference/` 의 v0.x 어휘 (coordinator/workers/routers) → 새 spec 어휘 (shard/router) 정리.
- F04 진입점: `internal/controller/backup/` — RFC 0001 `spec.backup` reconcile + BackupJob CRD 연결.

## 차단점

없음. F02 는 controller 와 별도 layer (instance binary) 라 mechanical 진행 가능.

## 근거 링크

- 본 세션 plan: `/Users/phil/.claude/plans/mighty-wiggling-hamming.md` (F01b 7-파일 wiring 결정)
- RFC 0001: `docs/rfcs/0001-postgrescluster-crd-v2.md` §3.1 (필드) + §3.4 (Condition 카탈로그)
- ADR 0008 (cascade delete, archived as v0.x): `docs/adr/_archive/v0.x/0008-finalizer-avoidance-policy.md`
- standards 적용: `~/Documents/ai-dev/standards/principles.md` §2 (Simplicity), §3 (Surgical)
- 이전 세션 HANDOFF: 본 파일 git history (commit f01894e 시점).
