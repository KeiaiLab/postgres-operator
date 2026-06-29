# 작업 핸드오프 — HA failover · PITR restore · E2E 정비

> 다른 환경(다른 머신·Dev Container·실 Kubernetes)에서 이 작업을 이어받거나 재검증하기 위한 인수인계 문서.
> 작성 기준: 2026-06-25. 브랜치 `chore/ha-pitr-e2e-consolidation` (main 미푸시).
>
> 문서 지도: [DOCS_MAP.ko.md](DOCS_MAP.ko.md) — 분석/작업/환경 문서군의 입구·역할·SSOT.
> 관련 문서: [PROJECT_OVERVIEW.md](PROJECT_OVERVIEW.md) · [FEATURE_DEEP_DIVE.md](FEATURE_DEEP_DIVE.md) · [TEST_ANALYSIS.md](TEST_ANALYSIS.md) · [E2E_TEST_REPORT.ko.md](E2E_TEST_REPORT.ko.md) · [dev-setup-devcontainer.md](dev-setup-devcontainer.md) · [dev-setup-wsl.md](dev-setup-wsl.md)

---

## 1. 한눈에 보기

| 항목 | 상태 |
|---|---|
| 브랜치 | `chore/ha-pitr-e2e-consolidation` (base: `main`) |
| 커밋 | **35+개**, **push 안 됨** (로컬 전용). 원 HA/PITR 8커밋(2장) + 분산 SQL 라우터(2026-06-26~27) |
| 범위 확장 | 원래 HA/PITR 정비 → **분산 SQL 라우터(Vitess-for-Postgres) 구축**으로 확장 (6장 참고) |
| 단위·통합 테스트 | ✅ 통과 (컨테이너 검증) |
| 라이브 검증 (호스트 kind) | ✅ **single-shard 성능 baseline** + ✅ **query-mode 쿼리 라우팅 end-to-end** (2 trust postgres) |
| lint | ⚠️ baseline-fail (main 자체 fail), 프로덕션 지적 정리됨 |

핵심: HA/PITR(단일샤드)은 검증 완료. 이번 세션은 **분산 SQL 라우터**를 구축·라이브 검증했다 — `pg-router` query-mode가 실 PG에서 쿼리 키로 샤드를 라우팅함을 확인(alice→shard-0/bob→shard-1). **앞으로의 개발 로드맵은 6.5장 + [ROUTER-GAP-ANALYSIS §6](sharding/ROUTER-GAP-ANALYSIS.ko.md)** 참고.

> 참고: **호스트 직접 kind는 정상 동작**한다(과거 "kind 불가"는 *컨테이너 안* kind 2중 중첩 한정). `kind create cluster`로 라이브 검증 가능.

---

## 2. 커밋 구성과 의도

순서대로 의미 단위로 분리했다. `main..HEAD` 기준.

### `08f59d6` chore(license): Apache 2.0 → MIT 헤더 통일
- router/cmd/chart-test 등 **16개 파일**의 라이선스 헤더를 저장소 `LICENSE`(MIT)와 일치. 로직 무관.

