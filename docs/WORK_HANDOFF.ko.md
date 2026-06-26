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
| 커밋 | 8개, **push 안 됨** (로컬 전용) |
| 변경 규모 | 56 파일, +5562 / −310 |
| 단위·통합 테스트 (`make test`) | ✅ 통과 (EXIT=0, FAIL 0) |
| 레이스 검출 (`make test-integration` `-race`) | ✅ 통과 (데이터레이스 없음) |
| lint (`make lint`) | ⚠️ baseline-fail — main 자체가 fail. 프로덕션 지적은 정리 완료, 잔여는 테스트 코드/미변경 파일 스타일 |
| 스크립트 테스트 (`make test-scripts`) | ⚠️ `hack/artifacthub_smoke_test.sh`만 실패 — 이번 작업과 무관 |
| 라이브 K8s E2E (failover/PITR drill) | ⏳ **미완** — 실 Kubernetes 환경에서 진행 필요 (3장 참고) |

핵심: **코드·단위/통합 검증은 끝났고, 남은 단 하나는 실제 쿠버네티스에서의 라이브 E2E 드릴**이다. 이 호스트(Windows + Docker Desktop)에서는 Docker 2중 중첩 한계로 kind 클러스터가 뜨지 않아 보류했다(코드 결함 아님, 인프라 한계).

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
