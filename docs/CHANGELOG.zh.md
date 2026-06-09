<p align="center">
  <a href="CHANGELOG.md">English</a> |
  <a href="CHANGELOG.ko.md">한국어</a> |
  <a href="CHANGELOG.ja.md">日本語</a> |
  <b>中文</b>
</p>

# Changelog (简体中文)

> 英文原文: [CHANGELOG.md](CHANGELOG.md) — canonical / 正本

本项目遵循 SemVer。

## [Unreleased]

### Added (新增)

- *(olm,docs)* `docs/operator-guide/community-operators-onboarding.md` —
  k8s-operatorhub/community-operators 频道的接入流程 (前置条件清单、
  bundle 镜像 build / push、`gh pr create`、upgrade-graph 运维、
  Artifact Hub OLM 校验)。T28 首个交付物。
- *(ci)* `make hooks-install` / `make hooks-check` 目标 — 用于启用
  lefthook DCO / Conventional Commits 网关的 wrapper。CONTRIBUTING
  也同步对齐。
- *(ci)* `make validate` 新增 4 个网关 — bundle CRD 数 ≥ 8、
  `operator-sdk bundle validate` 默认 + `suite=operatorframework`、
  Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag 漂移断言,
  以及 `.github/workflows/` 不存在 (ADR-0009 强制)。
- *(olm)* 在 CSV 的 `customresourcedefinitions.owned[]` 中为
  ImageCatalog / ClusterImageCatalog / PostgresDatabase / PostgresUser
  添加 `description` + `displayName` — 消除 operatorframework suite 的
  4 条 warning。
- *(oss)* 新增 `SUPPORT.md` (GitHub Discussions / Issues / PR 路径、
  安全路由、响应时间预期)。0.3.0-alpha.17 与 0.3.0-alpha.18 的
  CHANGELOG 条目也已对齐。
- *(docs)* README — 0.3.0-alpha.18 的 8-CRD 面板表 + 6 步 quickstart。
- *(docs)* TASKS — 登记 T26 (cross-cut OSS/OLM 对齐,已完成)、
  T27 (新 CRD 的 live smoke 自动化,①–④ 全部完成)、
  T28 (community-operators 注册流程)。
- *(smoke)* 在 `hack/smoke.sh` 中新增 4 个新 CRD 场景:
  `SMOKE_DATABASE=1` (PostgresDatabase → `status.applied` +
  `pg_database` + reclaim=delete DROP)、`SMOKE_USER=1` (PostgresUser →
  `status.applied` + `pg_roles` + DROP ROLE)、`SMOKE_SCHEDULEDBACKUP=1`
  (cron `immediate` → 创建 BackupJob)、`SMOKE_IMAGECATALOG=1`
  (ImageCatalog / ClusterImageCatalog schema + lookup)。步骤编号统一为
  N/15。
- *(api)* 为全部 8 个 owned CRD 添加 `kubebuilder:resource:categories`
  标记 — `kubectl get postgres` / `database` / `backup` / `pooler` /
  `image` / `role` / `all` 别名,以及 OperatorHub UI 分组。
- *(ci)* `make lint-k8s` 目标已并入 `make validate` — 对
  `dist/install.yaml` 与 helm-template 输出执行 kube-linter 静态分析
  (liveness-port / readiness-port / non-root / readOnlyRoot /
  resource-limit 等 30+ 项检查)。
- *(ci,olm)* `config/scorecard/` + `bundle/tests/scorecard/config.yaml`
  + `make scorecard` 目标 — 自动化 6 项 operator-sdk scorecard 测试
  (basic-check-spec、olm-bundle-validation、olm-crds-have-validation、
  olm-crds-have-resources、olm-spec-descriptors、olm-status-descriptors)。
  实际运行需要 kind 集群 (`make smoke` 后再 `make scorecard`)。