### `eabbd98` feat(ha): #220 자동 failover 안정화 — fencing·pod-readiness·promotion guard
- `aggregate_status.go`: instance-status heartbeat와 **별개로 K8s Pod/컨테이너 readiness**를 검사하고, fenced PVC 멤버를 promotion 후보에서 제외.
- `failover_promoter.go`: `pg_ctl promote` → **`pg_promote()`(SQL)** 전환, 승격 전 old-primary **PVC fencing**(`fencePodPVC`), 후보 K8s readiness 검증(`promotionCandidateReadyForExec`).
- `postgrescluster_controller.go`: `primaryEndpointForShard` 추출로 초기 HA bootstrap의 **false failover window** 차단 + #220 failback guard.
- `primary_endpoint.go` 신규(ord-0 DNS 즉시 주입).
- `failover_chaos_test.go`: 승격 경로 완성으로 live chaos drill을 PContext → Context로 활성화.
- ⚠️ **교차 파일**: `postgrescluster_controller.go`에는 PITR(#209)의 restore-in-progress 락(`AnnotationRestoreInProgress`) hunk도 함께 들어 있다.

### `df4aa52` feat(backup): #209 PITR restore 오프라인 오케스트레이션 완성
- `pgbackrest/plugin.go`: `--archive-check=n` 제거(복구 불가 백업을 실패로 노출), `restore --delta`, `restore_command`를 repo env + `--pg1-path` 포함 형태로 재작성, targetTime **microsecond 정밀도**.
- `backupjob_controller.go`: **`reconcileSidecarRestore`** — STS scale-0 → Pod 정지 대기 → data PVC 마운트 restore Job → 완료 시 restore-in-progress 락 해제.
- `builders.go`: pgBackRest filesystem repo를 **data PVC 내부**로 이동(`backupRepoMountPath`), spool EmptyDir(`ephemeral-pgbackrest-spool`), `archive-push "%p"`.
- `docs/superpowers/plans/2026-06-24-pitr-restore-orchestration.md`: 설계 플랜 문서.
- ⚠️ **교차 파일**: `builders.go`에는 failover(#220)의 PVC `postgres.keiailab.io/cluster` label hunk도 함께 들어 있다.

### `20c82af` test(e2e): fixture 독립화 + manager rollout 안정화
- quickstart 외부 전제를 제거하고 각 suite가 **`ensurePostgresClusterReady`**로 독립 PostgresCluster를 부트스트랩.
- DB/User live smoke에 **`waitPostgresUserApplied`**(status.applied=true) 검증 추가, CRD spec 구조(cluster.name object화, schema 내부 privileges)와 `kubectl exec -c postgres` 정정.
- 동일 tag 재실행 시 manager Deployment rollout 강제 + manager 이미지 `0.4.0-beta.8` 정렬.
- `helpers.go` 신규(공용 e2e 헬퍼).

### `e602751` feat(pooler): PgBouncer stats_users 파라미터 허용
- PgBouncer 동적 파라미터 허용 목록에 `stats_users` 추가 (독립 기능).

### `910ad01` docs: 프로젝트 분석 문서 + dev 환경 가이드
- `PROJECT_OVERVIEW` / `FEATURE_DEEP_DIVE` / `TEST_ANALYSIS` / `E2E_TEST_REPORT`(PITR drill 반영) + `dev-setup-devcontainer` / `dev-setup-wsl`.

### `dd2d8df` chore(dev): devcontainer + gitattributes
- devcontainer 정의/lock 갱신, Windows CRLF로 `.sh` shebang 깨지던 문제를 `.gitattributes`로 영구 차단.

### `e9804f2` style(controller): 프로덕션 lint 지적 정리
- `builders.go`: `pvcLabels` 복사 루프 → `maps.Copy` (mapsloop).
- `backupjob_controller.go`: `ensureClusterRestoreAnnotation`의 항상 nil인 `ctrl.Result` 반환 제거 (unparam). 동작 변경 없음.

---

## 3. 남은 일 — 라이브 K8s E2E 드릴

코드와 단위/통합 검증은 끝났다. **실제 쿠버네티스에서 다음 두 드릴을 통과 검증**하는 것만 남았다.

| 드릴 | make 타겟 | 무엇을 검증하나 |
|---|---|---|
| 자동 failover chaos | `make test-e2e-failover` | primary 강제 종료 → fencing → 후보 readiness → `pg_promote` 승격 → rogue-primary reseed. STS self-heal이 failover window를 선점하던 과거 라이브 실패(ADR-0027 shard-identity)가 재발하지 않는지. |
| PITR restore | `make test-e2e` (pitr_restore_e2e_test.go) | WAL 아카이빙 → 특정 시점 offline restore → STS scale-0 오케스트레이션 → 락 해제. |

### 왜 이 호스트에서 못 돌렸나 (인프라 한계, 코드 아님)
Windows + Docker Desktop 환경에서 kind를 띄우면 **Docker Desktop VM → DinD → kind node**의 2중 중첩이 된다. 이 구조에서:
- 노드 컨테이너 부팅 단계에서 `could not find a log line that matches "Reached target .*Multi-User System"` (cgroup v2 중첩),
- `--cgroupns=host -v /sys/fs/cgroup:...`로 우회해도 control-plane `kubeadm init`이 apiserver/scheduler 연결 deadline로 실패.

→ **2중 중첩이 원인.** 코드/매니페스트 문제가 아니다.

### 다른 곳에서 E2E 돌리는 방법 (택1)

**(A) 실제 단일 Linux 호스트 (권장)** — 중첩 없음
```bash
# Linux 머신에서 (Docker만 있으면 됨)
git clone <repo> && cd postgres-operator
git switch chore/ha-pitr-e2e-consolidation   # 또는 머지된 브랜치
make test-e2e-failover     # 자동 failover 드릴
make test-e2e              # 전체 e2e (PITR 포함)
```

**(B) 클라우드/온프렘 실 Kubernetes** — kind 불필요
```bash
export KUBECONFIG=/path/to/real-cluster
make docker-build IMG=<registry>/postgres-operator:test
make docker-push  IMG=<registry>/postgres-operator:test
helm install postgres-operator ./charts/postgres-operator \
  --set image.repository=<registry>/postgres-operator --set image.tag=test
# 이후 test/e2e/*.go 시나리오를 대상 클러스터에 맞춰 실행
```

**(C) Dev Container 안에서 DinD** — 이 호스트와 동일한 중첩이므로 비권장. 위 (A)/(B)를 쓸 것.

> 참고: 이 머신에는 과거 다른 세션이 만든 `kindest/node:v1.36.1` 클러스터가 호스트 Docker에 떠 있었으나(34h), 이번에 정리하며 제거했다. 추후 실 K8s가 생기면 DooD로 붙여 돌리는 것도 가능하다.

---

## 4. 재검증 방법 (호스트에 go/make 없는 경우)

이 Windows 호스트에는 go·make·WSL bash 툴체인이 없다. **모든 빌드/테스트는 컨테이너 안에서** 한다. Dev Container 정식 절차는 [dev-setup-devcontainer.md](dev-setup-devcontainer.md). 빠른 일회성 검증은 아래처럼 `golang:1.26` 컨테이너에 레포를 마운트:

```bash
# 빌드 + vet (가장 빠른 sanity check)
docker run --rm -v /path/to/postgres-operator:/workspace -w /workspace \
  -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c "go build ./... && go vet ./..."

# 단위 + 통합 테스트 (envtest 자동 셋업)
docker run --rm -v /path/to/postgres-operator:/workspace -w /workspace golang:1.26 \
  sh -c "make test"

# 레이스 검출 (failover 동시성 코드 핵심 검증)
docker run --rm -v /path/to/postgres-operator:/workspace -w /workspace golang:1.26 \
  sh -c "make test-integration"
```
Windows 호스트에서는 `-v E:\keiailab\postgres-operator:/workspace`처럼 경로를 준다. 최초 1회는 모듈 다운로드로 수 분 소요.

### 알려진 검증 결과 (참고치)
- `make test` ✅ — controller 73.2% / failover 98.8% / pgbackrest 86.6% / webhook 94.7%.
- `make test-integration -race` ✅ — 데이터레이스 0.
- `make lint` ⚠️ RC=2 — baseline-fail. 잔여 지적은 전부 사소한 스타일(goconst·gocyclo·lll·modernize·prealloc·unparam)이며 대부분 테스트 코드 또는 이번에 손대지 않은 파일(`cmd/instance/main.go`, `shardsplitjob_controller.go`, `election_test.go`). 프로덕션 2건은 `e9804f2`에서 정리.
- `make test-scripts` ⚠️ RC=2 — `hack/artifacthub_smoke_test.sh`만 실패, `hack/` 미변경이라 이번 작업과 무관.

---

## 5. 다음 작업자를 위한 주의점

- **push 안 됨.** 브랜치는 로컬 전용이다. 원격 반영이 필요하면 별도 지시 후 `git push -u origin chore/ha-pitr-e2e-consolidation`.
- **교차 파일 2개** (2장 ⚠️ 참고): `postgrescluster_controller.go`(failover 커밋에 PITR 락 hunk 포함), `builders.go`(PITR 커밋에 failover label hunk 포함). 커밋을 cherry-pick/revert할 때 이 의존을 염두에 둘 것.
- **failover wiring은 이번 작업 이전부터 이미 존재**했다. Reconcile 경로: `clusterFailoverDecision → DetectPrimaryFailure → shouldPromoteAfterDebounce`(8s) `→ #220 failback guard → executeClusterPromotion → reconcileRoguePrimaries`. 이번 변경은 그 위에 fencing/readiness/pg_promote 보강을 얹은 것.
- **DB/User status.applied도 이미 구현**돼 있다(`postgresdatabase_controller.go`, `postgresuser_controller.go`). 과거 분석 메모의 "미구현" 진단은 부정확했으니, 미완 여부는 문서가 아니라 **코드/`git log`로 확인**할 것.
- envtest는 Kubernetes 1.36 바이너리를 받는다(`make test`가 자동). 오프라인 환경이면 `bin/k8s/` 캐시 확인.

---

## 6. 심층 검토 후속 (2026-06-26) — failover lease 정리 + 발견 사항

> 별도 심층 검토에서 나온 **working-tree 변경 1건**(아직 커밋 안 됨)과 **기록용 발견 사항**. 커밋 구성(2장)과 분리해서 본다.

### 6.1 변경 — operator-level failover lease의 무효(dead) production 배선 제거

**문제**: `cmd/main.go`가 manager lease와 *별개*인 failover 전용 lease(`failover.FailoverLeaseName`)를 만들어 모든 replica가 경합하도록 등록했으나, 그 leadership 결과(`Lease.IsLeader()`)를 **어디서도 참조하지 않았다**(테스트 제외). `OnStartedLeading`/`OnStoppedLeading` 콜백은 **로그만** 찍었다. 실제 자동 failover(`clusterFailoverDecision → executeClusterPromotion`)는 PostgresCluster reconcile 루프에서 돌고, 이는 **controller-runtime manager 자체 leader election**(`--leader-elect`, 기본 true)으로 이미 단일 replica로 게이팅된다. ⇒ 전용 lease는 클러스터에 lease 객체를 만들고 2s마다 renew하면서도 **행동에 0 영향**을 주는 장식이었다.

**왜 순진하게 wiring하면 안 되나**: reconciler는 manager-lease holder에서만 돈다. 만약 failover 실행을 `failoverLease.IsLeader()`로 게이팅하면, 그 lease를 *다른* Pod가 쥐었을 때 manager-leader Pod는 (failover-leader가 아니라) 실행을 건너뛰고, failover-leader Pod는 (reconciler가 안 돌아서) 실행을 못 한다 ⇒ **failover 영구 정지(deadlock)**.

**조치** (working tree, 미커밋):
- `cmd/main.go`: failover lease 생성/등록 블록 + 헬퍼(`failoverIdentity`, `operatorNamespace`, `leaderElectionAgnosticRunnable`) 제거, unused import(`context`/`fmt`/`strings`/`kubernetes`/`failover`) 정리. 단일-active 보장이 manager lease로 충분함을 주석으로 명시.
- `internal/controller/failover/lease.go`: 패키지는 **테스트된 building block으로 보존**(leader 단일성+handoff 검증 `lease_test.go`)하되, 헤더 주석을 "아직 production 미배선, deadlock 이유로 순진한 게이팅 금지, 제대로 된 P2-T3는 failover를 reconcile 루프 밖 runnable로 먼저 분리해야 함"으로 정직하게 갱신.
- `docs/rfcs/0007-ha-election-and-fencing.md §7`: failover detection+promotion은 구현·동작(manager lease 게이팅)으로, 전용 failover-controller lease(P2-T3)는 미배선으로 상태 정정.

**검증**: `golang:1.26` 컨테이너에서 `go build ./...` ✅, `go vet ./cmd/... ./internal/controller/failover/...` ✅, `go test ./internal/controller/failover/... ./cmd/...` ✅(failover 4.2s ok). 동작 경로(reconcile+manager lease)는 불변이라 failover 거동에 영향 없음.

### 6.2 기록용 발견 사항 (조치 안 함 — 유지보수자 판단 영역)

- **샤딩(G3~G4)은 골격**: `shardsplitjob_controller.go`는 스스로 "phase 전이 골격"이라 명시. 7-step 중 실 데이터 이동(`router.CopyTable` DSN 결선)·CDC 논리복제·write-block cutover는 전부 "별 트랙"(미구현). `InitialCopy` phase는 데이터를 옮기지 않고 다음 phase로 넘어간다. ⇒ 제품 차별점(샤딩)이 end-to-end로 동작하지 않음.
- **분산 SQL 라우터는 PoC**: `cmd/pg-router/main.go`는 명시적 PoC(연결 단위 프록시, 2-shard 하드코딩). `config/`·`charts/`·`deploy/` 어디에도 배포 매니페스트 없음 ⇒ operator가 띄우지 않음.
- **성능 수치 0**: `docs/perf/baseline.md`는 측정 프로토콜만 있고 모든 결과 셀이 `(pending live measurement)`. 하네스(`test/bench/pgbench.sh`·`sysbench.sh`)는 존재하나 라이브 클러스터 부재로 미실행. **상품화 관점 최대 결손** — single-shard baseline부터 실측 필요.
- **ROADMAP G4 과소표기**: `docs/ROADMAP.ko.md`는 `ShardSplitJob CRD`를 `[ ]`(미완)로 적었으나 실제론 CRD + 골격 컨트롤러(phase 머신 + Bootstrap target 생성 + RoutingUpdate)가 존재. 4개 언어(ko/en/ja/zh) 동기 수정이 필요해 여기서는 보류 — 일괄 정정 권장.

### 6.3 분산 SQL 방향 착수 — 라우터 척추 (working tree, 미커밋)

타깃을 "범용 분산 SQL(Vitess-for-Postgres)"로 정하고 라우터를 척추로 세우는 작업 시작.

- **라우터 갭 분석 + 능력 사다리**: `docs/sharding/ROUTER-GAP-ANALYSIS.ko.md` 신설. 핵심 발견 — 라우터가 "두 반쪽"(pg-router 바이트 프록시 ↔ in-process 라이브러리)으로 분리돼 서로/reconciler에 미연결. 토폴로지 CRD→라우터 흐름 단절(하드코딩). vindex/resharding 검증은 진짜, scatter/sql_route는 골격, placement/metadata_store는 진짜지만 orphan.
- **교체 가능 라우팅키 추출 전략 도입**: `internal/router/route_extractor.go`(`RouteKeyExtractor` 인터페이스 + 선택기 regex|parser|auto) + `sql_route.go`(regex 컬럼 인지) + `route_extractor_parser.go`(**제로 의존성 토크나이저** 정확 전략) + 테스트. **기본 전략 `regex`**(현황 유지, 사용자 지정). 세 전략 모두 dep 0, 런타임 선택.
- **파서 결정(실측 → 외부 파서 기각)**: auxten/postgresql-parser v1.0.1 평가했으나 ① ~25 transitive 모듈 ② **치명적: 옛 monolithic genproto 고정 → 오퍼레이터 현대 deps(grpc 1.79/otel/cel-go split genproto)와 ambiguous import 충돌로 빌드 파괴**(build-tag로도 격리 불가, go.mod 모듈 전역). ⇒ 외부 파서 대신 **자체 토크나이저**(murmur3 철학). 정규식보다 정확(따옴표 내부·주석 가짜 predicate 오인 방지)하면서 dep 0. **검증**: build/vet/test ✅ gofmt clean **go.mod 무변경**.
- **(B) CRD 토폴로지 소싱 완료** (working tree): `internal/router/topology.go`(`TopologyProvider` 전략 + `Topology`/`BuildTopology` + `StaticTopologyProvider` + `CRDTopologyProvider` + `ShardRangeLister` 인터페이스) + `topology_test.go`(fake lister). pg-router를 `TopologyProvider`로 리팩터링 — `PGROUTER_TOPOLOGY=static|crd` 선택, crd 모드는 `PGROUTER_REFRESH` 주기 hot-reload, K8s 클라이언트는 `clientLister`로 가장자리 격리(router 패키지는 controller-runtime 미import, 순수 유지). `shardSpec()`/`backendFor()` 보존(기존 테스트 유지). **검증**: build/vet/test ✅ gofmt clean **go.mod 무변경**(controller-runtime 기존 dep).
- **(C) 라우터 배포 완료** (working tree): `Dockerfile.router`(pg-router distroless 이미지) + `config/router/`(SA + Role[shardranges get/list/watch] + RoleBinding + Deployment[replicas 2, restricted SecCtx, TCP probe, crd 토폴로지 env] + Service[:5432] + kustomization + README). pg-router에 **DNS 템플릿 BackendResolver**(`PGROUTER_BACKEND_TEMPLATE`, {cluster}/{shard}/{namespace}) 추가 — per-shard env 없이 매니페스트 깔끔, 모듈화(env resolver와 스왑). **검증**: build/vet/test ✅ gofmt clean go.mod 무변경 + `kustomize build config/router` 렌더 성공(5종, 이미지 매핑 정상).
- **샤드 장애 회복력 슬라이스 완료** (working tree): "라우팅은 백엔드 커넥션 확보가 전제인데 샤드가 죽어 있으면?"에 대응. ① **key→shard(토폴로지)와 shard→backend(resolver) 분리**(`topology.go` 재작성) ② **`StatusBackendResolver`** — `PostgresCluster.status.shards[].primary.endpoint`(Ready만)에서 백엔드 해소 → **failover-aware**(operator가 replica 승격+status 갱신하면 라우터가 따라감). primary 부재/not-ready → 에러 ③ pg-router: `PGROUTER_BACKEND=env|template|status` 선택, **dial 타임아웃**, **우아한 PG `ErrorResponse`**(조용한 drop 금지) ④ `ClusterStatusReader` 인터페이스로 K8s 격리, refresh 루프가 토폴로지+status 동시 hot-reload ⑤ 매니페스트: RBAC에 `postgresclusters` 추가, deployment `PGROUTER_BACKEND=status`. **검증**: build/vet/test ✅ gofmt clean go.mod 무변경 + kustomize 렌더 ✅.
- **향후 대작업은 백로그로 기록**: `docs/sharding/ROUTER-GAP-ANALYSIS.ko.md §6` — (E) 프로토콜 종단(vtgate급), 읽기→replica, scatter-gather 실연결, reference table, resharding 데이터 이동, 2PC, 커넥션 풀링, stable primary Service, watch 기반 hot-reload, failover lease P2-T3, 성능 실측 등. 능력 사다리(§4)에 매핑.

### 6.4 백로그 배치 처리 (2026-06-26, 커밋됨)

"하나씩 다 처리" 요청으로 백로그를 진행. **검증·커밋 완료(7건)**:
1. `perf(router)` 커넥션 풀링 — SQLShardExecutor shard별 *sql.DB 캐시(per-call open/close 제거).
2. `feat(router)` 라우터 HA — 백엔드 dial retry/backoff + per-backend circuit-breaker(dial/clock 주입 검증).
3. `feat(router)` 읽기→replica *부품* — StatusBackendResolver.ResolveRead(round-robin) + IsReadOnlyQuery(보수적 분류).
4. `feat(router)` reference table — ShardRange.referenceTables(CRD) + ExtractTables/ReferenceOnly/AnyShard.
5. `fix(router)` scatter merge — 타입 인지 정렬(숫자 버그 수정) + Limit.
6. `feat(router)` **QueryRouter** 라우팅 결정 엔진 — extractor+토폴로지+reference+read/write+resolver 합성(E 핵심).

전부 build/vet/test ✅, gofmt clean, go.mod 무변경(reference table만 CRD/deepcopy/chart 재생성).

**보류(3건, 무검증 랜딩 금지)**: per-shard primary Service(#5, 운영자 failover 경로+라이브 검증), watch 기반 hot-reload(#7, polling 최적화·informer 단위검증 난), failover lease P2-T3(#9, 고위험 failover 경로 리팩터·라이브 chaos drill 필수). 사유는 태스크/백로그에 기록.

### 6.5 분산 SQL 라우터 구축 + query-mode 라이브 검증 (2026-06-27)

라우터를 척추로 분산 SQL(Vitess-for-Postgres)을 단계 구축. **전부 커밋·검증**:
- **vindex**: hash/range + **consistent-hash**(샤드 추가 시 키 ~29%만 이동, 링 캐시).
- **라우팅 키 추출**: 제로 의존성 토크나이저(regex/parser/auto) — 모호키 bail·dollar-quote·복합 predicate 정확 처리.
- **토폴로지/백엔드**: 교체 가능 provider(static↔CRD watch) + **failover-aware 백엔드**(status.primary Ready) + 읽기→replica + reference table + dial circuit-breaker.
- **QueryRouter** 결정엔진 + **query-mode 프록시**(`PGROUTER_MODE=query`): PG wire 프레이머 + trust 핸드셰이크 + 첫 쿼리 라우팅.
- **🎉 라이브 검증**: 2 trust postgres + pg-router → 쿼리 키로 올바른 샤드 라우팅 확인(`docs/sharding/ROUTER-GAP-ANALYSIS §6`, `docs/perf/baseline.md §3.0`).

### 6.6 앞으로의 개발 로드맵 (우선순위)

SSOT는 [ROUTER-GAP-ANALYSIS §4 능력 사다리 + §6 백로그](sharding/ROUTER-GAP-ANALYSIS.ko.md). 요약:

| 우선 | 항목 | 왜 / 블로커 |
|---|---|---|
| ~~·~~ ✅ | ~~scram 인증 대행~~ | **완료** — 프로덕션 PG(scram) 동작 |
| ~~·~~ ✅ | ~~describe-round 대행~~ | **완료** — lib/pq 파라미터 쿼리 → **실 DB 드라이버와 동작** |
| ~~·~~ ✅ | ~~멀티샤드 scatter forwarding~~ | **완료(2026-06-28)** — 병렬 fan-out + UNION ALL 병합, 라이브 검증 |
| ~~·~~ ✅ | ~~per-query 라우팅 (연결 종단 + 백엔드 풀)~~ | **완료(2026-06-28)** — `persession.go` 세션 루프가 *매* simple Query를 키의 샤드로 독립 라우팅(vtgate 모델) + 샤드별 백엔드 lazy 풀. 라이브 검증: 한 연결에서 alice→shard-0/bob→shard-1/carol→shard-0. scatter·단일샤드 tx pin 포함. |
| ~~·~~ ✅ | ~~per-query extended protocol~~ | **완료(2026-06-28)** — `extsession.go`. extended(Parse/Bind/…)도 Sync까지 버퍼링→배치 per-query 라우팅, ParseComplete 합성+**샤드별 prepare-on-first-use**+주입분 필터. 라이브: lib/pq 한 연결+prepared `WHERE id=$1` 5회 다른키→키별 정확 라우팅. 구 pin-on-first 제거. |
| ~~·~~ ✅ | ~~분산 성능 수치 1차~~ | **완료(2026-06-28)** — `cmd/router-bench` + baseline.md §3.0b. 라우터 점읽기 워커수×TPS 1761(w1)→9437(w32), 오버헤드 ~2.2~4×. **2샤드≈1샤드(point read)=라우터가 병목, 수평스케일은 멀티호스트 필요**. |
| ~~·~~ ✅ | ~~라우터 오버헤드 정량화~~ | **완료(2026-06-28)** — baseline §3.0c. prepared-stmt 재사용이 라우터 처리량 ~1.9×(9K→17K TPS) — 키당 Parse가 오버헤드 ~절반. |
| ~~·~~ ✅ | ~~수평 스케일 실증(단일 호스트)~~ | **완료(2026-06-28)** — baseline §3.0d. read/write 모든 워크로드 2샤드 ≤ 1샤드 확정. 단일 호스트 CPU·스토리지 공유라 물리적 불가 — 진짜 수치는 멀티머신 필요(방법 기록). |
| ~~·~~ ✅ | ~~reference table·read-replica 프록시 결선~~ | **완료(2026-06-28)** — main.go read resolver(env replica/status ResolveRead)+PGROUTER_REFERENCE_TABLES. 라이브: read→replica, ref→AnyShard, write→primary. |
| ~~·~~ ✅ | ~~resharding 실데이터 이동(core)~~ | **완료(2026-06-28)** — `CopyShardRange`(hash-range 필터 copy)+`DeleteShardRange`(cutover). 라이브: 1..100 split→44/56 overlap=0 키유실0. |
| ~~·~~ ✅ | ~~bufio 라우터 최적화~~ | **완료(2026-06-28)** — writeMessage 단일 write + 연결당 읽기 버퍼(bufConn). baseline §3.0e: unprepared +50%(8955→13391), prepared 1shard +34%. |
| ~~·~~ ✅ | ~~ShardSplitJob InitialCopy/Cleanup K8s 결선~~ | **완료(2026-06-28)** — InitialCopy(복사)+Cleanup(source 이동분 삭제)이 target 별 K8s Job(reshard-copy 이미지, 내부 trust 접속)으로 데이터 이동 + 완료 게이트. `shardsplitjob_copy.go`, envtest 검증. 데이터 경로(복사→cutover→회수) 닫힘. |
| ~~·~~ ✅ | ~~Cutover write-block~~ | **완료(2026-06-28)** — ShardRangeSpec.WriteBlocked 신호 → 라우터가 쓰기 거부(읽기 통과), Cutover 가 set·RoutingUpdate 가 clear. router 단위+envtest+라이브. simple-query 에러 hang 버그도 수정. |
| ~~·~~ ✅ | ~~ShardSplitJob full e2e (라이브)~~ | **완료(2026-06-28)** 🎉 — kind 실 K8s+실 PG: 단일샤드(키 1..100)→ShardSplitJob→Bootstrap(target rsd-t0/t1 부팅)→InitialCopy(스키마+데이터 복사 Job)→Cutover(write-block)→RoutingUpdate(ShardRange flip+unblock)→Cleanup(source 삭제 Job)→Completed. **결과 t0=44/t1=56/source=0, 합=100 키유실0, ShardRange flip, write-block 해제.** e2e 가 갭 2개 발견·수정(Job 이미지명 env RESHARD_COPY_IMAGE, 스키마 우선 복제). |
| ~~·~~ ✅ | ~~CDC 증분 catch-up 빌딩블록~~ | **완료(2026-06-28)** — `reshard_cdc.go`(논리복제 pub/sub/lag/teardown + DeleteForeignRange) + wal_level=logical. 라이브검증: 구독 이후 라이브 INSERT/UPDATE 까지 target 복제(유실0). |
| ~~·~~ ✅ | ~~CDCCatchup phase 컨트롤러 결선(online)~~ | **완료(2026-06-28)** — `spec.Online` 게이트. reconcileCDC: cdc-setup Job(pub/sub+lag≤CDCMaxLag) → write-block ON → cdc-finalize Job(drain+drop+filter-delete) → done. write-block 이 finalize 감싸 무중단. Job mode 일반화(copy/delete/cdc-setup/cdc-finalize). envtest(순서·env) + 라이브 메커니즘(TestCDCLive) 검증. |
| ~~·~~ ✅ | ~~online 모드 full e2e (라이브)~~ | **완료(2026-06-28)** 🎉 — kind 실 K8s+실 PG: spec.Online=true → CDCCatchup(cdc-setup Job pub/sub copy_data=true+lag대기 37s → write-block → cdc-finalize Job drain+drop+filter-delete+인덱스복제) → Cleanup → Completed. **t0=44/t1=56/source=0, kv_pkey 복제, ShardRange flip+unblock.** offline+online 양 경로 실 클러스터 검증 완료. |
| ~~·~~ ✅ | ~~외래키/체크 제약 복제~~ | **완료(2026-06-28)** — `ReplicateConstraints`(CHECK 엄격·FK best-effort) offline/online 양 경로. 라이브검증(kv_val_pos CHECK 복제). resharded 스키마 충실도 완성(컬럼+인덱스/PK+제약). |
| **1** | **target shard 영구 승격 (RFC급)** | transient target(rsd-<id>)을 cluster 의 영구 ordinal shard 로 편입 — status/failover/메트릭 인식. **ADR-0027 이 #220-class 정체성 혼동 방지로 의도적 격리** 했으므로 승격은 정체성-임계 설계 필요(ADR/RFC 선행 권장, 서두르지 말 것). |
| **2** | **동시 쓰기 무중단 실증(native 라우터)** | shardingMode=native(라우터)에서 클라 쓰기를 라우터 경유로 받아 write-block 이 실제 클라 쓰기를 막는 무중단 cutover 실증(online e2e 는 정적 데이터로 검증됨, 라이브 쓰기 포착은 TestCDCLive). |
| ~~·~~ ✅ | ~~resharding 운영화(인덱스 복제·이미지 env)~~ | **완료(2026-06-28)** — `ReplicateIndexes`(source pg_indexes → target IF NOT EXISTS, PK 백킹 unique index 포함, 데이터 복사 후) offline/online 양 경로 결선. config/manager RESHARD_COPY_IMAGE env. 라이브검증(TestCDCLive: kv_pkey 복제). |
| **2** | **target shard 영구 승격(ordinal 편입)** | resharding 완료 후 transient target(rsd-<id>)을 cluster 의 영구 ordinal shard 로 승격 — status/failover/메트릭이 인식하도록. 외래키/체크 제약 복제도 후속 |
| **3** | **멀티머신 수평 스케일 실측** | 진짜 "분산처리능력" 수치 — 물리 분리 노드 필요(router-bench 가 샤드별 DSN 받음, 그대로 적용) |
| **4** | **멀티 라우터 인스턴스 수평확장** | 라우터 1-hop 왕복이 남은 오버헤드(prepared direct 86K vs router 23K) — 라우터를 여러 개 띄워 처리량 확장 |
| **6** | 보류 #5/#7/#9 | per-shard primary Service·watch·failover lease P2-T3 — 라이브 failover 필요 |

> 제약 현황(query-mode 라이브): ✅ simple+extended per-query(prepared 포함)·scatter·단일샤드 tx·scram 백엔드·reference·read-replica. ❌ extended scatter·cross-shard 2PC·Flush 파이프라이닝.

### 6.7 2026-06-28 세션 요약 + 코드 리뷰 + 다음 진입점

**이 세션에서 한 일(커밋 다수, 미푸시 — 브랜치 `chore/ha-pitr-e2e-consolidation`)**: distributed
SQL 라우터 종단 + 온라인 resharding 을 코드+테스트+(상당수)라이브 e2e 로 완성.

- **라우터 종단(per-query)**: simple + extended/prepared per-query 라우팅(연결 고정 해소,
  `persession.go`/`extsession.go`, 샤드별 prepare-on-first-use), scatter, reference table,
  read-replica 결선. bufio 읽기버퍼+단일write 최적화. 라이브 검증 다수.
- **온라인 resharding (ShardSplitJob multi-phase 실 결선)**: Bootstrap(target STS) → InitialCopy
  (offline: hash-range copy Job) / CDCCatchup(online: 논리복제 subscription) → Cutover
  (write-block) → RoutingUpdate(ShardRange flip+unblock) → Cleanup(source 삭제 Job). 데이터+
  스키마+인덱스/PK+CHECK/FK 제약 복제. K8s Job 실행 모델(내부 trust 접속). **offline·online
  full e2e 둘 다 kind 실 K8s+PG 성공**(t0=44/t1=56/source=0 키유실0). CDC 메커니즘은 `TestCDCLive`
  로 라이브 쓰기 유실0 증명.
- **성능**: `cmd/router-bench`(prepared/멀티라우터/모드). baseline §3.0b~f. 단일 호스트는 자원
  공유로 수평 스케일 미관측(물리 한계 확정) — 진짜 수치는 멀티머신 필요.
- **target 승격**: 정체성-임계라 **ADR-0029 설계** 후 **P-A(shard-id label 토대, additive)** 만
  구현. P-A.2/P-B/P-C 는 미착수.

**코드 리뷰 발견(테스트 미실행, 정독)**:
- ✅ **수정함**: `CreatePublication` DROP+CREATE → 멱등(skip-if-exists). 재시도 시 활성 sub
  의존 pub drop 방지(커밋 `0e6f043`).
- ⚠️ **운영 영향**: `wal_level=replica→logical`(builders.go) 는 *기존 클러스터에 operator 업그레이드
  시 1회 rolling restart* 유발(config-hash 변경). HA(replicas>0)가 가려주나 단일샤드(replicas=0)
  는 짧은 중단. CHANGELOG/릴리스노트 명시 필요.
- ⚠️ **미검증 경로**: online CDC 에서 *동시 쓰기* 중 PK-없는 target(PK 는 cdc-finalize 에서 추가)
  로의 UPDATE/DELETE 논리복제 — seq-scan 으로 동작하나 미검증(online e2e 는 정적 데이터, TestCDCLive
  는 target 에 PK 선존재). native 라우터 동시쓰기 e2e 필요(아래 #2).
- ⚠️ **abort 누수**: online resharding 이 cdc-setup 후 실패하면 source 의 pub/sub/replication-slot
  이 누수(slot 이 WAL 보존 → 디스크 bloat). abort 핸들러(sub/pub drop) 후속 필요.
- ⚠️ **FK best-effort**: cross-shard FK 는 의도적으로 skip(추가 실패 무시) — FK 강제 기대 사용자에
  주의. 코드 주석에 명시.
- ✅ **안전 확인**: shard-id label 은 셀렉터 미포함(additive)이라 업그레이드 race 없음. bufConn 은
  auth 후 wrapping(over-read 없음)+쓰기 직행(deadlock 없음). write-block 에러 경로는 ReadyForQuery
  동반(hang 버그 수정됨). 전 식별자 화이트리스트(SQL injection 안전). offline 경로는 mode 일반화
  후 envtest 로 커버(job 이름/env 불변).

**다음 작업 진입점(우선순위)**:
1. **ADR-0029 P-B 잔여(target 승격 안전성)**: P-A.2 selector/status 일반화와 Promote target-adopt
   slice 는 완료됐다. 다음은 Promote phase 의 precondition/fence 강화, source PDB/resource 삭제 정책,
   source decommission 멱등성, 그리고 **#220-class 정체성 위험**을 확인하는 라이브 chaos drill
   (승격 중 pod kill) 이다. 설계는 `docs/kb/adr/0029-*.md`.
2. **native 라우터 동시쓰기 무중단 e2e**: shardingMode=native(라우터 배포)에서 클라 쓰기를 라우터
   경유로 받아 write-block 이 실제 쓰기를 막는 무중단 cutover 실증 + 위 PK-없는-target 동시쓰기 경로
   검증.
3. **abort 누수 정리**: online 실패/abort 시 pub/sub/slot drop.
4. 멀티머신 수평 스케일 실측(하드웨어), 보류 #5/#7/#9(라이브 failover).

**라이브 e2e 재현 요약(kind, 다른 머신)**:
```bash
# 1) kind + 이미지
kind create cluster --name pgop-dev
docker build -t pgop:dev . ; docker build -t reshard-copy:dev -f Dockerfile.reshard .
kind load docker-image pgop:dev reshard-copy:dev --name pgop-dev
# 2) operator 배포 + 이미지/env
kubectl apply -k config/default --server-side
kubectl -n postgres-operator-system set image deploy/postgres-operator-controller-manager manager=pgop:dev
kubectl -n postgres-operator-system set env deploy/postgres-operator-controller-manager RESHARD_COPY_IMAGE=reshard-copy:dev
# 3) 단일샤드 cluster + 데이터 → ShardRange + ShardSplitJob(online:true) 적용 후 phase watch
#    (정적 데이터면 offline/online 모두 t0/t1 분할 + source=0 검증)
# CDC 메커니즘 단위: 2 PG `postgres:18 -c wal_level=logical` + RESHARD_LIVE_SOURCE/TARGET/CONNINFO
#    env + go test ./internal/router/ -run TestCDCLive
# 통합(envtest): make test-integration (또는 setup-envtest + KUBEBUILDER_ASSETS 절대경로)
```

### 6.8 다음 세션 실행 정책 (2026-06-28)

이번 후속 작업은 **자원 절약 모드**로 진행한다. Docker Desktop / WSL / 로컬 VM 은 개발 중에는
꺼 둔다. kind, Docker 이미지 빌드, 라이브 PG e2e 는 명시적인 검증 체크포인트에서만 다시 켠다.

작업 순서는 `docs/superpowers/plans/2026-06-28-reshard-hardening.md` 를 따른다. 핵심 원칙:

1. **개발 먼저, 테스트는 묶어서**: 작은 편집마다 테스트를 돌리지 않는다. Batch 0/1/2 단위로 코드를
   완성한 뒤 한 번에 검증한다.
2. **먼저 닫을 위험**: reference table 쓰기 단일 shard 라우팅 방지, keyless write scatter 금지,
   scatter error 경로 ReadyForQuery 보장, reshard-copy Job 의 vindex type 보존.
3. **그 다음 운영 안전장치**: online resharding 실패/abort 시 pub/sub/replication-slot 누수 정리.
4. **그 다음 ADR-0029 잔여**: selector/status 일반화와 Promote target-adopt 이후에는
   precondition/fence 강화, source cleanup 정책, named shard spec-model 전환으로 들어간다.
   #220-class 정체성 위험 때문에 live chaos drill 전에는 GA 완료로 보지 않는다.
5. **검증 체크포인트**: 개발 중에는 저비용 smoke 만 수행하고, 기능 단위가 닫히면 Docker/kind 를 켜서
   통합·라이브 검증을 묶어서 실행한다. 테스트 결과는 브리핑하고, `KEEP=1` 을 명시하지 않는 한 kind
   클러스터는 target 종료 시 반납한다.

2026-06-28 현재 진행 상태:

- Batch 0 개발 완료: reference table write 단일 shard 라우팅 방지, keyless write scatter 거부,
  scatter error `ReadyForQuery` 보장, `PGROUTER_VINDEX_TYPE` 전달/보존, 관련 focused tests 추가.
- Batch 1 개발 완료: online resharding terminal `Failed`/`Aborted` 경로에서 `cdc-abort` Job 으로
  subscription/publication 정리, 성공 후 write-block 해제, 실패 시 `AbortCleanup=False` condition 기록.
- Batch 2 P-A.2 개발 완료: `aggregateShardStatus` 가 legacy `postgres.keiailab.io/shard=<ord>` 와
  additive `postgres.keiailab.io/shard-id=shard-<ord>` 를 OR 필터링한다. StatefulSet/Service selector 는
  변경하지 않았다. metrics/failover 는 status 소비자라 aggregation 변경을 따라간다.
- Batch 2.5 개발 완료: reshard target Pod 가 status endpoint 를 source ordinal service 로 잘못 보고하지
  않도록 컨트롤러가 실제 StatefulSet service name 을 `POSTGRES_SERVICE_NAME` 으로 주입하고,
  instance manager 가 그 값을 endpoint 조립에 사용한다. env var 가 없는 기존 Pod 는 기존 ordinal
  service naming 으로 fallback 한다. 이 작업은 Promote P-B 전제 조건이며, target status/backend
  resolution 의 DNS 오염을 막는다.
- Batch 2.6 개발 완료: active `ShardRange.spec.ranges[].shard` 중 non-ordinal 이름을
  `PostgresCluster.status.shards[]` 에 추가한다. reshard target 은 `reshard-target=<id>` 또는
  adopt 이후 `shard-id=<id>` label 로 집계되며, 예: `name=t1`, `ordinal=-1`. 이로써 routing flip 이후
  router status backend 가 target primary endpoint 를 해석할 수 있다.
- Batch 2.7 개발 완료: native cluster 에 ShardRange 가 존재하면 그 ranges 의 union 을 active topology 로
  보고, active set 에서 빠진 ordinal source STS 는 replicas=0 으로 낮추며 status.shards 에서 제외한다.
  target 이 Ready 일 때 stale source fallback row 때문에 cluster 가 Provisioning/Degraded 에 묶이는
  문제를 해소했다. StatefulSet/PVC 삭제는 하지 않는다.
- Batch 2.8 개발 완료: active topology 에 포함된 non-ordinal target 은 PostgresCluster reconciler 가
  ConfigMap/Service/StatefulSet 을 유지하고, target STS replicas 를 `1 + spec.shards.replicas` 로 맞춘다.
  target replica 는 `POSTGRES_MEMBER_COUNT` 와 target primary `PRIMARY_ENDPOINT` 를 받아 basebackup 경로로
  들어갈 수 있다. hibernation/restore 중에는 active target 도 replicas=0 으로 내려간다.
- Batch 2.9 개발 완료: ShardSplitJob status phase 에 `Promote` 를 추가하고 state machine 을
  `Cleanup -> Promote -> Completed` 로 확장했다. Promote 는 target StatefulSet object label,
  Pod template label, live target Pod label 에 `postgres.keiailab.io/shard-id=<target>` 을 붙인다.
  Service/StatefulSet selector 는 계속 `postgres.keiailab.io/reshard-target=<target>` 에 고정해
  selector 불변성 및 target lease 격리를 유지한다. source StatefulSet/Service/PVC/PDB 삭제는 하지 않는다.
- Batch 2.10 개발 완료: Promote 는 matching `ShardRange` active set 을 먼저 확인한다. 모든 source 가
  active set 에서 빠지고 모든 target 이 active set 에 들어온 뒤에만 target adopt 를 수행한다. source 가
  아직 active 이면 phase 를 `Promote` 로 유지하고 requeue 하며 target StatefulSet/template/live Pod 에
  `shard-id` 를 붙이지 않는다.
- Batch 2.11 개발 완료: Promote 는 target Pod readiness 도 확인한다. target shard 별로
  `reshard-target=<id>` selector 에 걸리는 Pod 가 최소 1개 있어야 하고, 그중 하나 이상이
  `phase=Running`, `PodReady=True` 여야 target adopt 를 수행한다. target 이 아직 not Ready 이면 phase 를
  `Promote` 로 유지하고 requeue 하며 label mutation 을 하지 않는다.
- Batch 2.12 정책 고정: source resource cleanup 기본값은 retain-by-default 다. inactive ordinal source 는
  StatefulSet replicas=0 과 status 관측 제외까지만 수행하고, source Service, pre-existing PDB, PVC 는 자동
  삭제하지 않는다. destructive source deletion 은 향후 별도 opt-in 정책과 live drill 뒤에만 다룬다.
- Batch 2.13 설계 결정 완료: named shard topology 는 `ShardRange` 를 SSOT 로 유지한다.
  `PostgresCluster.spec.shards.initialCount` 는 bootstrap ordinal seed count 로 남기고, native cluster 에
  `ShardRange` 가 존재하면 active topology 는 `ShardRange.spec.ranges[].shard` 의 union 에서 온다.
  별도 `spec.shards.named[]` 는 추가하지 않는다.
- Batch 3 설계 문서 완료: native router concurrent-write e2e 시나리오를
  `docs/sharding/ROUTER-GAP-ANALYSIS.ko.md` 에 기록했다. write stream 중 online CDC, write-block
  `ReadyForQuery`, routing update, checksum/key ownership, PK 없는 target UPDATE/DELETE, abort cleanup 을
  한 live gate 로 검증한다.
- 아직 남은 범위: destructive source deletion 은 기본 동작이 아니라 향후 opt-in 정책으로만 검토한다.
  live chaos/e2e 검증은 여전히 필요하다.
- 라이브 검증 전 주의: `cdc-abort` 는 `DROP SUBSCRIPTION IF EXISTS` 로 원격 replication slot 정리까지
  시도한다. source 접속 불가 상황에서는 cleanup Job 이 실패하고 `AbortCleanup=False` 로 남는 것이 현재
  의도한 안전 동작이다. source-down 상태에서도 target subscription 만 강제 제거하는 fallback 은 live drill
  결과를 보고 별도 보강한다.
- 검증 상태: Docker Desktop / WSL / VM 은 종료 유지. Windows Go 1.26.4 로 다음 focused gate 를 통과했다:
  `go test -count=1 ./cmd/instance`,
  `go test -count=1 ./internal/controller -run TestBuildTargetShardStatefulSet_Isolation`,
  `go test -count=1 ./internal/controller -run TestAggregateNamedShardStatus_UsesReshardTargetLabel`,
  `go test -count=1 ./internal/controller --ginkgo.focus="adds active named reshard targets"`,
  `go test -count=1 ./api/v1alpha1 ./internal/controller -run "TestShardSplitJob|TestShardSplitJob_nextPhase"`,
  `go test -count=1 ./internal/controller --ginkgo.focus="not Ready"`,
  `go test -count=1 ./internal/controller --ginkgo.focus="Promote phase"`,
  `go test -count=1 ./internal/controller --ginkgo.focus="source active"`,
  `go test -count=1 ./internal/controller`,
  `go test -count=1 ./cmd/instance ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./api/v1alpha1 ./internal/controller`.
- Windows 로컬 테스트 wrapper(`scripts/test-windows.ps1`)는 개발 중 빠른 smoke 용도다. 최종 수용
  검증은 Docker/kind 또는 Dev Container 경로에서 묶어서 수행한다. wrapper 는 기본적으로 Go test cache 를
  살리고, `-Fresh` 를 줄 때만 `-count=1` 을 붙인다. `GOTMPDIR` / `GOCACHE` 는 repo 밖
  `%LOCALAPPDATA%\keiailab\postgres-operator\...` 로 고정해 `*.test.exe` 가 workspace 에 남지 않게 한다.
  smoke 예:

  ```powershell
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -GinkgoFocus "Promote phase"
  ```

  Windows Defender 가 `controller.test.exe` 같은 Go test 임시 실행 파일을 반복 검사해 병목을 만들면,
  관리자 PowerShell 에서 다음을 한 번 실행한다. repo 전체가 아니라 wrapper 가 사용하는 repo 외부 temp/cache
  디렉터리만 예외 처리한다:

  ```powershell
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\allow-windows-test-exe.ps1
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\allow-windows-test-exe.ps1 -Check
  ```

### 6.9 현재 미구현 / 부분구현 재정리 (2026-06-29)

- **테스트 자원 운영 정책**: 개발 중에는 Windows wrapper 같은 저비용 smoke 만 사용하고, 기능 단위가
  닫힌 뒤 Docker/kind/Dev Container 로 통합·라이브 검증을 묶어서 수행한다. e2e Make target 은
  `KEEP=1` 을 주지 않는 한 종료 시 `cleanup-test-e2e` 로 kind 클러스터를 반납한다. 테스트 완료 후에는
  pass/fail, 실패 RCA, 남은 위험, 정리된 자원 상태를 브리핑한다.
- **Router autoscaling (CPU HPA 구현 완료, 2026-06-29)**: `PostgresClusterReconciler` 가 router
  `ConfigMap` / `Service` / `Deployment` 에 더해 **`HorizontalPodAutoscaler`** 도 reconcile 한다.
  `spec.router.autoscale.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage}` →
  `buildRouterHPA`(autoscaling/v2, CPU utilization target) → `routerAutoscaleEnabled` gate 에서 upsert,
  비활성/DB 정지 시 `deleteRouterHPA`. `Owns(HorizontalPodAutoscaler)` watch, autoscaling/v2 RBAC
  (`config/rbac/role.yaml`), webhook bounds 검증(`maxReplicas>0`, `maxReplicas≥effective minReplicas`)까지
  결선·단위테스트 완료. **남은 것**: active-connection custom metric 노출/어댑터(현재는 CPU utilization
  기준). 미설정 시 기존대로 `spec.router.replicas` 수동 scale.
- **AutoSplit / 자동 shard 확장**: `spec.autoSplit` 스키마와 admission 검증은 있으나, shard size/latency/CPU
  관측, 지속시간 판정, 후보 계산, `ShardSplitJob` 자동 생성 루프는 미구현이다.
- **남은 live gate**: native router concurrent-write online resharding e2e, target promotion 후 live
  chaos/failover drill, source-down abort cleanup fallback 검증은 아직 남아 있다.
- **의도적 보류**: source Service/PVC/PDB 삭제는 기본 동작이 아니며, 향후 별도 opt-in 정책과 live drill 후에만
  검토한다. cross-shard 2PC, extended scatter, Flush 파이프라이닝도 아직 범위 밖이다.

---

## 7. 용어집

> 정의는 [GLOSSARY.ko.md](GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| Failover (장애 조치) | Primary 장애 감지 후 Replica 하나를 새 Primary로 자동 승격해 서비스를 잇는 동작. |
| Promotion (승격) | Replica를 Primary로 올리는 행위. 본 operator는 `pg_promote()`(SQL)로 수행. |
| Fencing (PVC Fencing) | 옛/이상 Primary가 데이터에 쓰지 못하도록 PVC 접근을 차단해 split-brain을 막는 격리. |
| PITR (Point-In-Time Recovery) | WAL을 재생해 데이터베이스를 특정 과거 시점으로 복원하는 기법. |
| WAL (Write-Ahead Log) | 변경을 먼저 기록하는 PostgreSQL의 로그. 복제·PITR의 기반. |
| RTO (Recovery Time Objective) | 장애에서 서비스 복구까지 허용되는 목표 시간. 본 프로젝트 failover 드릴 기준 30초. |
| DinD / DooD | Docker-in-Docker(컨테이너 안에서 또 데몬) / Docker-out-of-Docker(호스트 데몬 공유). |
| envtest | 실제 클러스터 없이 API 서버/etcd만 띄워 컨트롤러를 통합 테스트하는 도구. |
| SSOT (Single Source of Truth) | 한 사실을 한 곳에만 두고 나머지는 링크/발췌하는 단일 출처 원칙. |
| RCA (Root Cause Analysis) | 장애·실패의 근본 원인 분석. |
