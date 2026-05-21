<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator (简体中文)

> **基于 Apache-2.0 许可证的 Kubernetes PostgreSQL Operator —— 原生 PG18+,许可证干净,K8s 原生自动分片路线图**

> English README: [README.md](README.md) — canonical / 正本

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version"/></a>
  <a href="https://www.postgresql.org/"><img src="https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql" alt="PostgreSQL"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes" alt="Kubernetes"/></a>
  <a href="https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator"><img src="https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github" alt="Container Image"/></a>
  <a href="https://keiailab.github.io/postgres-operator"><img src="https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v" alt="Helm Chart"/></a>
  <a href="https://artifacthub.io/packages/helm/keiailab-postgres-operator/postgres-operator"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/keiailab-postgres-operator" alt="Artifact Hub"/></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/keiailab/postgres-operator"><img src="https://api.scorecard.dev/projects/github.com/keiailab/postgres-operator/badge" alt="OpenSSF Scorecard"/></a>
  <a href="https://github.com/keiailab/postgres-operator/discussions"><img src="https://img.shields.io/github/discussions/keiailab/postgres-operator?label=discussions&logo=github" alt="GitHub Discussions"/></a>
  <a href="https://github.com/keiailab/operator-commons/blob/main/docs/quality/audit-history.md"><img src="https://img.shields.io/badge/keiailab-v3.x--stable-success?style=flat-square" alt="keiailab v3.x-stable"/></a>
  <a href="https://github.com/keiailab/operator-commons/blob/main/scripts/audit-production-grade.sh"><img src="https://img.shields.io/badge/audit-100%25-success?style=flat-square" alt="audit"/></a>
</p>

<p align="center">
  <a href="README.md">English</a> |
  <a href="README.ko.md">한국어</a> |
  <a href="README.ja.md">日本語</a> |
  <b>中文</b>
</p>

---

## Identity (项目定位)

本 Operator 在 PostgreSQL 之上构建一个 *自研的分布式 SQL 层*。我们参考 PGO、Citus、Vitess、CloudNativePG、Patroni 和 CockroachDB 的公开设计与运维范式,但不嵌入或封装上述任何系统作为运行时依赖。代码、CRD、reconciler、实例管理器(instance manager)和路由器(router)全部在本仓库内直接实现,均遵循与 Apache-2.0 兼容的条款。

差异化亮点:

- **100% 兼容 PostgreSQL 18+** —— 应用代码无需修改即可采用分布式架构。所有 PG 扩展 / 类型 / 函数保持可用。
- **许可证干净** —— Apache-2.0 Operator 加上仅 BSD/Apache/MIT/PG-License 依赖。SaaS 暴露时无 copyleft 义务。
- **K8s 原生自动分片路线图** —— `ShardRange` CRD 作为唯一真实源(source of truth),KEDA 驱动的自动分裂,7 步在线重分片(cutover SLA 目标 p99 < 500 ms)。
- **单一接入端点路线图** —— 应用通过 PostgreSQL wire protocol 连接到 `pg-router` Deployment,无需感知分片细节。

PGO 等级是一个 *质量基准* —— 而不是 PGO 的 fork 或 PGO 嵌入。Citus 等级的分布式同样不意味着发布 Citus 扩展;而是重新实现 Citus 已验证的、作为 PostgreSQL 兼容新服务的问题域。来自 v0.x 归档的 Plugin SDK 信息已废弃;当前方向是范围窄、清晰的内部模块和显式的 CRD。

ADR 0001(`docs/kb/adr/0001-self-built-distributed-sql.md`)是该决策的基石。

## Architecture (架构概览)

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

