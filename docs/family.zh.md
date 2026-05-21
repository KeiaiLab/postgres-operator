<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# keiailab operator family

> 在共享基础之上构建的四个姊妹 Kubernetes operator —— `operator-commons` (Go 库) + Helm partials + Apache-2.0 技术栈。

您正在从 **`postgres-operator`** 仓库阅读本文档。本页是整个 family 的规范 cross-link 页面。

## Family 概览

| 项目 | 数据库 | 状态 | 仓库 |
|---|---|---|---|
| **`postgres-operator`** | PostgreSQL 18+ | active | https://github.com/keiailab/postgres-operator |
| **`mongodb-operator`** | MongoDB 7.0+ | active | https://github.com/keiailab/mongodb-operator |
| **`valkey-operator`** | Valkey 8.0+ (Redis fork, BSD-3) | active | https://github.com/keiailab/valkey-operator |
| **`operator-commons`** | 共享 Go 库 | v0.7.0 | https://github.com/keiailab/operator-commons |

## 共享内容

四个项目都汇聚于相同的运营原语:

- **Apache-2.0** 端到端 —— 无 SSPL、SaaS 表面无 copyleft
- **`operator-commons`** 共享 Go 库 (v0.7.0+) —— finalizer、labels、status sugars、security context builders、NetworkPolicy / ServiceMonitor partials
- **Helm chart skeleton** —— RFC-0027 `default` falsy-toggle 防护、RFC-0026 component-keyed values、cycle 26 hardening 6 markers (priorityClassName / lifecycle / SA / minReadySeconds / automount / revisionHistoryLimit)
- **OLM bundle parity** —— scorecard v1alpha3 6-test 矩阵
- **i18n** —— README + 11 个规范文档采用 English / 한국어 / 日本語 / 中文 (cleanup supercycle 2026-05-21 Wave 4)

## 不做的事

- ❌ **嵌入或包装上游 operator** (PGO, CloudNativePG, MongoDB Community Operator, Sentinel) —— license-clean,无 copyleft 义务
- ❌ **release gate 用 GitHub Actions** —— 本地 4-layer + GitLab CI L5 (RFC-0002, RFC-0043)
- ❌ **基于时间的 roadmap deadline** —— 功能检查列表 + 完成度 % (`standards/roadmap.md §1.1`)
- ❌ **Bitnami chart / image** —— registry deprecation 风险、Broadcom 收购 (ADR-0136 / ADR-0057)

## 起点

| 任务 | 入口 |
|---|---|
| 在 Kubernetes 上部署 `postgres-operator` | [README.md](../README.md) Quickstart 部分 |
| 阅读架构 | [ARCHITECTURE.md](../ARCHITECTURE.md) |
| 提交 issue 或功能请求 | https://github.com/keiailab/postgres-operator/issues |
| 讨论设计或 roadmap | https://github.com/keiailab/postgres-operator/discussions |
| 贡献代码 | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| 报告安全 issue | [SECURITY.md](../SECURITY.md) |
| 学习品牌 / 风格 | [BRANDING.md](../BRANDING.md) |
| 追踪 adopters / 使用案例 | [ADOPTERS.md](../ADOPTERS.md) |
| 查找维护者 | [MAINTAINERS.md](../MAINTAINERS.md) |
| 审查 governance 模型 | [GOVERNANCE.md](../GOVERNANCE.md) |
| 检查即将到来的工作 | [ROADMAP.md](../ROADMAP.md) |

## Cross-family 兼容性 (operator-commons)

三个数据库 operator 全部以相同版本 (当前 `v0.7.0+`) 导入 `github.com/keiailab/operator-commons`:

```go
import (
    "github.com/keiailab/operator-commons/pkg/version"
    "github.com/keiailab/operator-commons/pkg/security"
    "github.com/keiailab/operator-commons/pkg/labels"
    "github.com/keiailab/operator-commons/pkg/monitoring"
    "github.com/keiailab/operator-commons/pkg/finalizer"
    "github.com/keiailab/operator-commons/pkg/status"
)
```

`operator-commons` 中的 breaking change 需要在所有三个数据库 operator 中同步 bump —— 由 supercycle Wave 5 的 `make cross-validation` target 验证。

## i18n

本页 (以及所有规范项目文档) 提供四种语言:

- [English](family.md) (canonical)
- [한국어](family.ko.md)
- [日本語](family.ja.md)
- **中文** (本 file)

技术内容以英文版为权威源,各语言版本以 native 表达反映相同决定。

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
