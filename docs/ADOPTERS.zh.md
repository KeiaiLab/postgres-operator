<p align="center">
  <a href="ADOPTERS.md">English</a> |
  <a href="ADOPTERS.ko.md">한국어</a> |
  <a href="ADOPTERS.ja.md">日本語</a> |
  <b>中文</b>
</p>

# postgres-operator 采用者 (中文)

> 英文原文: [ADOPTERS.md](ADOPTERS.md) — canonical / 正本

本文档为在生产环境或评估环境中使用 `keiailab/postgres-operator` 的组织和项目的 *公开* 列表。欢迎自助登记 —— 请发起 PR 添加一行。

> 本 operator 当前处于 **0.4.0-beta** 阶段。生产部署前请先查阅 SLA 指南 (ROADMAP.md)。

## 生产用户

以 *production-grade SLA* 运行 postgres-operator 的用户。

| 用户 | 组件 | 使用模式 | 首次版本 | 当前版本 | 登记日期 |
|---|---|---|---|---|---|
| _暂无 (alpha 阶段)_ | — | 将在 G1 里程碑 (single-shard production) 之后添加。 | — | — | — |

## Evaluator (评估者)

在 PoC / evaluation / Day-0 环境中运行 operator 的用户。

| 用户 | 组件 | 阶段 | 登记日期 |
|---|---|---|---|
| **platform-data** ([keiailab](https://github.com/keiailab)) | PostgresCluster (single-shard, PG18) | Day-0 部署。PG18 failover smoke 通过,RTO 21 秒。HA replicas / backup-restore drill 尚未通过 —— 进入生产前需要 ROADMAP G1。 | 2026-05-07 |

## 如何添加自己

发起 PR 并在上表中添加一行:

```markdown
| **<组织 / 项目>** ([profile](<URL>)) | <组件 + 拓扑> | <阶段: Day-0 / G1 / G2 / G3> | <登记日期 YYYY-MM-DD> |
```

如希望以私密或匿名形式登记,请通过 SECURITY.md 渠道联系维护者,维护者会添加一行 *组织匿名化* 的记录。

## ROADMAP gate

进入生产的阶段遵循 ROADMAP.md 中定义的 G1–G4 里程碑:

- **G1** — Single-shard production (HA replica + failover drill + backup/restore/PITR + upgrade rollback)。
- **G2** — Native multi-shard (router + auto-split + cross-shard transactions)。
- **G3** — pgBackRest 集成 GA。
- **G4** — chaos-mesh 验证 + 多组织采用者。

## CNCF Sandbox 引用

本 ADOPTERS 列表同时作为公开引用以满足 CNCF graduation 标准 "≥ 1 个公开采用者 (或具有明确意向的评估者)"。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
