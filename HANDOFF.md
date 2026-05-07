# HANDOFF — postgresql-operator

> 다음 세션이 *컨버세이션 컨텍스트 없이* 재개 가능해야 한다. 시작 의식: 본 파일 → `TASKS.md` → 마지막 commit log 순서로 읽는다.

## 현재 상태 (2026-05-07, PG17/PG18 HA smoke + PG18 failover smoke 통과)

- **이번 세션**:
  - `hack/smoke.sh` 에 `PG_MAJOR` / `POSTGRES_VERSION` / `NS` / `SHARD_REPLICAS` override 지원 추가.
  - hardcoded dev sample apply 대신 smoke 스크립트가 quickstart `PostgresCluster` 를 지정 namespace/version 으로 생성한다.
  - PG17 + PG18 각각 Kind 에서 runtime image build/load → operator deploy → PostgresCluster Ready → `psql SELECT 1` round-trip PASS.
  - `SHARD_REPLICAS=1` smoke 로 primary+standby 2 Pod, `pg_stat_replication WHERE state='streaming'` 을 PG17/PG18 양쪽에서 PASS.
  - `SMOKE_FAILOVER=1` false-pass 제거: RTO 미측정/standby 미승격은 exit 1 로 처리. macOS `date -d` 의존 제거.
  - PG18 failover 차단 원인 수정: election identity 를 `podName/podUID` 로 바꾸고, `ReleaseOnCancel=false`, restarted ordinal-0 standby marker, `POSTGRES_MEMBER_COUNT`, CR status polling 을 추가했다.
  - PG18 `SHARD_REPLICAS=1 SMOKE_FAILOVER=1` smoke 재실행 결과 standby `*-1` 이 primary 로 승격, 기존 `*-0` 은 standby 로 재진입, CR status primary 도 `*-1` 로 수렴했다.
- **검증 인용**:
  ```
  $ ./hack/smoke.sh
  SUCCESS — quickstart cluster Ready, psql SELECT 1 = 1  # PG18 default

  $ CLUSTER_NAME=postgres-operator-smoke-17 PG_MAJOR=17 POSTGRES_VERSION=17 CR_NAME=quickstart17 ./hack/smoke.sh
  SUCCESS — quickstart cluster Ready, psql SELECT 1 = 1
  spec.postgresVersion: "17"

  $ CLUSTER_NAME=postgres-operator-smoke-18 PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18 ./hack/smoke.sh
  SUCCESS — quickstart cluster Ready, psql SELECT 1 = 1
  spec.postgresVersion: "18"

  $ CLUSTER_NAME=postgres-operator-smoke-17-ha PG_MAJOR=17 POSTGRES_VERSION=17 CR_NAME=quickstart17ha SHARD_REPLICAS=1 ./hack/smoke.sh
  walreceiver|streaming|00:00:00.00007|00:00:00.000244|00:00:00.000283

  $ CLUSTER_NAME=postgres-operator-smoke-18-ha PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18ha SHARD_REPLICAS=1 ./hack/smoke.sh
  walreceiver|streaming|00:00:00.000096|00:00:00.000277|00:00:00.000279

  $ CLUSTER_NAME=postgres-operator-smoke-18-failover PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18fo SHARD_REPLICAS=1 SMOKE_FAILOVER=1 ./hack/smoke.sh
  walreceiver|streaming|00:00:00.000093|00:00:00.000321|00:00:00.000367
  RTO = 21s (target < 30s)
  PASS: RTO < 30s
  PASS: CR status reflects quickstart18fo-shard-0-1 and restarted old primary is standby

  $ make test
  PASS (non-e2e all packages)

  $ make lint
  0 issues

  $ make validate
  helm lint --strict PASS, helm template CRD count PASS, dist/install.yaml generated
  ```
