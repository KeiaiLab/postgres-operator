<p align="center">
  <a href="CHANGELOG.md">English</a> |
  <a href="CHANGELOG.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="CHANGELOG.zh.md">中文</a>
</p>

# Changelog (日本語)

> 英語原文: [CHANGELOG.md](CHANGELOG.md) — canonical / 正本

本プロジェクトは SemVer に従う。

## [Unreleased]

### Added (追加)

- *(olm,docs)* `docs/operator-guide/community-operators-onboarding.md` —
  k8s-operatorhub/community-operators チャンネルへのオンボーディング手順
  (前提チェックリスト、bundle イメージの build / push、`gh pr create`、
  upgrade-graph 運用、Artifact Hub OLM 検証)。T28 の最初の成果物。
- *(ci)* `make hooks-install` / `make hooks-check` ターゲット — lefthook
  の DCO / Conventional Commits ゲートを有効化する wrapper。
  CONTRIBUTING も整合。
- *(ci)* `make validate` に 4 つの追加ゲート — bundle CRD 数 ≥ 8、
  `operator-sdk bundle validate` デフォルト + `suite=operatorframework`、
  Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag のドリフト
  検査、そして `.github/workflows/` の不在 (ADR-0009 強制)。
- *(olm)* CSV の `customresourcedefinitions.owned[]` に ImageCatalog /
  ClusterImageCatalog / PostgresDatabase / PostgresUser の `description`
  + `displayName` を追加 — operatorframework suite の 4 件の警告を解消。
- *(oss)* 新しい `SUPPORT.md` (GitHub Discussions / Issues / PR 経路、
  セキュリティルーティング、応答時間の期待値)。 0.3.0-alpha.17 と
  0.3.0-alpha.18 の CHANGELOG エントリも整合。
- *(docs)* README — 0.3.0-alpha.18 の 8-CRD サーフェス表 + 6-step
  quickstart。
- *(docs)* TASKS — T26 (cross-cut OSS/OLM 整合、完了)、T27 (新 CRD の
  live smoke 自動化、①–④ すべて完了)、T28 (community-operators 登録
  手順) を登録。
- *(smoke)* `hack/smoke.sh` に 4 つの新 CRD シナリオを追加:
  `SMOKE_DATABASE=1` (PostgresDatabase → `status.applied` +
  `pg_database` + reclaim=delete DROP)、`SMOKE_USER=1` (PostgresUser →
  `status.applied` + `pg_roles` + DROP ROLE)、`SMOKE_SCHEDULEDBACKUP=1`
  (cron `immediate` → BackupJob 生成)、`SMOKE_IMAGECATALOG=1`
  (ImageCatalog / ClusterImageCatalog schema + lookup)。ステップ番号は
  N/15 に揃えた。
- *(api)* owned CRD 8 つすべてに `kubebuilder:resource:categories`
  マーカーを追加 — `kubectl get postgres` / `database` / `backup` /
  `pooler` / `image` / `role` / `all` のエイリアスと OperatorHub UI の
  グルーピング。
- *(ci)* `make lint-k8s` ターゲットを `make validate` に統合 —
  `dist/install.yaml` と helm-template 出力に対する kube-linter の静的
  解析 (liveness-port / readiness-port / non-root / readOnlyRoot /
  resource-limit など 30+ チェック)。
- *(ci,olm)* `config/scorecard/` + `bundle/tests/scorecard/config.yaml`
  + `make scorecard` ターゲット — operator-sdk scorecard の 6 テスト
  (basic-check-spec、olm-bundle-validation、olm-crds-have-validation、
  olm-crds-have-resources、olm-spec-descriptors、olm-status-descriptors)
  を自動化。実行には kind クラスタが必要 (`make smoke` の後に
  `make scorecard`)。
- *(controller)* **Pooler built-in auth** (T27 ⑤) —
  `spec.pgbouncer.authSecretRef` が空の場合、operator は
  PostgresCluster の ready primary Pod に `keiailab_pooler_pgbouncer`
  LOGIN ロール (24-byte crypto/rand base64 パスワード) と userlist.txt
  Secret `<pooler-name>-builtin-auth` (Pooler 所有) を生成する。エコ
  システム互換性のため PgBouncer userlist 形式
  `cnpg_pooler_pgbouncer` を保持。primary を待つ間は
  Phase=Pending + Reason=BuiltinAuthWaitingForPrimary で idempotent
  ensure。`PoolerReconciler.PodExecutor` 配線の回帰も修正
  (PAUSE/RESUME + SIGHUP-reload 経路を復元)。