详细信息:[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## Features (功能)

### 当前发布版本(v0.3.0-alpha.18)

Helm chart 和 OperatorHub bundle 包含 **8 个自有 CRD**。CRD 状态反映了当前在生产集群上已 reconcile 的内容:

| CRD | 作用 | 状态 |
|---|---|---|
| `PostgresCluster` | 分片感知的拓扑(primary + standby + 原生分片路线图) | ✅ 可部署 |
| `BackupJob` | 原子备份/恢复 Job(pgBackRest 插件) | ⚠️ 控制器部分实现 |
| `ScheduledBackup` | Cron 驱动的 BackupJob 生成(6 字段调度) | ⚠️ 控制器部分实现 |
| `Pooler` | PgBouncer 连接池层(兼容 CNPG 表面) | ⚠️ 控制器部分实现 |
| `PostgresDatabase` | 声明式数据库/schema/扩展/FDW(ready-primary psql) | ⚠️ 控制器部分实现 |
| `PostgresUser` | 声明式角色 + 密码 + 成员关系(ready-primary psql) | ⚠️ 控制器部分实现 |
| `ImageCatalog` | 命名空间级 PostgreSQL 运行时镜像目录(兼容 CNPG) | ⚠️ rollout 路径 |
| `ClusterImageCatalog` | 集群级共享 PostgreSQL 运行时镜像目录 | ⚠️ rollout 路径 |

Helm chart 额外提供:PrometheusRule + Grafana 仪表盘(Pooler overview + Cluster overview)、受限 PSA SecurityContext、默认拒绝(deny-by-default)的 NetworkPolicy、cert-manager TLS 集成、OpenTelemetry-ready 钩子。

### Roadmap(阶段规划)

| 阶段 | 版本 | 关键交付物 |
|---|---|---|
| **P0** | 0.3.0 | 重新设计重置(ADR/RFC 0001–0014、ARCHITECTURE.md、runbook 骨架) |
| **P1** | 0.4.0 | 单分片生产可用(HA / 备份 / PITR 演练 / Lease 选举) |
| **P2** | 0.5.0 | pg-router + `ShardRange` CRD(手动多分片运维) |
| **P3** | 0.6.0 | vindex 扩展 + scatter-gather + 只读副本自动扩缩 |
| **P4** | 0.7.0 | `ShardSplitJob` 7 步(手动在线分裂触发) |
| **P5** | 0.8.0 | KEDA 自动分裂 + rebalancer(达成自动分片) |
| **P6** | 0.9.0 | 分布式事务(2PC + saga) + 跨分片 JOIN |
| **P7** | **1.0.0** | 稳定化 + chaos / benchmark + Artifact Hub 验证 |

完整的阶段细节(子任务、SLO、ADR/RFC 引用):[`docs/roadmap.md`](docs/roadmap.md) 和 [`ROADMAP.md`](ROADMAP.md)。

## License policy (许可证策略 — ADR 0003)

只有在 *全部* 满足以下条件时,才允许引入外部 OSS 依赖:
- 许可证:BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API:v1+ 稳定性承诺(12 个月废弃策略)

**永久禁止**:AGPLv3 / BUSL / CSL / SSPL。

自动化检查:`scripts/check-license-policy.sh`(P0 后续;接入为 lefthook L2 pre-push hook 和 `go-licenses.yml` GitHub Actions 检查)。

## Quickstart (快速开始)

```bash
# 1. 安装 Operator 和 8 个 CRD(helm chart 或 OperatorHub bundle)
helm install pgo charts/postgres-operator

# 2. 应用 quickstart PostgresCluster
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml

# 3. 等待 Ready
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m

# 4. (可选)应用声明式数据库/用户资源
kubectl apply -f config/samples/postgres_v1alpha1_postgresdatabase.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresuser.yaml

# 5. (可选)应用 PgBouncer Pooler 和定时备份
kubectl apply -f config/samples/postgres_v1alpha1_pooler.yaml
kubectl apply -f config/samples/postgres_v1alpha1_scheduledbackup.yaml

# 6. 启用监控(需要 prometheus-operator)
helm upgrade pgo charts/postgres-operator \
  --reuse-values \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

请参考 [`docs/operator-guide/deployment.md`](docs/operator-guide/deployment.md) 和 [`docs/operator-guide/pooler-monitoring.md`](docs/operator-guide/pooler-monitoring.md) 了解运维 playbook。

## Production readiness (生产就绪)

**当前状态(0.3.0-alpha.18,2026-05-12)**:在生产 Kubernetes 集群上,ArgoCD Application `platform-data-postgres-operator` 处于 `Synced/Healthy` 状态,`PostgresCluster/postgres` 报告 `Ready=True`。与 CloudNativePG 的交叉验证记录于 [`docs/operator-guide/cross-validation-cnpg.md`](docs/operator-guide/cross-validation-cnpg.md)。

距离 GA 的差距:
- **P1** —— 生产就绪的单分片需要 HA Lease 分布式锁控制器、BackupJob/ScheduledBackup 实战演练、PITR 校验和演练以及 chaos-mesh 故障切换套件。相关子任务跟踪于 `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md`。
- **P2** —— 多分片需要 `ShardRange` CRD + pg-router PoC([`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md))。
- 当前 alpha 版本 **不** 建议在没有您自行进行备份/恢复验证的情况下用于生产数据。

