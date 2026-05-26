# CNPG → Keiailab Postgres Operator: Gap分析

> **语言**: [English](GAP-ANALYSIS.md) | [한국어](GAP-ANALYSIS.ko.md) | [日本語](GAP-ANALYSIS.ja.md) | [中文](GAP-ANALYSIS.zh.md)

## 概述

本文档比较keiailab生产集群中当前运行的CloudNativePG (CNPG) v1.29与
keiailab/postgres-operator v0.3.0-alpha.18的功能。
目标: 在生产环境中替换CNPG之前识别必须解决的功能差距。

**主要发现:**
- 16个核心组件中12个已完全实现
- 5个P0（必须）差距阻止生产替换
- 3个额外P1差距需要运营可靠性
- 预估: 8个Sprint
- 部署准备度: 7.5/10

---

## Gap矩阵

### P0 — 生产阻塞

| # | 差距 | CNPG等价 | 工作量 | Sprint |
|---|------|---------|--------|--------|
| 1 | WAL归档 + 对象存储 | barmanObjectStore | ✅ Done (#127) | S3 |
| 2 | 对象存储PITR | spec.backup.recovery | 1周 | S4 |
| 3 | TLS Phase 3 (挂载 + ssl=on) | 默认行为 | 3天 | S1 |
| 4 | postgresql.conf热重载 | pg_reload_conf() | ✅ Done (#126) | S2 |
| 5 | 备份保留清理 | retentionPolicy | ✅ Done (#130) | S5 |

### P1 — 运营可靠性

| # | 差距 | CNPG等价 | 工作量 | Sprint |
|---|------|---------|--------|--------|
| 6 | Switchover | cnpg promote | ✅ Done (#130) | S5 |
| 7 | Fencing | cnpg fencing | 3天 | S6 |
| 8 | 同步复制 | syncReplicas | 2天 | S6 |
| 9 | pg_hba.conf重载 | Config重载 | ✅ Done (#126) | S2 |
| 10 | 自定义PG参数 | spec.postgresql.parameters | ✅ Done (#126) | S2 |

---

## MVP定义

**替换CNPG最低条件:** P0 (5) + P1的6, 7, 10 = **共8项功能**

---

## 迁移路线图

| Sprint | 焦点 | E2E验证 |
|--------|------|---------|
| S1 | TLS Phase 3 | psql sslmode=verify-full成功 |
| S2 | Config热重载 | SHOW反映spec变更 |
| S3 | WAL归档 + 备份 | WAL段到达对象存储 |
| S4 | PITR恢复 | 时间点恢复 + 数据验证 |
| S5 | 保留 + Switchover | 清理执行 + primary切换 |
| S6 | Fencing + 同步复制 | Split-brain模拟通过 |
| S7 | 集成测试 | keiailab集群全e2e |
| S8 | CNPG替换 | 零停机迁移 |