- *(controller)* **Pooler 内置 auth** (T27 ⑤) — 当
  `spec.pgbouncer.authSecretRef` 为空时,operator 在 PostgresCluster 的
  ready primary Pod 上创建 `keiailab_pooler_pgbouncer` LOGIN 角色
  (24 字节 crypto/rand base64 密码) 与 userlist.txt Secret
  `<pooler-name>-builtin-auth` (Pooler 所有)。为生态兼容性保留
  PgBouncer userlist 格式 `cnpg_pooler_pgbouncer`。等待 primary 期间为
  Phase=Pending + Reason=BuiltinAuthWaitingForPrimary,具有
  idempotent ensure。同时修复了
  `PoolerReconciler.PodExecutor` 接线的回归 (恢复 PAUSE/RESUME +
  SIGHUP-reload 路径)。
- *(controller)* **Pooler AutoTLS — cert-manager 集成** (T29) — 当
  `spec.pgbouncer.autoTLS` 被设置时,operator 通过 `unstructured` 发出
  cert-manager `Certificate` CR (按角色: server / client),并让
  cert-manager 颁发 Secret (`tls.crt` + `tls.key` + `ca.crt`);
  PgBouncer Deployment 透明地挂载自动生成的 Secret。显式指定的
  `Server/ClientTLSSecret` 仍然优先。新增 helper
  (`poolerEffectiveServerTLSSecretName`、
  `poolerEffectiveClientTLSSecretName`、
  `poolerAutoTLS{Server,Client}Active`)、新的针对
  `cert-manager.io/certificates` 的 RBAC 标记,2 个新 unit test
  (`TestPoolerAutoTLS_CreatesCertificate`、
  `TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence`),以及新样本
  `config/samples/postgres_v1alpha1_pooler_autotls.yaml`。
- *(controller)* **Pooler 内置 auth 密码轮换** (T27 ⑥) — 当设置注解
  `postgres.keiailab.io/rotate-pooler-password=true` 时,会触发以新密码
  执行 `ALTER ROLE`、原地更新 userlist.txt Secret、移除注解,并记录
  `Pooler.Status.BuiltinAuthLastRotation`。新增 unit test
  `TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation`。
- *(docs)* ROADMAP "current state" 快照 — 新增 4 行:alpha.18 + OLM
  bundle + 声明式 DB 表面 + 本地 4-layer 网关。
- *(docs)* 交叉校验矩阵 — 新增 5 个维度:OLM bundle / Helm chart /
  本地 supply-chain 网关 / 安全漏洞扫描 / DCO 签署强制。

### Added (新增)

- *(api,controller)* T29 stage 4 — `Pooler.spec.pgbouncer.autoTLS.selfSigned`
  字段。设为 true (且 `issuerRef` 为空) 时,operator 会在进程内生成
  RSA-2048 自签名 CA + leaf 证书 (有效期 1 年,30 天续期偏移),并以
  `tls.crt`/`tls.key`/`ca.crt` 键名存入 Secret
  `<pooler>-client-tls` / `<pooler>-server-tls` — 布局与 cert-manager
  Certificate 颁发的 Secret 完全一致。补齐了无 cert-manager 环境的
  缺口。 CEL XValidation 规则在 admission 时强制 "{issuerRef,
  selfSigned} 恰好二选一"。回归测试
  `TestPoolerAutoTLS_SelfSignedCreatesSecretAndMirrorsNotAfter` +
  样本 CR。
- *(api,controller)* T29 stage 5 — `Pooler.Status.AutoTLSClientCertNotAfter`
  + `AutoTLSServerCertNotAfter` 镜像 cert-manager 的
  `Certificate.status.notAfter`,使运维方能跨整个集群列出即将到期的
  证书 (`kubectl get poolers -A -o wide` 在 `priority=1` 处暴露这两列)。
  reconciler 通过 `unstructured` 读取 cert-manager 的 Certificate CR
  (零 SDK 依赖);查询过程中的错误会以 V(1) 日志记录并视为 "unknown",
  以避免 cert-manager 短暂故障阻塞 Pooler reconcile 的其他部分。回归
  测试 `TestPoolerAutoTLS_MirrorsNotAfterToStatus`。
- *(api,controller)* `PostgresUser.spec.userReclaimPolicy` (`retain`
  默认、`delete`) 镜像 `PostgresDatabase.spec.databaseReclaimPolicy`。
  设为 `delete` 时,reconciler 会附加
  `postgres.keiailab.io/postgresuser-finalizer`,并在允许垃圾回收之前
  通过既有 `ensure=absent` reconcile 脚本执行 `DROP ROLE`。解决了
  PG18 kind smoke iter#7 观察到的 "`kubectl delete postgresuser`
  会遗留 PostgreSQL 角色" 问题。