## Known limitations (已知限制)

- BackupJob / ScheduledBackup / Pooler / PostgresDatabase / PostgresUser 控制器为 *部分实现* —— CRD 表面已发布并 reconcile 核心路径,但实战演练验证(rotation / PITR / retain-policy)仍待完成,按阶段跟踪。
- ImageCatalog / ClusterImageCatalog rollout-drift 测量在 StatefulSet 注解层实现;生产 rollout SLA 尚未认证。
- Sharding 子系统(`ShardRange`、`pg-router`、`ShardSplitJob`)目前 **仅设计** —— 请参考 [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md) 了解规范;暂无运行时代码。
- 上述 Phase 路线图意味着多年的时间跨度 —— 当前运维范围仅限单分片 HA。

## Uninstall (卸载)

```bash
# 1. 先删除 CR 实例(否则 finalizers 会阻止 CRD 删除)
kubectl delete postgrescluster --all -A
kubectl delete pooler --all -A
kubectl delete scheduledbackup --all -A

# 2. 卸载 chart
helm uninstall pgo

# 3. 删除 CRD(可选;helm 默认保留 CRD 以保护集群状态)
kubectl delete crd postgresclusters.postgres.keiailab.com \
                  backupjobs.postgres.keiailab.com \
                  scheduledbackups.postgres.keiailab.com \
                  poolers.postgres.keiailab.com \
                  postgresdatabases.postgres.keiailab.com \
                  postgresusers.postgres.keiailab.com \
                  imagecatalogs.postgres.keiailab.com \
                  clusterimagecatalogs.postgres.keiailab.com
```

## Contributing (贡献)

```bash
make lint test validate    # 本地 4-layer L3 gate
make sync-crds              # 验证 config/crd/bases ↔ chart 同步
make test-e2e PILLAR=p1     # Kind 集群 e2e
```

GitHub Actions 运行 OSS 标准套件(CI / scorecard / CodeQL / DCO / dependency-review / go-licenses / kube-linter / helm-install-test / stale)。本地 pre-commit / pre-push hooks 仍是主要的开发者闸门;CI 是收敛检查。

请参考 [`CONTRIBUTING.md`](CONTRIBUTING.md) 了解贡献者指南、[`GOVERNANCE.md`](GOVERNANCE.md) 了解治理模型(lazy consensus / 2/3 supermajority),以及 [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) 了解行为准则。

## Documentation (文档)

- [`ARCHITECTURE.md`](ARCHITECTURE.md) —— 单页架构描述(8 个 CRD 表面 + 自研分布式 SQL + G0-G6 状态 + ADR 交叉链接)
- `docs/kb/adr/` —— 架构决策记录(当前:0001–0014;归档位于 `_archive/v0.x/`)
- `docs/rfcs/` —— RFC 草案(当前:0001–0007)
- `docs/operator-guide/` —— 部署 / pooler-monitoring / cross-validation-cnpg / community-operators-onboarding / HA
- `docs/runbooks/` —— 运维流程:ha / backup / restore / upgrade / security / migration(每一项都附带 SLO 目标 + 验证命令)
- `docs/sharding/` —— Sharding 架构规范(G3-G5)
- `docs/api-reference/` —— CRD 参考(自动生成,规划中)
- `docs/tutorials/` —— 分步用户指南(P1+ 规划中)

## Reporting vulnerabilities (漏洞报告)

请 **不要** 为安全报告开启公开 issue。请按照 [`SECURITY.md`](SECURITY.md) 通过 GitHub Security Advisory 私密渠道反馈。我们将在 5 个工作日内响应,并为高危发现协调披露时间线。

## Community (社区)

- **Discussions**:[GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) —— 使用问题、功能想法和运维实战分享。
- **Issues**:[GitHub Issues](https://github.com/keiailab/postgres-operator/issues) —— bug 报告和功能请求(请提交可复现的案例;`question.yml` 模板可指导问答)。
- **Governance**:[`GOVERNANCE.md`](GOVERNANCE.md) —— 决策流程(lazy consensus / 2/3 supermajority)。
- **Sponsorship**:请参考 [`.github/FUNDING.yml`](.github/FUNDING.yml) 了解 GitHub Sponsors 按钮。

## License (许可证)

Apache-2.0。请参考 [`LICENSE`](LICENSE) 文件。

## Maintainer (维护者)

[@phil](https://github.com/phil) —— `eightynine01@gmail.com`。维护者名单:[`MAINTAINERS.md`](MAINTAINERS.md)。

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
