<p align="center">
  <a href="ROADMAP.md">English</a> |
  <b>한국어</b> |
  <a href="ROADMAP.ja.md">日本語</a> |
  <a href="ROADMAP.zh.md">中文</a>
</p>

# ROADMAP — postgres-operator (한국어)

> 영문 원본: [ROADMAP.md](ROADMAP.md) — canonical / 정본

본 ROADMAP 은 검증 가능한 Gate 와 sub-task 체크리스트로 진행을 추적한다 — *날짜 약속이 아니다*. 프로젝트 정체성은 **MIT-licensed PostgreSQL Kubernetes Operator**. 외부 PostgreSQL operator runtime 을 fork / embed / wrap 하지 않고 production-grade 운영 품질을 목표로 한다.

## 체크박스 의미

| 표기 | 의미 |
|---|---|
| `[x]` | 코드 **및** 테스트 존재; e2e 또는 unit 테스트가 회귀 방지. |
| `[~]` | 부분 — 예: CRD 필드만, helper 미연결, 또는 e2e 누락. |
| `[ ]` | 미착수 (설계 또는 PoC 만). |

각 sub-task 의 *Verify* 행은 검증 명령 또는 e2e 파일을 인용한다.

## 원칙

- **외부 시스템은 본 제품 내부로 출하 금지** — 외부 PostgreSQL operator / sharding extension / HA agent runtime / 3rd-party DB 백엔드는 runtime artifact 에서 제외.
- **신규 서비스로 구현** — operator manager, instance manager, sharding 메타데이터, router, backup orchestration 은 본 저장소 내에서 permissive-license-compatible 의존성으로 구현.
- **품질 기준** — HA / backup / restore / upgrade / observability / security UX 의 *목표 수준* 은 특정 3rd-party 제품과 무관하게 약속한다.

## 현 상태 스냅샷

| 항목 | 상태 | Evidence |
|---|---|---|
| 프로젝트 / chart 이름 | `postgres-operator` | GitHub repo, Helm chart, GitOps path 모두 정합 |
| 라이선스 | MIT | `LICENSE`, ADR-0003 |
| 최신 릴리스 | `0.4.0-beta.1` | GHCR 이미지 + Helm chart publish + OLM bundle — Level 4 Deep Insights |
| OLM bundle | `bundle/manifests/` 가 8 CRD + alm-examples + CSV description 과 정합 | `operator-sdk bundle validate --select-optional suite=operatorframework` clean (T26) |
| 선언적 DB 표면 | Pooler / PostgresDatabase / PostgresUser / ScheduledBackup / ImageCatalog / ClusterImageCatalog / externalClusters / replica cluster | T22 / T24 / T25 cycle 완료; live kind smoke 자동화 (T27) 진행 중 |
| 로컬 4-layer gate | L1 lefthook pre-commit + L2 pre-push + L3 make validate/audit + L4 PR evidence | ADR-0009 / RFC-0002; version-drift assertion 및 bundle validate 자동화 (T26) |
| Production 배포 | Day-0 single-shard | `PostgresCluster/postgres` Ready |
| GHCR runtime image | 공개 pull 가능 | `ghcr.io/keiailab/pg:18` 가 pull secret 없이 restart |
| HA replicas | Partial (`Replicas` 필드만) | `api/v1alpha1/postgrescluster_types.go` |
| Backup / restore | 부분 구현 | `BackupJob` phase transitions + `ScheduledBackup` CRD/controller + `RestorePIT` call path + pgBackRest command-runner plugin + K8s sidecar exec path. 실 restore drill 은 pending. |
| 1.0.0 GA | 아직 | HA / backup / chaos / soak 필요 |

## Gate 계획

### Gate G0 — Day-0 배포 (~100% buffer)

**목표**: 사용자가 operator + single-shard Postgres 클러스터를 GitOps 로 배포 가능.

