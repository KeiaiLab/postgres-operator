<p align="center">
  <a href="ARCHITECTURE.md">English</a> |
  <a href="ARCHITECTURE.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="ARCHITECTURE.zh.md">中文</a>
</p>

# ARCHITECTURE — postgres-operator (日本語)

> 単一ページのアーキテクチャ説明。CRD サーフェス / Gate / reconcile パターンが変わった際に更新する。

> 英語原文: [ARCHITECTURE.md](ARCHITECTURE.md) — canonical / 正本

## 概要

- **目的**: Apache-2.0 PostgreSQL Kubernetes Operator — *自前実装* で production-grade な運用品質と distributed SQL を提供。外部 PostgreSQL operator の fork や wrapper ではない。
- **範囲**: K8s 上の vanilla PostgreSQL 18+、single-shard HA → sharding → online resharding → distributed SQL → GA。
- **安定性ティア**: v0.3.0-alpha.16 (G0 100% / G1 81% / G2 72% / G3 37% / G4-G5 0% / G6 12%)
- **License**: Apache-2.0 (依存: BSD/Apache/MIT/PG-License のみ — SaaS 公開時 copyleft 義務ゼロ)
- **Module path**: `github.com/keiailab/postgres-operator`

## CRD サーフェス (8 CRD)

| CRD | apiVersion | Scope | 説明 |
|---|---|---|---|
| `PostgresCluster` | `postgres.keiailab.com/v1alpha1` | Namespaced | Primary HA controller — StatefulSet + WAL + failover |
| `BackupJob` | `postgres.keiailab.com/v1alpha1` | Namespaced | pgBackRest backup / restore / PITR |
| `ScheduledBackup` | `postgres.keiailab.com/v1alpha1` | Namespaced | Cron ベース BackupJob トリガー |
| `PostgresDatabase` | `postgres.keiailab.com/v1alpha1` | Namespaced | 宣言的 database + schema + privilege |
| `PostgresUser` | `postgres.keiailab.com/v1alpha1` | Namespaced | 宣言的 role + password rotation |
| `Pooler` | `postgres.keiailab.com/v1alpha1` | Namespaced | PgBouncer 接続プール |
| `ImageCatalog` / `ClusterImageCatalog` | `postgres.keiailab.com/v1alpha1` | Namespaced / Cluster | 宣言的アップグレード用 image catalog |
| (G3+ 予定) `ShardRange` / `ShardSplitJob` | — | — | Sharding メタデータ + 7-step online resharding |

## 自前実装の distributed SQL アーキテクチャ

```
Application (libpq / JDBC / asyncpg)
    │ PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │ - vindex 評価 (hash / range / consistent-hash / lookup)
    │ - single-shard fast path / multi-shard scatter-gather
    │ - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (shard ごと: 1 primary + N replica)
    │ instance manager (election + fencing + postgres 監督)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth — G3+)
  - ShardSplitJob reconciler (7-step workflow — G4+)
  - Rebalancer / Backup / Autoscaler glue
```

ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) が keystone — *外部 operator を埋め込まない*。

## RBAC スコープ

- ClusterRole: CRD watch + cert-manager Certificate + ImageCatalog cluster-scope
- Role (ns 単位): StatefulSet / Service / Secret / ConfigMap / PVC / PDB / NetworkPolicy / Job / PgBouncer
- ServiceAccount: `postgres-operator`

## operator-commons import サーフェス

採用: **5/8 (63%)**。

| パッケージ | 状態 | 用途 |
|---|---|---|
| `pkg/security` | ✅ | restricted PSA (it8) |
| `pkg/version` | ⏳ | ローカルの `version.Combo` が commons.MustList より豊富 — delegation 保留 |
| `pkg/labels` | ✅ | 推奨 labels (it28) |
| `pkg/monitoring` | ⏳ | ServiceMonitor ローカル実装 — commons delegation 保留 |
| `pkg/networkpolicy` | ⏳ | NetworkPolicy ローカル実装 — commons delegation 保留 |
| `pkg/webhook` | ✅ | Validation ヘルパー (it34) |
| `pkg/finalizer` | ✅ | `Add` / `Remove` / `Has` |
| `pkg/status` | ✅ | Condition reason |

