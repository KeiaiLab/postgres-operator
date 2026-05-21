---
title: "Roadmap"
description: "postgres-operator Gate-based roadmap (sub-task checklist)"
---

# Roadmap

This ROADMAP tracks progress through verifiable Gates and sub-task
checklists — *not* date commitments. `postgres-operator` is an
*independent new implementation* that does not embed an external
PostgreSQL operator or distributed-SQL backend. Phrases like
"PGO-class" / "Citus-class" describe a *quality bar / problem domain*,
not a claim to fork those products or to bundle them as a runtime
dependency.

## Checkbox semantics

| Marker | Meaning |
|---|---|
| `[x]` | Code **and** tests exist; e2e or unit tests guard regressions. |
| `[~]` | Partial — e.g. CRD field only, helper not wired in, or e2e missing. |
| `[ ]` | Not started (design or PoC only). |

The *Verify* row on each sub-task quotes the verification command or
e2e file.

## Design principles

| Principle | Meaning |
|---|---|
| PGO-class quality | HA / backup / restore / upgrade / observability / security UX is benchmarked at commercial production levels. No PGO code is used. |
| Citus-class problem coverage | Shard placement / routing / rebalance / distributed-transaction problems are analyzed; the Citus extension itself is not bundled. |
| Plugin SDK message retired | The v0.x archive's broad Plugin-SDK positioning is retired. Only narrow, necessary extension points are designed. |
| Apache-2.0 clean-room | Only allowed-license dependencies are used; code from forbidden-license projects is never copied, translated, or ported. |
| GitOps first | Production deployment must be reproducible through the GitOps path + the Helm chart dependency graph. |

## Current state snapshot

| Area | State | Evidence |
|---|---|---|
| Naming | repo / chart / GitOps path aligned on `postgres-operator` | Archive docs preserve history |
| Release | `0.3.0-alpha.18` image / chart / SBOM published + OLM bundle (community-operators PR pending) | Not GA yet |
| Runtime image | `ghcr.io/keiailab/pg:18` public pull verified | multi-arch / runtime SBOM still to harden |
| Production cluster | `platform-data-postgres-operator` Synced/Healthy, `postgres` Ready | HA replicas / backup-restore / long-soak still pending |
| Fencing | PVC fence skeleton | Operator-driven recovery / runbook automation still pending |
| Backup | Partially implemented | `BackupJob` phase transitions + `ScheduledBackup` CRD/controller + `RestorePIT` call path + pgBackRest command-runner plugin + K8s sidecar exec path present. Actual restore drill still pending. |

## Gate plan

### Gate G0 — Day-0 deployment (~100% buffer)

**Goal**: a user can deploy the operator + a single-shard Postgres
cluster via GitOps.

- [x] CRD `PostgresCluster` definition — `api/v1alpha1/postgrescluster_types.go` (RFC-0001 v2 schema).
- [x] CRD `BackupJob` definition (Phase 1 spec) — `api/v1alpha1/backupjob_types.go`.
- [x] `PostgresClusterReconciler` builds desired state (ConfigMap / Headless Service / StatefulSet) — `internal/controller/postgrescluster_controller.go`.
- [x] Status phase transitions (Provisioning → Ready) — `internal/controller/status.go`, `aggregate_status.go`.
- [x] Pod readiness tracking — reconciler endpoint watch.
- [x] ArgoCD `Synced/Healthy` — production verified.
- [x] GHCR public pull.
- [x] Day-0 e2e — `test/e2e/e2e_test.go`, `postgrescluster_e2e_test.go`.
- Verify: ArgoCD `Synced/Healthy` + Pod `1/1` Running + `psql -c 'select version()'`.

### Gate G1 — Single-shard production HA (~30% buffer)

**Goal**: usable as a single-PostgreSQL production database, with HA.