- *(controller)* **Pooler AutoTLS — cert-manager 統合** (T29) —
  `spec.pgbouncer.autoTLS` が設定されると、operator は cert-manager の
  `Certificate` CR (役割別: server / client) を `unstructured` で emit し、
  cert-manager に Secret (`tls.crt` + `tls.key` + `ca.crt`) の発行を任せる。
  PgBouncer Deployment は自動生成された Secret を透過的にマウントする。
  明示的な `Server/ClientTLSSecret` は依然として優先される。新ヘルパ
  (`poolerEffectiveServerTLSSecretName`、
  `poolerEffectiveClientTLSSecretName`、
  `poolerAutoTLS{Server,Client}Active`)、
  `cert-manager.io/certificates` 用の新 RBAC マーカー、2 つの新 unit test
  (`TestPoolerAutoTLS_CreatesCertificate`、
  `TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence`)、新サンプル
  `config/samples/postgres_v1alpha1_pooler_autotls.yaml`。
- *(controller)* **Pooler built-in auth のパスワードローテーション**
  (T27 ⑥) — `postgres.keiailab.io/rotate-pooler-password=true` 注釈で
  新パスワードでの `ALTER ROLE`、userlist.txt Secret の in-place 更新、
  注釈の除去、`Pooler.Status.BuiltinAuthLastRotation` の記録をトリガー。
  新 unit test `TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation`。
- *(docs)* ROADMAP "current state" スナップショット — alpha.18 + OLM
  bundle + 宣言的 DB サーフェス + ローカル 4-layer ゲートの 4 行を追加。
- *(docs)* Cross-validation マトリクス — 5 次元を追加: OLM bundle /
  Helm chart / ローカル supply-chain ゲート / セキュリティ脆弱性スキャン
  / DCO サインオフ強制。

### Added (追加)

- *(api,controller)* T29 stage 4 — `Pooler.spec.pgbouncer.autoTLS.selfSigned`
  フィールド。true に設定し (かつ `issuerRef` が空の場合)、operator は
  in-process の RSA-2048 自己署名 CA + leaf 証明書 (有効期間 1 年、更新
  skew 30 日) を生成し、`<pooler>-client-tls` / `<pooler>-server-tls`
  という Secret (`tls.crt`/`tls.key`/`ca.crt` キー) に格納する —
  cert-manager Certificate が発行した Secret と同一レイアウト。
  cert-manager なし環境のギャップを埋める。 CEL XValidation ルールで
  admission 時に "{issuerRef, selfSigned} のうち厳密に 1 つ" を強制。
  回帰テスト
  `TestPoolerAutoTLS_SelfSignedCreatesSecretAndMirrorsNotAfter` +
  サンプル CR。
- *(api,controller)* T29 stage 5 — `Pooler.Status.AutoTLSClientCertNotAfter`
  + `AutoTLSServerCertNotAfter` が cert-manager の
  `Certificate.status.notAfter` をミラーリングし、運用者がフリート全体の
  満期切迫証明書を一覧できる (`kubectl get poolers -A -o wide` が
  `priority=1` で 2 列を露出)。 reconciler は `unstructured` 経由で
  cert-manager Certificate CR を読む (SDK 依存なし)。 lookup 中のエラー
  は V(1) でログされ "unknown" 扱いとなるため、一時的な cert-manager
  停止が Pooler reconcile の残りをブロックしない。回帰テスト
  `TestPoolerAutoTLS_MirrorsNotAfterToStatus`。
