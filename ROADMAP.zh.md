<p align="center">
  <a href="ROADMAP.md">English</a> |
  <a href="ROADMAP.ko.md">한국어</a> |
  <a href="ROADMAP.ja.md">日本語</a> |
  <b>中文</b>
</p>

# ROADMAP — postgres-operator (中文)

> 英文原文: [ROADMAP.md](ROADMAP.md) — canonical / 正本

本 ROADMAP 通过可验证的 Gate 与 sub-task 清单跟踪进度 —— *并非日期承诺*。项目定位为 **Apache-2.0 PostgreSQL Kubernetes Operator**。我们的目标是达到 production-grade 运维质量,且不 fork / embed / wrap 任何外部 PostgreSQL operator 运行时。

## 复选框含义

| 标记 | 含义 |
|---|---|
| `[x]` | 代码 **与** 测试均已存在;e2e 或 unit 测试守护回归。 |
| `[~]` | 部分完成 —— 例如仅有 CRD 字段、helper 未接入、或缺少 e2e。 |
| `[ ]` | 未开始 (仅设计或 PoC)。 |

每个 sub-task 的 *Verify* 行会引用验证命令或 e2e 文件。

## 原则

- **外部系统不可随本产品出货** —— 外部 PostgreSQL operator、sharding extension、HA agent runtime、第三方 DB 后端不在 runtime artifact 范围内。
- **作为新服务实现** —— operator manager、instance manager、sharding 元数据、router、backup orchestration 全部在本仓库以 Apache-2.0 兼容依赖实现。
- **质量基线** —— HA / backup / restore / upgrade / observability / security UX 的 *目标水平* 独立于任何具体的第三方产品。

## 当前状态快照

| 项 | 状态 | Evidence |
|---|---|---|
| 项目 / chart 名称 | `postgres-operator` | GitHub repo、Helm chart 与 GitOps path 一致 |
| 许可证 | Apache-2.0 | `LICENSE`、ADR-0003 |
| 最新发布 | `0.3.0-alpha.18` | GHCR 镜像 + Helm chart 发布 + OLM bundle (community-operators PR pending) |
| OLM bundle | `bundle/manifests/` 与 8 CRD + alm-examples + CSV 描述一致 | `operator-sdk bundle validate --select-optional suite=operatorframework` 干净 (T26) |
| 声明式 DB 表面 | Pooler / PostgresDatabase / PostgresUser / ScheduledBackup / ImageCatalog / ClusterImageCatalog / externalClusters / replica cluster | T22 / T24 / T25 周期完成;live kind smoke 自动化 (T27) 进行中 |
| 本地 4-layer 网关 | L1 lefthook pre-commit + L2 pre-push + L3 make validate/audit + L4 PR evidence | ADR-0009 / RFC-0002;version-drift assertion 与 bundle validate 自动化 (T26) |
| production 部署 | Day-0 single-shard | `PostgresCluster/postgres` Ready |
| GHCR runtime 镜像 | 可公开 pull | `ghcr.io/keiailab/pg:18` 无需 pull secret 即可 restart |
| HA replicas | 部分 (仅有 `Replicas` 字段) | `api/v1alpha1/postgrescluster_types.go` |
| Backup / restore | 部分实现 | `BackupJob` phase transitions + `ScheduledBackup` CRD/controller + `RestorePIT` call path + pgBackRest command-runner plugin + K8s sidecar exec path。实际 restore drill 仍 pending。 |
| 1.0.0 GA | 尚未 | 仍需 HA / backup / chaos / soak |

## Gate 规划

### Gate G0 — Day-0 部署 (~100% buffer)

**目标**: 用户通过 GitOps 部署 operator + single-shard Postgres 集群。

- [x] CRD `PostgresCluster` 定义 —— `api/v1alpha1/postgrescluster_types.go` (RFC-0001 v2 schema)。
- [x] CRD `BackupJob` 定义 (Phase 1 spec) —— `api/v1alpha1/backupjob_types.go`。
- [x] `PostgresClusterReconciler` 构建期望状态 (ConfigMap / headless Service / StatefulSet) —— `internal/controller/postgrescluster_controller.go`。
- [x] Status phase 转换 (Provisioning → Ready) —— `internal/controller/status.go`、`aggregate_status.go`。
- [x] Pod readiness 跟踪 —— reconciler endpoint watch。
- [x] ArgoCD `Synced/Healthy` —— 在 production 验证 (`platform-data-postgres-operator`)。
- [x] GHCR 公开 pull —— `ghcr.io/keiailab/pg:18` 无需 pull secret 即可 restart。
- [x] Day-0 e2e —— `test/e2e/e2e_test.go`、`postgrescluster_e2e_test.go`。
- Verify: ArgoCD `Synced/Healthy` + Pod `1/1` Running + `psql -c 'select version()'`。

