<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator

> **Apache-2.0 PostgreSQL Operator for Kubernetes — vanilla PG18+, license-clean, K8s-native auto-sharding roadmap**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go" alt="Go Version"/></a>
  <a href="https://www.postgresql.org/"><img src="https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql" alt="PostgreSQL"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes" alt="Kubernetes"/></a>
  <a href="https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator"><img src="https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github" alt="Container Image"/></a>
  <a href="https://keiailab.github.io/postgres-operator"><img src="https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v" alt="Helm Chart"/></a>
</p>

<p align="center">
  <b>English</b> |
  <a href="docs/README.ko.md">한국어</a> |
  <a href="docs/README.ja.md">日本語</a> |
  <a href="docs/README.zh.md">中文</a>
</p>

---

## Identity

`postgres-operator` is an Apache-2.0 Kubernetes operator that runs vanilla PostgreSQL 18+. It builds a *self-built distributed SQL layer* on top of upstream PostgreSQL with no embedded runtime backend. All code — CRDs, reconcilers, instance manager, router, and webhook — is implemented directly in this repository and ships under Apache-2.0–compatible terms.

Differentiators:

- **100% PostgreSQL 18+ compatible** — adopt distribution without changing application code. All upstream PostgreSQL extensions, types, and functions remain available.
- **License-clean** — Apache-2.0 operator plus only BSD / Apache / MIT / PostgreSQL-License / ISC / MPL-2.0 dependencies. No copyleft obligations on SaaS exposure.
- **K8s-native auto-sharding roadmap** — the `ShardRange` CRD is the source of truth, with KEDA-driven auto-split and a 7-step online resharding workflow (cutover SLA target: p99 < 500 ms).
- **Single-endpoint roadmap** — applications connect to the `pg-router` Deployment over the PostgreSQL wire protocol with no sharding awareness required.

[`docs/kb/adr/0001-self-built-distributed-sql.md`](docs/kb/adr/0001-self-built-distributed-sql.md) is the keystone ADR for the self-built decision.

## Architecture (summary)

```
Application (libpq / JDBC / asyncpg)
    │  PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │  - vindex evaluation (hash / range / consistent-hash / lookup)
    │  - single-shard fast path / multi-shard scatter-gather
    │  - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (per shard: 1 primary + N replicas)
    │ instance manager (election + fencing + supervise postgres)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth)
  - ShardSplitJob reconciler (7-step workflow)
  - Rebalancer / Backup / Autoscaler glue
    │
  KEDA + Prometheus  (auto-split trigger: size + p99 + cpu)
```

Details: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Features

### Currently shipping (v0.3.0-alpha.18)

The helm chart and OperatorHub bundle ship **8 owned CRDs**:

| CRD | Role | Status |
|---|---|---|
| `PostgresCluster` | Shard-aware topology (primary + standby + native-sharding roadmap) | ✅ deployable |
| `BackupJob` | Atomic backup/restore Job (pgBackRest plugin) | ⚠️ controller partial |
| `ScheduledBackup` | Cron-driven BackupJob generation (6-field schedule) | ⚠️ controller partial |
| `Pooler` | PgBouncer connection pool layer | ⚠️ controller partial |
| `PostgresDatabase` | Declarative database / schema / extension / FDW (ready-primary psql) | ⚠️ controller partial |
| `PostgresUser` | Declarative role + password + membership (ready-primary psql) | ⚠️ controller partial |
| `ImageCatalog` | Namespace-scoped PostgreSQL runtime image catalog | ⚠️ rollout path |
| `ClusterImageCatalog` | Cluster-wide shared PostgreSQL runtime image catalog | ⚠️ rollout path |

Helm chart adds: PrometheusRule + Grafana dashboards (Pooler overview + Cluster overview), restricted PSA SecurityContext, deny-by-default NetworkPolicy, cert-manager TLS integration, OpenTelemetry-ready hooks.

### Roadmap (phase plan)

