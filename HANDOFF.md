# HANDOFF — postgresql-operator

> 다음 세션이 *컨버세이션 컨텍스트 없이* 재개 가능해야 한다. 시작 의식: 본 파일 → `TASKS.md` → 마지막 commit log 순서로 읽는다.

## 현재 상태 (2026-05-03)

- **HEAD (예정)**: F02 — `feat(instance): supervise 패키지 + election callback wiring` (commit 직전)
- **HEAD~1**: T15 — `refactor(instance)!: election lease 명명 규약 shard ordinal 모델로 마이그레이션`
- **HEAD~2**: F01b — `feat(controller): RFC 0001 spec 기반 reconcile 본체 + envtest 재작성`
- **HEAD~3**: F01a — `feat!(api): RFC 0001 PostgresCluster CRD v2 schema 실장`
- **브랜치**: main
- **현재 phase**: **P1 진행 중**. F01a + F01b + T15 완료. **F02 60% (구현)** — controller env 주입 + readiness HTTP 후속.
- **검증 결과 (F02)**: `make lint` 0 issues / `go test ./... -count=1` 모든 패키지 PASS (supervise 신규 ~10 tests pass).

## 본 세션 (F02 supervise wiring) 의사결정 기록

1. **2026-05-03**: `TestReal_StopTimeoutSendsSIGKILL` race 진단 — fake-postgres trap 설치 직전에 SIGTERM 도착 시 default 동작 (즉시 종료) 발동 → exitCh 가 ctx.Done 보다 먼저 fire. Reload 테스트의 stderr "FAKE_POSTGRES_PID=" 대기 패턴을 `waitForStderr` 헬퍼로 추출 후 timeout 테스트에도 적용.
2. **2026-05-03**: TERM_DELAY=10 → 2 로 단축. SIGKILL 후에도 trap 의 sleep 자식이 orphan 되어 Go cmd.Wait() 가 stderr pipe EOF 를 기다리며 block (Go cmd.Stderr 가 non-os.File 일 때 내부 pipe 사용). ctx (1s) 보다 크되 ExitCh 대기 (3s) 안에 끝나야 함.
3. **2026-05-03**: `--supervise-disabled` flag 추가 — election/fencing 과 동일 패턴. dev/local 모드 + 단위 테스트에서 postgres binary 부재 시 fork 회피.
4. **2026-05-03**: OnStoppedLeading 의 demote 는 `sup.Stop(ctx, fast=true)` (SIGINT) 만 호출 — PG 는 native pg_demote 부재. 본 instance 는 ExitCh fire 시 통째 exit → K8s Pod 재시작 → 다음 부팅에 standby 진입. standby.signal 재구성 로직은 F03 후속.
5. **2026-05-03**: gocyclo 31 (>30) 해소를 위해 `buildSupervisor` / `startSupervisor` / `gracefulStopSupervisor` / `buildFencer` 4 helper 추출. F01b 의 `applyClusterConditions` 추출 패턴 동일. main 흐름이 한 줄 의도로 압축됨.
6. **2026-05-03**: ExitCh watcher goroutine 이 supExitCh 에 송출 → main select 의 5번째 case → `os.Exit(1)`. postgres child 가 죽으면 instance 도 함께 죽음 (PID 1 모델 ADR 0002 그대로).

## 이전 세션 (F01b) 의사결정 기록

1. **2026-05-03**: helper 시그니처는 호출자 결정형 유지 (`buildConfigMap(cluster, name, role, shardOrdinal, reg)` 등). 응집형 (`buildShardConfigMap(cluster, ordinal, reg)`) 도 후보였으나 plan 의 "5개 helper 시그니처 통일" 명시에 따라 §3 Surgical 우선 — pool string → shardOrdinal int32 만 적용하고 함수 갯수/이름은 보존.
2. **2026-05-03**: `SelectorLabels(cluster, role, shardOrdinal int32)` 의 ordinal=-1 sentinel 로 router 의 "shard 차원 부재" 표현. 별도 `RouterSelectorLabels()` 분리 회피 (§2 Simplicity — 단일 사용 코드에 추상화 금지).
3. **2026-05-03**: envtest 의 STS/Deployment controller 부재 ↔ `Status.ReadyReplicas` 자동 진행 불가. `markSTSReady` 헬퍼로 mock + spec annotation bump 로 reconcile re-trigger. 이는 envtest 의 표준 패턴이며 실 클러스터에서는 STS controller 가 자동 처리.
4. **2026-05-03**: cascade-delete envtest 는 GC controller 부재로 *직접 삭제 관측 불가* — 대신 OwnerReference (Controller=true, BlockOwnerDeletion=true, UID 일치) 부착 자체를 검증. K8s GC 의 cascade 동작은 본 메타데이터를 단일 진실로 사용하므로 이 검증이 cascade GC 의 *전제 조건* 을 보장한다.
5. **2026-05-03**: `r.upsert` 직후 같은 reconcile 내에서 `r.Get(STS)` 시 cache propagation 지연으로 NotFound 가 잠깐 나타날 수 있다 → graceful fallback (readiness=false 로 단순화, 다음 reconcile 에 진짜 status 관측). 동일 패턴을 router Deployment 에도 적용.
6. **2026-05-03**: Reconcile cyclomatic complexity 가 31 (>30) → status 갱신부를 `applyClusterConditions` 헬퍼로 분리. 단일 책임 + 테스트 가능성 향상.
7. **2026-05-03**: `internal/plugin/sharding/api.go` Name() doc comment 의 `PostgresClusterSpec.Sharding.Backend 와 일치` → `PostgresClusterSpec.ShardingMode 가 "native" 일 때 활성화` 로 정정. 새 spec 에 sharding 필드 부재.

## 다음 단계 (F02 잔여 + F03 진입)

F02 의 supervise + wiring 60% 완료 — 잔여 40% 는 *operator 측 통합* 이라 별도 PR 권장:

**F02-residual (별도 plan)** — 100% 도달까지:
1. `internal/controller/postgrescluster_controller.go` 의 `buildShardStatefulSet` 등에 Pod env 주입: `POSTGRES_BIN_DIR` (init container 또는 runtime image path), `POSTGRES_DATA_DIR`, `POSTGRES_CONFIG_FILE`, `POSTGRES_HBA_FILE`, `POSTGRES_LOCAL_DSN`. ConfigMap mount + 결정 (별도 ADR — config file path convention).
2. Readiness probe HTTP endpoint — `/readyz` 에 `sup.IsReady(ctx)` 통합. 현재는 election Status 만 반영.
3. WAL lag 측정 — `pg_stat_replication` query → metrics endpoint.
4. `Status.Shards[].Primary.Endpoint` 갱신 — sidecar patch vs controller active probe (별도 ADR).

**F03 진입점** — RFC 0003 election/fencing receiver 측 → active 측 완성:
- Demote 후 standby.signal 재구성 로직 (지금은 sup.Stop 만 — restart 시 어떻게 standby 로 부팅할지 결정).
- Fence 해제 정책 (운영자 수동 vs 자동 timeout).
- multi-shard 동시 fail 시 election 우선순위.

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
