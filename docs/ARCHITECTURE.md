<p align="center">
  <b>English</b> |
  <a href="ARCHITECTURE.ko.md">한국어</a> |
  <a href="ARCHITECTURE.ja.md">日本語</a> |
  <a href="ARCHITECTURE.zh.md">中文</a>
</p>

# ARCHITECTURE — postgres-operator

> Single-page architecture description. Updated when CRD surface / Gate / reconcile pattern changes.

## Overview

- **Purpose**: Apache-2.0 PostgreSQL Kubernetes Operator delivering production-grade operational quality and distributed SQL via *self-built* code — no external PostgreSQL operator fork or wrapper.
- **Scope**: vanilla PostgreSQL 18+ on K8s, single-shard HA → sharding → online resharding → distributed SQL → GA.
- **Stability tier**: v0.4.0-beta.1 — Level 4 Deep Insights (metrics, alerts, dashboards, WAL archiving, backup retention, switchover)
- **License**: Apache-2.0 (deps: BSD/Apache/MIT/PG-License only — no copyleft on SaaS)
- **Module path**: `github.com/keiailab/postgres-operator`

## CRD surface (8 CRDs)

| CRD | apiVersion | Scope | Description |
|---|---|---|---|
| `PostgresCluster` | `postgres.keiailab.com/v1alpha1` | Namespaced | Primary HA controller — StatefulSet + WAL + failover |
| `BackupJob` | `postgres.keiailab.com/v1alpha1` | Namespaced | pgBackRest backup / restore / PITR |
| `ScheduledBackup` | `postgres.keiailab.com/v1alpha1` | Namespaced | Cron-based BackupJob trigger |
| `PostgresDatabase` | `postgres.keiailab.com/v1alpha1` | Namespaced | Declarative database + schema + privilege |
| `PostgresUser` | `postgres.keiailab.com/v1alpha1` | Namespaced | Declarative role + password rotation |
| `Pooler` | `postgres.keiailab.com/v1alpha1` | Namespaced | PgBouncer connection pooler |
| `ImageCatalog` / `ClusterImageCatalog` | `postgres.keiailab.com/v1alpha1` | Namespaced / Cluster | Image catalog for declarative upgrades |
| (G3+ planned) `ShardRange` / `ShardSplitJob` | — | — | Sharding metadata + 7-step online resharding |

## Self-built distributed SQL architecture

```
Application (libpq / JDBC / asyncpg)
    │ PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │ - vindex evaluation (hash / range / consistent-hash / lookup)
    │ - single-shard fast path / multi-shard scatter-gather
    │ - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (per shard: 1 primary + N replicas)
    │ instance manager (election + fencing + supervise postgres)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth — G3+)
  - ShardSplitJob reconciler (7-step workflow — G4+)
  - Rebalancer / Backup / Autoscaler glue
```

ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) is the keystone — *no external operator embedding*.

## RBAC scope

- ClusterRole: CRD watch + cert-manager Certificate + ImageCatalog cluster-scope
- Role (per ns): StatefulSet / Service / Secret / ConfigMap / PVC / PDB / NetworkPolicy / Job / PgBouncer
- ServiceAccount: `postgres-operator`

## Common library packages

Adoption: **5/8 (63%)**.

| Package | Status | Usage |
|---|---|---|
| `pkg/security` | ✅ | restricted PSA (it8) |
| `pkg/version` | ⏳ | Local `version.Combo` richer — delegation deferred |
| `pkg/labels` | ✅ | Recommended labels (it28) |
| `pkg/monitoring` | ⏳ | ServiceMonitor local impl — delegation pending |
| `pkg/networkpolicy` | ⏳ | Local NetworkPolicy — delegation pending |
| `pkg/webhook` | ✅ | Validation helpers (it34) |
| `pkg/finalizer` | ✅ | `Add` / `Remove` / `Has` |
| `pkg/status` | ✅ | Condition reasons |

## Gate plan (G0 → G6)

| Gate | Goal | Status |
|---|---|---|
| G0 | Day-0 deployment | **100%** (7/7) |
| G1 | Single-shard HA (failover + sync repl + PVC fence + lease) | 81% (HA election Lease pending) |
| G2 | Operational quality (TLS auto / PrometheusRule / Grafana / Pooler / RBAC / ImageCatalog / Hibernation) | 72% (live drill pending) |
| G3 | Sharding foundation (`ShardRange` CRD + pg-router PoC + metadata) | 37% |
| G4 | Online resharding (`ShardSplitJob` 7-step) | 0% |
| G5 | Distributed SQL (scatter-gather + 2PC/saga + isolation + benchmarks) | 0% |
| G6 | 1.0.0 GA (soak ≥7d + chaos + SBOM + cosign + 6 runbooks) | 12% |

## Test layers

| Layer | Location | Coverage |
|---|---|---|
| Unit | `internal/**/_test.go`, `api/**/_test.go` | `make test-unit` |
| Integration (envtest) | `test/integration/` | `make test-integration` |
| E2E (kind) | `test/e2e/{*,pg,failover,sharding}/` | `make test-e2e*` |
| Bench | `test/bench/` (G5) | sysbench / pgbench |
| Scorecard | `bundle/tests/scorecard/` | OLM v1alpha3 |

## Build / deploy

- Container image: `ghcr.io/keiailab/postgres-operator:v0.4.0-beta.1`
- Helm chart: `charts/postgres-operator/` (`keiailab.github.io/postgres-operator`)
- OLM bundle: `bundle/`
- ArtifactHub: `keiailab-postgres-operator`
- pg-router: separate binary `cmd/pg-router/` (G3+)

## Security supply chain

- OpenSSF Scorecard active
- License audit allowlist (BSD/Apache/MIT/PG-License only)
- ADR-0009 enforces no legacy GitHub Actions (RFC-0002)
- Lefthook DCO + Conventional Commits + lint gate

## ADR cross-link (24 ADRs)

Notable:
- ADR-0001: self-built distributed SQL (keystone)
- ADR-0006: introduce the GitOps deploy overlay
- ADR-0007: hook tooling — pre-commit instead of lefthook
- ADR-0009: webhook validate — accumulate-errors
- ADR-0013: OperatorHub.io bundle scaffold cross-cut
- ADR-0014: community-operators upstream sync automation
- ADR-0019: GitHub Actions retention (v2.0 dual-track)
- ADR-0022: GHA narrow exception — 3 workflow (helm-publish + release + scorecard)
- ADR-0023: v3.x-stable baseline 인정
- ADR-0024: lefthook pre-push incremental lint + envtest
- ADR-0025: Repmgr / PgBouncer / Barman integration plan (bitnami parity)
- ADR-0026: OperatorHub.io 자동 sync

Full list: `docs/kb/adr/INDEX.md`.

## Non-goals

- ❌ PostgreSQL < 18 (v18 minimum per `pkg/version` decision)
- ❌ Repackaging an external PostgreSQL operator (Apache-2.0 boundary)
- ❌ Embedding a third-party sharding extension (we re-implement the problem space)
- ❌ Third-party HA agent runtime dependency (self-built instance manager)
- ❌ Copyleft dependencies (license-clean Apache-2.0 only)
- ❌ Plugin SDK (retired from v0.x archive — explicit CRDs instead)

## References

- `README.md` — identity + architecture summary + features
- `ROADMAP.md` — Gate matrix checkbox
- `CHANGELOG.md`
- `ADOPTERS.md`
- `CONTRIBUTING.md` + `MAINTAINERS.md`
- `GOVERNANCE.md`
- `SUPPORT.md`
- `AGENTS.md`
- `docs/kb/adr/INDEX.md` — 17 ADRs

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