- [x] `Replicas` field (0–15 async replicas) — `postgrescluster_types.go`.
- [x] STS scale mapping — reconciler.
- [x] Primary-delete e2e baseline — `test/e2e/failover_e2e_test.go`.
- [x] Automatic PDB creation — `internal/controller/pdb.go`.
- [~] PVC fencing (split-brain fail-fast) — fencing skeleton only; runbook automation pending.
- [ ] **Automatic failover logic** — new `internal/controller/failover/` directory.
  - [x] Primary failure detection — `DetectPrimaryFailure` + `SelectPromotionCandidate`.
  - [x] Standby promotion (`pg_ctl promote` or logical-replication promotion) — plan / helper + controller-layer replica-Pod exec + promoted `instance-status` annotation patch all done.
  - [x] Post-Ready primary-failure status surface — `status.phase=Degraded` + `FailoverReady=False` + promotion-candidate message.
  - [~] Replica rejoin (`pg_basebackup` or `pg_rewind`) — first-boot `pg_basebackup` + existing-PGDATA old-primary marker generalization + current-primary endpoint main env + `pg_rewind` command-runner + HBA normal-connection auth + fresh `pg_basebackup` fallback all done. Live chaos / rewind drill verification still pending.
  - [~] Synchronous replication — `spec.postgresql.synchronous.{method,number,dataDurability}` + CEL `number<=shards.replicas` + `ANY/FIRST N (...)` rendering + `required/preferred` quorum policy + standby `application_name` wiring + ConfigMap-hash rolling reconcile all done. Live commit / RPO drill still pending.
  - [ ] HA-election distributed lock (K8s Lease).
- [ ] **Backup / restore controller implementation** — bolster `internal/controller/backupjob_controller.go`.
  - [x] `BackupJob.Phase` transitions (Pending → Running → Succeeded/Failed).
  - [x] `ScheduledBackup` CRD / controller — 6-field cron schedule → atomic `BackupJob` creation; `suspend` / `immediate` / `ownerReference` / `concurrency` guards.
  - [x] `BackupJob.spec.type=restore` → `BackupPlugin.RestorePIT(targetTime)` call path + required `targetTime` validation.
  - [x] `BackupJob.spec.executionMode=job` → owned `batch/v1.Job` create + observe; `jobTemplate` standard env injection.
  - [~] Plugin invocation — pgBackRest command-runner + sidecar command planning done. WAL-G / Barman pending.
  - [x] Sidecar mode branch — pgBackRest argv delivered via K8s `pods/exec` to the ready primary Pod's `postgres` container.
- [~] **PITR restore** — `BackupRestoreSpec.TargetTime`-driven pgBackRest `restore --type=time --target=...` call path + sidecar exec path both present. Actual restore + checksum drill still pending.
- [ ] **Upgrade rollback runbook** — new `docs/runbooks/upgrade.md`.
- [ ] **RTO / RPO measurement + recording** — new `docs/runbooks/ha.md`.
- Verify: after primary delete, a replica is promoted within N seconds + `pg_is_in_recovery()=false` + 0 data loss; after a fresh-cluster restore, data checksums match.

### Gate G2 — Operational quality (~25% buffer)

**Goal**: cover the PGO-class operational surface.

- [x] `/metrics` baseline exposure (port 8443) — `internal/controller/metrics.go`, `cmd/main.go`.
- [x] TLS path setup (certificate mount + `ssl=on`) — `internal/controller/builders.go:renderPostgresConf()`, `tls.go`.
- [x] Topology spread integration — `internal/controller/topology_spread.go`.
- [x] PVC online resize — `internal/controller/pvc_resize.go`.
- [x] Cascade-delete guard — `internal/controller/cascade_delete_test.go`.
- [~] cert-manager integration — mount path only; issuance mechanism still TBD (T29).
- [~] **Automatic PrometheusRule generation** — Helm metrics Service / ServiceMonitor / PrometheusRule rendering + real `postgres_operator_backupjob_phase` metric driving BackupJob failure alerts.
  - [x] Replication-lag warning — instance status `LagBytes` → `postgres_operator_postgrescluster_replication_lag_bytes` + Helm `PostgresReplicationLagHigh`.
  - [x] Pooler failure / saturation warnings — `postgres_operator_pooler_phase{phase="Failed"}` + render verification of CNPG `cnpg_pgbouncer_*` exporter-metric-driven collection-failure / client-waiting / max-wait alerts.
  - [x] Disk pressure — `kubelet_volume_stats_*` data-PVC alert.
  - [x] Backup failure — `postgres_operator_backupjob_phase{phase="Failed"}`.
