<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator (日本語)

> **Kubernetes 向け Apache-2.0 PostgreSQL Operator — vanilla PG18+、ライセンスクリーン、K8s ネイティブ自動シャーディングロードマップ**

> English README: [README.md](../README.md) — canonical / 正本

<p align="center">
  <a href="../LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version"/></a>
  <a href="https://www.postgresql.org/"><img src="https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql" alt="PostgreSQL"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes" alt="Kubernetes"/></a>
  <a href="https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator"><img src="https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github" alt="Container Image"/></a>
  <a href="https://keiailab.github.io/postgres-operator"><img src="https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v" alt="Helm Chart"/></a>
  <a href="https://artifacthub.io/packages/helm/keiailab-postgres-operator/postgres-operator"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/keiailab-postgres-operator" alt="Artifact Hub"/></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/keiailab/postgres-operator"><img src="https://api.scorecard.dev/projects/github.com/keiailab/postgres-operator/badge" alt="OpenSSF Scorecard"/></a>
  <a href="https://github.com/keiailab/postgres-operator/discussions"><img src="https://img.shields.io/github/discussions/keiailab/postgres-operator?label=discussions&logo=github" alt="GitHub Discussions"/></a>
</p>

<p align="center">
  <a href="../README.md">English</a> |
  <a href="README.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="README.zh.md">中文</a>
</p>

---

## Identity (アイデンティティ)

本オペレーターは、upstream PostgreSQL の上に *自前で構築した分散 SQL レイヤー* を実装します。外部の PostgreSQL operator ランタイムを埋め込んだりラップしたりはせず、コード、CRD、reconciler、instance manager、router はすべて本リポジトリ内で Apache-2.0 互換ライセンスのもとに直接実装されています。

差別化ポイント:

- **100% PostgreSQL 18+ 互換** — アプリケーションコードを変更せずに分散化を導入できます。すべての PG 拡張・型・関数は引き続き利用可能です。
- **ライセンスクリーン** — Apache-2.0 のオペレーター本体に加え、依存は BSD/Apache/MIT/PG-License のみ。SaaS 公開時の copyleft 義務は発生しません。
- **K8s ネイティブ自動シャーディングロードマップ** — `ShardRange` CRD を真実の源とし、KEDA 駆動の自動分割、7 ステップのオンライン再シャーディング(カットオーバー SLA 目標 p99 < 500 ms)。
- **シングルエンドポイントロードマップ** — アプリケーションは PostgreSQL wire protocol で `pg-router` Deployment に接続し、シャーディングを意識する必要はありません。

v0.x アーカイブにあった Plugin SDK 構想は撤回され、現在の方向性は狭く範囲を限定した内部モジュールと明示的な CRD です。

ADR 0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) がこの決定の中核を成します。

## Architecture (アーキテクチャ概要)

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

詳細: [`ARCHITECTURE.md`](ARCHITECTURE.md)。

## Features (機能)

### 現在出荷中 (v0.3.0-alpha.18)

helm チャートおよび OperatorHub バンドルでは **8 つの所有 CRD** を出荷しています。CRD のステータスは、本番クラスターにおいて現時点で reconcile されている内容を反映します:

| CRD | 役割 | ステータス |
|---|---|---|
| `PostgresCluster` | シャード対応トポロジー(primary + standby + ネイティブシャーディングロードマップ) | ✅ deployable |
| `BackupJob` | アトミックな backup/restore Job (pgBackRest plugin) | ⚠️ controller partial |
| `ScheduledBackup` | cron 駆動の BackupJob 生成(6 フィールドスケジュール) | ⚠️ controller partial |
| `Pooler` | PgBouncer コネクションプール層 | ⚠️ controller partial |
| `PostgresDatabase` | 宣言的な database/schema/extension/FDW (ready-primary psql) | ⚠️ controller partial |
| `PostgresUser` | 宣言的な role + password + membership (ready-primary psql) | ⚠️ controller partial |
| `ImageCatalog` | Namespace スコープの PostgreSQL ランタイムイメージカタログ | ⚠️ rollout path |
| `ClusterImageCatalog` | クラスター全体共有の PostgreSQL ランタイムイメージカタログ | ⚠️ rollout path |

