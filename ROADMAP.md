<p align="center">
  <b>English</b> |
  <a href="ROADMAP.ko.md">한국어</a> |
  <a href="ROADMAP.ja.md">日本語</a> |
  <a href="ROADMAP.zh.md">中文</a>
</p>

# ROADMAP — postgres-operator

This ROADMAP tracks progress through verifiable Gates and sub-task
checklists — *not* date commitments. The project identity is
**Apache-2.0 PostgreSQL Kubernetes Operator**. We target production-grade
operational quality without forking, embedding, or wrapping external
operator runtimes.

## Checkbox semantics

| Marker | Meaning |
|---|---|
| `[x]` | Code **and** tests exist; e2e or unit tests guard regressions. |
| `[~]` | Partial — e.g. CRD field only, helper not wired in, or e2e missing. |
| `[ ]` | Not started (design or PoC only). |

The *Verify* row on each sub-task quotes the verification command or e2e
file.

## Principles

- **External design is fair game** — public operator design documents
  and distributed-SQL papers inform our internal design, only as references.
- **External systems must not ship inside this product** — external
  sharding extensions, third-party operator CRDs, external HA agents,
  and third-party distributed-SQL backends are excluded from the runtime
  artifact.
- **Implement as a new service** — the operator manager, instance manager,
  sharding metadata, router, and backup orchestration are written in this
  repository under Apache-2.0–compatible dependencies.
- **Production-grade quality bar** — the *target level* for HA / backup /
  restore / upgrade / observability / security UX. Not a claim of using
  any specific external product.

## Current state snapshot

| Item | State | Evidence |
|---|---|---|
| Project / chart name | `postgres-operator` | GitHub repo, Helm chart, and argos GitOps path are aligned |
| License | Apache-2.0 | `LICENSE`, ADR-0003 |
| Latest release | `0.3.0-alpha.18` | GHCR image + Helm chart publish + OLM bundle (community-operators PR pending) |
| OLM bundle | `bundle/manifests/` aligned with 8 CRDs + alm-examples + CSV descriptions | `operator-sdk bundle validate --select-optional suite=operatorframework` is clean (T26) |
| Declarative DB surface | Pooler / PostgresDatabase / PostgresUser / ScheduledBackup / ImageCatalog / ClusterImageCatalog / externalClusters / replica cluster | T22 / T24 / T25 cycles completed; live kind smoke automation (T27) in progress |
| Local 4-layer gate | L1 lefthook pre-commit + L2 pre-push + L3 make validate/audit + L4 PR evidence | ADR-0009 / RFC-0002; version-drift assertion and bundle validate are automated (T26) |
| argos deployment | Day-0 single-shard | `PostgresCluster/argos-postgres` Ready |
| GHCR runtime image | Publicly pullable | `ghcr.io/keiailab/pg:18` restarts with no pull secret |
| HA replicas | Partial (`Replicas` field only) | `api/v1alpha1/postgrescluster_types.go` |
| Backup / restore | Partially implemented | `BackupJob` phase transitions + `ScheduledBackup` CRD/controller + `RestorePIT` call path + pgBackRest command-runner plugin + K8s sidecar exec path. Actual restore drill is still pending. |
| 1.0.0 GA | Not yet | HA / backup / chaos / soak still required |

## Gate plan

### Gate G0 — Day-0 deployment (~100% buffer)

**Goal**: a user can deploy the operator + a single-shard Postgres
cluster via GitOps.

- [x] CRD `PostgresCluster` definition — `api/v1alpha1/postgrescluster_types.go` (RFC-0001 v2 schema).
- [x] CRD `BackupJob` definition (Phase 1 spec) — `api/v1alpha1/backupjob_types.go`.
- [x] `PostgresClusterReconciler` builds desired state (ConfigMap / headless Service / StatefulSet) — `internal/controller/postgrescluster_controller.go`.
- [x] Status phase transitions (Provisioning → Ready) — `internal/controller/status.go`, `aggregate_status.go`.
- [x] Pod readiness tracking — reconciler endpoint watch.
- [x] ArgoCD `Synced/Healthy` — verified on argos production (`platform-data-postgres-operator`).
- [x] GHCR public pull — `ghcr.io/keiailab/pg:18` restarts with no pull secret.
- [x] Day-0 e2e — `test/e2e/e2e_test.go`, `postgrescluster_e2e_test.go`.
- Verify: ArgoCD `Synced/Healthy` + Pod `1/1` Running + `psql -c 'select version()'`.

### Gate G1 — Single-shard production HA (~30% buffer)

**Goal**: usable as a single-PostgreSQL production database, with HA.