- [~] **Grafana dashboards** — Helm dashboard ConfigMap rendering done (`postgres-operator-cluster-overview.json`, `postgres-operator-pooler.json`); live Grafana import / panel verification still pending.
- [~] **Connection pooler (PgBouncer)** — `Pooler` CRD + ConfigMap / Deployment / Service reconcile (first slice).
  - [x] CRD `Pooler.spec.{cluster, instances, type, pgbouncer.poolMode, pgbouncer.parameters}` added.
  - [x] Separate PgBouncer Deployment / Service / ConfigMap created + `userlist.txt` Secret fail-closed validation.
  - [x] Default PgBouncer readiness / liveness / startup probes + exporter `/metrics` readiness / liveness probes.
  - [x] CNPG-compatible PgBouncer parameter allowlist + operator-owned-key fail-closed validation.
  - [x] Automatic topology spread + PodDisruptionBudget when `instances > 1`.
  - [x] Stronger rolling-update defaults — `maxUnavailable=0`, `maxSurge=1`, `minReadySeconds=5`.
  - [x] CNPG Pooler parity — `deploymentStrategy`, `serviceAccountName`, status `backendTargets/configHash`.
  - [x] `pg_hba` → PgBouncer `pg_hba.conf` rendering + operator-owned validation of `auth_type=hba` / `auth_hba_file`.
  - [x] User-supplied server / client TLS Secret rendering + Secret/key fail-closed validation.
  - [x] `type=ro` full ready-replica host-list rendering + `server_round_robin=1` + `server_login_retry=2` defaults.
  - [~] PgBouncer exporter — explicit sidecar + `metrics` ServicePort + PodMonitor selector label/sample + PrometheusRule alert render verification on CNPG metric prefixes; live Prometheus scrape / Grafana verification still pending.
  - [x] **Built-in auth user automation** (T27 ⑤).
  - [x] **Built-in auth password rotation** (T27 ⑥).
  - [ ] Built-in TLS auto-issuance (T29).
  - [x] Paused PAUSE/RESUME reconciliation — `spec.paused` → PgBouncer `SIGUSR1/SIGUSR2`, `status.paused`, Pod annotation audit.
  - [x] Pooler Service `psql` smoke (2026-05-12 `SMOKE_POOLER=1 ./hack/smoke.sh --keep` PASS).
  - [x] In-place PgBouncer config reload — patching `pgbouncer.parameters` waits for the ConfigMap `config.sha256` projection, sends `SIGHUP` to ready Pods, and audits the Pod hash annotation while preserving Deployment generation and Pod names.
- [ ] **User / DB / RBAC declarative**.
  - [~] CRD `PostgresDatabase` — `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready-primary `psql` reconcile + `status.applied` + `databaseReclaimPolicy=delete` finalizer + database/schema privilege grant/revoke implemented. Live smoke / retain-policy verification still pending.
  - [~] CRD `PostgresUser` — `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready-primary `psql` reconcile + `status.applied/passwordSecretResourceVersion` implemented; membership `REVOKE` + password Secret username match + `disablePassword` fail-closed + referenced-Secret update watch + `PostgresCluster.status.managedRolesStatus` aggregation done. Live smoke + password-rotation SQL round-trip still pending.
  - [~] Role/permission reconcile — `PostgresUser` role flags + membership `GRANT/REVOKE` + cluster-level managed-role status (first slice) done; the database-object privilege model still pending.
