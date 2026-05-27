<p align="center">
  <a href="ROADMAP.md">English</a> |
  <a href="ROADMAP.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="ROADMAP.zh.md">中文</a>
</p>

# ROADMAP — postgres-operator (日本語)

> 英語原文: [ROADMAP.md](ROADMAP.md) — canonical / 正本

本 ROADMAP は、検証可能な Gate と sub-task チェックリストで進捗を追跡します — *日付コミットメントではありません*。プロジェクトの定義は **Apache-2.0 PostgreSQL Kubernetes Operator** です。外部 PostgreSQL operator ランタイムを fork / embed / wrap せずに production-grade な運用品質を目指します。

## チェックボックスの意味

| マーカー | 意味 |
|---|---|
| `[x]` | コード **および** テストが存在。e2e または unit テストで回帰を防止。 |
| `[~]` | 部分実装 — 例: CRD フィールドのみ、ヘルパー未接続、または e2e なし。 |
| `[ ]` | 未着手 (設計または PoC のみ)。 |

各 sub-task の *Verify* 行は検証コマンドまたは e2e ファイルを引用します。

## 原則

- **外部システムは本製品に同梱しない** — 外部 PostgreSQL operator、sharding extension、HA agent ランタイム、サードパーティ DB バックエンドは runtime artifact から除外。
- **新規サービスとして実装** — operator manager、instance manager、sharding メタデータ、router、backup orchestration は本リポジトリ内に Apache-2.0 互換ライセンスで実装。
- **品質基準** — HA / backup / restore / upgrade / observability / security UX の *目標水準* は、特定のサードパーティ製品に依存せず約束します。

## 現状スナップショット

| 項目 | 状態 | Evidence |
|---|---|---|
| プロジェクト / chart 名 | `postgres-operator` | GitHub repo、Helm chart、GitOps path 全部整合 |
| ライセンス | Apache-2.0 | `LICENSE`, ADR-0003 |
| 最新リリース | `0.4.0-beta.1` | GHCR イメージ + Helm chart publish + OLM bundle — Level 4 Deep Insights |
| OLM bundle | `bundle/manifests/` が 8 CRD + alm-examples + CSV description と整合 | `operator-sdk bundle validate --select-optional suite=operatorframework` clean (T26) |
| 宣言的 DB サーフェス | Pooler / PostgresDatabase / PostgresUser / ScheduledBackup / ImageCatalog / ClusterImageCatalog / externalClusters / replica cluster | T22 / T24 / T25 サイクル完了。live kind smoke 自動化 (T27) 進行中 |
| ローカル 4-layer ゲート | L1 lefthook pre-commit + L2 pre-push + L3 make validate/audit + L4 PR evidence | ADR-0009 / RFC-0002。version-drift assertion と bundle validate を自動化 (T26) |
| 本番デプロイ | Day-0 single-shard | `PostgresCluster/postgres` Ready |
| GHCR runtime image | 公開 pull 可能 | `ghcr.io/keiailab/pg:18` が pull secret なしで restart |
| HA replicas | Partial (`Replicas` フィールドのみ) | `api/v1alpha1/postgrescluster_types.go` |
| Backup / restore | 部分実装 | `BackupJob` phase transitions + `ScheduledBackup` CRD/controller + `RestorePIT` call path + pgBackRest command-runner plugin + K8s sidecar exec path。実際の restore drill は pending。 |
| 1.0.0 GA | 未達 | HA / backup / chaos / soak が依然必要 |

## Gate プラン

### Gate G0 — Day-0 デプロイ (~100% buffer)

**目標**: ユーザーが GitOps で operator + single-shard Postgres クラスターをデプロイ可能。

- [x] CRD `PostgresCluster` 定義 — `api/v1alpha1/postgrescluster_types.go` (RFC-0001 v2 schema)。
- [x] CRD `BackupJob` 定義 (Phase 1 spec) — `api/v1alpha1/backupjob_types.go`。
- [x] `PostgresClusterReconciler` が desired state を構築 (ConfigMap / headless Service / StatefulSet) — `internal/controller/postgrescluster_controller.go`。
- [x] Status phase transition (Provisioning → Ready) — `internal/controller/status.go`、`aggregate_status.go`。
- [x] Pod readiness トラッキング — reconciler endpoint watch。
- [x] ArgoCD `Synced/Healthy` — 本番で検証 (`platform-data-postgres-operator`)。
- [x] GHCR 公開 pull — `ghcr.io/keiailab/pg:18` が pull secret なしで restart。
- [x] Day-0 e2e — `test/e2e/e2e_test.go`、`postgrescluster_e2e_test.go`。
- Verify: ArgoCD `Synced/Healthy` + Pod `1/1` Running + `psql -c 'select version()'`。