helm チャートには次が追加されています: PrometheusRule + Grafana ダッシュボード(Pooler overview + Cluster overview)、restricted PSA SecurityContext、deny-by-default の NetworkPolicy、cert-manager TLS 統合、OpenTelemetry 対応フック。

### Roadmap (フェーズ計画)

| Phase | Version | 主な成果物 |
|---|---|---|
| **P0** | 0.3.0 | 再設計リセット(ADR/RFC 0001–0014、ARCHITECTURE.md、runbook スキャフォールディング) |
| **P1** | 0.4.0 | シングルシャードの本番対応(HA / backup / PITR drill / Lease election) |
| **P2** | 0.5.0 | pg-router + `ShardRange` CRD(手動マルチシャード運用) |
| **P3** | 0.6.0 | vindex 拡張 + scatter-gather + read replica オートスケール |
| **P4** | 0.7.0 | `ShardSplitJob` 7 ステップ(手動オンライン分割トリガー) |
| **P5** | 0.8.0 | KEDA 自動分割 + rebalancer(自動シャーディング到達) |
| **P6** | 0.9.0 | 分散トランザクション(2PC + saga) + クロスシャード JOIN |
| **P7** | **1.0.0** | 安定化 + chaos / benchmark + Artifact Hub verified |

フェーズ詳細(サブタスク、SLO、ADR/RFC 参照): [`ROADMAP.md`](ROADMAP.md)。

## License policy (ライセンスポリシー、ADR 0003)

外部 OSS 依存は、次の *すべて* を満たす場合にのみ許可されます:
- ライセンス: BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API: v1+ の安定性コミットメント(12 か月の deprecation policy)

**永久に禁止**: AGPLv3 / BUSL / CSL / SSPL。

自動 enforcement: `scripts/check-license-policy.sh` (P0 follow-up; lefthook L2 pre-push hook および `go-licenses.yml` GitHub Actions チェックとして接続)。

## Quickstart (クイックスタート)

```bash
# 1. オペレーター + 8 CRD のインストール (helm チャートまたは OperatorHub bundle)
helm install postgres-operator charts/postgres-operator

# 2. quickstart 用 PostgresCluster を適用
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml

# 3. Ready 待機
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m

# 4. (オプション) 宣言的な database/user リソースを適用
kubectl apply -f config/samples/postgres_v1alpha1_postgresdatabase.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresuser.yaml

# 5. (オプション) PgBouncer Pooler と cron バックアップを適用
kubectl apply -f config/samples/postgres_v1alpha1_pooler.yaml
kubectl apply -f config/samples/postgres_v1alpha1_scheduledbackup.yaml

# 6. モニタリングを有効化 (prometheus-operator が必要)
helm upgrade postgres-operator charts/postgres-operator \
  --reuse-values \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

運用プレイブックは [`docs/operator-guide/deployment.md`](operator-guide/deployment.md) および [`docs/operator-guide/pooler-monitoring.md`](operator-guide/pooler-monitoring.md) を参照してください。

## Production readiness (本番運用準備)

**現状 (0.3.0-alpha.18)**: リファレンス Kubernetes クラスターにおいて、ArgoCD Application `platform-data-postgres-operator` は `Synced/Healthy` であり、`PostgresCluster/postgres` は `Ready=True` を報告しています。

GA までの距離:
- **P1** — 本番対応のシングルシャードには、HA Lease 分散ロックコントローラー、BackupJob/ScheduledBackup の実機 drill、PITR チェックサム drill、および chaos-mesh failover スイートが必要です。サブタスクは `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md` で追跡しています。
- **P2** — マルチシャードには `ShardRange` CRD + pg-router PoC が必要です ([`docs/sharding/SHARDING.md`](sharding/SHARDING.md))。
- 現在の alpha は、ユーザー自身による backup/restore 検証なしでは本番データに対して**推奨されません**。

## Known limitations (既知の制限)

- BackupJob / ScheduledBackup / Pooler / PostgresDatabase / PostgresUser コントローラーは *partial* — CRD サーフェスは出荷されコアパスは reconcile されますが、実機 drill 検証 (rotation / PITR / retain-policy) はフェーズ別に保留・追跡中です。
- ImageCatalog / ClusterImageCatalog の rollout-drift 測定は StatefulSet アノテーション層で実装済みですが、本番 rollout SLA はまだ認定されていません。
- Sharding サブシステム (`ShardRange`、`pg-router`、`ShardSplitJob`) は **設計のみ** — 仕様は [`docs/sharding/SHARDING.md`](sharding/SHARDING.md) を参照。ランタイムコードはまだありません。
- 上記のフェーズロードマップは複数年スパンを示唆しており、現時点の運用範囲はシングルシャード HA のみです。

## Uninstall (アンインストール)

```bash
# 1. 最初に CR インスタンスを削除 (さもないと finalizer が CRD 削除をブロックします)
kubectl delete postgrescluster --all -A
kubectl delete pooler --all -A
kubectl delete scheduledbackup --all -A