- [x] CRD `PostgresCluster` 정의 — `api/v1alpha1/postgrescluster_types.go` (RFC-0001 v2 schema).
- [x] CRD `BackupJob` 정의 (Phase 1 spec) — `api/v1alpha1/backupjob_types.go`.
- [x] `PostgresClusterReconciler` 가 desired state 구성 (ConfigMap / headless Service / StatefulSet) — `internal/controller/postgrescluster_controller.go`.
- [x] Status phase transition (Provisioning → Ready) — `internal/controller/status.go`, `aggregate_status.go`.
- [x] Pod readiness 추적 — reconciler endpoint watch.
- [x] ArgoCD `Synced/Healthy` — production 에서 검증 (`platform-data-postgres-operator`).
- [x] GHCR 공개 pull — `ghcr.io/keiailab/pg:18` 가 pull secret 없이 restart.
- [x] Day-0 e2e — `test/e2e/e2e_test.go`, `postgrescluster_e2e_test.go`.
- Verify: ArgoCD `Synced/Healthy` + Pod `1/1` Running + `psql -c 'select version()'`.

### Gate G1 — Single-shard production HA (~30% buffer)

**목표**: HA 를 갖춘 single-PostgreSQL production 데이터베이스로 사용 가능.

- [x] `Replicas` 필드 (0~15 async replica) — `postgrescluster_types.go`.
- [x] STS scale 매핑 — reconciler.
- [x] Primary-delete e2e baseline — `test/e2e/failover_e2e_test.go`.
- [x] 자동 PDB 생성 — `internal/controller/pdb.go`.
- [~] PVC fencing (split-brain fail-fast) — fencing skeleton 만; runbook automation pending.
- [ ] **자동 failover 로직** — 신규 디렉토리 `internal/controller/failover/`.
  - [x] Primary 장애 감지 — `internal/controller/failover/detection.go` (`DetectPrimaryFailure` + `SelectPromotionCandidate`, 순수 함수, 4 `FailureReason` enum, 9 unit test, PR #38).
  - [x] Standby 승격 (`pg_ctl promote` 또는 logical-replication 승격) — `internal/controller/failover/promotion.go` (`BuildPromotionPlan` + `Promoter` interface + `PromoteFromDecision` helper, 4-step plan: RemoveStandbySignal / PgCtlPromote / WaitNotInRecovery / UpdateInstanceRole; 6 unit test; PR #39). `internal/controller/failover_promoter.go` 가 replica Pod 의 `postgres` 컨테이너 exec 와 승격된 `instance-status` annotation patch 를 구현.
  - [x] Post-Ready primary-failure status 노출 — `status.phase=Degraded` + `FailoverReady=False` + promotion-candidate 메시지.
  - [x] Replica rejoin (`pg_basebackup` 또는 `pg_rewind`) — first-boot `pg_basebackup` + 기존 PGDATA old-primary marker 일반화 + 현재 primary endpoint main env + `pg_rewind` command-runner + HBA normal-connection auth + fresh `pg_basebackup` fallback 완료. **Live A.1 basebackup drill PASS (T31, 2026-05-17, commits 09abbb5/dca3fa0)**: `quickstart-shard-0-1` standby PVC delete + in-pod PGDATA wipe + Pod kill → reconciler init container 가 fresh `pg_basebackup` 실행 → `pg_stat_replication{application_name=quickstart-shard-0-1, state=streaming, sync_state=async, lag=0}` 회복. STS PVC retention `Retain` 회피 path 까지 evidence. A.2 pg_rewind live drill 은 별 task (SMOKE_FAILOVER operator-driven promotion live trigger 회귀 — `docs/g1-ha-election-fact-fix` 영역 위임).
  - [x] Synchronous replication — `spec.postgresql.synchronous.{method,number,dataDurability}` + CEL `number<=shards.replicas` + `ANY/FIRST N (...)` rendering + `required/preferred` quorum policy + standby `application_name` wiring + ConfigMap-hash rolling reconcile 모두 완료. **Live B.1~B.3 RPO=0 drill PASS (T31, 2026-05-17, commit dca3fa0)**: `synchronous_standby_names='ANY 1 ("quickstart-shard-0-1","quickstart-shard-0-0")'` 적용 → `sync/quorum replica count=1` → 1000-row commit 후 `commit_lsn=0/3DA43A0 / flush_lsn=0/3DA43A0` (`pg_wal_lsn_diff=0`) → **RPO=0 직접 증명**. drill 함수: `hack/smoke.sh::drill_sync` (SMOKE_SYNC=1). B.4 sync standby kill scenario 는 opt-in (`SMOKE_SYNC_KILL=1`).
  - [~] HA election distributed lock (K8s Lease) — `internal/controller/failover/lease.go` (`FailoverLeaseName` + `LeaseConfig` + `NewLease`/`Run`/`IsLeader`, §2 Simplicity 에 따라 `internal/instance/election.Real` 의 thin adapter; fake clientset 으로 single-leader + handoff 를 검증하는 2 unit test). Live e2e multi-replica failover drill 은 cluster mesh restore 후 pending.
- [ ] **Backup / restore controller 구현** — `internal/controller/backupjob_controller.go` 보강.
  - [x] `BackupJob.Phase` transition (Pending → Running → Succeeded/Failed) — `internal/controller/backupjob_controller.go` reconcile switch + 8 unit test.
  - [x] `ScheduledBackup` CRD / controller — 6-field cron schedule → atomic `BackupJob` 생성; `suspend` / `immediate` / `ownerReference` / `concurrency` guard; 5 unit test.
  - [x] `BackupJob.spec.type=restore` → `BackupPlugin.RestorePIT(targetTime)` call path + required `targetTime` validation.
  - [x] `BackupJob.spec.executionMode=job` → owned `batch/v1.Job` 생성 + observe; `jobTemplate` 표준 env injection.
  - [~] Plugin 호출 — pgBackRest command-runner + sidecar 명령 계획 완료. WAL-G / Barman pending.
  - [x] Sidecar mode 분기 — pgBackRest argv 를 K8s `pods/exec` 를 통해 ready primary Pod 의 `postgres` 컨테이너로 전달.
- [~] **PITR restore** — `BackupRestoreSpec.TargetTime` 기반 pgBackRest `restore --type=time --target=...` call path + sidecar exec path 모두 존재. 실 restore + checksum drill 은 pending.
- [x] **Upgrade rollback runbook** — `docs/runbooks/upgrade.md` (stub: pre-upgrade 체크 + ImageCatalog 단계 + rollback) (PR #54)
- [x] **RTO / RPO 측정 + 기록** — `docs/runbooks/ha.md` (SLO RTO≤60s + RPO=0 + verify 단계) (PR #54)
- Verify: primary delete 후 N 초 이내 replica 승격 + `pg_is_in_recovery()=false` + 0 데이터 손실; fresh-cluster restore 후 데이터 checksum 일치.

### Gate G2 — 운영 품질 (~25% buffer)

**목표**: production-grade 운영 표면 커버.

- [x] `/metrics` baseline 노출 (port 8443) — `internal/controller/metrics.go`, `cmd/main.go`.
- [x] TLS path 설정 (certificate mount + `ssl=on`) — `internal/controller/builders.go:renderPostgresConf()`, `tls.go`.
- [x] Topology spread 통합 — `internal/controller/topology_spread.go`.
- [x] PVC online resize — `internal/controller/pvc_resize.go`.
- [x] Cascade-delete guard — `internal/controller/cascade_delete_test.go`.
- [~] cert-manager 통합 — mount path 만; 발급 메커니즘 TBD.
- [~] **자동 PrometheusRule 생성** — Helm metrics Service / ServiceMonitor / PrometheusRule rendering + 실 `postgres_operator_backupjob_phase` 메트릭 기반 BackupJob failure alert.
  - [x] Replication-lag 경고 — instance status `LagBytes` → `postgres_operator_postgrescluster_replication_lag_bytes` + Helm `PostgresReplicationLagHigh`.
  - [x] Pooler failure / saturation 경고 — `postgres_operator_pooler_phase{phase="Failed"}` + PgBouncer exporter 메트릭 기반 collection-failure / client-waiting / max-wait alert rendering 검증.
  - [x] 디스크 압박 — `kubelet_volume_stats_*` data-PVC alert.
  - [x] Backup 실패 — `postgres_operator_backupjob_phase{phase="Failed"}`.
- [~] **Grafana 대시보드** — Helm 대시보드 ConfigMap rendering 완료 (`postgres-operator-cluster-overview.json`, `postgres-operator-pooler.json`); live Grafana import / panel 검증은 pending.
- [~] **Connection pooler (PgBouncer)** — `Pooler` CRD + ConfigMap / Deployment / Service reconcile (first slice).
  - [x] CRD `Pooler.spec.{cluster, instances, type, pgbouncer.poolMode, pgbouncer.parameters}` 추가.
  - [x] 분리된 PgBouncer Deployment / Service / ConfigMap 생성 + `userlist.txt` Secret fail-closed validation.
  - [x] 기본 PgBouncer readiness / liveness / startup probe + exporter `/metrics` readiness / liveness probe.
  - [x] PgBouncer 파라미터 allowlist + operator-owned-key fail-closed validation.
  - [x] `instances > 1` 시 자동 topology spread + PodDisruptionBudget.
  - [x] 더 강한 rolling-update 기본값 — `maxUnavailable=0`, `maxSurge=1`, `minReadySeconds=5`.
  - [x] Pooler parity 표면 — `deploymentStrategy`, `serviceAccountName`, status `backendTargets/configHash`.
  - [x] `pg_hba` → PgBouncer `pg_hba.conf` rendering + operator-owned validation of `auth_type=hba` / `auth_hba_file`.
  - [x] 사용자 제공 server / client TLS Secret rendering + Secret/key fail-closed validation.
  - [x] `type=ro` full ready-replica host-list rendering + `server_round_robin=1` + `server_login_retry=2` 기본값.
  - [~] PgBouncer exporter — 명시적 sidecar + `metrics` ServicePort + PodMonitor selector label/sample + PgBouncer metric prefix 의 PrometheusRule alert render 검증; live Prometheus scrape / Grafana 검증은 pending.
  - [x] **Built-in auth 사용자 자동화** (T27 ⑤) — `authSecretRef` 비어 있을 때 `keiailab_pooler_pgbouncer` LOGIN role + `<pooler-name>-builtin-auth` Secret 자동 프로비저닝.
  - [x] **Built-in auth 비밀번호 rotation** (T27 ⑥) — `postgres.keiailab.io/rotate-pooler-password=true` annotation 이 in-place `ALTER ROLE` + Secret update + status timestamp 를 trigger; ConfigHash 가 userlist 를 포함해 자동 reload.
  - [ ] Built-in TLS 자동 발급 (T29).
  - [x] Paused PAUSE/RESUME reconciliation — `spec.paused` → PgBouncer `SIGUSR1/SIGUSR2`, `status.paused`, Pod annotation audit.
  - [x] Pooler Service `psql` smoke — 2026-05-12 `SMOKE_POOLER=1 ./hack/smoke.sh --keep` 이 kind 에서 통과 (`quickstart` + Pooler Service `SELECT 1 = 1`, PAUSE 가 timeout 으로 신규 client 차단, RESUME 이 `SELECT 1 = 1` 재활성화, Deployment `2/2`).
  - [x] In-place PgBouncer config reload — `pgbouncer.parameters` patch 가 ConfigMap `config.sha256` projection 을 대기 → ready Pod 에 `SIGHUP` 전송 → Pod hash annotation audit 하며 Deployment generation 및 Pod 이름 보존.
- [ ] **User / DB / RBAC 선언적**.
  - [~] CRD `PostgresDatabase` — `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready-primary `psql` reconcile + `status.applied` + `databaseReclaimPolicy=delete` finalizer + database/schema privilege grant/revoke 구현. Live smoke / retain-policy 검증 pending.
  - [~] CRD `PostgresUser` — `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready-primary `psql` reconcile + `status.applied/passwordSecretResourceVersion` 구현; membership `REVOKE` + password Secret username 일치 + `disablePassword` fail-closed + referenced-Secret update watch + `PostgresCluster.status.managedRolesStatus` aggregation 완료. Live smoke + password-rotation SQL round-trip 은 pending.
  - [~] Role/permission reconcile — `PostgresUser` role flag + membership `GRANT/REVOKE` + cluster-level managed-role status (first slice) 완료; database-object privilege 모델은 pending.
- [ ] **Upgrade smoke** — `test/e2e/version_upgrade_e2e_test.go` 확장 (skeleton 이미 있음).
- [ ] **Security 기본값 강화** — restricted PSA, NetworkPolicy 기본 on.
- [~] **ImageCatalog / ClusterImageCatalog** — CRD + `spec.imageCatalogRef.{apiGroup,kind,name,major}` + catalog 이미지 → StatefulSet init/main 컨테이너 이미지 + image-hash annotation rollout-drift 추적 + catalog watch / envtest 완료. Extension-image volume mount, official digest catalog 공급, live rollout 측정은 pending.
- [~] **Replica cluster / externalClusters** — `externalClusters[].connectionParameters` + `password` + `sslKey/sslCert/sslRootCert` + `bootstrap.pg_basebackup.source` + `replica.enabled/source` 표면, streaming standalone replica bootstrap, ordinal-0 외부 `pg_basebackup`, `standby.signal`/`primary_conninfo`, password passfile + TLS client/root cert conninfo, persistent-follower election (local promotion 차단), fail-closed status 모두 검증. WAL-archive / object-store hybrid, distributed-topology demotion/promotion-token, live cross-cluster drill 은 pending.
- [~] **선언적 hibernation** — `postgres.keiailab.io/hibernation=on/off` annotation, shard StatefulSet/PVC-template preservation + `replicas=0`, native router `replicas=0`, `status.phase=Hibernated`, hibernation condition 모두 envtest 검증. `SMOKE_HIBERNATION=1` path 는 PVC-marker-row 보존 및 rehydration SQL round-trip drill 도 수행; live kind 검증 pending.
- [~] **Release smoke test** — `scripts/release-smoke-test.sh` 6-stage (GH Release tag + GHCR manifest + GH Pages + helm index + helm pull/template + trivy post-publish scan). path 정정 (hack/→scripts/).
- Verify: PrometheusRule / Grafana dashboard rendering, Pooler Service 를 통한 `psql` 접근, live PgBouncer exporter scrape, upgrade rolling restart 성공.

### Gate G3 — 자체 sharding 기반 (~0% buffer)

**목표**: 외부 sharding runtime 없이 sharding 메타데이터를 자체 구현.

- [x] `ShardingMode` 필드 (`none` / `native`) — `postgrescluster_types.go`. Constants + Spec round-trip 을 `TestShardingMode` 가 guard (`api/v1alpha1/postgrescluster_types_test.go`); enum validation 은 `+kubebuilder:validation:Enum=none;native` marker 로 apiserver 에서 강제. RFC 0001 §3.1 / RFC 0002.
- [x] `ShardsSpec` (초기 shard 수 / replica / storage) — `postgrescluster_types.go`. 필드 round-trip + `DeepCopy` 슬라이스 독립성 + `Replicas=0` (HA-off dev) 을 `TestShardsSpec` 가 guard (`api/v1alpha1/postgrescluster_types_test.go`). RFC 0001 §3.1.
- [x] Sharding plugin interface — `internal/plugin/sharding/api.go`. 컴파일타임 interface freeze + `Registry` register/get/Names round-trip + `Capabilities` 광고 + `ErrUnsupported` sentinel 을 `TestShardingPlugin` umbrella 가 guard (`internal/plugin/sharding/api_test.go`). RFC 0001~0005 / RFC 0004 (router 아키텍처).
- [x] **`ShardRange` CRD** — `api/v1alpha1/shardrange_types.go` + `config/crd/bases/...shardranges.yaml` (RFC 0002). `referenceTables` 필드 추가됨.
  - [x] **hash / range / consistent-hash vindex 구현** — `internal/router/vindex.go`·`vindex_consistent.go` (murmur3/fnv/crc32, 링 캐시). list/lookup 후속.
  - [~] Metadata store — `pg_keiailab` 카탈로그 + 마이그레이션 구현(`metadata_store.go`), reconciler 결선은 후속(orphan).
- [x] **`pg-router` service** — `cmd/pg-router/`. **배포 가능**(`Dockerfile.router` + `config/router/`: SA+Role[shardranges·postgresclusters]+Deployment+Service).
  - [x] **라우팅 키 추출** — 제로 의존성 토크나이저(regex/parser/auto). 모호키 bail·dollar-quote·extended Parse 처리.
  - [x] **Shard-placement lookup** — vindex + 교체 가능 토폴로지(static↔CRD watch) + failover-aware 백엔드(status.primary).
  - [x] **Connection routing** — connection mode(startup param) + **query mode**(쿼리 인지, `PGROUTER_MODE=query`): 첫 쿼리에서 키 추출→QueryRouter→샤드 backend.
  - [x] **Router 수평확장** — stateless router Deployment 를 `spec.router.replicas` 로 수동 scale + **CPU 기반 HPA**(`spec.router.autoscale.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage}`): `buildRouterHPA`(autoscaling/v2, CPU utilization target) + reconcile/delete gate + `Owns(HPA)` + autoscaling/v2 RBAC + webhook bounds 검증(max>0, max≥effective-min). active-connection custom metric 어댑터는 후속.
- [~] **drift guard / placement** — `placement.go` drift 감지(Missing/Extra/Zone/NotReady) 순수함수 구현, reconciler 결선 후속.
- **✅ Verify 완료 (2026-06-27 라이브)**: 2 trust postgres + pg-router query-mode → psql `SELECT located_on FROM probe WHERE id='alice'`이 **alice→shard-0 / bob→shard-1 / carol→shard-0** 결정적·올바른 라우팅. (제약: trust 백엔드 + simple/inline-literal query. scram 인증·describe-first 파라미터·scatter는 후속 — [ROUTER-GAP-ANALYSIS §6](sharding/ROUTER-GAP-ANALYSIS.ko.md).)

### Gate G4 — Online resharding (~0% buffer)

**목표**: 데이터 손실 없는 split / rebalance.

- [x] **`ShardSplitJob` CRD** — `api/v1alpha1/shardsplitjob_types.go` (`spec.online` / `cdcMaxLag` / `allowForwardOnly` 포함).
- [x] **phase state machine** — `shardsplitjob_controller.go` + `shardsplitjob_copy.go` + `shardsplitjob_abort.go`. 전 phase 가 K8s Job 실행모델로 결선됨(컨트롤러는 PG 직접접속 대신 cluster 내부 reshard Job 을 생성·게이트):
  - [~] 1. Snapshot + WAL — phase 전이.
  - [x] 2. 대상 shard bootstrap — ConfigMap+Service+STS 생성(격리 식별, ADR-0027).
  - [x] 3. Initial copy (offline) — `reconcileInitialCopy` 가 target 별 reshard-copy Job 생성, `router.CopyShardRange`(vindex 로 자기 범위 row 만 복사) + `ensureTargetTable` + `ReplicateIndexes` + `ReplicateConstraints`.
  - [x] 4. CDC catch-up (online 무중단) — `reconcileCDC`: cdc-setup Job(`CreatePublication`/`CreateSubscription` copy_data + lag≤`cdcMaxLag` 대기) → write-block → cdc-finalize Job(drain + `DeleteForeignRange` + sub/pub drop). `wal_level=logical`. live `TestCDCLive` 가 라이브쓰기 유실 0 증명.
  - [x] 5. Cutover (write-block) — `ShardRange.spec.writeBlocked` → 라우터 `ErrWriteBlocked`(SQLSTATE 25006), 읽기는 통과. `setWriteBlock` + RoutingUpdate 가 flip 과 함께 해제.
  - [x] 6. Routing 갱신 — ShardRange ranges 교체(가역).
  - [x] 7. Source cleanup — `reconcileCleanup` 가 source 에서 이동분 삭제 Job(`DeleteShardRange`, delete-only)을 띄우고 게이트.
- [x] **Target 승격 (ADR-0029 P-B)** — `Promote` phase: source 가 active set 에서 빠지고 target Pod 가 Ready 일 때만 `adoptTargetShardIdentity`(named `shard-id` label 부여, fenced single-authority). source 자원은 retain-by-default(STS replicas=0 + 관측 제외).
- [x] **split-plan 보존 불변식 검증** — `resharding.go` ValidateSplitPlan(gap/overlap/coverage).
- **✅ Verify 완료 (2026-06-28 라이브)**: kind 실 K8s + 실 PG 에서 offline·online 양 경로 full e2e — 단일샤드(키 100)→ShardRange+ShardSplitJob→전 phase(Bootstrap→InitialCopy/CDCCatchup→Cutover→RoutingUpdate→Cleanup→Completed)→ **t0=44 / t1=56 / source=0, 합=100 키유실 0, PK 인덱스 target 복제, ShardRange flip + writeBlock 해제**. 남은 live gate: native router 동시쓰기 부하하 무중단 실증, target 승격 후 chaos/failover drill.

### Gate G5 — Distributed SQL (~0% buffer)

**목표**: cross-shard 쿼리 / 트랜잭션 지원 범위를 명확히 한정.

- [x] **Per-query routing (wire-protocol 종단)** — `cmd/pg-router` query-mode 가 simple(`persession.go`)·extended(`extsession.go`) 둘 다 *매 쿼리* 독립 라우팅(vtgate 모델), 샤드별 백엔드 lazy 풀링, prepare-on-first-use, tx pin. scram-sha-256 백엔드 인증 대행(`scram.go`) + describe-round(`describeround.go`)로 lib/pq/pgx 등 실 드라이버 동작. **라이브 검증됨**.
- [x] **Scatter-gather** 쿼리 path — 키 없는 쿼리를 모든 샤드에 병렬 fan-out 후 재조립하는 **프록시 레벨 wire forwarding**(`scattermode.go`) + in-process 라이브러리(`scatter.go` 타입인지 merge + LIMIT/ORDER BY pushdown). **라이브 검증됨**(SELECT 키없음 → 양샤드 병합). Ref: RFC-0004 §2.2.
- [x] **Reference / read-replica 라우팅** — reference-only → AnyShard, 읽기 → failover-aware read resolver(`PGROUTER_BACKEND_<SHARD>_REPLICA`). pg-router 결선·라이브 검증됨.
- [~] **2PC / saga** 분산 트랜잭션 선택 — ADR-0015 결정 (2PC primary + saga deferred). 실 구현은 명시적 후순위(멀티테넌트 v1 불필요). cross-shard 2PC, extended scatter, Flush 파이프라이닝은 범위 밖.
- [x] **Isolation matrix** 문서화 — `docs/sql/isolation-matrix.md` (D.10.3).
- [x] **분산 처리량 실측** — `cmd/router-bench` + `docs/perf/baseline.md §3.0b~3.0f`: single-shard baseline, 라우터경유 점읽기 동시성 스케일(1.7K→9.4K TPS), prepared 재사용 ~1.9×, bufio 최적화(+34~50%), 멀티샤드/멀티라우터 측정. **발견: 단일 16vCPU 호스트에선 CPU+overlay-fsync 공유로 2샤드 ≤ 1샤드 — 진짜 수평스케일 수치는 물리분리 노드 필요**(router-bench 가 멀티머신 DSN 수용).
- Verify: 라이브 per-query/scatter 라우팅(2026-06-28, 단일 연결에서 alice→shard-0/bob→shard-1 결정적) + baseline §3.0 분산 수치. 멀티머신 수평스케일 실측은 하드웨어 후속.

### Gate G6 — 1.0.0 GA (~15% buffer)

**목표**: 상용 등급 품질.

- [x] e2e baseline — `test/e2e/`.
- [ ] **Long-running soak** — ≥ 7 일, 다운타임 0. (NON-GOAL single session) (NON-GOAL for single session — 7-day wall clock required)
- [ ] **Chaos engineering** — pod kill / network partition / disk pressure. (multi-day drill) (multi-day chaos drill required)
- [ ] **Restore rehearsal** — 주기적 자동 backup-restore + 검증. (monthly cron drill — out of single session)
- [ ] **Upgrade matrix** — N → N+1 / N → N+2 / minor patches. (G2 D.6.3 dependency — substantial e2e)
- [ ] **SBOM + signing** — SPDX SBOM + cosign signature.
- [ ] **Docs / runbook 완비**.
  - [ ] HA / backup / restore / upgrade / security / migration runbook.
- Verify: 7-day soak 통과 + N chaos 시나리오 통과 + SBOM 첨부 + 모든 runbook 존재.

## Non-goals (의도적 제외)

- ❌ 외부 PostgreSQL operator 재패키징 또는 fork.
- ❌ 외부 sharding extension 을 first-class built-in 으로 채택 (runtime 의존 아님).
- ❌ 범용 Plugin SDK 제품 스토리 (v0.x archive 에서 retired).
- ❌ **필수 릴리스 게이트로서의 GitHub Actions** — RFC 0002 (org-wide) 참조. 로컬 4-layer gate 로 위임.
- ❌ **날짜 기반 로드맵 데드라인** — org-wide `workflow.md` 참조.
- ❌ 검증 전 HA / backup 기능을 `production-ready` 로 마케팅.

## Change log

| 날짜 | 변경 |
|---|---|
| 2026-06-29 | G3 §pg-router: router CPU HPA(`spec.router.autoscale`, `buildRouterHPA` autoscaling/v2 + reconcile/delete gate + RBAC + webhook bounds 검증) `[x]` 추가. |
| 2026-06-28 | G4 전 phase `[~]골격` → `[x]`: InitialCopy/CDCCatchup(online CDC)/Cutover(write-block)/Cleanup K8s Job 결선 + ADR-0029 P-B Promote phase. offline·online full e2e(키유실 0) 라이브 검증. G5: per-query routing(simple+extended wire 종단)·scatter forwarding·reference/read-replica `[x]`, 분산 처리량 실측(baseline §3.0b~3.0f) `[x]`. |
| 2026-05-16 | G3 §Sharding foundation: `ShardingMode` / `ShardsSpec` / `Sharding plugin interface` 를 unit-test coverage 와 함께 `[~]` → `[x]` 로 flip (`TestShardingMode`, `TestShardsSpec`, `TestShardingPlugin`). Plans `2026-05-14-4-operators-100pct/P-D` §D.7. |
| 2026-05-12 | Backup/restore 격차 해소: `ScheduledBackup` CRD/controller, cron firing 시 `BackupJob` 생성, `BackupJob.spec.type=restore` → `RestorePIT` call path, `executionMode=job` runner Job lifecycle, pgBackRest command-runner plugin 등록, sidecar pod-exec path 추가. |
| 2026-05-12 | Observability 격차 해소: Helm metrics Service / ServiceMonitor / PrometheusRule + `postgres_operator_backupjob_phase` Prometheus 메트릭 추가. |
| 2026-05-11 | G1 §Backup/Restore `BackupJob.Phase` transition (Pending → Running → Succeeded/Failed) 구현 + 8 unit test — `[x]` (ralph-loop iter#3). |
| 2026-05-11 | 전체 재작성 — Gate-scoped sub-task 체크리스트, buffer 지표 도입, date-style 표현 제거. |
| 2026-05-07 | `0.3.0-alpha.3` 릴리스, 공개 GHCR pull 로 전환, legacy staging operator 제거, "no embedded external systems" 원칙 명시화. |

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