- *(api,controller)* `PostgresUser.spec.userReclaimPolicy` (`retain`
  デフォルト、`delete`) は `PostgresDatabase.spec.databaseReclaimPolicy`
  をミラー。 `delete` に設定すると、reconciler は
  `postgres.keiailab.io/postgresuser-finalizer` を付与し、garbage-
  collection の前に既存の `ensure=absent` reconcile スクリプトで
  `DROP ROLE` を実行する。 PG18 kind smoke iter#7 の「`kubectl delete
  postgresuser` が PostgreSQL ロールを残す」観察を解消。

### Fixed (修正)

- *(instance)* HA bootstrap fence race — 最終修正。元の規則
  「memberCount>1 クラスタにおける全 leader-stop で fence」は、いかなる
  一時的な lease-renewal 漏れでも bootstrap Pod の PVC を fence し、
  standby.signal を seed してしまい、その後の起動が常に Follower 分岐に
  入ったままになる原因となっていた。 3 層の修正:
  (i) `supervise.IsStandby(dataDir)` ショートサーキット;
  (ii) `promotedAtLeastOnce atomic.Bool` フラグにより、実際に promote
  された状態でのみ fencing をゲート; (iii) **standby-pod election
  downgrade** — ディスクに standby.signal を抱えて起動する pod は
  Follower election を取る (lease を奪い合わない)。 加えて
  `handleStoppedLeading` は side-effect-free に — failover は
  `executeClusterPromotion` 経由でのみ operator-driven。 PG18 / PG17
  SHARD_REPLICAS=1 HA smoke 5/5 PASS + WAL レプリケーション検証;
  SHARD_REPLICAS=0 でも 5/5 回帰確認。新回帰テスト
  `TestHandleStoppedLeading_NeverFencesOrDemotes` が no-op 契約を固定。
- *(controller)* PostgresDatabase / PostgresUser の psql 呼び出しが
  OS ユーザ `pg-keiailab` (Dockerfile.pg の USER ディレクティブ) を既定で
  使用していた。 iter#5 の `eval` バグ除去後にこれが
  `FATAL: role "pg-keiailab" does not exist` として表面化 (PG18 kind
  smoke iter#6)。 レンダリングされた reconcile スクリプト中のすべての
  psql 呼び出しに明示的な `-U postgres` を追加 (`psql_base` 定数 +
  per-database 呼び出しすべて)。 回帰テスト
  `TestPostgresDatabaseReconcileScriptDoesNotUseEval` を更新し、
  レンダリング後のコマンドに `-U postgres` を要求するように。
- *(smoke)* `hack/smoke.sh` が `kubectl apply -f dist/install.yaml` の後
  に operator Pod を再起動していなかったため、kind が再実行時にキャッ
  シュ済みイメージを再利用 (`imagePullPolicy=IfNotPresent` + 同タグ)。
  Pod がディスク上のソースより古い operator バイナリを実行し、新しい
  修正を覆い隠していた。 smoke.sh は apply 後に controller-manager
  deployment を `kubectl rollout restart` し、`rollout status` で新
  ReplicaSet を待つように。
- *(controller)* PostgresDatabase / PostgresUser の reconcile スクリプト
  が psql を `eval "$psql_base" -c '<SQL>'` で呼び出していた。 外側の
  shell が `<SQL>` を囲む single quote を `eval` に渡す前に剥がしてしまい、
  `eval` が全引数を空白で連結して文字列全体を再パースする。 SQL が空白
  で word-split され、psql は `-c CREATE`、`DATABASE`、`smoke_db_x`、…
  を別々の引数として認識 — `FATAL: role "1" does not exist` および
  `FATAL: role "DATABASE" does not exist` を発生させた (PG18 kind smoke
  iter#5 観測)。 すべての `eval "$psql_base" …` 呼び出し箇所を inline
  の完全な `psql -v ON_ERROR_STOP=1 -X -q -d postgres -c '<SQL>'` 呼び
  出しに置換し、SQL が単一の shell-quoted 引数に留まり psql に atomic
  に渡るようにした。 2 つの新回帰テスト
  (`TestPostgresDatabaseReconcileScriptDoesNotUseEval`、
  `TestPostgresUserReconcileScriptDoesNotUseEval`) が、レンダリング後の
  スクリプトに `eval` が含まれないことを保証する。
- *(controller)* PostgresDatabase / PostgresUser の `status.applied` が、
  finalizer は既に付いているのに未設定 (no condition、空の `status: {}`)
  のままになりうる現象。 根本原因は 2 つ — *(a)* finalizer-add 経路が
  `Requeue:true` を返して SQL 適用を 2 nd pass に先送りしており、
  informer-cache 伝搬遅延下で古い snapshot をループしやすかった;
  *(b)* `statusUpdate` が `apierrors.IsConflict` を黙って飲み込んで
  しまい、同一 generation で finalizer Update と status Update が競合
  すると status payload が丸ごと落ちていた。 reconciler は今や
  (i) finalizer を追加して*同じ* reconcile pass を継続 (single-pass
  apply + status)、(ii) conflict 時には一度 re-fetch して retry してから
  諦める。 PG18 kind smoke iter#3 で観測;更新テスト
  `TestPostgresDatabaseReconcileDeletePolicyAddsFinalizerBeforeApply` が
  single-pass `status.applied=true` を保証する形で覆う。 同じ
  conflict-retry パターンを一貫性のため BackupJob、ScheduledBackup、
  Pooler の `statusUpdate` ヘルパにもバックポート。
- *(controller)* Pooler — 上流の PostgresCluster の
  `status.shards[0].primary.ready` が、Pooler の最初の reconcile *の後*
  に true に flip した場合、PoolerReconciler が PostgresCluster の
  `Watches` を持っていないために Pooler が永遠に `phase=Failed,
  reason=TargetNotFound` に張り付く (PG18 kind smoke iter#4 観測:
  Pooler が 14:29:38Z に reconcile、cluster が 14:29:42Z に Ready=True、
  Deployment は生成されず)。 PoolerReconciler は今や
  `Watches(&PostgresCluster{}, EnqueueRequestsFromMapFunc(...))` で、
  status 変化に合致する `spec.cluster.name` の namespace 内すべての Pooler
  を re-enqueue し、missing-target 分岐は `Failed` ではなく
  `phase=Pending` + `RequeueAfter` をマーク。 回帰テスト
  `TestPoolerReconcileTargetNotFoundIsPendingWithRequeue` を追加。
- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; SPDY streaming 経由 Kubelet / CRI-O /
  kube-apiserver DoS)。 `trivy fs --severity HIGH,CRITICAL --exit-code 1`
  が再び green。
- *(ci,kustomize)* manager Deployment が `containerPorts` に 8081 ヘルス
  ポートを列挙していなかったドリフトを解消。 `config/manager/manager.yaml`
  が `ports: []` から `ports: [{name: health, containerPort: 8081,
  protocol: TCP}]` に切り替わり、helm chart と `dist/install.yaml` の
  manager Deployment を整合 (kube-linter の liveness-port /
  readiness-port チェック)。
- *(docs,license)* NOTICE から古い legacy AGPL-3.0 サードパーティ
  sharding-extension エントリを削除 — ADR-0003 (AGPLv3 永久禁止の
  ライセンスポリシー) および ADR-0001 (self-built distributed SQL)。
  NOTICE は今や `go.mod` の直接依存 (Prometheus、Ginkgo、robfig/cron、
  moby/spdystream、…) のみを列挙。

## [0.3.0-alpha.18] - 2026-05-12

### Added (追加)

- *(api,controller)* `ImageCatalog` + `ClusterImageCatalog` CRD を追加
  (TASKS T24)。 `spec.imageCatalogRef.{apiGroup,kind,name,major}`
  (エコシステム互換のため `postgresql.cnpg.io` apiGroup を受容)、
  namespaced / cluster-scoped lookup、catalog → StatefulSet 画像伝播、
  image-hash 注釈駆動 rollout ドリフト。
- *(api,controller)* `PostgresDatabase` + `PostgresUser` CRD (TASKS
  T22)。 Ready-primary `psql` reconcile が database / tablespace /
  schema / extension / FDW / foreign server、 加えて role flags /
  membership / `connectionLimit` / `passwordSecretRef` /
  `disablePassword` / `validUntil` を適用。
  `databaseReclaimPolicy=delete` finalizer +
  `status.applied/observedGeneration/conditions` + `managedRolesStatus`
  集約。
- *(controller,instance)* Standalone replica cluster + externalClusters
  ストリーミング経路 (TASKS T25)。 `spec.externalClusters[]`、
  `bootstrap.pg_basebackup.source`、`replica.enabled/source`。
  `POSTGRES_REPLICA_CLUSTER=standalone` persistent-follower election、
  password Secret passfile + TLS Secret projected mount、source-
  mismatch fail-closed。
- *(api,controller)* `Pooler` CRD + PgBouncer 接続プール層 (F05)。
  `instances`、`type=rw/ro`、`pgbouncer.{poolMode,parameters,pg_hba}`、
  auth / TLS Secret、exporter サイドカー、`spec.paused` PAUSE/RESUME、
  `pgbouncer.parameters` SIGHUP reload、HA topology / PDB。
- *(observability)* metrics + Grafana ダッシュボード + PrometheusRule
  + ServiceMonitor (F05)。 BackupJob / Pooler の phase メトリクス、
  replication-lag バイト、PgBouncer exporter のアラート、cluster-
  overview + Pooler ダッシュボード ConfigMap、kube-prometheus-stack
  サイドカー互換。
- *(controller,instance)* Failover promoter 実行 + follower election
  (F03 のフォローアップ、PR #38/#39 着地)。 Replica-Pod の `postgres`
  コンテナ exec → `pg_ctl promote` → `pg_is_in_recovery()` ポーリング
  → primary 注釈パッチ。
- *(backup)* `ScheduledBackup` CRD + サイドカー exec runner + pgBackRest
  command-runner プラグイン (F04)。 6-field cron + `concurrencyPolicy`
  Allow/Forbid + retention + JobTemplate。
- *(release,ci)* Artifact Hub 自動登録 / smoke `hack/artifacthub_*.sh`
  + Makefile `artifacthub-{register,smoke}` ターゲット。 kind smoke に
  `SMOKE_HIBERNATION=1` (エコシステムツール互換のため hibernation
  注釈 `cnpg.io/hibernation` を保持 + PVC マーカー保存) と
  `SMOKE_POOLER=1` (PgBouncer Service psql / PAUSE / RESUME / config
  reload) シナリオを追加。 `make validate` の CRD 数アサートが 2 → 8 に
  引き上げられ、18 件の monitoring-render grep 検査を追加。
- *(olm)* `bundle/manifests/` が 0.3.0-alpha.18 に整合 — 8 CRD +
  alm-examples が一貫 (`operator-sdk bundle validate` 0 warnings)。
  owned-CRD の 7 件の `config/samples/` ファイルすべてを有効化。

### Fixed (修正)

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; SPDY streaming 経由 Kubelet / CRI-O /
  kube-apiserver DoS)。 k8s.io/client-go からの間接サーフェスも refresh。

### Changed (変更)

- *(chart)* `version` 0.3.0-alpha.16 → 0.3.0-alpha.18、`appVersion`
  0.3.0-alpha.17 → 0.3.0-alpha.18、manager-image `newTag`
  0.3.0-alpha.18。 前回の alpha.17 bump が `version: 0.3.0-alpha.16` を
  残していた — このサイクルで 3 つ全てを揃える。

## [0.3.0-alpha.17] - 2026-05-12

### Fixed (修正)

- *(bootstrap)* 空でない stale な `postmaster.pid` の PID-alive 検査
  (INC-0046 P19 ⑲、プロダクションクラスタスコープ)。 残存ゾンビ
  ファイルが新規 PG 起動をブロックしていた回帰を解消。

## [0.3.0-alpha.16] - 2026-05-10

### Bug fixes (バグ修正)

- *(lint)* SA1019 + gocyclo nolint ディレクティブを追加。
- *(bundle)* generate-kustomize-manifests ステップを廃止 (PR-B9.4)
  (#25)。

### Chores (雑務)

- *(oss)* `CITATION.cff` を追加 (#23)。

### Features (機能)

- *(bundle)* OperatorHub.io bundle scaffold + ADR-0013 (PR-B9 cross-cut)
  (#24)。

## [0.3.0-alpha.12] - 2026-05-08

### Fixed (修正)

- `copySpec` panic — `*unstructured.Unstructured` (cert-manager
  `Certificate` CR) が未対応だった。 switch case を追加 (NestedMap spec
  + Labels)。

## [0.3.0-alpha.11] - 2026-05-08

### Fixed (修正)

- Helm chart の `rbac.yaml` で `cert-manager.io/certificates` ルールが
  欠落 (alpha.10 の controller-gen 更新は `config/rbac/role.yaml` のみを
  sync; Helm chart の `rbac.yaml` は手動メンテナンス)。 ライブクラスタの
  `ClusterRole` が out-of-sync となり、`Certificate` 要求が Forbidden に。

## [0.3.0-alpha.10] - 2026-05-08

### Fixed (修正)

- ClusterRole の `cert-manager.io/certificates` RBAC が欠落 → Phase-2 の
  `Certificate` CR upsert が Forbidden。 `kubebuilder:rbac` マーカーを追加。

## [0.3.0-alpha.9] - 2026-05-08

### Fixed (修正)

- `buildCertificate` panic — `unstructured.SetNestedField` の `dnsNames`
  が deep-copy 互換のため `[]string` → `[]any` に変換。 alpha.8 以降の
  最初のライブ適用で捕捉。

## [0.3.0-alpha.8] - 2026-05-08

### Added (Pillar P7 §7 — TLS 統合 3-phase 仕上げ)

- **Phase 1 (alpha.5)**: `spec.tls` フィールド facade —
  `TLSSpec{Enabled, IssuerRef, CertSecretName}`。 webhook は
  `enabled=true` のとき `NotImplemented` で拒否。
- **Phase 2 (alpha.6)**: cert-manager `Certificate` CR の自動 emit
  (unstructured、cert-manager Go SDK 依存ゼロ)。 `IssuerRef` 設定 +
  `Enabled=true` のとき、reconciler は `<cluster>-tls` Secret の発行を
  委譲。 SAN = cluster name + per-shard headless service の DNS 形式 4×。
  ECDSA P-256 + `rotationPolicy=Always`。
- **Phase 3a (alpha.7)**: サーバ証明書マウントのための STS `Volumes`
  + `VolumeMounts` (`/etc/ssl/postgres`、PG key-file 権限チェックのため
  `defaultMode=0o400`)。
- **Phase 3b (alpha.8)**: `postgresql.conf` に `ssl=on` +
  `ssl_cert_file` / `ssl_key_file` / `ssl_ca_file` +
  `ssl_min_protocol_version=TLSv1.2`。 `pg_hba.conf` が `host` →
  `hostssl` に切替 (外部クライアントの plaintext 接続を禁止;
  replication は pod-to-pod が信頼境界のため `host` を保持)。

### Refactored (リファクタ)

- `Reconcile` の cyclomatic-complexity を削減 — `reconcileInstanceRBAC`
  (3 つの upsert を統合) と `reconcileTLS` ヘルパに抽出。 gocyclo < 30
  baseline を復元。

## [0.3.0-alpha.4] - 2026-05-08

### Fixed (修正)

- `dist/install.yaml` / Helm chart / ライブ GitOps dry-run 検証フローを
  復元し、`PostgresCluster` インストールバンドルが再び server-side
  dry-run を通過。
- release-gate baseline を Go 1.25.10 builder イメージに整合 — stdlib
  セキュリティ baseline と一致。

## [0.3.0-alpha.3] - 2026-05-07

### Fixed (修正)

- 既存の PGDATA を持つ Postgres Pod の再起動時、bootstrap init
  コンテナが、kubelet が `fsGroup` を適用した後にも
  `chmod 0700 "$PGDATA"` を再実行するようにした。 `data/postgres-shard-0-0`
  再生成中に PostgreSQL が `invalid permissions` で終了する回帰を
  ライブで観測。

## [0.3.0-alpha.2] - 2026-05-07

### Added (追加)

- `hack/smoke.sh` の PG17/PG18 マトリクスオーバーライド (`PG_MAJOR`、
  `POSTGRES_VERSION`、`SHARD_REPLICAS`) と HA WAL-streaming ゲート。
- PG18 failover smoke ゲート: primary Pod 削除後の standby-promotion
  RTO 計測、CR-status の primary 収束を確認、再起動した旧 primary が
  standby として再投入されることを検証。
- `deploy/overlays/prod/` GitOps エントリポイント — kubebuilder の
  `config/{crd,rbac,manager}` を prod ネームスペースに整列し、自動生成
  された Namespace リソースを削除。 ArgoCD の一方向 sync を前提。
- `deploy/postgres-cluster.yaml` — プロダクション `PostgresCluster`
  CR サンプル (db ネームスペース、`shardingMode=none`、`replicas=2`、
  ceph-block、monitoring on)。
- `deploy/README.md` — 運用 runbook (前提、適用、ロールバック)。
- ADR-0006 — GitOps deploy-overlay 採用決定。

### Fixed (修正)

- election identity を `podName/podUID` に切替え、同名で再生成された
  ordinal が以前の primary の lease を即座に奪い返せないようにした。
- 再起動された ordinal-0 primary が `standby.signal` / `primary_conninfo`
  を再構築するように; `ReleaseOnCancel=false` と status ポーリング
  を追加 — PG18 failover smoke で RTO 21 s (< 30 s) を観測。

## [0.3.0-alpha.1] - 2026-05-06

### Changed (変更)

- Chart.yaml の `version` + `appVersion` 0.3.0-alpha → 0.3.0-alpha.1
  (反復的 pre-release 表記)。
- `config/manager/kustomization.yaml` の `newTag` を同期。
- `dist/install.yaml` を再生成 (`make build-installer`) — イメージタグ
  0.3.0-alpha.1。

### Fixed (修正)

- `release` ターゲットが今や
  `docker buildx build --platform linux/amd64 --push` でイメージを
  build & push (組織 §2 によりデフォルトビルダーを明示)。 Build と push
  が単一呼び出しで atomic ($(CONTAINER_TOOL) build の独立ステップを
  廃止)。

### Changed (BREAKING)

- **`PostgresCluster` CRD スキーマ再定義 (RFC 0001 v2 — F01a)**:
  `spec.coordinator` / `spec.workers[]` / `spec.routers` /
  `spec.extensions` / `spec.sharding.backend` / `spec.deployment` を
  削除。 新しい 6 フィールド構造 (`postgresVersion` / `shardingMode` /
  `shards` / `router` / `autoSplit` / `backup` / `monitoring`) に
  置換。 `status` も同様に `topology` / `channel` を落とし、`phase` /
  `shards[]` / `router` を導入。 v0.x マニフェストは互換性なし (alpha
  チャンネル方針)。
- CRD が RFC 0001 §3.3 の 3 つの CEL XValidation を埋め込むようになった
  — `shardingMode↔shards`、`router↔native`、`autoSplit↔native` — API
  サーバが直接拒否する。
- Webhook 検証は PostgresVersion マトリクス検索 + autoSplit-トリガ
  整合性 + 空でない backup スケジュールに単純化。 厳密な cron パース /
  duration パースは、外部依存を導入する F01b/F02 で到来予定。

### Deferred to F01b

- 新仕様の reconcile 本体 (`ShardsSpec` → StatefulSet topology、
  `RouterSpec` → Deployment、`BackupSpec` → 自動 `BackupJob` 生成)。
  今ターンは `// TODO(F01b)` コメントと最小 noop reconcile
  (`status.phase=Provisioning`、`Ready=False reason=NotApplicable`) を
  残す。