### Gate G1 — Single-shard production HA (~30% buffer)

**目标**: 可作为带 HA 的 single-PostgreSQL 生产数据库。

- [x] `Replicas` 字段 (0–15 async replica) —— `postgrescluster_types.go`。
- [x] STS 缩放映射 —— reconciler。
- [x] Primary-delete e2e baseline —— `test/e2e/failover_e2e_test.go`。
- [x] 自动 PDB 创建 —— `internal/controller/pdb.go`。
- [~] PVC fencing (split-brain fail-fast) —— 仅有 fencing skeleton;runbook 自动化 pending。
- [ ] **自动 failover 逻辑** —— 新目录 `internal/controller/failover/`。
  - [x] Primary 故障检测 —— `internal/controller/failover/detection.go` (`DetectPrimaryFailure` + `SelectPromotionCandidate`,纯函数,4 个 `FailureReason` enum,9 个 unit test,PR #38)。
  - [x] Standby 提升 (`pg_ctl promote` 或 logical-replication promotion) —— `internal/controller/failover/promotion.go` (`BuildPromotionPlan` + `Promoter` 接口 + `PromoteFromDecision` helper,4-step plan: RemoveStandbySignal / PgCtlPromote / WaitNotInRecovery / UpdateInstanceRole;6 个 unit test;PR #39)。`internal/controller/failover_promoter.go` 实现对 replica Pod 的 `postgres` 容器的 exec 与提升后的 `instance-status` annotation patch。
  - [x] Post-Ready primary-failure status 可见 —— `status.phase=Degraded` + `FailoverReady=False` + promotion-candidate 消息。
  - [x] Replica rejoin (`pg_basebackup` 或 `pg_rewind`) —— first-boot `pg_basebackup` + 已有 PGDATA old-primary marker 通用化 + 当前 primary endpoint main env + `pg_rewind` command-runner + HBA 正常连接认证 + fresh `pg_basebackup` fallback 全部完成。**Live A.1 basebackup drill PASS (T31, 2026-05-17, commits 09abbb5/dca3fa0)**: `quickstart-shard-0-1` standby PVC delete + in-pod PGDATA wipe + Pod kill → reconciler init container 运行 fresh `pg_basebackup` → `pg_stat_replication{application_name=quickstart-shard-0-1, state=streaming, sync_state=async, lag=0}` 恢复。包含 STS PVC retention `Retain` 规避 path 的 evidence。A.2 pg_rewind live drill 是另一个 task (SMOKE_FAILOVER operator-driven promotion live trigger 回归 —— 委托给 `docs/g1-ha-election-fact-fix` 领域)。
  - [x] Synchronous replication —— `spec.postgresql.synchronous.{method,number,dataDurability}` + CEL `number<=shards.replicas` + `ANY/FIRST N (...)` 渲染 + `required/preferred` 法定多数策略 + standby `application_name` 接线 + ConfigMap-hash 滚动 reconcile 全部完成。**Live B.1~B.3 RPO=0 drill PASS (T31, 2026-05-17, commit dca3fa0)**: 应用 `synchronous_standby_names='ANY 1 ("quickstart-shard-0-1","quickstart-shard-0-0")'` → `sync/quorum replica count=1` → 1000 行 commit 后 `commit_lsn=0/3DA43A0 / flush_lsn=0/3DA43A0` (`pg_wal_lsn_diff=0`) → **直接证明 RPO=0**。drill 函数: `hack/smoke.sh::drill_sync` (SMOKE_SYNC=1)。B.4 sync standby kill 场景为 opt-in (`SMOKE_SYNC_KILL=1`)。
  - [~] HA election 分布式锁 (K8s Lease) —— `internal/controller/failover/lease.go` (`FailoverLeaseName` + `LeaseConfig` + `NewLease`/`Run`/`IsLeader`,依据 §2 Simplicity 为 `internal/instance/election.Real` 的薄 adapter;使用 fake clientset 的 2 个 unit test 验证 single-leader + handoff)。Live e2e multi-replica failover drill 等待 cluster mesh restore。
- [ ] **Backup / restore 控制器实现** —— 加强 `internal/controller/backupjob_controller.go`。
  - [x] `BackupJob.Phase` 转换 (Pending → Running → Succeeded/Failed) —— `internal/controller/backupjob_controller.go` reconcile switch + 8 个 unit test。
  - [x] `ScheduledBackup` CRD / controller —— 6 字段 cron schedule → atomic `BackupJob` 创建;`suspend` / `immediate` / `ownerReference` / `concurrency` 守卫;5 个 unit test。
  - [x] `BackupJob.spec.type=restore` → `BackupPlugin.RestorePIT(targetTime)` call path + 必填 `targetTime` validation。
  - [x] `BackupJob.spec.executionMode=job` → owned `batch/v1.Job` 创建 + observe;`jobTemplate` 标准 env 注入。
  - [~] Plugin 调用 —— pgBackRest command-runner + sidecar 命令规划完成。WAL-G / Barman 仍 pending。
  - [x] Sidecar 模式分支 —— pgBackRest argv 通过 K8s `pods/exec` 投递到 ready primary Pod 的 `postgres` 容器。
- [~] **PITR restore** —— 由 `BackupRestoreSpec.TargetTime` 驱动的 pgBackRest `restore --type=time --target=...` call path + sidecar exec path 都已具备。实际 restore + checksum drill 仍 pending。
- [x] **Upgrade rollback runbook** —— `docs/runbooks/upgrade.md` (stub: pre-upgrade 检查 + ImageCatalog 步骤 + rollback) (PR #54)。
- [x] **RTO / RPO 测量 + 记录** —— `docs/runbooks/ha.md` (SLO RTO≤60s + RPO=0 + verify 步骤) (PR #54)。
- Verify: primary 删除后在 N 秒内 replica 被提升 + `pg_is_in_recovery()=false` + 0 数据损失;fresh-cluster restore 后数据 checksum 一致。

### Gate G2 — 运维质量 (~25% buffer)

**目标**: 覆盖 production-grade 运维表面。

- [x] `/metrics` baseline 暴露 (port 8443) —— `internal/controller/metrics.go`、`cmd/main.go`。
- [x] TLS path 设置 (证书挂载 + `ssl=on`) —— `internal/controller/builders.go:renderPostgresConf()`、`tls.go`。
- [x] Topology spread 集成 —— `internal/controller/topology_spread.go`。
- [x] PVC online resize —— `internal/controller/pvc_resize.go`。
- [x] Cascade-delete 守卫 —— `internal/controller/cascade_delete_test.go`。
- [~] cert-manager 集成 —— 仅 mount path;签发机制仍 TBD。
- [~] **自动 PrometheusRule 生成** —— Helm metrics Service / ServiceMonitor / PrometheusRule 渲染 + 真实 `postgres_operator_backupjob_phase` 指标驱动的 BackupJob failure alert。
  - [x] Replication-lag 警告 —— 实例状态 `LagBytes` → `postgres_operator_postgrescluster_replication_lag_bytes` + Helm `PostgresReplicationLagHigh`。
  - [x] Pooler failure / saturation 警告 —— `postgres_operator_pooler_phase{phase="Failed"}` + PgBouncer exporter 指标驱动的 collection-failure / client-waiting / max-wait 告警渲染验证。
  - [x] 磁盘压力 —— `kubelet_volume_stats_*` data-PVC alert。
  - [x] Backup 失败 —— `postgres_operator_backupjob_phase{phase="Failed"}`。
- [~] **Grafana dashboards** —— Helm dashboard ConfigMap 渲染完成 (`postgres-operator-cluster-overview.json`、`postgres-operator-pooler.json`);live Grafana 导入 / panel 验证仍 pending。
- [~] **Connection pooler (PgBouncer)** —— `Pooler` CRD + ConfigMap / Deployment / Service reconcile (first slice)。
  - [x] CRD `Pooler.spec.{cluster, instances, type, pgbouncer.poolMode, pgbouncer.parameters}` 添加。
  - [x] 单独的 PgBouncer Deployment / Service / ConfigMap 创建 + `userlist.txt` Secret fail-closed validation。
  - [x] 默认的 PgBouncer readiness / liveness / startup probe + exporter `/metrics` readiness / liveness probe。
  - [x] PgBouncer 参数 allowlist + operator-owned-key fail-closed validation。
  - [x] `instances > 1` 时自动 topology spread + PodDisruptionBudget。
  - [x] 更强的 rolling-update 默认值 —— `maxUnavailable=0`、`maxSurge=1`、`minReadySeconds=5`。
  - [x] Pooler 对齐表面 —— `deploymentStrategy`、`serviceAccountName`、status `backendTargets/configHash`。
  - [x] `pg_hba` → PgBouncer `pg_hba.conf` 渲染 + operator-owned 校验 `auth_type=hba` / `auth_hba_file`。
  - [x] 用户提供的 server / client TLS Secret 渲染 + Secret/key fail-closed validation。
  - [x] `type=ro` 渲染完整 ready-replica host-list + `server_round_robin=1` + `server_login_retry=2` 默认值。
  - [~] PgBouncer exporter —— 显式 sidecar + `metrics` ServicePort + PodMonitor selector 标签/样本 + 在 PgBouncer 指标前缀上的 PrometheusRule alert 渲染验证;live Prometheus scrape / Grafana 验证仍 pending。
  - [x] **内置 auth 用户自动化** (T27 ⑤) —— 当 `authSecretRef` 为空时,自动配置 `keiailab_pooler_pgbouncer` LOGIN role 与 `<pooler-name>-builtin-auth` Secret。
  - [x] **内置 auth 密码轮换** (T27 ⑥) —— `postgres.keiailab.io/rotate-pooler-password=true` annotation 触发 in-place `ALTER ROLE` + Secret 更新 + status timestamp;ConfigHash 包含 userlist,可自动 reload。
  - [ ] 内置 TLS 自动签发 (T29)。
  - [x] Paused PAUSE/RESUME reconciliation —— `spec.paused` → PgBouncer `SIGUSR1/SIGUSR2`、`status.paused`、Pod annotation audit。
  - [x] Pooler Service `psql` smoke —— 2026-05-12 在 kind 上 `SMOKE_POOLER=1 ./hack/smoke.sh --keep` 通过 (`quickstart` + Pooler Service `SELECT 1 = 1`,PAUSE 以 timeout 阻塞新客户端,RESUME 重新启用 `SELECT 1 = 1`,Deployment `2/2`)。
  - [x] In-place PgBouncer config reload —— 修补 `pgbouncer.parameters` 会等待 ConfigMap `config.sha256` 投影,向 ready Pod 发送 `SIGHUP`,并审计 Pod hash annotation,同时保留 Deployment generation 与 Pod 名称。
- [ ] **User / DB / RBAC 声明式**。
  - [~] CRD `PostgresDatabase` —— `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready-primary `psql` reconcile + `status.applied` + `databaseReclaimPolicy=delete` finalizer + database/schema 权限 grant/revoke 实现完成。Live smoke / retain-policy 验证仍 pending。
  - [~] CRD `PostgresUser` —— `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready-primary `psql` reconcile + `status.applied/passwordSecretResourceVersion` 实现完成;membership `REVOKE` + password Secret username 匹配 + `disablePassword` fail-closed + 引用 Secret 更新 watch + `PostgresCluster.status.managedRolesStatus` 聚合完成。Live smoke + password-rotation SQL 双向 round-trip 仍 pending。
  - [~] Role/permission reconcile —— `PostgresUser` role flag + membership `GRANT/REVOKE` + cluster 级 managed-role status (first slice) 完成;database 对象权限模型仍 pending。
- [ ] **Upgrade smoke** —— 扩展 `test/e2e/version_upgrade_e2e_test.go` (skeleton 已存在)。
- [ ] **安全默认值加固** —— restricted PSA、NetworkPolicy 默认开启。
- [~] **ImageCatalog / ClusterImageCatalog** —— CRD + `spec.imageCatalogRef.{apiGroup,kind,name,major}` + catalog 镜像 → StatefulSet init/main 容器镜像 + image-hash annotation rollout-drift 跟踪 + catalog watch / envtest 完成。Extension 镜像 volume mount、官方 digest catalog 供给、live rollout 测量仍 pending。
- [~] **Replica cluster / externalClusters** —— `externalClusters[].connectionParameters` + `password` + `sslKey/sslCert/sslRootCert` + `bootstrap.pg_basebackup.source` + `replica.enabled/source` 表面、streaming standalone replica bootstrap、ordinal-0 外部 `pg_basebackup`、`standby.signal`/`primary_conninfo`、password passfile + TLS client/root cert conninfo、persistent-follower election (阻塞 local promotion)、fail-closed status 全部验证。WAL-archive / object-store hybrid、distributed-topology demotion/promotion-token、live cross-cluster drill 仍 pending。
- [~] **声明式 hibernation** —— `postgres.keiailab.io/hibernation=on/off` annotation、shard StatefulSet/PVC-template 保留 + `replicas=0`、native router `replicas=0`、`status.phase=Hibernated`、hibernation condition 全部通过 envtest 验证。`SMOKE_HIBERNATION=1` 路径还会执行 PVC-marker-row 保留与 rehydration SQL round-trip drill;live kind 验证仍 pending。
- [~] **Release smoke test** —— `scripts/release-smoke-test.sh` 6-stage (与 mongodb sister 模式对齐 —— GH Release tag + GHCR manifest + GH Pages + helm index + helm pull/template + trivy post-publish scan)。修正 path (hack/→scripts/) + 修正 stage count "12" 的假设 (sister 标准 = 6)。
- Verify: PrometheusRule / Grafana dashboard 渲染、通过 Pooler Service 的 `psql` 访问、live PgBouncer exporter scrape、upgrade rolling restart 成功。

### Gate G3 — 自建 sharding 基础 (~0% buffer)

**目标**: 不依赖外部 sharding 运行时,自建 sharding 元数据。

- [x] `ShardingMode` 字段 (`none` / `native`) —— `postgrescluster_types.go`。Constants + Spec round-trip 由 `TestShardingMode` 守护 (`api/v1alpha1/postgrescluster_types_test.go`);通过 `+kubebuilder:validation:Enum=none;native` marker 在 apiserver 强制 enum validation。RFC 0001 §3.1 / RFC 0002。
- [x] `ShardsSpec` (初始 shard 数 / replica / storage) —— `postgrescluster_types.go`。字段 round-trip + `DeepCopy` 切片独立性 + `Replicas=0` (HA-off dev) 由 `TestShardsSpec` 守护 (`api/v1alpha1/postgrescluster_types_test.go`)。RFC 0001 §3.1。
- [x] Sharding plugin 接口 —— `internal/plugin/sharding/api.go`。编译期 interface freeze + `Registry` register/get/Names round-trip + `Capabilities` 公告 + `ErrUnsupported` sentinel 由 `TestShardingPlugin` umbrella 守护 (`internal/plugin/sharding/api_test.go`)。RFC 0001~0005 / RFC 0004 (router 架构)。
- [x] **`ShardRange` CRD** —— `api/v1alpha1/shardrange_types.go` + `config/crd/bases/postgres.keiailab.io_shardranges.yaml` (RFC 0002,offline yaml parse PASS,`make manifests` 通过)。
  - [~] Hash-range / list / range 策略分支 (vindex enum 定义完成,reconciler 未实现 —— 后续 sub-task)。
  - [ ] 元数据存储 (Postgres 系统目录或 sidecar)。
- [ ] **`pg-router` service PoC** —— 新增 `cmd/pg-router/`。
  - [ ] SQL parser (libpg_query 或自研)。
  - [ ] Shard-placement lookup。
  - [ ] Connection routing (libpq passthrough)。
- [ ] **手动 shard placement** —— `ShardRange.Spec.PlacementHints`。
- [ ] **GitOps drift guard** —— 检测 sharding 元数据与实际放置之间的偏差。
- Verify: 在 2-shard 集群上经 `pg-router` 的查询被路由到正确的 shard。

### Gate G4 — Online resharding (~0% buffer)

**目标**: 无数据丢失的 split / rebalance。

- [ ] **`ShardSplitJob` CRD** —— 新建 `api/v1alpha1/shardsplitjob_types.go`。
- [ ] **7-step e2e** 场景。
  - [ ] 1. Snapshot + WAL 捕获。
  - [ ] 2. 目标 shard bootstrap。
  - [ ] 3. Initial copy。
  - [ ] 4. CDC catch-up。
  - [ ] 5. Cutover (最小 write-block window)。
  - [ ] 6. Routing 更新。
  - [ ] 7. Source 清理。
- [ ] **Cutover rollback / forward-only** 验证。
- Verify: split 期间数据完整性 (checksum) + cutover-window 测量 + rollback 可行性。

### Gate G5 — Distributed SQL (~0% buffer)

**目标**: 明确界定 cross-shard 查询 / 事务支持。

- [~] **Scatter-gather** 查询路径 —— skeleton (`internal/router/scatter.go` + `ErrNotImplemented` sentinel,Executor 接口 freeze)。真实 wire-protocol 转发 + 合并在 P3+。Ref: RFC-0004 §2.2 Scenario 2 + ADR-0015。
- [~] **2PC / saga** 分布式事务选择 —— ADR-0015 决定 (2PC primary + saga deferred) + `internal/tx/` skeleton。真实实现在 D.2.2 Lease election 集成之后。
- [x] **Isolation matrix** 文档化 —— 哪些 isolation level 在哪些条件下成立。Evidence: `docs/sql/isolation-matrix.md` (D.10.3)。
- [~] **基准测试** —— sysbench / pgbench 变体 (`test/bench/pgbench.sh` + `sysbench.sh` + `docs/perf/baseline.md` skeleton;live 测量 pending)。
- Verify: 按 isolation level 的 anomaly / no-anomaly 表 + 基准测试数据。

### Gate G6 — 1.0.0 GA (~15% buffer)

**目标**: 商业级质量。

- [x] e2e baseline —— `test/e2e/`。
- [ ] **长时间 soak** —— ≥ 7 天,零停机。(NON-GOAL single session) (NON-GOAL for single session — 7-day wall clock required)
- [ ] **Chaos engineering** —— pod kill / network partition / disk pressure。(multi-day drill) (multi-day chaos drill required)
- [ ] **Restore 演练** —— 周期性自动 backup-restore + 验证。(monthly cron drill — out of single session)
- [ ] **Upgrade matrix** —— N → N+1 / N → N+2 / minor patches。(G2 D.6.3 依赖 —— substantial e2e)
- [ ] **SBOM + 签名** —— SPDX SBOM + cosign 签名。(可引入 commons sbom-attach.sh,P-C.7 sister)
- [ ] **Docs / runbook 完整**。
  - [ ] HA / backup / restore / upgrade / security / migration runbook。
- Verify: 7 天 soak 通过 + N 个 chaos 场景通过 + SBOM 附带 + 所有 runbook 存在。

## Non-goals (有意排除)

- ❌ 重新打包或 fork 外部 PostgreSQL operator。
- ❌ 将外部 sharding 扩展作为 first-class built-in (并非 runtime 依赖)。
- ❌ 通用 Plugin SDK 产品叙事 (已从 v0.x archive 中 retired)。
- ❌ **作为必需发布门的 GitHub Actions** —— 见 RFC 0002 (org-wide)。委托给本地 4-layer 网关。
- ❌ **基于日期的路线图截止时间** —— 见 org-wide `workflow.md`。
- ❌ 在验证之前将 HA / backup 特性宣传为 `production-ready`。

## Change log

| 日期 | 变更 |
|---|---|
| 2026-05-16 | G3 §Sharding foundation: 将 `ShardingMode` / `ShardsSpec` / `Sharding plugin interface` 连同 unit-test 覆盖一起从 `[~]` 翻转到 `[x]` (`TestShardingMode`、`TestShardsSpec`、`TestShardingPlugin`)。Plans `2026-05-14-4-operators-100pct/P-D` §D.7。 |
| 2026-05-12 | 关闭 backup/restore 差距: 增加 `ScheduledBackup` CRD/controller、cron 触发时的 `BackupJob` 创建、`BackupJob.spec.type=restore` → `RestorePIT` call path、`executionMode=job` runner Job 生命周期、pgBackRest command-runner plugin 注册以及 sidecar pod-exec path。 |
| 2026-05-12 | 关闭 observability 差距: 添加 Helm metrics Service / ServiceMonitor / PrometheusRule + `postgres_operator_backupjob_phase` Prometheus 指标。 |
| 2026-05-11 | G1 §Backup/Restore `BackupJob.Phase` 转换 (Pending → Running → Succeeded/Failed) 实现 + 8 unit test —— `[x]` (ralph-loop iter#3)。 |
| 2026-05-11 | 整体重写 —— 引入 Gate-scoped sub-task 清单、buffer 指标,移除所有 date-style 表述。 |
| 2026-05-07 | 发布 `0.3.0-alpha.3`,切换到公开 GHCR pull,移除历史 staging operator,并明确 "no embedded external systems" 原则。 |

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
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