- [ ] **Upgrade smoke** — extend `test/e2e/version_upgrade_e2e_test.go` (skeleton already in place).
- [ ] **Security defaults hardening** — restricted PSA, NetworkPolicy on by default.
- [~] **ImageCatalog / ClusterImageCatalog** — CRD + `spec.imageCatalogRef.{apiGroup,kind,name,major}` + catalog image → StatefulSet init/main container image + image-hash annotation rollout-drift tracking + catalog watch / envtest done. Extension-image volume mount, official digest catalog supply, and live rollout measurement still pending.
- [~] **Replica clusters / externalClusters** — `externalClusters[].connectionParameters` + `password` + `sslKey/sslCert/sslRootCert` + `bootstrap.pg_basebackup.source` + `replica.enabled/source` surface, streaming standalone replica bootstrap, ordinal-0 external `pg_basebackup`, `standby.signal`/`primary_conninfo`, password passfile + TLS client/root cert conninfo, persistent-follower election that blocks local promotion, and fail-closed status all verified. WAL-archive / object-store hybrid, distributed-topology demotion/promotion-token, and live cross-cluster drill still pending.
- [~] **Declarative hibernation** — CNPG-compatible `cnpg.io/hibernation=on/off` annotation, shard StatefulSet/PVC-template preservation + `replicas=0`, native router `replicas=0`, `status.phase=Hibernated`, condition `cnpg.io/hibernation`, all envtest-verified. The `SMOKE_HIBERNATION=1` path exercises PVC-marker-row preservation and a rehydration SQL round-trip drill; live kind verification still pending.
- [ ] **Release smoke test** — `hack/release-smoke-test.sh` 12/12 (mongodb pattern).
- Verify: PrometheusRule / Grafana dashboard rendering, `psql` access through the Pooler Service, live PgBouncer exporter scrape, and an upgrade rolling restart succeed.

### Gate G3 — Self-built sharding foundation (~0% buffer)

**Goal**: implement sharding metadata in-house, without Citus.

- [~] `ShardingMode` field (`none` / `native`) — `postgrescluster_types.go`.
- [~] `ShardsSpec` (initial shard count / replicas / storage) — `postgrescluster_types.go`.
- [~] Sharding plugin interface — `internal/plugin/sharding/api.go`.
- [ ] **`ShardRange` CRD** — new `api/v1alpha1/shardrange_types.go`.
- [ ] **`pg-router` service PoC** — new `cmd/pg-router/`.
- [ ] **Manual shard placement** — `ShardRange.Spec.PlacementHints`.
- [ ] **GitOps drift guard** — detect divergence between sharding metadata and actual placement.
- Verify: queries through `pg-router` on a 2-shard cluster are routed to the correct shard.

### Gate G4 — Online resharding (~0% buffer)

**Goal**: split / rebalance without data loss.

- [ ] **`ShardSplitJob` CRD** — new `api/v1alpha1/shardsplitjob_types.go`.
- [ ] **7-step e2e** scenario (snapshot + WAL → bootstrap target → initial copy → CDC catch-up → cutover → routing update → source cleanup).
- [ ] **Cutover rollback / forward-only** verification.
- Verify: data integrity during split (checksum) + cutover-window measurement + rollback feasibility.

### Gate G5 — Distributed SQL (~0% buffer)

**Goal**: clearly bound cross-shard query / transaction support.

- [ ] **Scatter-gather** query path.
- [ ] **2PC / saga** distributed-transaction choice.
- [ ] **Isolation matrix** documented.
- [~] **Benchmarks** — sysbench / pgbench variants (`test/bench/` + `docs/perf/baseline.md` skeleton; pending live measurement).

### Gate G6 — 1.0.0 GA (~15% buffer)

**Goal**: commercial-grade quality.

- [x] e2e baseline — `test/e2e/`.
- [ ] **Long-running soak** — ≥ 7 days, no downtime.
- [ ] **Chaos engineering** — pod kill / network partition / disk pressure.
- [ ] **Restore rehearsal** — periodic automated backup-restore + verification.
- [ ] **Upgrade matrix** — N → N+1 / N → N+2 / minor patches.
- [ ] **SBOM + signing** — SPDX SBOM + cosign signature.
- [ ] **Docs / runbooks complete** — HA / backup / restore / upgrade / security / migration runbooks.

## Non-goals (intentional exclusions)

- ❌ Repackaging an external PostgreSQL operator (forking PGO / CNPG / Patroni).
- ❌ Citus's first-class built-in features (Citus is a *design reference*, not a runtime dependency).
- ❌ A general-purpose Plugin SDK product story (retired from the v0.x archive).
- ❌ **GitHub Actions as a required release gate** — see RFC 0002 (org-wide). Delegated to the local 4-layer gate.
- ❌ **Date-based roadmap deadlines** — see the org-wide `workflow.md`.
- ❌ Marketing HA / backup features as `production-ready` before they are verified.