## Gate plan (G0 → G6)

| Gate | 目標 | 状態 |
|---|---|---|
| G0 | Day-0 deployment | **100%** (7/7) |
| G1 | Single-shard HA (failover + sync repl + PVC fence + lease) | 81% (HA election Lease 保留) |
| G2 | 運用品質 (TLS auto / PrometheusRule / Grafana / Pooler / RBAC / ImageCatalog / Hibernation) | 72% (live drill 保留) |
| G3 | Sharding foundation (`ShardRange` CRD + pg-router PoC + メタデータ) | 37% |
| G4 | Online resharding (`ShardSplitJob` 7-step) | 0% |
| G5 | Distributed SQL (scatter-gather + 2PC/saga + isolation + benchmark) | 0% |
| G6 | 1.0.0 GA (soak ≥7d + chaos + SBOM + cosign + 6 runbook) | 12% |

100% までのプラン (G6): `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md` (59 sub-task)。

## テストレイヤー

| レイヤー | 場所 | カバレッジ |
|---|---|---|
| Unit | `internal/**/_test.go`, `api/**/_test.go` | `make test-unit` |
| Integration (envtest) | `test/integration/` | `make test-integration` |
| E2E (kind) | `test/e2e/{*,pg,failover,sharding}/` | `make test-e2e*` |
| Bench | `test/bench/` (G5) | sysbench / pgbench |
| Scorecard | `bundle/tests/scorecard/` | OLM v1alpha3 |

## ビルド / デプロイ

- コンテナイメージ: `ghcr.io/keiailab/postgres-operator:v0.3.0-alpha.16`
- Helm chart: `charts/postgres-operator/` (`keiailab.github.io/postgres-operator`)
- OLM bundle: `bundle/`
- ArtifactHub: `keiailab-postgres-operator`
- pg-router: 別バイナリ `cmd/pg-router/` (G3+)

## セキュリティサプライチェーン

- OpenSSF Scorecard 有効
- License audit allowlist (BSD/Apache/MIT/PG-License のみ)
- ADR-0009 が legacy GitHub Actions を禁止強制 (RFC-0002)
- Lefthook DCO + Conventional Commits + lint gate

## ADR cross-link (24 ADR)

Notable:
- ADR-0001: 自前実装の distributed SQL (keystone)
- ADR-0006: GitOps deploy overlay 導入 (3-repo 整合)
- ADR-0007: Hook tooling — lefthook ではなく pre-commit
- ADR-0009: webhook validate — accumulate-errors
- ADR-0013: OperatorHub.io bundle scaffold cross-cut
- ADR-0014: community-operators upstream sync 自動化
- ADR-0019: GitHub Actions 維持 (operator family v2.0 dual-track)
- ADR-0022: GHA narrow exception — 3 workflow (helm-publish + release + scorecard)
- ADR-0023: v3.x-stable baseline の承認
- ADR-0024: lefthook pre-push incremental lint + envtest
- ADR-0025: Repmgr / PgBouncer / Barman 統合 (bitnami parity)
- ADR-0026: OperatorHub.io 自動 sync

全リスト: `docs/kb/adr/INDEX.md`。

## Non-goals

- ❌ PostgreSQL < 18 (`pkg/version` 決定により v18 最小)
- ❌ 外部 PostgreSQL operator の再パッケージング (Apache-2.0 境界)
- ❌ 外部 sharding extension の同梱 (問題領域を *再実装*)
- ❌ 外部 HA agent runtime 依存 (自前 instance manager)
- ❌ Copyleft 依存 (license-clean Apache-2.0 のみ)
- ❌ Plugin SDK (v0.x archive にて retired — 明示 CRD で置換)

## 参考

- `README.md` — アイデンティティ + アーキテクチャ要約 + 機能
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
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
