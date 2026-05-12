# Changelog

This project follows SemVer.

## [Unreleased]

### Added

- *(olm,docs)* `docs/operator-guide/community-operators-onboarding.md` —
  k8s-operatorhub/community-operators channel onboarding procedure
  (prerequisite checklist, bundle image build / push, `gh pr create`,
  upgrade-graph operation, Artifact Hub OLM verification). First T28
  deliverable.
- *(ci)* `make hooks-install` / `make hooks-check` targets — wrappers that
  enable the lefthook DCO / Conventional Commits gate. CONTRIBUTING
  aligned.
- *(ci)* Four extra `make validate` gates — bundle CRD count ≥ 8,
  `operator-sdk bundle validate` default + `suite=operatorframework`,
  Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag drift
  assertion, and `.github/workflows/` absence (ADR-0009 enforce).
- *(olm)* Added `description` + `displayName` for ImageCatalog /
  ClusterImageCatalog / PostgresDatabase / PostgresUser in CSV
  `customresourcedefinitions.owned[]` — eliminated 4 operatorframework
  suite warnings.
- *(oss)* New `SUPPORT.md` (GitHub Discussions / Issues / PR paths,
  security routing, response-time expectations). CHANGELOG entries for
  0.3.0-alpha.17 and 0.3.0-alpha.18 aligned.
- *(docs)* README — 0.3.0-alpha.18 8-CRD surface table + the 6-step
  quickstart.
- *(docs)* TASKS — registered T26 (cross-cut OSS/OLM alignment, complete),
  T27 (live smoke automation for the new CRDs, ①–④ all done),
  T28 (community-operators registration procedure).
- *(smoke)* Added 4 new-CRD scenarios to `hack/smoke.sh`:
  `SMOKE_DATABASE=1` (PostgresDatabase → `status.applied` +
  `pg_database` + reclaim=delete DROP), `SMOKE_USER=1` (PostgresUser →
  `status.applied` + `pg_roles` + DROP ROLE), `SMOKE_SCHEDULEDBACKUP=1`
  (cron `immediate` → BackupJob creation), `SMOKE_IMAGECATALOG=1`
  (ImageCatalog / ClusterImageCatalog schema + lookup). Step numbering
  aligned to N/15.
- *(api)* Added `kubebuilder:resource:categories` markers to all 8 owned
  CRDs — `kubectl get postgres` / `database` / `backup` / `pooler` /
  `image` / `role` / `all` aliases plus OperatorHub UI grouping.
- *(ci)* `make lint-k8s` target integrated into `make validate` —
  kube-linter static analysis (liveness-port / readiness-port / non-root /
  readOnlyRoot / resource-limit, 30+ checks) on `dist/install.yaml` and
  the helm-template output.
- *(ci,olm)* `config/scorecard/` + `bundle/tests/scorecard/config.yaml` +
  `make scorecard` target — automates the 6 operator-sdk scorecard tests
  (basic-check-spec, olm-bundle-validation, olm-crds-have-validation,
  olm-crds-have-resources, olm-spec-descriptors, olm-status-descriptors).
  Live execution requires a kind cluster (`make smoke` then
  `make scorecard`).
- *(controller)* **Pooler built-in auth** (T27 ⑤) — when
  `spec.pgbouncer.authSecretRef` is empty, the operator creates the
  `keiailab_pooler_pgbouncer` LOGIN role on the PostgresCluster ready
  primary Pod (24-byte crypto/rand base64 password) and a userlist.txt
  Secret `<pooler-name>-builtin-auth` (Pooler-owned). CNPG
  `cnpg_pooler_pgbouncer` compatible. While waiting for the primary,
  Phase=Pending + Reason=BuiltinAuthWaitingForPrimary, idempotent ensure.
  The `PoolerReconciler.PodExecutor` wiring regression was also fixed
  (restores PAUSE/RESUME + SIGHUP-reload paths).
- *(controller)* **Pooler AutoTLS — cert-manager integration** (T29) — when `spec.pgbouncer.autoTLS` is set, the operator emits a cert-manager `Certificate` CR (per role: server / client) via `unstructured` and lets cert-manager provision a Secret (`tls.crt` + `tls.key` + `ca.crt`); the PgBouncer Deployment mounts the auto-generated Secret transparently. Explicit `Server/ClientTLSSecret` still wins. New helpers (`poolerEffectiveServerTLSSecretName`, `poolerEffectiveClientTLSSecretName`, `poolerAutoTLS{Server,Client}Active`), new RBAC marker for `cert-manager.io/certificates`, 2 new unit tests (`TestPoolerAutoTLS_CreatesCertificate`, `TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence`), and a new sample `config/samples/postgres_v1alpha1_pooler_autotls.yaml`.
- *(controller)* **Pooler built-in auth password rotation** (T27 ⑥) —
  setting `postgres.keiailab.io/rotate-pooler-password=true` triggers an
  `ALTER ROLE` with a new password, an in-place userlist.txt Secret
  update, removal of the annotation, and recording of
  `Pooler.Status.BuiltinAuthLastRotation`. New unit test
  `TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation`.
