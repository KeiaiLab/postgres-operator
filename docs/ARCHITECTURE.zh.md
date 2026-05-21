<p align="center">
  <a href="ARCHITECTURE.md">English</a> |
  <a href="ARCHITECTURE.ko.md">한국어</a> |
  <a href="ARCHITECTURE.ja.md">日本語</a> |
  <b>中文</b>
</p>

# ARCHITECTURE — postgres-operator (中文)

> 单页架构说明。在 CRD 表面 / Gate / reconcile 模式发生变化时更新。

> 英文原文: [ARCHITECTURE.md](ARCHITECTURE.md) — canonical / 正本

## 概述

- **目的**: Apache-2.0 PostgreSQL Kubernetes Operator — 以 *自建* 代码实现 production-grade 运营质量与 distributed SQL。非外部 PostgreSQL operator 的 fork 或 wrapper。
- **范围**: K8s 上的 vanilla PostgreSQL 18+,single-shard HA → sharding → online resharding → distributed SQL → GA。
- **稳定性等级**: v0.3.0-alpha.16 (G0 100% / G1 81% / G2 72% / G3 37% / G4-G5 0% / G6 12%)
- **License**: Apache-2.0 (依赖: 仅 BSD/Apache/MIT/PG-License — SaaS 暴露时 copyleft 义务为 0)
- **Module path**: `github.com/keiailab/postgres-operator`

## CRD 表面 (8 CRD)

| CRD | apiVersion | Scope | 描述 |
|---|---|---|---|
| `PostgresCluster` | `postgres.keiailab.com/v1alpha1` | Namespaced | Primary HA controller — StatefulSet + WAL + failover |
| `BackupJob` | `postgres.keiailab.com/v1alpha1` | Namespaced | pgBackRest backup / restore / PITR |
| `ScheduledBackup` | `postgres.keiailab.com/v1alpha1` | Namespaced | 基于 Cron 的 BackupJob 触发器 |
| `PostgresDatabase` | `postgres.keiailab.com/v1alpha1` | Namespaced | 声明式 database + schema + privilege |
| `PostgresUser` | `postgres.keiailab.com/v1alpha1` | Namespaced | 声明式 role + password rotation |
| `Pooler` | `postgres.keiailab.com/v1alpha1` | Namespaced | PgBouncer 连接池 |
| `ImageCatalog` / `ClusterImageCatalog` | `postgres.keiailab.com/v1alpha1` | Namespaced / Cluster | 声明式升级用 image catalog |
| (G3+ 计划) `ShardRange` / `ShardSplitJob` | — | — | Sharding 元数据 + 7-step online resharding |

## 自建 distributed SQL 架构

```
Application (libpq / JDBC / asyncpg)
    │ PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │ - vindex 评估 (hash / range / consistent-hash / lookup)
    │ - single-shard fast path / multi-shard scatter-gather
    │ - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (每 shard: 1 primary + N replica)
    │ instance manager (election + fencing + postgres 监督)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth — G3+)
  - ShardSplitJob reconciler (7-step workflow — G4+)
  - Rebalancer / Backup / Autoscaler glue
```

ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 是 keystone — *不内嵌任何外部 operator*。

## RBAC 范围

- ClusterRole: CRD watch + cert-manager Certificate + ImageCatalog cluster-scope
- Role (按 ns): StatefulSet / Service / Secret / ConfigMap / PVC / PDB / NetworkPolicy / Job / PgBouncer
- ServiceAccount: `postgres-operator`

## operator-commons import 表面

采用: **5/8 (63%)**。

| 包 | 状态 | 用途 |
|---|---|---|
| `pkg/security` | ✅ | restricted PSA (it8) |
| `pkg/version` | ⏳ | 本地 `version.Combo` 比 commons.MustList 更丰富 — delegation 暂缓 |
| `pkg/labels` | ✅ | 推荐 labels (it28) |
| `pkg/monitoring` | ⏳ | ServiceMonitor 本地实现 — commons delegation 暂缓 |
| `pkg/networkpolicy` | ⏳ | NetworkPolicy 本地实现 — commons delegation 暂缓 |
| `pkg/webhook` | ✅ | Validation 帮助函数 (it34) |
| `pkg/finalizer` | ✅ | `Add` / `Remove` / `Has` |
| `pkg/status` | ✅ | Condition reason |