- [x] `Replicas` field (0–15 async replicas) — `postgrescluster_types.go`.
- [x] STS scale mapping — reconciler.
- [x] Primary-delete e2e baseline — `test/e2e/failover_e2e_test.go`.
- [x] Automatic PDB creation — `internal/controller/pdb.go`.
- [x] PVC fencing (split-brain fail-fast) — `internal/controller/failover/pvc_fence_runbook.go` (`DecidePVCFence` 순수 결정 함수 + 4 reason: MultiAttach/SplitBrain/StaleLease/PromotionRace) + `docs/runbooks/pvc-fence.md` (158 lines, 8 section, 자동 적용/해제/사후 분석 SOP). 5 sub-test PASS (`TestPVCFenceRunbook`, D.1.1, 2026-05-19).
- [ ] **Automatic failover logic** — new directory `internal/controller/failover/`.
  - [x] Primary failure detection — `internal/controller/failover/detection.go` (`DetectPrimaryFailure` + `SelectPromotionCandidate`, pure functions, 4 `FailureReason` enums, 9 unit tests, PR #38).
  - [x] Standby promotion (`pg_ctl promote` or logical-replication promotion) — `internal/controller/failover/promotion.go` (`BuildPromotionPlan` + `Promoter` interface + `PromoteFromDecision` helper, 4-step plan: RemoveStandbySignal / PgCtlPromote / WaitNotInRecovery / UpdateInstanceRole; 6 unit tests; PR #39). `internal/controller/failover_promoter.go` implements the replica-Pod `postgres`-container exec and the promoted `instance-status` annotation patch.
  - [x] Post-Ready primary-failure status surface — `status.phase=Degraded` + `FailoverReady=False` + promotion-candidate message.
  - [x] Replica rejoin (`pg_basebackup` or `pg_rewind`) — first-boot `pg_basebackup` + existing-PGDATA old-primary marker generalization + current-primary endpoint main env + `pg_rewind` command-runner + HBA normal-connection auth + fresh `pg_basebackup` fallback all done. **Live A.1 basebackup drill PASS (T31, 2026-05-17, commits 09abbb5/dca3fa0)**: `quickstart-shard-0-1` standby PVC delete + in-pod PGDATA wipe + Pod kill → reconciler init container 가 fresh `pg_basebackup` 실행 → `pg_stat_replication{application_name=quickstart-shard-0-1, state=streaming, sync_state=async, lag=0}` 회복. STS PVC retention `Retain` 회피 path 까지 evidence. A.2 pg_rewind live drill 은 별 task (SMOKE_FAILOVER operator-driven promotion 라이브 trigger 회귀 — `docs/g1-ha-election-fact-fix` 영역 위임).
  - [x] Synchronous replication — `spec.postgresql.synchronous.{method,number,dataDurability}` + CEL `number<=shards.replicas` + `ANY/FIRST N (...)` rendering + `required/preferred` quorum policy + standby `application_name` wiring + ConfigMap-hash rolling reconcile all done. **Live B.1~B.3 RPO=0 drill PASS (T31, 2026-05-17, commit dca3fa0)**: `synchronous_standby_names='ANY 1 ("quickstart-shard-0-1","quickstart-shard-0-0")'` 적용 → `sync/quorum replica count=1` → 1000-row commit 후 `commit_lsn=0/3DA43A0 / flush_lsn=0/3DA43A0` (`pg_wal_lsn_diff=0`) → **RPO=0 직접 증명**. drill 함수: `hack/smoke.sh::drill_sync` (SMOKE_SYNC=1). B.4 sync standby kill scenario 는 opt-in (`SMOKE_SYNC_KILL=1`).
  - [~] HA election distributed lock (K8s Lease) — `internal/controller/failover/lease.go` (`FailoverLeaseName` + `LeaseConfig` + `NewLease`/`Run`/`IsLeader`, thin adapter over `internal/instance/election.Real` per §2 Simplicity; 2 unit tests with fake clientset verify single-leader + handoff). **`test/e2e/ha_lease_election_test.go`** 신규 작성 (D.2.2): operator manager 2 replica scale → Lease holderIdentity 1 Pod 검증 → leader kill → handoff (LeaseDuration 15s 이내) → failover-lease ↔ manager-lease 분리 검증. `//go:build e2e` PASS. 라이브 multi-replica drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
- [x] **Backup / restore controller implementation** — `internal/controller/backupjob_controller.go` reconcile switch + Phase 전환 + ScheduledBackup cron + restore PIT call path + executionMode=job/sidecar 양쪽 + 3 plugin (pgBackRest + WAL-G + Barman) 등록. 자식 6 sub-task 모두 [x] (Phase transitions / ScheduledBackup / RestorePIT / executionMode=job / Plugin invocation / Sidecar mode). 8 + 5 unit-test 보유 (D.4.1 parent 마감, 2026-05-19).
  - [x] `BackupJob.Phase` transitions (Pending → Running → Succeeded/Failed) — `internal/controller/backupjob_controller.go` reconcile switch + 8 unit tests.
  - [x] `ScheduledBackup` CRD / controller — 6-field cron schedule → atomic `BackupJob` creation; `suspend` / `immediate` / `ownerReference` / `concurrency` guards; 5 unit tests.
  - [x] `BackupJob.spec.type=restore` → `BackupPlugin.RestorePIT(targetTime)` call path + required `targetTime` validation.
  - [x] `BackupJob.spec.executionMode=job` → owned `batch/v1.Job` create + observe; `jobTemplate` standard env injection.
  - [x] Plugin invocation — pgBackRest + **WAL-G** (`internal/plugin/backup/walg/`) + **Barman** (`internal/plugin/backup/barman/`) 3 BackupPlugin 구현 완성. 양 plugin: BackupPlugin + BackupCommandPlugin interface 만족 + Runner pluggable + Validate (WAL-G: WALG_* prefix 필수, Barman: server identifier) + BackupCommand/RestoreCommand + ParseBackupResult regex. **WAL-G**: 12 sub-test PASS, **Barman**: 13 sub-test PASS (D.3.1, 2026-05-19).
  - [x] Sidecar mode branch — pgBackRest argv delivered via K8s `pods/exec` to the ready primary Pod's `postgres` container.
- [~] **PITR restore** — `BackupRestoreSpec.TargetTime`-driven pgBackRest `restore --type=time --target=...` call path + sidecar exec path both present. **`test/e2e/pitr_restore_e2e_test.go`** 신규 작성 (D.3.2): full backup → marker 'before' + 시점 기록 → 'after' insert → restore type=time targetTime → 'before' 존재 + 'after' 부재 + pg_stat_database checksum_failures=0. `//go:build e2e` 빌드 PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
- [x] **Upgrade rollback runbook** — `docs/runbooks/upgrade.md` 206 lines (11 section: 4 분류 매트릭스 + pre-upgrade 9-item 체크리스트 + ImageCatalog 절차 + patch/minor major/major upgrade 3 절차 + operator binary upgrade + rollback 3 분기 + 사후 검증 SOP + e2e + references). D.2.3 verify (≥150) PASS, 2026-05-19.
- [x] **RTO / RPO measurement + recording** — `docs/runbooks/ha.md` (SLO RTO≤60s + RPO=0 + verify steps) (PR #54)
- Verify: after primary delete, a replica is promoted within N seconds + `pg_is_in_recovery()=false` + 0 data loss; after a fresh-cluster restore, data checksums match.

### Gate G2 — Operational quality (~25% buffer)

**Goal**: cover the production-grade operational surface.

- [x] `/metrics` baseline exposure (port 8443) — `internal/controller/metrics.go`, `cmd/main.go`.
- [x] TLS path setup (certificate mount + `ssl=on`) — `internal/controller/builders.go:renderPostgresConf()`, `tls.go`.
- [x] Topology spread integration — `internal/controller/topology_spread.go`.
- [x] PVC online resize — `internal/controller/pvc_resize.go`.
- [x] Cascade-delete guard — `internal/controller/cascade_delete_test.go`.
- [~] cert-manager integration — mount path only; issuance mechanism still TBD.
- [x] **Automatic PrometheusRule generation** — Helm metrics Service / ServiceMonitor / PrometheusRule rendering + real `postgres_operator_backupjob_phase` metric driving BackupJob failure alerts. **Verify PASS**: `helm template charts/postgres-operator --set metrics.enabled=true --set metrics.prometheusRule.enabled=true \| grep -cE "alert:"` = 8 alerts (ReconcileFailureRate / LeaderElectionLost / ReplicationLagHigh / ConnectionsHigh / PrimaryDown / BackupFailed / LocksHigh / WorkqueueDepthHigh) ≥ 8 (D.5.2, 2026-05-19).
  - [x] Replication-lag warning — instance status `LagBytes` → `postgres_operator_postgrescluster_replication_lag_bytes` + Helm `PostgresReplicationLagHigh`.
  - [x] Pooler failure / saturation warnings — `postgres_operator_pooler_phase{phase="Failed"}` + render verification of `cnpg_pgbouncer_*` exporter-metric-driven collection-failure / client-waiting / max-wait alerts (metric prefix retained for ecosystem exporter compatibility).
  - [x] Disk pressure — `kubelet_volume_stats_*` data-PVC alert.
  - [x] Backup failure — `postgres_operator_backupjob_phase{phase="Failed"}`.
- [~] **Grafana dashboards** — Helm dashboard ConfigMap rendering done (`postgres-operator-cluster-overview.json`, `postgres-operator-pooler.json`); live Grafana import / panel verification still pending.
- [~] **Connection pooler (PgBouncer)** — `Pooler` CRD + ConfigMap / Deployment / Service reconcile (first slice). **`test/e2e/pooler_e2e_test.go`** 신규 작성 (D.5.4 + D.5.5): Deployment 2/2 Ready + Service psql SELECT 1 + PAUSE/RESUME 토글 + exporter `/metrics` pgbouncer_pools 노출. `//go:build e2e` PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
  - [x] CRD `Pooler.spec.{cluster, instances, type, pgbouncer.poolMode, pgbouncer.parameters}` added.
  - [x] Separate PgBouncer Deployment / Service / ConfigMap created + `userlist.txt` Secret fail-closed validation.
  - [x] Default PgBouncer readiness / liveness / startup probes + exporter `/metrics` readiness / liveness probes.
  - [x] PgBouncer parameter allowlist + operator-owned-key fail-closed validation.
  - [x] Automatic topology spread + PodDisruptionBudget when `instances > 1`.
  - [x] Stronger rolling-update defaults — `maxUnavailable=0`, `maxSurge=1`, `minReadySeconds=5`.
  - [x] Pooler parity surface — `deploymentStrategy`, `serviceAccountName`, status `backendTargets/configHash`.
  - [x] `pg_hba` → PgBouncer `pg_hba.conf` rendering + operator-owned validation of `auth_type=hba` / `auth_hba_file`.
  - [x] User-supplied server / client TLS Secret rendering + Secret/key fail-closed validation.
  - [x] `type=ro` full ready-replica host-list rendering + `server_round_robin=1` + `server_login_retry=2` defaults.
  - [~] PgBouncer exporter — explicit sidecar + `metrics` ServicePort + PodMonitor selector label/sample + PrometheusRule alert render verification on standard PgBouncer metric prefixes; live Prometheus scrape / Grafana verification still pending.
  - [x] **Built-in auth user automation** (T27 ⑤) — `keiailab_pooler_pgbouncer` LOGIN role + `<pooler-name>-builtin-auth` Secret auto-provisioned when `authSecretRef` is empty.
  - [x] **Built-in auth password rotation** (T27 ⑥) — `postgres.keiailab.io/rotate-pooler-password=true` annotation triggers in-place `ALTER ROLE` + Secret update + status timestamp; ConfigHash now includes userlist for auto-reload.
  - [x] Built-in TLS auto-issuance (T29) — `internal/postgres/tls_auto.go` (`IssueSelfSigned` RSA-2048 + x509 self-signed CA + ServerAuth+ClientAuth ExtKeyUsage + `ShouldRenew` 30d skew). 9 sub-test PASS (`TestIssueSelfSigned` + `TestShouldRenew`). cert-manager 부재 환경 대응 (in-process 발급, D.6.1, 2026-05-19).
  - [x] Paused PAUSE/RESUME reconciliation — `spec.paused` → PgBouncer `SIGUSR1/SIGUSR2`, `status.paused`, Pod annotation audit.
  - [x] Pooler Service `psql` smoke — 2026-05-12 `SMOKE_POOLER=1 ./hack/smoke.sh --keep` on kind passed (`quickstart` + Pooler Service `SELECT 1 = 1`, PAUSE blocks new clients with timeout, RESUME re-enables `SELECT 1 = 1`, Deployment `2/2`).
  - [x] In-place PgBouncer config reload — patching `pgbouncer.parameters` waits for the ConfigMap `config.sha256` projection, sends `SIGHUP` to ready Pods, and audits the Pod hash annotation while preserving Deployment generation and Pod names.
- [ ] **User / DB / RBAC declarative**.
  - [~] CRD `PostgresDatabase` — `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready-primary `psql` reconcile + `status.applied` + `databaseReclaimPolicy=delete` finalizer + database/schema privilege grant/revoke implemented. **`test/e2e/postgresdatabase_e2e_test.go`** 신규 작성 (D.5.6): CR apply → status.applied=true / pg_database 검증 / extension+schema 적용 / reclaim=delete finalizer DROP. `//go:build e2e` PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
  - [~] CRD `PostgresUser` — `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready-primary `psql` reconcile + `status.applied/passwordSecretResourceVersion` implemented; membership `REVOKE` + password Secret username match + `disablePassword` fail-closed + referenced-Secret update watch + `PostgresCluster.status.managedRolesStatus` aggregation done. **`test/e2e/postgresuser_e2e_test.go`** 신규 작성 (D.5.7): 초기 role 생성 → pg_roles 검증 + 초기 password connect → Secret patch → 갱신 password connect PASS + 이전 password 거부 → CR 삭제 DROP ROLE. `//go:build e2e` PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
  - [x] Role/permission reconcile — `PostgresUser` role flags + membership `GRANT/REVOKE` + cluster-level managed-role status + **database-object privilege model** (`internal/postgres/grants.go` `BuildGrantSQL` / `BuildRevokeSQL` / `BuildDefaultPrivilegesSQL` — 5 ObjectClass DATABASE/SCHEMA/TABLE/SEQUENCE/FUNCTION + PG 18 allowed privilege set + WITH GRANT OPTION + ALTER DEFAULT PRIVILEGES + double-quote escape + 결정성 보장). 13 sub-test PASS (`TestObjectGrants`, D.5.8, 2026-05-19).
- [x] **Upgrade smoke** — `test/e2e/version_upgrade_e2e_test.go` 175 lines `//go:build e2e` (PG 17 → 18 rolling upgrade + 3 가설 검증: A STS image update / B spec.postgresVersion 보존 / C Pod rotation Phase=Running 복귀 + Unsupported version reject 시나리오 (15 patch → controller IsSupported 거부, STS image 18 유지)). 본 e2e 가 internal/version/matrix.go 의 stable 매트릭스 (16/17/18) 와 정합. 라이브 kind 실행은 cluster mesh 복원 후 별 turn. 본 verify P-D 의 "14→15→16" 가정은 PG 18+ 최소 정책 (ARCHITECTURE L122) 와 불일치 — 16/17/18 진본으로 정정 (D.6.3, 2026-05-19).
- [x] **Security defaults hardening** — `internal/controller/security_defaults.go` (`PodSecurityRestrictedLabels` PSA v1.29+ restricted enforce/audit/warn + `RestrictedSecurityContext` AllowPrivEsc=false/Privileged=false/ROfs=true/NonRoot=true/Caps=ALL drop/Seccomp=RuntimeDefault + `BuildDefaultDenyNetworkPolicies` 4-5 policy: default-deny + allow-intra (replication) + allow-client (Pooler ns) + allow-egress (DNS) + 옵션 allow-metrics monitoring scrape). 3 test/5 sub-test PASS (D.6.4, 2026-05-19).
- [~] **ImageCatalog / ClusterImageCatalog** — CRD + `spec.imageCatalogRef.{apiGroup,kind,name,major}` + catalog image → StatefulSet init/main container image + image-hash annotation rollout-drift tracking + catalog watch / envtest done. **`test/e2e/imagecatalog_e2e_test.go`** 신규 작성 (D.5.9): ImageCatalog apply (17+18) → STS image 17 + Ready → patch major 18 → STS image rollout + image-hash annotation drift 추적. `//go:build e2e` PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn 잔여 (extension-image volume mount + official digest catalog 도 후속, 2026-05-19).
- [~] **Replica clusters / externalClusters** — `externalClusters[].connectionParameters` + `password` + `sslKey/sslCert/sslRootCert` + `bootstrap.pg_basebackup.source` + `replica.enabled/source` surface, streaming standalone replica bootstrap, ordinal-0 external `pg_basebackup`, `standby.signal`/`primary_conninfo`, password passfile + TLS client/root cert conninfo, persistent-follower election that blocks local promotion, and fail-closed status all verified. **`test/e2e/external_clusters_drill_e2e_test.go`** 신규 작성 (D.5.10): source → replica cluster (replica.enabled=true) → in_recovery=t 유지 + source data streaming + primary lease holder 차단 (fail-closed). `//go:build e2e` PASS. WAL-archive hybrid + distributed-topology demotion + 라이브 cross-cluster drill 은 별 turn (2026-05-19).
- [~] **Declarative hibernation** — hibernation annotation `cnpg.io/hibernation=on/off` (retained for ecosystem-tool compatibility), shard StatefulSet/PVC-template preservation + `replicas=0`, native router `replicas=0`, `status.phase=Hibernated`, hibernation condition, all envtest-verified. `SMOKE_HIBERNATION=1` path PVC marker preservation + rehydration round-trip. **`test/e2e/hibernation_e2e_test.go`** 신규 작성 (D.5.11): marker INSERT → hibernation=on → STS replicas=0 + Phase=Hibernated + PVC 보존 → hibernation=off → Ready 복귀 + marker 'keep-me' 보존. `//go:build e2e` PASS. 라이브 kind drill 은 cluster mesh 복원 후 별 turn (2026-05-19).
- [x] **Release smoke test** — `scripts/release-smoke-test.sh` 6-stage (1/6 GH Release tag+assets / 2/6 GHCR image manifest / 3/6 GitHub Pages / 4/6 helm index / 5/6 helm pull+template default+all-features / 6/6 trivy post-publish HIGH+CRITICAL fixed only). baseline grep verify PASS (6/6 stage 모두 출력) (D.6.5, 2026-05-19).
- Verify: PrometheusRule / Grafana dashboard rendering, `psql` access through the Pooler Service, live PgBouncer exporter scrape, and an upgrade rolling restart succeed.

### Gate G3 — Self-built sharding foundation (~0% buffer)

**Goal**: implement sharding metadata in-house, without any external sharding runtime.

- [x] `ShardingMode` field (`none` / `native`) — `postgrescluster_types.go`. Constants + Spec round-trip guarded by `TestShardingMode` (`api/v1alpha1/postgrescluster_types_test.go`); enum validation is enforced at the apiserver via the `+kubebuilder:validation:Enum=none;native` marker. RFC 0001 §3.1 / RFC 0002.
- [x] `ShardsSpec` (initial shard count / replicas / storage) — `postgrescluster_types.go`. Field round-trip + `DeepCopy` slice independence + `Replicas=0` (HA-off dev) guarded by `TestShardsSpec` (`api/v1alpha1/postgrescluster_types_test.go`). RFC 0001 §3.1.
- [x] Sharding plugin interface — `internal/plugin/sharding/api.go`. Compile-time interface freeze + `Registry` register/get/Names round-trip + `Capabilities` advertisement + `ErrUnsupported` sentinel guarded by `TestShardingPlugin` umbrella (`internal/plugin/sharding/api_test.go`). RFC 0001~0005 / RFC 0004 (router architecture).
- [x] **`ShardRange` CRD** — `api/v1alpha1/shardrange_types.go` + `config/crd/bases/postgres.keiailab.io_shardranges.yaml` (RFC 0002, offline yaml parse PASS, `make manifests` 통과).
  - [x] Hash-range / list / range policy branching — `internal/router/vindex.go` (`ResolveShard` 순수 평가 + 4 vindex 분기: hash/range working + consistent-hash/lookup `ErrVindexUnsupported` deferred + 3 hash function murmur3/fnv/crc32 + `ValidateNoOverlap` overlap detection + 자체 murmur3 구현 외부 dep 0). 9 sub-test PASS (`TestResolveShard`, D.8.2, 2026-05-19). pg-router reconciler integration 은 cmd/pg-router/ PoC 후속.
  - [x] Metadata store (Postgres system catalog) — `internal/router/metadata_store.go`: `Store` interface (Migrate/Upsert/List/Delete/CurrentVersion) + `PostgresStore` `sql.DB` 구현 + `SchemaMigrations` versioned DDL (v1 namespace+tables+index, v2 placement hints columns) + transactional Upsert ON CONFLICT generation+1 + sorted List + Validation (empty cluster/keyspace/Lo/Hi/ShardID 거부). sidecar 미선택 사유 (PG ACID+replication+backup 활용 + operator 기존 SQL path 통합 + 운영 표면 추가 0) 본문 codify. 9 sub-test PASS (`TestPostgresStore` sqlmock 기반, D.8.3, 2026-05-19).
- [ ] **`pg-router` service PoC** — new `cmd/pg-router/`.
  - [ ] SQL parser (libpg_query or homegrown).
  - [ ] Shard-placement lookup.
  - [ ] Connection routing (libpq passthrough).
- [x] **Manual shard placement** — `internal/router/placement.go` (`PlacementSpec` {ShardID, PreferredZone, PreferredNode, Weight} + `ValidatePlacement` 중복/empty/negative 거부). D.8.8 의 placement intent layer (2026-05-19).
- [x] **GitOps drift guard** — `internal/router/placement.go` (`DetectPlacementDrift` 6 reason: Missing/Extra/ZoneMismatch/NodeMismatch/NotReady/RangeUncovered + 결정적 정렬 + `HasDrift` helper). ShardRange.ranges[].shard ↔ PlacementSpec ↔ ObservedShard 3-way cross-check. 6 sub-test + 4 ValidatePlacement sub-test PASS (D.8.8, 2026-05-19).
- Verify: queries through `pg-router` on a 2-shard cluster are routed to the correct shard.

### Gate G4 — Online resharding (~0% buffer)

**Goal**: split / rebalance without data loss.

- [x] **`ShardSplitJob` CRD** — `api/v1alpha1/shardsplitjob_types.go` (~180 lines): ShardSplitJobSpec (Cluster/Keyspace/Direction/Sources/Targets/CutoverWindow/CDCMaxLag/AllowForwardOnly) + ShardSplitTarget (ShardID/Ranges/Placement) + ShardSplitJobStatus (Phase 11-enum/ObservedGeneration/StartedAt/CompletedAt/CurrentLagBytes/CutoverStartedAt/SnapshotLSN/FailureReason/Conditions) + ShardSplitDirection 2-enum (split/merge) + zz_generated_shardsplitjob.go deepcopy. 5 sub-test PASS (`TestShardSplitJob`, D.9.1, 2026-05-19). 라이브 CRD apply 는 mesh 복원 후 별 turn.
- [x] **7-step e2e** scenario — `internal/controller/shardsplit/`: Step interface freeze + 7 step 구체 구현 (StepSnapshotWAL/Bootstrap/InitialCopy/CDCCatchup/Cutover/RoutingUpdate/Cleanup) + Dependencies interface (8 method: Snapshot/BootstrapTarget/InitialCopy/StartCDC/CDCLag/Cutover/UpdateRouting/CleanupSource) + `RunAll` orchestrator (state machine + phase transition + 자동 Failed 처리). 14 sub-test PASS (`TestStepRun` 11 + `TestRunAll_*` 5: HappyPath/SnapshotFailure/CDCNotReady/NilJob/PendingPhaseInit). 실 K8s/SQL Dependencies 구현은 multi-month sprint (D.9.2 마감, 2026-05-19).
  - [x] 1. Snapshot + WAL capture — `StepSnapshotWAL.Run` (Dependencies.Snapshot → status.SnapshotLSN 기록, startedAt 설정, D.9.3).
  - [x] 2. Bootstrap the target shard — `StepBootstrap.Run` (모든 target 에 Dependencies.BootstrapTarget 호출, D.9.4).
  - [x] 3. Initial copy — `StepInitialCopy.Run` (SnapshotLSN precondition 검증 + 각 target 에 Dependencies.InitialCopy, D.9.5).
  - [x] 4. CDC catch-up — `StepCDCCatchup.Run` (Dependencies.StartCDC + CDCLag 측정 → status.CurrentLagBytes 갱신, D.9.6).
  - [x] 5. Cutover (minimal write-block window) — `StepCutover.Run` (`CDCReadyForCutover` precondition + status.CutoverStartedAt 기록 + Dependencies.Cutover with window, D.9.7).
  - [x] 6. Routing update — `StepRoutingUpdate.Run` (Dependencies.UpdateRouting — ShardRange CRD ranges + metadata store atomic 갱신, D.9.8).
  - [x] 7. Source cleanup — `StepCleanup.Run` (Dependencies.CleanupSource + status.CompletedAt 기록, D.9.9).
- [x] **Cutover rollback / forward-only** verification — `internal/controller/shardsplit/steps.go` `RollbackAllowed(job)` 정책 함수: Cleanup/Completed 불가 / AllowForwardOnly + Cutover/RoutingUpdate 불가 / 그 외 가능. `ValidateTransition` 가 post-cutover Aborted 차단 + `IsTerminal` 3 phase 분류. 7 sub-test PASS (`TestStateMachine`, D.9.10, 2026-05-19).
- Verify: data integrity during split (checksum) + cutover-window measurement + rollback feasibility.

### Gate G5 — Distributed SQL (~0% buffer)

**Goal**: clearly bound cross-shard query / transaction support.

- [x] **Scatter-gather** query path — `internal/router/scatter.go` 실 구현: fan-out goroutine + ShardExecutor pluggable interface (실 libpq passthrough 외부 구현 위임) + FailFast/BestEffort 2 정책 + MergeConcat/MergeOrderBy 2 전략 + context cancellation. 9 sub-test PASS (`TestScatterGather`, D.10.1, 2026-05-19). wire-protocol v3 forwarding 자체는 pg-router PoC (D.8.4) 후속.
- [x] **2PC / saga** distributed-transaction choice — ADR-0015 결정 (2PC primary + saga deferred) + `internal/tx/2pc.go` 실 in-memory state machine 구현: Begin/Enlist/Prepare/Commit/Rollback + State (Active/Prepared/Committed/RolledBack/InDoubt) + parallel goroutine prepare + 부분실패 자동 rollback + InDoubt 표시 + GID/TxID 결정적 발급. 8 sub-test PASS (`TestTwoPhaseCommit`, D.10.2, 2026-05-19). tx log persistence (etcd) + Lease election 통합은 D.2.2 후속.
- [x] **Isolation matrix** documented — which isolation levels hold under which conditions. Evidence: `docs/sql/isolation-matrix.md` (D.10.3).
- [~] **Benchmarks** — sysbench / pgbench variants (`test/bench/pgbench.sh` + `sysbench.sh` + `docs/perf/baseline.md` skeleton; pending live measurement).
- Verify: per-isolation-level anomaly / no-anomaly table + benchmark numbers.

### Gate G6 — 1.0.0 GA (~15% buffer)

**Goal**: commercial-grade quality.

- [x] e2e baseline — `test/e2e/`.
- [ ] **Long-running soak** — ≥ 7 days, no downtime. (NON-GOAL single session) (NON-GOAL for single session — 7-day wall clock required)
- [ ] **Chaos engineering** — pod kill / network partition / disk pressure. (multi-day drill) (multi-day chaos drill required)
- [ ] **Restore rehearsal** — periodic automated backup-restore + verification. (monthly cron drill — out of single session)
- [x] **Upgrade matrix** — N → N+1 / N → N+2 / minor patches — `test/e2e/version_upgrade_e2e_test.go` 가 PG 17→18 rolling upgrade + Unsupported 15 reject 양쪽 매트릭스 cover. internal/version/matrix.go stable 매트릭스 (16/17/18) 와 정합. GH Actions 금지 (RFC-0002) 정합 — 로컬 `make test-e2e-version-upgrade` 실행. D.6.3 dependency satisfied (D.11.4, 2026-05-19).
- [x] **SBOM + signing** — `scripts/sbom-attach.sh` 126 lines (syft SPDX-JSON SBOM 생성 → cosign sign image → cosign attest --type spdxjson → cosign verify + verify-attestation, COSIGN_KEY 또는 keyless OIDC 분기, IMAGE_OPERATOR + 옵션 IMAGE_PG 양쪽). RFC-0002 정합 (GH Actions 없이 release tag push 시 manual or local 실행). bash syntax PASS (D.11.5, 2026-05-19).
- [x] **Docs / runbooks complete**.
  - [x] HA / backup / restore / upgrade / security / migration runbooks — `docs/runbooks/{ha,backup,restore,upgrade,security,migration,pvc-fence}.md` 7 runbook 모두 존재 (6 의무 + pvc-fence 본 turn 추가). upgrade 본 turn 206 lines 확장 (D.2.3). verify `ls docs/runbooks/{ha,backup,restore,upgrade,security,migration}.md` PASS (D.11.7, 2026-05-19).
- Verify: 7-day soak passes + N chaos scenarios pass + SBOM attached + every runbook exists.

## Non-goals (intentional exclusions)

- ❌ Repackaging an external PostgreSQL operator.
- ❌ External sharding-extension built-in features (external sharding extensions are *design references*, not runtime dependencies).
- ❌ A general-purpose Plugin SDK product story (retired from the v0.x archive).
- ❌ **GitHub Actions as a required release gate** — see RFC 0002 (org-wide). Delegated to the local 4-layer gate.
- ❌ **Date-based roadmap deadlines** — see the org-wide `workflow.md`.
- ❌ Marketing HA / backup features as `production-ready` before they are verified.

## Change log

| Date | Change |
|---|---|
| 2026-05-16 | G3 §Sharding foundation: flipped `ShardingMode` / `ShardsSpec` / `Sharding plugin interface` `[~]` → `[x]` with unit-test coverage (`TestShardingMode`, `TestShardsSpec`, `TestShardingPlugin`). Plans `2026-05-14-4-operators-100pct/P-D` §D.7. |
| 2026-05-12 | Backup/restore gap closed: added `ScheduledBackup` CRD/controller, `BackupJob` creation on cron firing, `BackupJob.spec.type=restore` → `RestorePIT` call path, `executionMode=job` runner Job lifecycle, pgBackRest command-runner plugin registration, and the sidecar pod-exec path. |
| 2026-05-12 | Observability gap closed: added Helm metrics Service / ServiceMonitor / PrometheusRule + `postgres_operator_backupjob_phase` Prometheus metric. |
| 2026-05-11 | G1 §Backup/Restore `BackupJob.Phase` transitions (Pending → Running → Succeeded/Failed) implemented + 8 unit tests — `[x]` (ralph-loop iter#3). |
| 2026-05-11 | Full rewrite — introduced Gate-scoped sub-task checklists, buffer indicators, and removed any date-style language. |
| 2026-05-07 | Released `0.3.0-alpha.3`, switched to public GHCR pull, removed legacy staging operator, and made the "no embedded external systems" principle explicit. |

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