- *(docs)* ROADMAP "current state" snapshot — added 4 rows for
  alpha.18 + OLM bundle + CNPG-compatible surface + local 4-layer gate.
- *(docs)* `cross-validation-cnpg.md` matrix — added 5 dimensions:
  OLM bundle / Helm chart / Local supply-chain gates / Security
  vulnerability scan / DCO sign-off enforcement.

### Fixed

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; Kubelet / CRI-O / kube-apiserver DoS via SPDY
  streaming). `trivy fs --severity HIGH,CRITICAL --exit-code 1` is green
  again.
- *(ci,kustomize)* Closed a drift where the manager Deployment did not
  list the 8081 health port in `containerPorts`. `config/manager/manager.yaml`
  goes from `ports: []` to `ports: [{name: health, containerPort: 8081,
  protocol: TCP}]`, aligning the helm chart and `dist/install.yaml`
  manager Deployments (kube-linter liveness-port / readiness-port
  checks).
- *(docs,license)* Removed the stale `Citus AGPL-3.0` entry from NOTICE —
  ADR-0003 (license policy that permanently forbids AGPLv3) and ADR-0001
  (self-built distributed SQL). NOTICE now lists only direct dependencies
  from `go.mod` (Prometheus, Ginkgo, robfig/cron, moby/spdystream, …).

## [0.3.0-alpha.18] - 2026-05-12

### Added

- *(api,controller)* `ImageCatalog` + `ClusterImageCatalog` CRDs ported
  (TASKS T24). CloudNativePG-compatible `spec.imageCatalogRef.{apiGroup,
  kind,name,major}`, namespaced / cluster-scoped lookup, catalog →
  StatefulSet image propagation, image-hash annotation-driven rollout
  drift.
- *(api,controller)* `PostgresDatabase` + `PostgresUser` CRDs (TASKS
  T22). Ready-primary `psql` reconcile applies database / tablespace /
  schema / extension / FDW / foreign server, plus role flags / membership
  / `connectionLimit` / `passwordSecretRef` / `disablePassword` /
  `validUntil`. `databaseReclaimPolicy=delete` finalizer +
  `status.applied/observedGeneration/conditions` +
  `managedRolesStatus` aggregation.
- *(controller,instance)* Standalone replica cluster + externalClusters
  streaming path (TASKS T25). `spec.externalClusters[]`,
  `bootstrap.pg_basebackup.source`, `replica.enabled/source`.
  `POSTGRES_REPLICA_CLUSTER=standalone` persistent-follower election,
  password Secret passfile + TLS Secret projected mount, source-mismatch
  fail-closed.
- *(api,controller)* `Pooler` CRD + PgBouncer connection-pool layer (F05).
  `instances`, `type=rw/ro`, `pgbouncer.{poolMode,parameters,pg_hba}`,
  auth / TLS Secret, exporter sidecar, `spec.paused` PAUSE/RESUME,
  `pgbouncer.parameters` SIGHUP reload, HA topology / PDB.
- *(observability)* metrics + Grafana dashboards + PrometheusRule +
  ServiceMonitor (F05). BackupJob / Pooler phase metrics, replication-lag
  bytes, PgBouncer exporter alerts, cluster-overview + Pooler dashboard
  ConfigMap, compatible with the kube-prometheus-stack sidecar.
- *(controller,instance)* Failover promoter execution + follower election
  (F03 follow-up, PR #38/#39 landing). Replica-Pod `postgres` container
  exec → `pg_ctl promote` → `pg_is_in_recovery()` polling → primary
  annotation patch.
- *(backup)* `ScheduledBackup` CRD + sidecar exec runner + pgBackRest
  command-runner plugin (F04). 6-field cron + `concurrencyPolicy`
  Allow/Forbid + retention + JobTemplate.
- *(release,ci)* Artifact Hub auto-registration / smoke
  `hack/artifacthub_*.sh` + Makefile `artifacthub-{register,smoke}`
  targets. Added `SMOKE_HIBERNATION=1` (`cnpg.io/hibernation` annotation
  + PVC marker preservation) and `SMOKE_POOLER=1` (PgBouncer Service psql
  / PAUSE / RESUME / config reload) scenarios to the kind smoke.
  `make validate` raises the CRD count assertion from 2 to 8 and adds 18
  monitoring-render grep checks.
- *(olm)* `bundle/manifests/` aligned for 0.3.0-alpha.18 — 8 CRDs +
  alm-examples consistent (`operator-sdk bundle validate` 0 warnings).
  All 7 owned-CRD `config/samples/` files enabled.

### Fixed

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; Kubelet / CRI-O / kube-apiserver DoS via SPDY
  streaming). Indirect surface from k8s.io/client-go refreshed.