### Gate G1 — Single-shard production HA (~30% buffer)

**目標**: HA を備えた single-PostgreSQL production データベースとして利用可能。

- [x] `Replicas` フィールド (0〜15 async replica) — `postgrescluster_types.go`。
- [x] STS scale マッピング — reconciler。
- [x] Primary-delete e2e baseline — `test/e2e/failover_e2e_test.go`。
- [x] 自動 PDB 生成 — `internal/controller/pdb.go`。
- [~] PVC fencing (split-brain fail-fast) — fencing skeleton のみ。runbook automation pending。
- [ ] **自動 failover ロジック** — 新規ディレクトリ `internal/controller/failover/`。
  - [x] Primary 障害検知 — `internal/controller/failover/detection.go` (`DetectPrimaryFailure` + `SelectPromotionCandidate`、純粋関数、4 種類の `FailureReason` enum、9 unit test、PR #38)。
  - [x] Standby 昇格 (`pg_ctl promote` または logical-replication 昇格) — `internal/controller/failover/promotion.go` (`BuildPromotionPlan` + `Promoter` インタフェース + `PromoteFromDecision` helper、4-step plan: RemoveStandbySignal / PgCtlPromote / WaitNotInRecovery / UpdateInstanceRole。6 unit test、PR #39)。`internal/controller/failover_promoter.go` が replica Pod の `postgres` コンテナへの exec と昇格後の `instance-status` annotation patch を実装。
  - [x] Post-Ready primary-failure status 可視化 — `status.phase=Degraded` + `FailoverReady=False` + promotion-candidate メッセージ。
  - [x] Replica rejoin (`pg_basebackup` または `pg_rewind`) — first-boot `pg_basebackup` + 既存 PGDATA old-primary marker 一般化 + 現 primary endpoint main env + `pg_rewind` command-runner + HBA normal-connection auth + fresh `pg_basebackup` fallback すべて完了。**Live A.1 basebackup drill PASS (T31, 2026-05-17, commits 09abbb5/dca3fa0)**: `quickstart-shard-0-1` standby PVC delete + in-pod PGDATA wipe + Pod kill → reconciler init container が fresh `pg_basebackup` を実行 → `pg_stat_replication{application_name=quickstart-shard-0-1, state=streaming, sync_state=async, lag=0}` 回復。STS PVC retention `Retain` 回避 path までの evidence。A.2 pg_rewind live drill は別 task (SMOKE_FAILOVER operator-driven promotion live trigger 回帰 — `docs/g1-ha-election-fact-fix` 領域に委任)。
  - [x] Synchronous replication — `spec.postgresql.synchronous.{method,number,dataDurability}` + CEL `number<=shards.replicas` + `ANY/FIRST N (...)` rendering + `required/preferred` quorum policy + standby `application_name` wiring + ConfigMap-hash rolling reconcile すべて完了。**Live B.1〜B.3 RPO=0 drill PASS (T31, 2026-05-17, commit dca3fa0)**: `synchronous_standby_names='ANY 1 ("quickstart-shard-0-1","quickstart-shard-0-0")'` 適用 → `sync/quorum replica count=1` → 1000-row commit 後の `commit_lsn=0/3DA43A0 / flush_lsn=0/3DA43A0` (`pg_wal_lsn_diff=0`) → **RPO=0 を直接証明**。drill 関数: `hack/smoke.sh::drill_sync` (SMOKE_SYNC=1)。B.4 sync standby kill シナリオは opt-in (`SMOKE_SYNC_KILL=1`)。
  - [~] HA election 分散ロック (K8s Lease) — `internal/controller/failover/lease.go` (`FailoverLeaseName` + `LeaseConfig` + `NewLease`/`Run`/`IsLeader`、§2 Simplicity に従い `internal/instance/election.Real` の薄い adapter。fake clientset で single-leader + handoff を検証する 2 unit test)。Live e2e multi-replica failover drill は cluster mesh restore 後 pending。
- [ ] **Backup / restore コントローラ実装** — `internal/controller/backupjob_controller.go` を強化。
  - [x] `BackupJob.Phase` transition (Pending → Running → Succeeded/Failed) — `internal/controller/backupjob_controller.go` reconcile switch + 8 unit test。
  - [x] `ScheduledBackup` CRD / controller — 6 フィールド cron schedule → atomic `BackupJob` 生成。`suspend` / `immediate` / `ownerReference` / `concurrency` ガード。5 unit test。
  - [x] `BackupJob.spec.type=restore` → `BackupPlugin.RestorePIT(targetTime)` call path + 必須 `targetTime` validation。
  - [x] `BackupJob.spec.executionMode=job` → owned `batch/v1.Job` 生成 + observe。`jobTemplate` 標準 env injection。
  - [~] Plugin 呼び出し — pgBackRest command-runner + sidecar コマンド計画完了。WAL-G / Barman pending。
  - [x] Sidecar mode 分岐 — pgBackRest argv を K8s `pods/exec` 経由で ready primary Pod の `postgres` コンテナへ送信。
- [~] **PITR restore** — `BackupRestoreSpec.TargetTime` 駆動の pgBackRest `restore --type=time --target=...` call path + sidecar exec path 双方あり。実際の restore + checksum drill は pending。
- [x] **Upgrade rollback runbook** — `docs/runbooks/upgrade.md` (stub: pre-upgrade チェック + ImageCatalog 手順 + rollback) (PR #54)。
- [x] **RTO / RPO 測定 + 記録** — `docs/runbooks/ha.md` (SLO RTO≤60s + RPO=0 + verify 手順) (PR #54)。
- Verify: primary 削除後 N 秒以内に replica 昇格 + `pg_is_in_recovery()=false` + データ損失 0。fresh-cluster restore 後にデータ checksum が一致。

### Gate G2 — 運用品質 (~25% buffer)

**目標**: production-grade な運用サーフェスをカバー。

- [x] `/metrics` baseline 公開 (port 8443) — `internal/controller/metrics.go`、`cmd/main.go`。
- [x] TLS path セットアップ (certificate mount + `ssl=on`) — `internal/controller/builders.go:renderPostgresConf()`、`tls.go`。
- [x] Topology spread 統合 — `internal/controller/topology_spread.go`。
- [x] PVC online resize — `internal/controller/pvc_resize.go`。
- [x] Cascade-delete ガード — `internal/controller/cascade_delete_test.go`。
- [~] cert-manager 統合 — mount path のみ。発行メカニズムは TBD。
- [~] **自動 PrometheusRule 生成** — Helm metrics Service / ServiceMonitor / PrometheusRule rendering + 実 `postgres_operator_backupjob_phase` メトリクスによる BackupJob failure alert。
  - [x] Replication-lag 警告 — instance status `LagBytes` → `postgres_operator_postgrescluster_replication_lag_bytes` + Helm `PostgresReplicationLagHigh`。
  - [x] Pooler failure / saturation 警告 — `postgres_operator_pooler_phase{phase="Failed"}` + PgBouncer exporter メトリクス駆動の collection-failure / client-waiting / max-wait alert rendering 検証。
  - [x] ディスク逼迫 — `kubelet_volume_stats_*` data-PVC alert。
  - [x] Backup 失敗 — `postgres_operator_backupjob_phase{phase="Failed"}`。
- [~] **Grafana ダッシュボード** — Helm ダッシュボード ConfigMap rendering 完了 (`postgres-operator-cluster-overview.json`、`postgres-operator-pooler.json`)。live Grafana import / panel 検証は pending。
- [~] **Connection pooler (PgBouncer)** — `Pooler` CRD + ConfigMap / Deployment / Service reconcile (first slice)。
  - [x] CRD `Pooler.spec.{cluster, instances, type, pgbouncer.poolMode, pgbouncer.parameters}` 追加。
  - [x] 分離された PgBouncer Deployment / Service / ConfigMap 生成 + `userlist.txt` Secret fail-closed validation。
  - [x] デフォルト PgBouncer readiness / liveness / startup probe + exporter `/metrics` readiness / liveness probe。
  - [x] PgBouncer パラメータ allowlist + operator-owned-key fail-closed validation。
  - [x] `instances > 1` 時に自動 topology spread + PodDisruptionBudget。
  - [x] より強い rolling-update デフォルト — `maxUnavailable=0`、`maxSurge=1`、`minReadySeconds=5`。
  - [x] Pooler parity サーフェス — `deploymentStrategy`、`serviceAccountName`、status `backendTargets/configHash`。
  - [x] `pg_hba` → PgBouncer `pg_hba.conf` rendering + operator-owned validation of `auth_type=hba` / `auth_hba_file`。
  - [x] ユーザー提供 server / client TLS Secret rendering + Secret/key fail-closed validation。
  - [x] `type=ro` full ready-replica host-list rendering + `server_round_robin=1` + `server_login_retry=2` デフォルト。
  - [~] PgBouncer exporter — 明示的 sidecar + `metrics` ServicePort + PodMonitor selector ラベル/サンプル + PgBouncer メトリクスプレフィックスでの PrometheusRule alert render 検証。live Prometheus scrape / Grafana 検証は pending。
  - [x] **Built-in auth ユーザー自動化** (T27 ⑤) — `authSecretRef` が空のとき `keiailab_pooler_pgbouncer` LOGIN role + `<pooler-name>-builtin-auth` Secret を自動プロビジョニング。
  - [x] **Built-in auth パスワード rotation** (T27 ⑥) — `postgres.keiailab.io/rotate-pooler-password=true` annotation で in-place `ALTER ROLE` + Secret update + status timestamp を trigger。ConfigHash が userlist を含み自動リロード。
  - [ ] Built-in TLS 自動発行 (T29)。
  - [x] Paused PAUSE/RESUME reconciliation — `spec.paused` → PgBouncer `SIGUSR1/SIGUSR2`、`status.paused`、Pod annotation audit。
  - [x] Pooler Service `psql` smoke — 2026-05-12 に kind 上で `SMOKE_POOLER=1 ./hack/smoke.sh --keep` が通過 (`quickstart` + Pooler Service `SELECT 1 = 1`、PAUSE がタイムアウトで新規クライアントをブロック、RESUME が `SELECT 1 = 1` を再有効化、Deployment `2/2`)。
  - [x] In-place PgBouncer config reload — `pgbouncer.parameters` の patch が ConfigMap `config.sha256` projection を待機 → ready Pod に `SIGHUP` を送信 → Pod hash annotation を audit しつつ Deployment generation と Pod 名を保存。
- [ ] **User / DB / RBAC 宣言的**。
  - [~] CRD `PostgresDatabase` — `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready-primary `psql` reconcile + `status.applied` + `databaseReclaimPolicy=delete` finalizer + database/schema privilege grant/revoke 実装済み。Live smoke / retain-policy 検証は pending。
  - [~] CRD `PostgresUser` — `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready-primary `psql` reconcile + `status.applied/passwordSecretResourceVersion` 実装済み。membership `REVOKE` + password Secret username 一致 + `disablePassword` fail-closed + 参照 Secret の更新 watch + `PostgresCluster.status.managedRolesStatus` 集約済み。Live smoke + password-rotation SQL round-trip は pending。
  - [~] Role/permission reconcile — `PostgresUser` role flag + membership `GRANT/REVOKE` + cluster-level managed-role status (first slice) 完了。データベースオブジェクト privilege モデルは pending。
- [ ] **Upgrade smoke** — `test/e2e/version_upgrade_e2e_test.go` を拡張 (skeleton はすでに存在)。
- [ ] **Security デフォルト強化** — restricted PSA、NetworkPolicy デフォルト on。
- [~] **ImageCatalog / ClusterImageCatalog** — CRD + `spec.imageCatalogRef.{apiGroup,kind,name,major}` + catalog 画像 → StatefulSet init/main コンテナ画像 + image-hash annotation rollout-drift 追跡 + catalog watch / envtest 完了。Extension-image volume mount、公式 digest catalog 供給、live rollout 計測は pending。
- [~] **Replica cluster / externalClusters** — `externalClusters[].connectionParameters` + `password` + `sslKey/sslCert/sslRootCert` + `bootstrap.pg_basebackup.source` + `replica.enabled/source` 表面、streaming standalone replica bootstrap、ordinal-0 外部 `pg_basebackup`、`standby.signal`/`primary_conninfo`、password passfile + TLS client/root cert conninfo、persistent-follower election (local promotion をブロック)、fail-closed status すべて検証済み。WAL-archive / object-store ハイブリッド、distributed-topology demotion/promotion-token、live cross-cluster drill は pending。
- [~] **宣言的 hibernation** — `postgres.keiailab.io/hibernation=on/off` annotation、shard StatefulSet/PVC-template 保持 + `replicas=0`、native router `replicas=0`、`status.phase=Hibernated`、hibernation condition をすべて envtest で検証。`SMOKE_HIBERNATION=1` パスは PVC-marker-row 保持と rehydration SQL round-trip drill も実行。live kind 検証は pending。
- [~] **Release smoke test** — `scripts/release-smoke-test.sh` 6-stage (GH Release tag + GHCR manifest + GH Pages + helm index + helm pull/template + trivy post-publish scan)。path 修正 (hack/→scripts/)。
- Verify: PrometheusRule / Grafana ダッシュボード rendering、Pooler Service 経由の `psql` アクセス、live PgBouncer exporter scrape、upgrade rolling restart の成功。

### Gate G3 — 自前 sharding 基盤 (~0% buffer)

**目標**: 外部 sharding ランタイムなしで sharding メタデータを自前実装。

- [x] `ShardingMode` フィールド (`none` / `native`) — `postgrescluster_types.go`。Constants + Spec round-trip を `TestShardingMode` がガード (`api/v1alpha1/postgrescluster_types_test.go`)。enum validation は `+kubebuilder:validation:Enum=none;native` マーカーで apiserver にて強制。RFC 0001 §3.1 / RFC 0002。
- [x] `ShardsSpec` (初期 shard 数 / replica / storage) — `postgrescluster_types.go`。フィールド round-trip + `DeepCopy` スライス独立性 + `Replicas=0` (HA-off dev) を `TestShardsSpec` がガード (`api/v1alpha1/postgrescluster_types_test.go`)。RFC 0001 §3.1。
- [x] Sharding plugin interface — `internal/plugin/sharding/api.go`。コンパイル時 interface freeze + `Registry` register/get/Names round-trip + `Capabilities` 広告 + `ErrUnsupported` sentinel を `TestShardingPlugin` umbrella がガード (`internal/plugin/sharding/api_test.go`)。RFC 0001〜0005 / RFC 0004 (router アーキテクチャ)。
- [x] **`ShardRange` CRD** — `api/v1alpha1/shardrange_types.go` + `config/crd/bases/postgres.keiailab.io_shardranges.yaml` (RFC 0002、offline yaml parse PASS、`make manifests` 通過)。
  - [~] Hash-range / list / range ポリシー分岐 (vindex enum 定義完了、reconciler 未実装 — 後続 sub-task)。
  - [ ] メタデータストア (Postgres システムカタログまたは sidecar)。
- [ ] **`pg-router` service PoC** — 新規 `cmd/pg-router/`。
  - [ ] SQL parser (libpg_query または homegrown)。
  - [ ] Shard-placement lookup。
  - [ ] Connection routing (libpq passthrough)。
- [ ] **手動 shard placement** — `ShardRange.Spec.PlacementHints`。
- [ ] **GitOps drift guard** — sharding メタデータと実際の placement の乖離検知。
- Verify: 2-shard クラスターでの `pg-router` 経由クエリが正しい shard にルーティングされる。

### Gate G4 — Online resharding (~0% buffer)

**目標**: データ損失なしの split / rebalance。

- [ ] **`ShardSplitJob` CRD** — 新規 `api/v1alpha1/shardsplitjob_types.go`。
- [ ] **7-step e2e** シナリオ。
  - [ ] 1. Snapshot + WAL キャプチャ。
  - [ ] 2. ターゲット shard の bootstrap。
  - [ ] 3. Initial copy。
  - [ ] 4. CDC catch-up。
  - [ ] 5. Cutover (最小 write-block window)。
  - [ ] 6. Routing 更新。
  - [ ] 7. Source cleanup。
- [ ] **Cutover rollback / forward-only** 検証。
- Verify: split 中のデータ整合性 (checksum) + cutover-window 計測 + rollback 実行可能性。

### Gate G5 — Distributed SQL (~0% buffer)

**目標**: cross-shard クエリ / トランザクションの対応範囲を明確化。

- [~] **Scatter-gather** クエリパス — skeleton (`internal/router/scatter.go` + `ErrNotImplemented` sentinel、Executor interface freeze)。実 wire-protocol forwarding + merge は P3+。Ref: RFC-0004 §2.2 Scenario 2 + ADR-0015。
- [~] **2PC / saga** 分散トランザクションの選択 — ADR-0015 決定 (2PC primary + saga deferred) + `internal/tx/` skeleton。実装は D.2.2 Lease election 統合後。
- [x] **Isolation matrix** ドキュメント化 — どの isolation level がどの条件で保持されるか。Evidence: `docs/sql/isolation-matrix.md` (D.10.3)。
- [~] **ベンチマーク** — sysbench / pgbench バリアント (`test/bench/pgbench.sh` + `sysbench.sh` + `docs/perf/baseline.md` skeleton。live 計測は pending)。
- Verify: isolation level 別の anomaly / no-anomaly 表 + ベンチマーク値。

### Gate G6 — 1.0.0 GA (~15% buffer)

**目標**: 商用グレードの品質。

- [x] e2e baseline — `test/e2e/`。
- [ ] **Long-running soak** — ≥ 7 日、ダウンタイム 0。(NON-GOAL single session) (NON-GOAL for single session — 7-day wall clock required)
- [ ] **Chaos engineering** — pod kill / network partition / disk pressure。(multi-day drill) (multi-day chaos drill required)
- [ ] **Restore rehearsal** — 周期的な自動 backup-restore + 検証。(monthly cron drill — out of single session)
- [ ] **Upgrade matrix** — N → N+1 / N → N+2 / minor patches。(G2 D.6.3 依存 — substantial e2e)
- [ ] **SBOM + signing** — SPDX SBOM + cosign 署名。
- [ ] **Docs / runbook 完備**。
  - [ ] HA / backup / restore / upgrade / security / migration runbook。
- Verify: 7 日 soak 通過 + N 種類の chaos シナリオ通過 + SBOM 添付 + すべての runbook 存在。

## Non-goals (意図的な除外)

- ❌ 外部 PostgreSQL operator の再パッケージングまたは fork。
- ❌ 外部 sharding extension を first-class built-in として採用 (runtime 依存ではない)。
- ❌ 汎用 Plugin SDK 製品ストーリー (v0.x archive から retired)。
- ❌ **必須リリースゲートとしての GitHub Actions** — RFC 0002 (org-wide) 参照。ローカル 4-layer ゲートに委任。
- ❌ **日付ベースのロードマップ締切** — org-wide `workflow.md` 参照。
- ❌ 未検証の HA / backup 機能を `production-ready` としてマーケティング。

## Change log

| 日付 | 変更 |
|---|---|
| 2026-05-16 | G3 §Sharding foundation: `ShardingMode` / `ShardsSpec` / `Sharding plugin interface` を unit-test カバレッジと共に `[~]` → `[x]` に flip (`TestShardingMode`、`TestShardsSpec`、`TestShardingPlugin`)。Plans `2026-05-14-4-operators-100pct/P-D` §D.7。 |
| 2026-05-12 | Backup/restore のギャップ解消: `ScheduledBackup` CRD/controller、cron 発火時の `BackupJob` 生成、`BackupJob.spec.type=restore` → `RestorePIT` call path、`executionMode=job` runner Job ライフサイクル、pgBackRest command-runner plugin 登録、sidecar pod-exec path を追加。 |
| 2026-05-12 | Observability のギャップ解消: Helm metrics Service / ServiceMonitor / PrometheusRule + `postgres_operator_backupjob_phase` Prometheus メトリクスを追加。 |
| 2026-05-11 | G1 §Backup/Restore `BackupJob.Phase` transition (Pending → Running → Succeeded/Failed) 実装 + 8 unit test — `[x]` (ralph-loop iter#3)。 |
| 2026-05-11 | 全面書き直し — Gate-scoped sub-task チェックリスト、buffer 指標を導入し、date-style 表現を削除。 |
| 2026-05-07 | `0.3.0-alpha.3` をリリース、公開 GHCR pull に移行、レガシー staging operator を削除、"no embedded external systems" 原則を明文化。 |

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