| Phase | Version | Key deliverable |
|---|---|---|
| **P0** | 0.3.0 | Redesign reset (ADR / RFC 0001–0014, architecture docs, runbook scaffolding) |
| **P1** | 0.4.0 | Single-shard production-ready (HA / backup / PITR drill / Lease election) |
| **P2** | 0.5.0 | pg-router + `ShardRange` CRD (manual multi-shard ops) |
| **P3** | 0.6.0 | vindex extension + scatter-gather + read-replica autoscale |
| **P4** | 0.7.0 | `ShardSplitJob` 7-step (manual online split trigger) |
| **P5** | 0.8.0 | KEDA auto-split + rebalancer (auto-sharding reached) |
| **P6** | 0.9.0 | Distributed transactions (2PC + saga) + cross-shard JOIN |
| **P7** | **1.0.0** | Stabilization + chaos / benchmark + Artifact Hub verified |

Full phase detail (sub-tasks, SLO, ADR/RFC references): [`docs/ROADMAP.md`](docs/ROADMAP.md).

## License policy

External OSS dependencies are permitted only when *all* of the following hold:

- License: BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API: v1+ stability commitment (12-month deprecation policy)

**Permanently forbidden**: AGPLv3 / BUSL / CSL / SSPL.

Automated enforcement: `scripts/check-license-policy.sh` (wired as a lefthook pre-push hook).

## Quickstart

```bash
# 1. Install the operator + 8 CRDs (helm chart or OperatorHub bundle)
helm install postgres-operator charts/postgres-operator

# 2. Apply the quickstart PostgresCluster
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml

# 3. Wait for Ready
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m

# 4. (Optional) Apply declarative database/user resources
kubectl apply -f config/samples/postgres_v1alpha1_postgresdatabase.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresuser.yaml

# 5. (Optional) Apply a PgBouncer Pooler and a cron backup
kubectl apply -f config/samples/postgres_v1alpha1_pooler.yaml
kubectl apply -f config/samples/postgres_v1alpha1_scheduledbackup.yaml

# 6. Enable monitoring (requires prometheus-operator)
helm upgrade postgres-operator charts/postgres-operator \
  --reuse-values \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

See [`docs/operator-guide/deployment.md`](docs/operator-guide/deployment.md) and [`docs/operator-guide/pooler-monitoring.md`](docs/operator-guide/pooler-monitoring.md) for the operations playbook.

## Production readiness

**Current state (0.3.0-alpha.18)**: on the reference Kubernetes cluster, the ArgoCD Application `platform-data-postgres-operator` is `Synced/Healthy` and `PostgresCluster/postgres` reports `Ready=True`.

GA distance:

- **P1** — production-ready single-shard requires the HA Lease distributed-lock controller, the `BackupJob` / `ScheduledBackup` live drill, the PITR checksum drill, and the chaos failover suite.
- **P2** — multi-shard requires the `ShardRange` CRD + `pg-router` PoC (see [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md)).
- The current alpha is **not** recommended for production data without your own backup/restore verification.

## Known limitations

- `BackupJob` / `ScheduledBackup` / `Pooler` / `PostgresDatabase` / `PostgresUser` controllers are *partial* — the CRD surface ships and reconciles the core path, but live drill verification (rotation / PITR / retain-policy) is still pending and tracked per phase.
- `ImageCatalog` / `ClusterImageCatalog` rollout-drift measurement is implemented at the StatefulSet annotation layer; production rollout SLA is not yet certified.
- The Sharding subsystem (`ShardRange`, `pg-router`, `ShardSplitJob`) is **design-only** — see [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md) for spec; no runtime code yet.
- The Phase roadmap above implies a multi-year horizon — operational scope today is single-shard HA only.

## Uninstall

```bash
# 1. Drop CR instances first (otherwise finalizers block CRD removal)
kubectl delete postgrescluster --all -A
kubectl delete pooler --all -A
kubectl delete scheduledbackup --all -A

# 2. Uninstall the chart
helm uninstall postgres-operator