### Changed

- *(chart)* `version` 0.3.0-alpha.16 → 0.3.0-alpha.18, `appVersion`
  0.3.0-alpha.17 → 0.3.0-alpha.18, manager-image `newTag`
  0.3.0-alpha.18. The previous alpha.17 bump left `version:
  0.3.0-alpha.16` behind — this cycle aligns all three.

## [0.3.0-alpha.17] - 2026-05-12

### Fixed

- *(bootstrap)* PID-alive check for non-empty stale `postmaster.pid`
  (argos INC-0046 P19 ⑲). Closes the regression where a leftover
  zombie file blocked fresh PG startup.

## [0.3.0-alpha.16] - 2026-05-10

### Bug fixes

- *(lint)* SA1019 + gocyclo nolint (mongodb ADR-0022 pattern alignment).
- *(bundle)* Dropped the generate-kustomize-manifests step (PR-B9.4,
  mongodb ADR-0023 alignment) (#25).

### Chores

- *(oss)* Added `CITATION.cff` (#23).

### Features

- *(bundle)* OperatorHub.io bundle scaffold + ADR-0013 (PR-B9 cross-cut)
  (#24).

## [0.3.0-alpha.12] - 2026-05-08

### Fixed

- `copySpec` panic — `*unstructured.Unstructured` (cert-manager
  `Certificate` CR) was not supported. Switch case added (NestedMap spec +
  Labels).

## [0.3.0-alpha.11] - 2026-05-08

### Fixed

- Missing `cert-manager.io/certificates` rule in the Helm chart's
  `rbac.yaml` (the alpha.10 controller-gen update only synced
  `config/rbac/role.yaml`; the Helm chart `rbac.yaml` is manually
  maintained). The live-cluster `ClusterRole` was out of sync, leaving
  the `Certificate` request Forbidden.

## [0.3.0-alpha.10] - 2026-05-08

### Fixed

- Missing `cert-manager.io/certificates` RBAC on the ClusterRole →
  Phase-2 `Certificate` CR upserts were Forbidden. Added the
  `kubebuilder:rbac` marker.

## [0.3.0-alpha.9] - 2026-05-08

### Fixed

- `buildCertificate` panic — `unstructured.SetNestedField` `dnsNames`
  converted `[]string` → `[]any` for deep-copy compatibility. Caught in
  the first live application after alpha.8.

## [0.3.0-alpha.8] - 2026-05-08

### Added (Pillar P7 §7 — TLS integration 3-phase finalize)

- **Phase 1 (alpha.5)**: `spec.tls` field facade —
  `TLSSpec{Enabled, IssuerRef, CertSecretName}`. Webhook rejects with
  `NotImplemented` when `enabled=true`.
- **Phase 2 (alpha.6)**: Automatic cert-manager `Certificate` CR emission
  (unstructured, zero cert-manager Go SDK dependency). When `IssuerRef` is
  set and `Enabled=true`, the reconciler delegates issuance of the
  `<cluster>-tls` Secret. SANs = cluster name + DNS forms 4× per shard
  headless service. ECDSA P-256 with `rotationPolicy=Always`.
- **Phase 3a (alpha.7)**: STS `Volumes` + `VolumeMounts` for server-cert
  mount (`/etc/ssl/postgres`, `defaultMode=0o400` for PG key-file
  permission check).
- **Phase 3b (alpha.8)**: `postgresql.conf` gets `ssl=on` +
  `ssl_cert_file` / `ssl_key_file` / `ssl_ca_file` +
  `ssl_min_protocol_version=TLSv1.2`. `pg_hba.conf` switches `host` →
  `hostssl` (forbids plaintext external client connections; replication
  stays on `host` because pod-to-pod is the trust boundary).

### Refactored

- Cyclomatic-complexity reduction in `Reconcile` — extracted
  `reconcileInstanceRBAC` (3 upserts unified) and `reconcileTLS` helpers.
  gocyclo < 30 baseline restored.

## [0.3.0-alpha.4] - 2026-05-08

### Fixed

- Restored the `dist/install.yaml` / Helm chart / live GitOps dry-run
  verification flow so the `PostgresCluster` install bundle once again
  passes server-side dry-run.
- Aligned the release-gate baseline with the Go 1.25.10 builder image to
  match the stdlib security baseline.

## [0.3.0-alpha.3] - 2026-05-07

### Fixed

- When a Postgres Pod with an existing PGDATA restarts, the bootstrap
  init container now re-runs `chmod 0700 "$PGDATA"` even after kubelet
  has applied `fsGroup`. The regression was observed live during
  `data/argos-postgres-shard-0-0` re-creation, where PostgreSQL exited
  with `invalid permissions`.

## [0.3.0-alpha.2] - 2026-05-07

### Added

- `hack/smoke.sh` PG17/PG18 matrix overrides (`PG_MAJOR`,
  `POSTGRES_VERSION`, `SHARD_REPLICAS`) and the HA WAL-streaming gate.
- PG18 failover smoke gate: measure standby-promotion RTO after a
  primary Pod delete, confirm CR-status primary convergence, and verify
  the restarted old primary re-enters as standby.
- `deploy/overlays/prod/` GitOps entry point — aligns kubebuilder
  `config/{crd,rbac,manager}` into the prod namespace and removes any
  auto-generated Namespace resources. Presumes one-way ArgoCD sync.
- `deploy/postgres-cluster.yaml` — production `PostgresCluster` CR sample
  (db namespace, `shardingMode=none`, `replicas=2`, ceph-block,
  monitoring on).
- `deploy/README.md` — operational runbook (prerequisites, application,
  rollback).
- ADR-0006 — GitOps deploy-overlay adoption decision (3-repo structural
  alignment with mongodb-operator / valkey-operator).

### Fixed

- Switched election identity to `podName/podUID` so that re-creating an
  ordinal at the same name cannot immediately reclaim the previous
  primary's lease.
- Restarted ordinal-0 primary now reconstructs `standby.signal` /
  `primary_conninfo`; `ReleaseOnCancel=false` and status polling were
  added — observed RTO 21 s (< 30 s) on the PG18 failover smoke.

## [0.3.0-alpha.1] - 2026-05-06

### Changed

- Chart.yaml `version` + `appVersion` 0.3.0-alpha → 0.3.0-alpha.1
  (iterative pre-release notation, matching mongodb-operator beta.N +
  valkey-operator alpha.N).
- `config/manager/kustomization.yaml` `newTag` synced.
- `dist/install.yaml` regenerated (`make build-installer`) — image tag
  0.3.0-alpha.1.

### Fixed

- The `release` target now builds and pushes the image with
  `docker buildx build --platform linux/amd64 --push` (per org §2,
  default builder explicit). Build + push are now atomic in a single
  call (the separate `$(CONTAINER_TOOL) build` is gone).

### Changed (BREAKING)

- **`PostgresCluster` CRD schema redefinition (RFC 0001 v2 — F01a)**:
  removed `spec.coordinator` / `spec.workers[]` / `spec.routers` /
  `spec.extensions` / `spec.sharding.backend` / `spec.deployment`.
  Replaced with the new 6-field structure (`postgresVersion` /
  `shardingMode` / `shards` / `router` / `autoSplit` / `backup` /
  `monitoring`). `status` likewise drops `topology` / `channel` and
  introduces `phase` / `shards[]` / `router`. v0.x manifests are not
  compatible (alpha-channel policy).
- The CRD now embeds the 3 CEL XValidations from RFC 0001 §3.3 —
  `shardingMode↔shards`, `router↔native`, `autoSplit↔native` — so the
  API server rejects directly.
- Webhook validation is simplified to PostgresVersion matrix lookup +
  autoSplit-trigger consistency + non-empty backup schedule. Precise cron
  parsing / duration parsing arrive in F01b/F02 once external dependencies
  are introduced.

### Deferred to F01b

- The new-spec reconcile body (`ShardsSpec` → StatefulSet topology,
  `RouterSpec` → Deployment, `BackupSpec` → automatic `BackupJob`
  creation). This turn leaves a `// TODO(F01b)` comment and a minimal
  noop reconcile (`status.phase=Provisioning`,
  `Ready=False reason=NotApplicable`).
- `internal/controller/builders.go` helpers keep their signatures and
  carry `//nolint:unused` — they will be wired up by the F01b reconcile.
- The 2 envtests (`postgrescluster_controller_test.go`,
  `cascade_delete_test.go`) are removed and will be rewritten against
  the RFC 0001 spec in F01b.

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **Redesign**: pivot to a self-built distributed-SQL layer on top of
  PostgreSQL. ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`)
  is the keystone.
- Supersedes ADR-0010 (legacy Citus AGPL isolation + vanilla-PG default).
  From this phase on, the runtime carries *zero lines* of Citus code; the
  Citus-isolation plugin model is retired.
- External-dependency license policy (ADR-0003): BSD / Apache / MIT / PG
  License with v1+ stability only. **AGPL / BUSL / CSL / SSPL are
  permanently forbidden.**
- Helm packaging (ADR-0002): single chart + component flags (router /
  resharder / rebalancer / keda / backup / monitoring).
- CRD lifecycle (ADR-0004): owned by the operator manager (server-side
  apply). The Helm `crds/` directory will be retired in a future phase.
- Version channels (ADR-0005): alpha (P0–P3) → beta (P4–P5) → stable
  (P6+). CRD apiVersion v1alpha1 → v1beta1 → v1.

### Added

- New ADRs: 0001 (self-built distributed SQL — keystone), 0002 (single
  chart with flags), 0003 (license policy: no AGPL / BUSL / CSL / SSPL),
  0004 (operator-managed CRD lifecycle), 0005 (versioning + channels).
- New RFCs: 0001 (PostgresCluster CRD v2), 0002 (`ShardRange` CRD), 0003
  (`ShardSplitJob` 7-step online resharding), 0004 (pg-router
  architecture), 0005 (distributed transactions — 2PC + saga).
- `README.md` rewrite — self-built distributed-SQL identity, 8-phase
  roadmap (P0–P7, ~64 months), explicit license policy.
- `TASKS.md` rewrite — P0 task table + a preview of the next phase (P1).
- `HANDOFF.md` rewrite — entry point for the next session, code-removal
  isolation guidance.

### Archived

- The original ADR 0001–0010 moved to `docs/kb/adr/_archive/v0.x/` (git
  history preserved).
- The original RFC 0001–0005 moved to `docs/rfcs/_archive/v0.x/`.

### Deprecated (to be removed in the next session)

- `internal/citus/` — violates ADR-0003.
- `internal/plugin/extension/citus/`.
- The Citus opt-in messaging in `charts/postgres-operator/`
  (`citusLibPQ.dsn`, NOTES.txt AGPL guidance).

## [0.2.0-alpha] - 2026-05-01

### Changed (BREAKING)

- ADR 0010 — switched the default stack to vanilla PostgreSQL 18. The
  Citus integration is now isolated to a Beta-channel opt-in. Users who
  enable Citus explicitly accept the AGPL-3.0 §13 SaaS obligation (the
  operator itself stays Apache-2.0 clean).
- `VersionSpec.Citus` is now Optional (`omitempty`) — was Required. An
  empty / missing value selects vanilla PG.
- Stable channel: PG 16/17/18 vanilla. All Citus combinations are
  downgraded to Beta.
- Removed `extensions: [citus]` defaults from the chart's
  `config/samples/*`. The recommended default is now vanilla PG18.

### Added

- Added the PG 18 vanilla Stable combination (`ghcr.io/keiailab/pg:18`) to
  `internal/version/matrix.go`.
- ADR 0010 (license + sharding strategy) — Citus AGPL isolation decision
  + license-obligation allocation record.
- RFC 0005 (native sharding plugin) — decomposition of the 7 core Citus
  mechanisms, draft design of an in-house plugin interface, plus the
  Phase 2A → Phase 4 milestones.
- License-disclosure message in the chart's `NOTES.txt`
  (Apache-2.0 operator + the opt-in AGPL Citus notice).
- Doc warning on `internal/plugin/extension/citus/` package and function
  docs about the AGPL §13 SaaS obligation.

### Removed

- Removed the stale `ChannelPreviewPG18` placeholder — obsolete now that
  PG18 is on Stable.
- Removed the webhook's PG18 + `PostgresEighteen` feature-gate check —
  no longer needed on Stable.

## [0.1.1-alpha] - 2026-05-01

### Added

- Local release automation via `make validate`, `make gate`,
  `make release-preflight`, `make release`, `make helm-publish`.
- `config/crd/kustomization.yaml` restores the
  `make install / uninstall` and CRD-render paths.
- `make sync-crds` blocks drift between `config/crd/bases` and
  `charts/postgres-operator/crds`.
- Helm chart `.helmignore`, `values.schema.json`, README, and Artifact
  Hub metadata.
- `dist/install.yaml` single-install artifact verification path.

### Fixed

- Adjusted the controller test suite to use a local envtest-asset
  fallback when running `go test` directly.
- Aligned the chart's default image repository to
  `ghcr.io/keiailab/postgres-operator`.
- Helm RBAC now includes `BackupJob` resource permissions.