### Fixed (修复)

- *(instance)* HA bootstrap fence race — 最终修复。原本的
  "memberCount>1 集群中,任何 leader-stop 都 fence" 规则会在任何
  瞬时的 lease-renewal 中断时把 bootstrap Pod 的 PVC 隔离掉并植入
  standby.signal,导致后续启动永远走 Follower 分支。三层修复:
  (i) `supervise.IsStandby(dataDir)` 短路;
  (ii) `promotedAtLeastOnce atomic.Bool` 标记,只有真正 promote 过的
  状态才会触发 fencing;(iii) **standby-pod election 降级** —
  启动时磁盘上带有 standby.signal 的 pod 走 Follower election (永不
  争夺 lease)。在此之上,`handleStoppedLeading` 现在无副作用 —
  failover 仅由 operator 通过 `executeClusterPromotion` 驱动。
  PG18 / PG17 SHARD_REPLICAS=1 HA smoke 5/5 PASS + WAL 复制验证通过;
  SHARD_REPLICAS=0 也确认 5/5 回归。新回归测试
  `TestHandleStoppedLeading_NeverFencesOrDemotes` 固定 no-op 契约。
- *(controller)* PostgresDatabase / PostgresUser 的 psql 调用默认采用
  OS 用户 `pg-keiailab` (Dockerfile.pg 的 USER 指令)。在 iter#5 的
  `eval` bug 被移除后,此问题显现为
  `FATAL: role "pg-keiailab" does not exist` (PG18 kind smoke iter#6)。
  已为渲染后 reconcile 脚本中所有 psql 调用显式加上 `-U postgres`
  (`psql_base` 常量 + 每个 per-database 调用)。回归测试
  `TestPostgresDatabaseReconcileScriptDoesNotUseEval` 更新为要求渲染
  后的命令包含 `-U postgres`。
- *(smoke)* `hack/smoke.sh` 在 `kubectl apply -f dist/install.yaml`
  之后没有重启 operator Pod,使 kind 在重试时仍复用缓存镜像
  (`imagePullPolicy=IfNotPresent` + 同 tag)。Pod 运行的 operator 二进制
  比磁盘上的源码更老,遮蔽了新修复。smoke.sh 现在会在 apply 后对
  controller-manager Deployment 执行 `kubectl rollout restart`,并以
  `rollout status` 等待新 ReplicaSet。
- *(controller)* PostgresDatabase / PostgresUser 的 reconcile 脚本
  使用 `eval "$psql_base" -c '<SQL>'` 调用 psql;外层 shell 在把参数
  传给 `eval` 之前剥掉了 `<SQL>` 两侧的 single quote,`eval` 接着把
  所有参数以空格拼起来并重新解析整个字符串。SQL 因此按空白被
  word-split,psql 看到的是 `-c CREATE`、`DATABASE`、`smoke_db_x` …
  这样若干独立参数 — 引发 `FATAL: role "1" does not exist` 与
  `FATAL: role "DATABASE" does not exist` (PG18 kind smoke iter#5 观察)。
  将每处 `eval "$psql_base" …` 调用都替换为内联的完整
  `psql -v ON_ERROR_STOP=1 -X -q -d postgres -c '<SQL>'` 调用,使 SQL
  停留在单个 shell-quoted 参数中,原子地交付给 psql。新增 2 个回归
  测试 (`TestPostgresDatabaseReconcileScriptDoesNotUseEval`、
  `TestPostgresUserReconcileScriptDoesNotUseEval`),断言渲染后的
  脚本不含 `eval`。
- *(controller)* PostgresDatabase / PostgresUser 的 `status.applied`
  可能在 finalizer 已经附加时仍未被设置 (无 condition、空
  `status: {}`)。两个根因 — *(a)* finalizer-add 路径返回
  `Requeue:true` 并把 SQL apply 推到第二个 pass,在 informer-cache
  传播延迟下容易在过时快照上循环;*(b)* `statusUpdate` 默默吞掉了
  `apierrors.IsConflict`,因此当同一 generation 上的 finalizer Update
  与 status Update 竞争时,status payload 会被整体丢弃。reconciler
  现在 (i) 添加 finalizer 并继续*同一个* reconcile pass (single-pass
  apply + status),(ii) 在 conflict 时 re-fetch 并重试一次后再放弃。
  在 PG18 kind smoke iter#3 中观察;更新后的测试
  `TestPostgresDatabaseReconcileDeletePolicyAddsFinalizerBeforeApply`
  现在断言 single-pass `status.applied=true`。同样的 conflict-retry
  模式也被 retrofit 到 BackupJob、ScheduledBackup、Pooler 的
  `statusUpdate` 辅助函数中以保持一致。
- *(controller)* Pooler — 当上游 PostgresCluster 的
  `status.shards[0].primary.ready` 在 Pooler 首次 reconcile *之后*
  才翻为 true 时,由于 PoolerReconciler 没有对 PostgresCluster 的
  `Watches`,Pooler 会永远卡在 `phase=Failed, reason=TargetNotFound`
  (PG18 kind smoke iter#4 观察:Pooler 在 14:29:38Z reconcile,
  cluster 在 14:29:42Z Ready=True,Deployment 始终未创建)。
  PoolerReconciler 现在使用
  `Watches(&PostgresCluster{}, EnqueueRequestsFromMapFunc(...))`,
  以便在 status 变化时把 namespace 中所有 `spec.cluster.name` 匹配的
  Pooler 重新入队;missing-target 分支也改为标记 `phase=Pending`
  + `RequeueAfter` 而非 `Failed`。新增回归测试
  `TestPoolerReconcileTargetNotFoundIsPendingWithRequeue`。
- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH;通过 SPDY streaming 的 Kubelet / CRI-O /
  kube-apiserver DoS)。`trivy fs --severity HIGH,CRITICAL --exit-code 1`
  再次恢复 green。
- *(ci,kustomize)* 修复了 manager Deployment 没有在 `containerPorts`
  中列出 8081 health 端口的漂移。`config/manager/manager.yaml` 从
  `ports: []` 改为 `ports: [{name: health, containerPort: 8081,
  protocol: TCP}]`,使 helm chart 与 `dist/install.yaml` 的 manager
  Deployment 对齐 (kube-linter 的 liveness-port / readiness-port 检查)。
- *(docs,license)* 从 NOTICE 中移除了过时的 legacy AGPL-3.0 第三方
  sharding-extension 条目 — 依据 ADR-0003 (永久禁止 AGPLv3 的许可
  政策) 与 ADR-0001 (自建 distributed SQL)。 NOTICE 现在只列出
  `go.mod` 中的直接依赖 (Prometheus、Ginkgo、robfig/cron、
  moby/spdystream、…)。

## [0.3.0-alpha.18] - 2026-05-12

### Added (新增)

- *(api,controller)* 新增 `ImageCatalog` + `ClusterImageCatalog` CRD
  (TASKS T24)。`spec.imageCatalogRef.{apiGroup,kind,name,major}` (为
  生态兼容性接受 `postgresql.cnpg.io` apiGroup)、namespaced /
  cluster-scoped lookup、catalog → StatefulSet 镜像传播、基于
  image-hash 注解驱动的 rollout 漂移。
- *(api,controller)* `PostgresDatabase` + `PostgresUser` CRD (TASKS
  T22)。 Ready-primary 的 `psql` reconcile 会应用 database /
  tablespace / schema / extension / FDW / foreign server,以及 role
  flags / membership / `connectionLimit` / `passwordSecretRef` /
  `disablePassword` / `validUntil`。`databaseReclaimPolicy=delete`
  finalizer + `status.applied/observedGeneration/conditions` +
  `managedRolesStatus` 聚合。
- *(controller,instance)* Standalone replica cluster + externalClusters
  流式路径 (TASKS T25)。`spec.externalClusters[]`、
  `bootstrap.pg_basebackup.source`、`replica.enabled/source`。
  `POSTGRES_REPLICA_CLUSTER=standalone` persistent-follower election、
  password Secret passfile + TLS Secret projected mount、source-
  mismatch fail-closed。
- *(api,controller)* `Pooler` CRD + PgBouncer 连接池层 (F05)。
  `instances`、`type=rw/ro`、`pgbouncer.{poolMode,parameters,pg_hba}`、
  auth / TLS Secret、exporter sidecar、`spec.paused` PAUSE/RESUME、
  `pgbouncer.parameters` SIGHUP reload、HA topology / PDB。
- *(observability)* metrics + Grafana 仪表盘 + PrometheusRule +
  ServiceMonitor (F05)。BackupJob / Pooler 阶段指标、复制滞后字节、
  PgBouncer exporter 告警、cluster-overview + Pooler 仪表盘 ConfigMap,
  与 kube-prometheus-stack sidecar 兼容。
- *(controller,instance)* Failover promoter 执行 + follower election
  (F03 后续,PR #38/#39 落地)。 Replica-Pod 的 `postgres` 容器 exec
  → `pg_ctl promote` → `pg_is_in_recovery()` 轮询 → primary 注解 patch。
- *(backup)* `ScheduledBackup` CRD + sidecar exec runner + pgBackRest
  command-runner 插件 (F04)。6-field cron + `concurrencyPolicy`
  Allow/Forbid + retention + JobTemplate。
- *(release,ci)* Artifact Hub 自动注册 / smoke `hack/artifacthub_*.sh`
  + Makefile `artifacthub-{register,smoke}` 目标。 kind smoke 中新增
  `SMOKE_HIBERNATION=1` (为生态工具兼容性保留 hibernation 注解
  `cnpg.io/hibernation` + PVC 标记保留) 与 `SMOKE_POOLER=1`
  (PgBouncer Service psql / PAUSE / RESUME / config reload) 场景。
  `make validate` 将 CRD 数量断言从 2 提升至 8,并新增 18 项
  monitoring-render grep 检查。
- *(olm)* `bundle/manifests/` 对齐到 0.3.0-alpha.18 — 8 个 CRD +
  alm-examples 一致 (`operator-sdk bundle validate` 0 warnings)。 7 个
  owned-CRD 的 `config/samples/` 文件全部启用。

### Fixed (修复)

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH;通过 SPDY streaming 的 Kubelet / CRI-O /
  kube-apiserver DoS)。来自 k8s.io/client-go 的间接面也已刷新。

### Changed (变更)

- *(chart)* `version` 0.3.0-alpha.16 → 0.3.0-alpha.18、`appVersion`
  0.3.0-alpha.17 → 0.3.0-alpha.18、manager-image `newTag`
  0.3.0-alpha.18。上一次 alpha.17 bump 时遗留下了 `version:
  0.3.0-alpha.16` — 本周期把三者对齐。

## [0.3.0-alpha.17] - 2026-05-12

### Fixed (修复)

- *(bootstrap)* 对非空、陈旧的 `postmaster.pid` 做 PID-alive 检查
  (INC-0046 P19 ⑲,生产集群范围)。修复了残留僵尸文件阻塞新 PG
  启动的回归。

## [0.3.0-alpha.16] - 2026-05-10

### Bug fixes (缺陷修复)

- *(lint)* SA1019 + gocyclo nolint 指令已添加。
- *(bundle)* 移除 generate-kustomize-manifests 步骤 (PR-B9.4) (#25)。

### Chores (杂项)

- *(oss)* 新增 `CITATION.cff` (#23)。

### Features (功能)

- *(bundle)* OperatorHub.io bundle 脚手架 + ADR-0013 (PR-B9 cross-cut)
  (#24)。

## [0.3.0-alpha.12] - 2026-05-08

### Fixed (修复)

- `copySpec` panic — 不支持 `*unstructured.Unstructured` (cert-manager
  `Certificate` CR)。补充了 switch case (NestedMap spec + Labels)。

## [0.3.0-alpha.11] - 2026-05-08

### Fixed (修复)

- Helm chart 的 `rbac.yaml` 缺失 `cert-manager.io/certificates` 规则
  (alpha.10 的 controller-gen 更新只同步了 `config/rbac/role.yaml`;
  Helm chart 的 `rbac.yaml` 是手工维护的)。线上集群的 `ClusterRole`
  失同步,导致 `Certificate` 请求 Forbidden。

## [0.3.0-alpha.10] - 2026-05-08

### Fixed (修复)

- ClusterRole 上缺失 `cert-manager.io/certificates` RBAC → Phase-2 的
  `Certificate` CR upsert 被 Forbidden。补充了 `kubebuilder:rbac`
  marker。

## [0.3.0-alpha.9] - 2026-05-08

### Fixed (修复)

- `buildCertificate` panic — `unstructured.SetNestedField` 中的
  `dnsNames` 已转换为 `[]string` → `[]any` 以兼容 deep-copy。alpha.8
  之后首次实际部署即捕获。

## [0.3.0-alpha.8] - 2026-05-08

### Added (Pillar P7 §7 — TLS 集成 3-phase 收尾)

- **Phase 1 (alpha.5)**:`spec.tls` 字段 facade —
  `TLSSpec{Enabled, IssuerRef, CertSecretName}`。当 `enabled=true` 时
  webhook 以 `NotImplemented` 拒绝。
- **Phase 2 (alpha.6)**: 自动签发 cert-manager `Certificate` CR
  (unstructured,零 cert-manager Go SDK 依赖)。当 `IssuerRef` 已设置
  且 `Enabled=true` 时,reconciler 把 `<cluster>-tls` Secret 的签发委托
  出去。SAN = cluster 名 + 每个分片 headless service 的 DNS 形式 4×。
  ECDSA P-256 + `rotationPolicy=Always`。
- **Phase 3a (alpha.7)**: 用于挂载 server 证书的 STS `Volumes` +
  `VolumeMounts` (`/etc/ssl/postgres`,因 PG key-file 权限检查需要
  `defaultMode=0o400`)。
- **Phase 3b (alpha.8)**: `postgresql.conf` 启用 `ssl=on` +
  `ssl_cert_file` / `ssl_key_file` / `ssl_ca_file` +
  `ssl_min_protocol_version=TLSv1.2`。`pg_hba.conf` 将 `host` →
  `hostssl` (禁止外部客户端的明文连接;由于 pod-to-pod 是信任边界,
  replication 仍保留为 `host`)。

### Refactored (重构)

- 削减 `Reconcile` 的 cyclomatic-complexity — 抽出
  `reconcileInstanceRBAC` (统一 3 处 upsert) 与 `reconcileTLS` 辅助
  函数。gocyclo < 30 baseline 恢复。

## [0.3.0-alpha.4] - 2026-05-08

### Fixed (修复)

- 恢复了 `dist/install.yaml` / Helm chart / 线上 GitOps dry-run 校验
  流程,使 `PostgresCluster` 安装 bundle 再次通过 server-side dry-run。
- 将 release-gate baseline 与 Go 1.25.10 builder 镜像对齐,以匹配
  stdlib 的安全基线。

## [0.3.0-alpha.3] - 2026-05-07

### Fixed (修复)

- 当带有既有 PGDATA 的 Postgres Pod 重启时,bootstrap init 容器
  现在即便在 kubelet 已应用 `fsGroup` 之后,也会重新执行
  `chmod 0700 "$PGDATA"`。该回归在 `data/postgres-shard-0-0` 重建
  过程中现场观察到,PostgreSQL 因 `invalid permissions` 退出。

## [0.3.0-alpha.2] - 2026-05-07

### Added (新增)

- `hack/smoke.sh` 的 PG17/PG18 矩阵覆盖 (`PG_MAJOR`、`POSTGRES_VERSION`、
  `SHARD_REPLICAS`) 以及 HA WAL-streaming 网关。
- PG18 failover smoke 网关:删除 primary Pod 后测量 standby-promotion
  RTO,确认 CR-status primary 收敛,并验证重启的旧 primary 以 standby
  身份回入。
- `deploy/overlays/prod/` GitOps 入口 — 把 kubebuilder 的
  `config/{crd,rbac,manager}` 对齐到 prod 命名空间,并移除自动生成的
  Namespace 资源。假定 ArgoCD 单向 sync。
- `deploy/postgres-cluster.yaml` — 生产 `PostgresCluster` CR 样本
  (db 命名空间、`shardingMode=none`、`replicas=2`、ceph-block、
  monitoring on)。
- `deploy/README.md` — 运维 runbook (前置条件、应用、回滚)。
- ADR-0006 — GitOps deploy-overlay 采用决定。

### Fixed (修复)

- 将 election 身份切换为 `podName/podUID`,以避免同名重建的 ordinal
  立即回收前 primary 的 lease。
- 重启后的 ordinal-0 primary 现在会重建 `standby.signal` /
  `primary_conninfo`;新增 `ReleaseOnCancel=false` 与 status 轮询 —
  在 PG18 failover smoke 上观察到 RTO 21 s (< 30 s)。

## [0.3.0-alpha.1] - 2026-05-06

### Changed (变更)

- Chart.yaml 的 `version` + `appVersion` 0.3.0-alpha → 0.3.0-alpha.1
  (迭代式 pre-release 标记)。
- 同步 `config/manager/kustomization.yaml` 的 `newTag`。
- 重新生成 `dist/install.yaml` (`make build-installer`) — 镜像标签
  0.3.0-alpha.1。

### Fixed (修复)

- `release` 目标现在以 `docker buildx build --platform linux/amd64
  --push` 一次性构建并推送镜像 (依据组织 §2,显式使用默认 builder)。
  Build 与 push 在单次调用中是原子的 (移除独立的
  `$(CONTAINER_TOOL) build`)。

### Changed (BREAKING)

- **`PostgresCluster` CRD schema 重定义 (RFC 0001 v2 — F01a)**:
  移除 `spec.coordinator` / `spec.workers[]` / `spec.routers` /
  `spec.extensions` / `spec.sharding.backend` / `spec.deployment`。
  替换为新的 6 字段结构 (`postgresVersion` / `shardingMode` /
  `shards` / `router` / `autoSplit` / `backup` / `monitoring`)。
  `status` 同样去掉 `topology` / `channel`,引入 `phase` /
  `shards[]` / `router`。v0.x 清单不兼容 (alpha 频道策略)。
- CRD 现在内嵌 RFC 0001 §3.3 的 3 项 CEL XValidation —
  `shardingMode↔shards`、`router↔native`、`autoSplit↔native` — 由
  API 服务器直接拒绝。
- Webhook 校验简化为 PostgresVersion 矩阵查找 + autoSplit-trigger
  一致性 + 非空 backup 计划。精确的 cron 解析 / duration 解析将随
  外部依赖引入在 F01b/F02 中到来。

### Deferred to F01b

- 新规格的 reconcile 主体 (`ShardsSpec` → StatefulSet 拓扑、
  `RouterSpec` → Deployment、`BackupSpec` → 自动创建 `BackupJob`)。
  本轮仅保留 `// TODO(F01b)` 注释和最小 noop reconcile
  (`status.phase=Provisioning`、`Ready=False reason=NotApplicable`)。
- `internal/controller/builders.go` 中的 helper 维持原有签名并附带
  `//nolint:unused` — 将由 F01b 的 reconcile 接入。
- 2 个 envtest (`postgrescluster_controller_test.go`、
  `cascade_delete_test.go`) 已被删除,将在 F01b 中按 RFC 0001 规格
  重写。

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **重新设计**:转向以 PostgreSQL 为底座、自建的 distributed-SQL 层。
  ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 为基石。
- 替代了归档的 AGPL 第三方扩展隔离 + 默认 vanilla-PG 模型。从本阶段
  起,运行时不再包含该扩展的*任何一行*代码;隔离插件模型退役。
- 外部依赖许可政策 (ADR-0003):仅允许具备 v1+ 稳定性的 BSD /
  Apache / MIT / PG License。**AGPL / BUSL / CSL / SSPL 永久禁止。**
- Helm 打包 (ADR-0002):单 chart + 组件 flag (router / resharder /
  rebalancer / keda / backup / monitoring)。
- CRD 生命周期 (ADR-0004):由 operator manager 拥有 (server-side
  apply)。 Helm 的 `crds/` 目录将在未来阶段退役。
- 版本频道 (ADR-0005):alpha (P0–P3) → beta (P4–P5) → stable
  (P6+)。CRD apiVersion v1alpha1 → v1beta1 → v1。

### Added (新增)

- 新 ADR:0001 (自建 distributed SQL — 基石)、0002 (带 flag 的单 chart)、
  0003 (许可政策:禁止 AGPL / BUSL / CSL / SSPL)、0004 (operator
  管理的 CRD 生命周期)、0005 (versioning + channels)。
- 新 RFC:0001 (PostgresCluster CRD v2)、0002 (`ShardRange` CRD)、
  0003 (`ShardSplitJob` 7-step 在线 resharding)、0004 (pg-router 架构)、
  0005 (分布式事务 — 2PC + saga)。
- 重写 `README.md` — 自建 distributed-SQL 定位、8-阶段路线图
  (P0–P7,约 64 个月)、明确的许可政策。
- 重写 `TASKS.md` — P0 任务表 + 下一阶段 (P1) 预览。
- 重写 `HANDOFF.md` — 下一会话的入口,代码移除的隔离指引。

### Archived (归档)

- 原 ADR 0001–0010 移至 `docs/kb/adr/_archive/v0.x/` (保留 git
  历史)。
- 原 RFC 0001–0005 移至 `docs/rfcs/_archive/v0.x/`。

### Deprecated (将在下一会话移除)

- 第三方 AGPL sharding 扩展的内部包 — 违反 ADR-0003。
- `charts/postgres-operator/` 中关于该扩展的 opt-in 信息 (legacy
  DSN 字段、NOTES.txt 的 AGPL 指引)。

## [0.2.0-alpha] - 2026-05-01

### Changed (BREAKING)

- 前一阶段的 ADR (现已归档) — 把默认栈切换为 vanilla PostgreSQL
  18。第三方 AGPL sharding-extension 集成被隔离至 Beta 频道 opt-in。
  显式启用的用户即接受 AGPL-3.0 §13 的 SaaS 义务 (operator 自身仍
  保持 Apache-2.0 clean)。
- `VersionSpec` 中的 legacy extension 字段现在为 Optional
  (`omitempty`) — 之前为 Required。空 / 缺失值即选择 vanilla PG。
- Stable 频道:PG 16/17/18 vanilla。所有第三方 sharding-extension
  组合都降级至 Beta。
- 移除 chart 的 `config/samples/*` 中第三方扩展的默认值。推荐默认值
  现为 vanilla PG18。

### Added (新增)

- 向 `internal/version/matrix.go` 新增 PG 18 vanilla Stable 组合
  (`ghcr.io/keiailab/pg:18`)。
- 前一阶段的 ADR (已归档) — 关于许可 + sharding 策略。记录了对
  AGPL 第三方 sharding 扩展的隔离方式与许可义务分配。
- RFC 0005 (native sharding plugin) — 7 大核心 distributed-SQL 机制
  的拆分、自研插件接口的草案设计,以及 Phase 2A → Phase 4 的里程碑。
- chart 的 `NOTES.txt` 中加入许可披露提示 (MIT operator +
  opt-in AGPL 第三方扩展告示)。
- 在第三方扩展的插件包与函数文档中加入关于 AGPL §13 SaaS 义务的
  文档警告。

### Removed (移除)

- 移除已过期的 `ChannelPreviewPG18` placeholder — PG18 已进入 Stable,
  该占位符已废弃。
- 移除 webhook 的 PG18 + `PostgresEighteen` feature-gate 检查 —
  Stable 已不再需要。

## [0.1.1-alpha] - 2026-05-01

### Added (新增)

- 通过 `make validate`、`make gate`、`make release-preflight`、
  `make release`、`make helm-publish` 的本地 release 自动化。
- `config/crd/kustomization.yaml` 恢复了 `make install / uninstall`
  与 CRD-render 路径。
- `make sync-crds` 阻断 `config/crd/bases` 与
  `charts/postgres-operator/crds` 之间的漂移。
- Helm chart 的 `.helmignore`、`values.schema.json`、README,
  以及 Artifact Hub 元数据。
- `dist/install.yaml` 单一安装产物校验路径。

### Fixed (修复)

- 调整 controller 测试 suite,直接运行 `go test` 时使用本地
  envtest-asset fallback。
- 把 chart 的默认 image repository 对齐为
  `ghcr.io/keiailab/postgres-operator`。
- Helm RBAC 现在包含 `BackupJob` 资源权限。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