# 2. チャートをアンインストール
helm uninstall postgres-operator

# 3. CRD を削除 (オプション; helm はクラスター状態保全のためデフォルトで CRD を残します)
kubectl delete crd postgresclusters.postgres.keiailab.com \
                  backupjobs.postgres.keiailab.com \
                  scheduledbackups.postgres.keiailab.com \
                  poolers.postgres.keiailab.com \
                  postgresdatabases.postgres.keiailab.com \
                  postgresusers.postgres.keiailab.com \
                  imagecatalogs.postgres.keiailab.com \
                  clusterimagecatalogs.postgres.keiailab.com
```

## Contributing (コントリビューション)

```bash
make lint test validate    # ローカル 4-layer L3 ゲート
make sync-crds              # config/crd/bases ↔ chart の同期を検証
make test-e2e PILLAR=p1     # Kind クラスター e2e
```

GitHub Actions が OSS 標準スイートを実行します (CI / scorecard / CodeQL / DCO / dependency-review / go-licenses / kube-linter / helm-install-test / stale)。ローカルの pre-commit / pre-push フックが主要な開発者ゲートであり、CI は収束チェックです。

コントリビューターガイドは [`CONTRIBUTING.md`](CONTRIBUTING.md)、ガバナンスモデル(lazy consensus / 2/3 supermajority)は [`GOVERNANCE.md`](GOVERNANCE.md)、行動規範は [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) を参照してください。

## Documentation (ドキュメント)

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — 単一ページのアーキテクチャ説明(8 CRD サーフェス + 自前構築の分散 SQL + G0-G6 ステータス + ADR クロスリンク)
- `docs/kb/adr/` — Architecture Decision Records (現行: 0001–0026)
- `docs/rfcs/` — RFC ドラフト (現行: 0001–0007)
- `docs/operator-guide/` — Deployment / pooler-monitoring / community-operators-onboarding / HA
- `docs/runbooks/` — 運用手順: ha / backup / restore / upgrade / security / migration (各 SLO 目標 + verify コマンド付き)
- `docs/sharding/` — シャーディングアーキテクチャ仕様 (G3-G5)
- `docs/api-reference/` — CRD リファレンス (自動生成、計画中)
- `docs/tutorials/` — ステップバイステップのユーザーガイド (P1+ で計画中)

## Reporting vulnerabilities (脆弱性報告)

セキュリティ報告のために公開 issue を**作成しないでください**。[`SECURITY.md`](SECURITY.md) に従って GitHub Security Advisory のプライベートチャネルをご利用ください。5 営業日以内に応答し、重大度の高い発見については開示タイムラインを調整します。

## Community (コミュニティ)

- **Discussions**: [GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) — 利用に関する質問、機能アイデア、運用上の経験談。
- **Issues**: [GitHub Issues](https://github.com/keiailab/postgres-operator/issues) — バグおよび機能要望(再現可能なケースを提出してください。`question.yml` テンプレートが Q&A をガイドします)。
- **Governance**: [`GOVERNANCE.md`](GOVERNANCE.md) — 意思決定プロセス (lazy consensus / 2/3 supermajority)。
- **Sponsorship**: GitHub Sponsors ボタンについては [`.github/FUNDING.yml`](../.github/FUNDING.yml) を参照。

## License (ライセンス)

Apache-2.0。[`LICENSE`](../LICENSE) ファイルを参照してください。

## Maintainer (メンテナー)

[@phil](https://github.com/phil) — `eightynine01@gmail.com`。メンテナー名簿: [`MAINTAINERS.md`](MAINTAINERS.md)。

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
