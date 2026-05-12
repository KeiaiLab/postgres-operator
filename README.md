# postgres-operator

> **Apache-2.0 PostgreSQL Kubernetes Operator** — vanilla PG18+, license-clean, targets PGO-class operational quality without forking, embedding, or wrapping any external operator/backend.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql)](https://www.postgresql.org/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io/)
[![Container Image](https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github)](https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator)
[![Helm Chart](https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v)](https://keiailab.github.io/postgres-operator)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/keiailab-postgres-operator)](https://artifacthub.io/packages/helm/keiailab-postgres-operator/postgres-operator)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/keiailab/postgres-operator/badge)](https://scorecard.dev/viewer/?uri=github.com/keiailab/postgres-operator)
[![GitHub Discussions](https://img.shields.io/github/discussions/keiailab/postgres-operator?label=discussions&logo=github)](https://github.com/keiailab/postgres-operator/discussions)

---

## Identity

This operator builds a *self-built distributed SQL layer* on top of PostgreSQL. We reference the public designs and operational idioms of PGO, Citus, Vitess, CloudNativePG, Patroni, and CockroachDB, but do not embed or wrap any of those systems as a runtime dependency. The code, CRDs, reconciler, instance manager, and router are all implemented directly in this repository under Apache-2.0–compatible terms.

Differentiators:

- **100% PostgreSQL 18+ compatible** — adopt distribution without changing application code. All PG extensions / types / functions remain available.
- **License-clean** — Apache-2.0 operator plus only BSD/Apache/MIT/PG-License dependencies. No copyleft obligations on SaaS exposure.
- **K8s-native auto-sharding roadmap** — `ShardRange` CRD as source of truth, KEDA-driven auto-split, 7-step online resharding (cutover SLA target p99 < 500 ms).
- **Single-endpoint roadmap** — applications connect to the `pg-router` Deployment over the PostgreSQL wire protocol, with no sharding awareness required.

PGO-class is a *quality bar* — not a PGO fork or PGO embedding. Citus-class distribution likewise does not mean shipping the Citus extension; it means re-implementing the problem space Citus has validated as a PostgreSQL-compatible new service. The Plugin SDK message from the v0.x archive has been retired; the current direction is narrowly scoped internal modules and explicit CRDs.

ADR 0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) is the keystone of this decision.

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

Details: `docs/architecture/overview.md` (to be added in P0).

## Phase roadmap

| Phase | Version | Key deliverable | Estimated duration |
|---|---|---|---|
| **P0** | 0.3.0 | Redesign reset (ADR/RFC 0001–0005, README, code removal) | 2 months |
| **P1** | 0.4.0 | Single-shard production-ready (HA / backup / PITR) | 6 months |
| **P2** | 0.5.0 | pg-router + `ShardRange` CRD (manual multi-shard ops) | 10 months |
| **P3** | 0.6.0 | vindex extension + scatter-gather + read replica autoscale | 8 months |
| **P4** | 0.7.0 | `ShardSplitJob` 7-step (manual online split trigger) | 12 months |
| **P5** | 0.8.0 | KEDA auto-split + rebalancer (auto-sharding reached) | 8 months |
| **P6** | 0.9.0 | Distributed transactions (2PC + saga) + cross-shard JOIN | 12 months |
| **P7** | **1.0.0** | Stabilization + chaos / benchmark + Artifact Hub verified | 6 months |

**Total ≈ 64 months (5.3 years)** assuming one engineer at 50% capacity. Each phase ends with a *production-deployable* artifact.

## License policy (ADR 0003)

External OSS dependencies are permitted only when *all* of the following hold:
- License: BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API: v1+ stability commitment (12-month deprecation policy)

**Permanently forbidden**: AGPLv3 / BUSL / CSL / SSPL.

Automated enforcement: `scripts/check-license-policy.sh` (P0 follow-up; will be wired as a lefthook L2 pre-push hook).

## Quickstart

```bash
# 1. Install the operator + 8 CRDs (helm chart or OperatorHub bundle)
helm install pgo charts/postgres-operator

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
helm upgrade pgo charts/postgres-operator \
  --reuse-values \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

See [`docs/operator-guide/deployment.md`](docs/operator-guide/deployment.md) and [`docs/operator-guide/pooler-monitoring.md`](docs/operator-guide/pooler-monitoring.md) for the operations playbook.

**Current state (0.3.0-alpha.18, 2026-05-12)**: on the argos Kubernetes cluster, the ArgoCD Application `platform-data-postgres-operator` is `Synced/Healthy` and `PostgresCluster/argos-postgres` reports `Ready=True`. The helm chart and OperatorHub bundle ship **8 owned CRDs**:

| CRD | Role | Status |
|---|---|---|
| `PostgresCluster` | Shard-aware topology (primary + standby + native-sharding roadmap) | ✅ deployable |
| `BackupJob` | Atomic backup/restore Job (pgBackRest plugin) | ⚠️ controller partial |
| `ScheduledBackup` | Cron-driven BackupJob generation (6-field schedule) | ⚠️ controller partial |
| `Pooler` | PgBouncer connection pool layer (CNPG-compatible surface) | ⚠️ controller partial |
| `PostgresDatabase` | Declarative database/schema/extension/FDW (ready-primary psql) | ⚠️ controller partial |
| `PostgresUser` | Declarative role + password + membership (ready-primary psql) | ⚠️ controller partial |
| `ImageCatalog` | Namespace-scoped PostgreSQL runtime image catalog (CNPG-compatible) | ⚠️ rollout path |
| `ClusterImageCatalog` | Cluster-wide shared PostgreSQL runtime image catalog | ⚠️ rollout path |

GA distance: 0.4.0 production-ready (HA replicas, backup/restore drill, PITR, chaos-mesh failover) and P2 multi-shard remain in subsequent phases. See [`docs/operator-guide/cross-validation-cnpg.md`](docs/operator-guide/cross-validation-cnpg.md) for the feature matrix against CloudNativePG.

## Contributing

```bash
make lint test validate    # Local 4-layer L3 gate
make sync-crds              # Verify config/crd/bases ↔ chart synchronization
make test-e2e PILLAR=p1     # Kind-cluster e2e
```

GitHub Actions is permanently forbidden (RFC 0002 archive); all gates run locally (pre-commit / pre-push / Makefile / PR review).

See `CONTRIBUTING.md` for the contributor guide, `GOVERNANCE.md` for the governance model, and `CODE_OF_CONDUCT.md` for the code of conduct.

## Documentation

- `docs/architecture/` — Distributed-system design (overview / routing-layer / sharding-model / consistency / ha-and-fencing) — *to be added in P0*
- `docs/kb/adr/` — Architecture Decision Records (current: 0001–0005; archive in `_archive/v0.x/`)
- `docs/rfcs/` — RFC drafts (current: 0001–0005)
- `docs/api-reference/` — CRD reference (auto-generated, planned)
- `docs/runbooks/` — Operations procedures (split / failover / backup, planned for P4+)
- `docs/tutorials/` — Step-by-step user guides (planned for P1+)

## Community

- **Discussions**: [GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) — usage questions, feature ideas, operational war stories.
- **Issues**: [GitHub Issues](https://github.com/keiailab/postgres-operator/issues) — bugs and feature requests (please file reproducible cases).
- **Security reports**: [SECURITY.md](SECURITY.md) — vulnerabilities are reported via a *private* channel (GitHub Security Advisory).
- **Governance**: [GOVERNANCE.md](GOVERNANCE.md) — decision process (lazy consensus / 2/3 supermajority).

## License

Apache-2.0. See the `LICENSE` file.

## Maintainer

[@phil](https://github.com/phil) — `eightynine01@gmail.com`