# 3. Remove CRDs (optional; helm keeps CRDs by default to preserve cluster state)
kubectl delete crd postgresclusters.postgres.keiailab.com \
                  backupjobs.postgres.keiailab.com \
                  scheduledbackups.postgres.keiailab.com \
                  poolers.postgres.keiailab.com \
                  postgresdatabases.postgres.keiailab.com \
                  postgresusers.postgres.keiailab.com \
                  imagecatalogs.postgres.keiailab.com \
                  clusterimagecatalogs.postgres.keiailab.com
```

## Contributing

```bash
make lint test validate    # Local 4-layer gate (lint + test + audit + validate)
make sync-crds              # Verify config/crd/bases ↔ chart synchronization
make test-e2e PILLAR=p1     # Kind-cluster e2e
```

The release gate is enforced **locally** via lefthook (pre-commit / commit-msg / pre-push) and the `make gate` target. See [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for the contributor guide, [`docs/GOVERNANCE.md`](docs/GOVERNANCE.md) for the governance model (lazy consensus / 2/3 supermajority), and [`docs/CODE_OF_CONDUCT.md`](docs/CODE_OF_CONDUCT.md) for the code of conduct.

## Documentation

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — Single-page architecture description (8 CRD surface + self-built distributed SQL + G0-G6 status + ADR cross-link)
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — Phase roadmap and Gate checklists
- [`docs/CHANGELOG.md`](docs/CHANGELOG.md) — Release history (en / [ko](docs/CHANGELOG.ko.md) / [ja](docs/CHANGELOG.ja.md) / [zh](docs/CHANGELOG.zh.md))
- [`docs/kb/adr/INDEX.md`](docs/kb/adr/INDEX.md) — Architecture Decision Records (active set)
- [`docs/kb/deps/2026-05.md`](docs/kb/deps/2026-05.md) — Monthly dependency change log (lockfile diff append-only)
- [`docs/rfcs/INDEX.md`](docs/rfcs/INDEX.md) — RFC index (0001–0007 active drafts)
- [`docs/runbooks/INDEX.md`](docs/runbooks/INDEX.md) — Operations runbook index (ha / backup / restore / upgrade / security / migration / pvc-fence)
- [`docs/operator-guide/`](docs/operator-guide/) — Deployment, pooler-monitoring, ha-election, community-operators onboarding
- [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md) — Sharding architecture spec
- [`docs/sql/isolation-matrix.md`](docs/sql/isolation-matrix.md) — Distributed-SQL isolation guarantees
- [`docs/perf/baseline.md`](docs/perf/baseline.md) — Performance baseline
- [`docs/releases/release-process.md`](docs/releases/release-process.md) — Release process
- [`docs/UPGRADING.md`](docs/UPGRADING.md) — Upgrade guide (en / [ko](docs/UPGRADING.ko.md) / [ja](docs/UPGRADING.ja.md) / [zh](docs/UPGRADING.zh.md))

## Reporting vulnerabilities

Please **do not** open a public issue for security reports. Use the private GitHub Security Advisory channel per [`docs/SECURITY.md`](docs/SECURITY.md). We respond within 5 business days and coordinate disclosure timelines for high-severity findings.

## Community

- **Discussions**: [GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) — usage questions, feature ideas, operational war stories.
- **Issues**: [GitHub Issues](https://github.com/keiailab/postgres-operator/issues) — bugs and feature requests (please file reproducible cases; the `question.yml` template guides Q&A).
- **Governance**: [`docs/GOVERNANCE.md`](docs/GOVERNANCE.md) — decision process (lazy consensus / 2/3 supermajority).
- **Sponsorship**: see [`.github/FUNDING.yml`](.github/FUNDING.yml) for the GitHub Sponsors button.

## License

Apache-2.0. See the [`LICENSE`](LICENSE) file.

## Maintainer

[@phil](https://github.com/phil) — `support@masblue.studio`. Maintainer roster: [`docs/MAINTAINERS.md`](docs/MAINTAINERS.md).

---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