- `internal/controller/builders.go` のヘルパはシグネチャを維持し
  `//nolint:unused` を付ける — F01b の reconcile で配線される。
- 2 つの envtest (`postgrescluster_controller_test.go`、
  `cascade_delete_test.go`) は削除し、F01b で RFC 0001 仕様に基づき
  書き直す。

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **再設計**: PostgreSQL 上に自前構築する distributed-SQL レイヤへ
  ピボット。 ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`)
  が要石。
- アーカイブされた AGPL サードパーティ extension の分離 + vanilla-PG
  デフォルトモデルを置換。 本フェーズ以降、ランタイムにはその extension
  のコードが*一行も*含まれない;分離プラグインモデルを廃止。
- 外部依存ライセンスポリシー (ADR-0003): v1+ 安定性を持つ BSD /
  Apache / MIT / PG License のみ。 **AGPL / BUSL / CSL / SSPL は永久
  禁止。**
- Helm パッケージング (ADR-0002): 単一 chart + コンポーネントフラグ
  (router / resharder / rebalancer / keda / backup / monitoring)。
- CRD ライフサイクル (ADR-0004): operator manager の所有 (server-side
  apply)。 Helm の `crds/` ディレクトリは将来フェーズで廃止予定。
- バージョンチャンネル (ADR-0005): alpha (P0–P3) → beta (P4–P5) →
  stable (P6+)。 CRD apiVersion v1alpha1 → v1beta1 → v1。

### Added (追加)

- 新 ADR: 0001 (自前 distributed SQL — 要石)、0002 (フラグ付き単一
  chart)、0003 (ライセンスポリシー: AGPL / BUSL / CSL / SSPL 禁止)、
  0004 (operator 管理の CRD ライフサイクル)、0005 (versioning +
  channels)。
- 新 RFC: 0001 (PostgresCluster CRD v2)、0002 (`ShardRange` CRD)、0003
  (`ShardSplitJob` 7-step オンライン resharding)、0004 (pg-router
  アーキテクチャ)、0005 (分散トランザクション — 2PC + saga)。
- `README.md` 書き直し — 自前 distributed-SQL アイデンティティ、8-
  フェーズロードマップ (P0–P7、~64 ヶ月)、明示的ライセンスポリシー。
- `TASKS.md` 書き直し — P0 タスクテーブル + 次フェーズ (P1) のプレ
  ビュー。
- `HANDOFF.md` 書き直し — 次セッションのエントリポイント、コード除去
  の隔離ガイダンス。

### Archived (アーカイブ)

- 元の ADR 0001–0010 を `docs/kb/adr/_archive/v0.x/` に移動 (git
  history 保持)。
- 元の RFC 0001–0005 を `docs/rfcs/_archive/v0.x/` に移動。

### Deprecated (次セッションで削除予定)

- サードパーティ AGPL sharding extension の内部パッケージ群 —
  ADR-0003 違反。
- `charts/postgres-operator/` における当該 extension の opt-in メッセ
  ージング (レガシー DSN フィールド、NOTES.txt の AGPL ガイダンス)。

## [0.2.0-alpha] - 2026-05-01

### Changed (BREAKING)

- 前フェーズの ADR (現在はアーカイブ) — デフォルトスタックを vanilla
  PostgreSQL 18 に切替。 サードパーティ AGPL sharding-extension の統合
  は Beta チャンネル opt-in に隔離。 明示的に有効化したユーザは
  AGPL-3.0 §13 の SaaS 義務を受容 (operator 自体は Apache-2.0 クリーン
  を維持)。
- `VersionSpec` のレガシー extension フィールドが Optional (`omitempty`)
  に — 以前は Required。 空 / 未指定の値は vanilla PG を選択。
- Stable チャンネル: PG 16/17/18 vanilla。 サードパーティ sharding-
  extension の組み合わせはすべて Beta に降格。
- chart の `config/samples/*` からサードパーティ extension のデフォルト
  を削除。 推奨デフォルトは vanilla PG18。

### Added (追加)

- `internal/version/matrix.go` に PG 18 vanilla Stable 組み合わせ
  (`ghcr.io/keiailab/pg:18`) を追加。
- 前フェーズの ADR (アーカイブ) — ライセンス + sharding 戦略。 AGPL
  サードパーティ sharding extension の隔離を文書化、ライセンス義務の
  配分を記録。
- RFC 0005 (native sharding plugin) — 7 つのコア distributed-SQL
  メカニズムの分解、自前プラグインインターフェースのドラフト設計、
  Phase 2A → Phase 4 までのマイルストーン。
- chart の `NOTES.txt` にライセンス開示メッセージ (Apache-2.0
  operator + opt-in AGPL サードパーティ-extension の告知)。
- サードパーティ-extension プラグインパッケージおよび関数ドキュメント
  に AGPL §13 SaaS 義務についての注意書き。

### Removed (削除)

- 古い `ChannelPreviewPG18` placeholder を削除 — PG18 が Stable に乗った
  今 obsolete。
- webhook の PG18 + `PostgresEighteen` feature-gate チェックを削除 —
  Stable では不要。

## [0.1.1-alpha] - 2026-05-01

### Added (追加)

- `make validate`、`make gate`、`make release-preflight`、`make release`、
  `make helm-publish` によるローカルリリース自動化。
- `config/crd/kustomization.yaml` が `make install / uninstall` と
  CRD-render パスを復元。
- `make sync-crds` が `config/crd/bases` と
  `charts/postgres-operator/crds` 間のドリフトをブロック。
- Helm chart の `.helmignore`、`values.schema.json`、README、Artifact
  Hub メタデータ。
- `dist/install.yaml` 単一インストールアーティファクトの検証経路。

### Fixed (修正)

- `go test` を直接実行する際、controller テスト suite がローカル
  envtest-asset フォールバックを使うよう調整。
- chart の既定イメージ repository を
  `ghcr.io/keiailab/postgres-operator` に整合。
- Helm RBAC に `BackupJob` リソース権限を追加。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