- **남은 리스크**:
  - PG18 primary Pod delete smoke 는 RTO < 30s 로 통과했다. 다만 chaos-mesh 기반 kill/network partition, multi-node 장애, pgBackRest 결합, 일반화된 fencing/old-primary 재합류는 아직 F03/F05 범위로 남아 있다.

## 현재 상태 (2026-05-07, governance 표준 정합 — P0+P1 baseline 도달)

- **이번 세션 (상용 제품 수준 trajectory)**:
  - **ADR 경로 표준화**: `docs/adr/` → `docs/kb/adr/` (`git mv` 16 files, history 보존). 8 file 의 참조 갱신 (CHANGELOG/README/HANDOFF/deploy/README/docs/rfcs).
  - **ADR INDEX.md 신규** — 활성 6 + archived 10 의 v0.x 처리 명시 (Superseded / 활성 / 비고).
  - **ADR-0007 신규**: Hook 도구로 pre-commit 채택 (글로벌 lefthook 표준 분기) — RFC 0002 4 계층 매핑 명시 정합 + 마이그레이션 트리거 정의.
  - **deps log seed**: `docs/kb/deps/2026-05.md` (P1 추적성, enforcement.md §2.4).
- **검증 인용**:
  ```
  $ make lint
  golangci-lint v2.8.0 build → 0 issues. ✓ (3-repo 모두 PASS)
  ```
- **3-repo governance 정합 매트릭스**:
  | 항목 | mongodb | postgres | valkey | 비고 |
  |---|---|---|---|---|
  | ADR 경로 | docs/kb/adr ✓ | docs/kb/adr ✓ | docs/kb/adr ✓ | 표준 정합 |
  | ADR INDEX | ✓ | ✓ (신규) | ✓ (정렬) | 표준 정합 |
  | deps log | ✓ (신규) | ✓ (신규) | ✓ (신규) | seed 완료 |
  | hook 도구 | pre-commit | pre-commit | lefthook | mongodb+postgres 는 ADR-0011/0007 정당화 |
  | GH workflows 0 | ✓ | ✓ | ✓ | RFC 0002 |
  | Makefile 게이트 | ✓ | ✓ | ✓ | RFC 0002 L3 |
- **다음 단계 (열린 트랙)**: F02 cycle 5 후속 (kind smoke — `hack/smoke.sh`), F02-residual (Pod env 주입 / readiness HTTP), mongodb golangci-lint 도입 (현재 staticcheck-only).

## Quality baseline (2026-05-07 실측)

`enforcement.md §3.4 (Coverage 합산)` 의 P2 측정 — 본 세션 baseline.

```
$ make test    # exit 0 / FAIL: 0
internal/plugin/sharding         coverage: 100.0% of statements
internal/webhook/v1alpha1        coverage:  88.5% of statements
internal/version                 coverage:  73.3% of statements
internal/plugin/extension/*      coverage:   0.0% of statements (no test files — 6 pkg)
test/utils                       coverage:   0.0% of statements
```

**80% 목표** 대비:
- ✓ sharding 100% / webhook 88.5% — 안정 영역
- △ version 73.3% — 근접
- ✗ plugin/extension/{pgaudit,pgcron,pgnodemx,pgvector,postgis,setuser} 6 pkg 0% — *최대 gap*. 강화 우선 후속 트랙.

`enforcement` 의 "절대치보다 *변경된 코드의 커버 여부*가 우선" 원칙 적용 — 본 baseline 은 회귀 비교 기준점.

## 이전 상태 (2026-05-06, release pipeline 강화 — 3-repo 정합)

- **HEAD `c6aec64`**: `chore(release): smoke-test SBOM+trivy 검증 강화 + step 번호 정합 + .gitignore (dist/ + .claude/)`
- **이번 세션 작업 (3-repo 정합)**:
  - SBOM (SPDX `.spdx.json`) asset 검증 step 추가 — supply chain 표준
  - trivy image post-publish HIGH/CRITICAL fixed-only scan 추가 (`--ignore-unfixed --exit-code 1`) — 운영 후 CVE 모니터링
  - smoke-test step 번호 [N/5] → [N/6] 정합 (재번호 누락 버그 수정)
  - .gitignore: `dist/` (kubebuilder build-installer 산출물) + `.claude/ralph-loop.local.md`