## Gate plan (G0 → G6)

| Gate | 目标 | 状态 |
|---|---|---|
| G0 | Day-0 deployment | **100%** (7/7) |
| G1 | Single-shard HA (failover + sync repl + PVC fence + lease) | 81% (HA election Lease 暂缓) |
| G2 | 运营质量 (TLS auto / PrometheusRule / Grafana / Pooler / RBAC / ImageCatalog / Hibernation) | 72% (live drill 暂缓) |
| G3 | Sharding foundation (`ShardRange` CRD + pg-router PoC + 元数据) | 37% |
| G4 | Online resharding (`ShardSplitJob` 7-step) | 0% |
| G5 | Distributed SQL (scatter-gather + 2PC/saga + isolation + benchmark) | 0% |
| G6 | 1.0.0 GA (soak ≥7d + chaos + SBOM + cosign + 6 runbook) | 12% |

到 100% 的计划 (G6): `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md` (59 sub-task)。

## 测试层次

| 层次 | 位置 | 覆盖率 |
|---|---|---|
| Unit | `internal/**/_test.go`, `api/**/_test.go` | `make test-unit` |
| Integration (envtest) | `test/integration/` | `make test-integration` |
| E2E (kind) | `test/e2e/{*,pg,failover,sharding}/` | `make test-e2e*` |
| Bench | `test/bench/` (G5) | sysbench / pgbench |
| Scorecard | `bundle/tests/scorecard/` | OLM v1alpha3 |

## 构建 / 部署

- 容器镜像: `ghcr.io/keiailab/postgres-operator:v0.3.0-alpha.16`
- Helm chart: `charts/postgres-operator/` (`keiailab.github.io/postgres-operator`)
- OLM bundle: `bundle/`
- ArtifactHub: `keiailab-postgres-operator`
- pg-router: 独立 binary `cmd/pg-router/` (G3+)

## 安全供应链

- OpenSSF Scorecard 启用
- License audit allowlist (仅 BSD/Apache/MIT/PG-License)
- ADR-0009 强制禁止 legacy GitHub Actions (RFC-0002)
- Lefthook DCO + Conventional Commits + lint gate

## ADR cross-link (24 ADR)

Notable:
- ADR-0001: 自建 distributed SQL (keystone)
- ADR-0006: 引入 GitOps deploy overlay (3-repo 一致)
- ADR-0007: Hook tooling — 用 pre-commit 而非 lefthook
- ADR-0009: webhook validate — accumulate-errors
- ADR-0013: OperatorHub.io bundle scaffold cross-cut
- ADR-0014: community-operators upstream sync 自动化
- ADR-0019: 保留 GitHub Actions (operator family v2.0 dual-track)
- ADR-0022: GHA narrow exception — 3 workflow (helm-publish + release + scorecard)
- ADR-0023: 承认 v3.x-stable baseline
- ADR-0024: lefthook pre-push incremental lint + envtest
- ADR-0025: Repmgr / PgBouncer / Barman 集成 (bitnami parity)
- ADR-0026: OperatorHub.io 自动 sync

完整列表: `docs/kb/adr/INDEX.md`。

## Non-goals

- ❌ PostgreSQL < 18 (`pkg/version` 决定 v18 最低)
- ❌ 重新打包外部 PostgreSQL operator (Apache-2.0 边界)
- ❌ 出货外部 sharding extension (将问题领域 *重新实现*)
- ❌ 外部 HA agent runtime 依赖 (自建 instance manager)
- ❌ Copyleft 依赖 (license-clean Apache-2.0 only)
- ❌ Plugin SDK (在 v0.x archive 已 retired — 用显式 CRD 替代)

## 参考

- `README.md` — 身份 + 架构摘要 + 功能
- `ROADMAP.md` — Gate matrix checkbox
- `CHANGELOG.md`
- `ADOPTERS.md`
- `CONTRIBUTING.md` + `MAINTAINERS.md`
- `GOVERNANCE.md`
- `SUPPORT.md`
- `AGENTS.md`
- `docs/kb/adr/INDEX.md` — 24 ADR

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a> ·
  <a href="https://github.com/keiailab/forgewise">forgewise</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