- **검증 인용 (mongodb 동일 변경 실측)**:
  ```
  $ ./scripts/release-smoke-test.sh
  ▸ [1/6] GH Release tag + assets   → SBOM (SPDX) asset 첨부 ✓
  ▸ [6/6] trivy image post-publish scan → 0 HIGH+CRITICAL ✓
  RESULT: 12 PASS / 0 FAIL (v1.4.5)
  ```
  postgres v0.3.0-alpha.1 release 에 SBOM asset (postgresql-operator-v0.3.0-alpha.1.spdx.json) 존재 사전 확인.
- **다음 단계 (열린 트랙)**: F02 cycle 5 후속 (kind smoke 실측 — `hack/smoke.sh`), F02-residual (Pod env 주입 / readiness HTTP endpoint).

## 이전 상태 (2026-05-06, T16 GitOps deploy 정합 — *완료, PR #17 머지*)

- **T16 머지됨** (`fix(deploy): namespace prod/db → data 통합 + storageClass ceph-rbd 정합 (ADR-0006) (#17)`).
- **결정 기록**: patch target name 은 `system` (config/manager 직접 import → namePrefix 미적용). mongodb-operator 의 `system → mongodb-operator-system` 수동 수정 방식은 kubebuilder regenerate 호환성 저하로 거절.

## 현재 상태 (2026-05-03)

- **HEAD**: F02 cycle 5 — `docs(deployment): kind smoke script + operator-guide 배포 가이드` (commit 직전)
- **HEAD~1..4**: F02 cycle 1~4 — sample CR / Dockerfile.pg / env+probes / RBAC+initdb / /readyz IsReady
- **HEAD~5**: F02 wiring (a548d37) — supervise 패키지 + election callback
- **HEAD~6**: T15 — election lease shard ordinal 마이그레이션
- **HEAD~7**: F01b — controller reconcile 본체
- **브랜치**: feat/ha-alpha-c1c2
- **현재 phase**: **P1 진행 중**. F01a + F01b + T15 + **F02 90% (테스트)** 완료.
- **F02 deployable 검증 결과**:
  - `make lint`: 0 issues
  - `go test ./... -count=1`: 모든 패키지 PASS
  - `make validate`: kustomize + helm lint --strict + build-installer 통과
  - 미실측 (다음 세션): `hack/smoke.sh` 로 kind 클러스터 적용 — Pod Ready + psql round-trip 검증.

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

## 다음 단계 (F02 100% 도달 + F03 진입)

**즉시 (F02 90% → 100%)** — 외부 환경 의존:

1. `./hack/smoke.sh` 실행 — kind 클러스터에 quickstart sample apply 후 Pod Ready + psql round-trip 검증.
   첫 실행 시 발견되는 모든 환경 이슈 (image push, fsGroup propagation, RBAC 빠진 권한, Pod sandbox 초기화 race 등) 는 fix-forward.
2. (선택) `ghcr.io/keiailab/pg:18` push 자동화 — 현재는 `make docker-build-pg && make docker-push-pg` 수동.
3. WAL lag 측정 — `pg_stat_replication` 폴러를 instance manager 에 추가 + Status.Shards[].Replicas[].LagBytes 갱신.

**F02 잔여 (별도 plan)**:

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
- ADR 0008 (cascade delete, archived as v0.x): `docs/kb/adr/_archive/v0.x/0008-finalizer-avoidance-policy.md`
- standards 적용: `~/Documents/ai-dev/standards/principles.md` §2 (Simplicity), §3 (Surgical)
- 이전 세션 HANDOFF: 본 파일 git history (commit f01894e 시점).
