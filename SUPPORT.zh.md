<p align="center">
  <a href="SUPPORT.md">English</a> |
  <a href="SUPPORT.ko.md">한국어</a> |
  <a href="SUPPORT.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 支持 (中文)

> 英文原文: [SUPPORT.md](SUPPORT.md) — canonical / 正本

在使用 keiailab/postgres-operator 时遇到问题,请使用以下渠道。**请勿** 在此报告安全漏洞 —— 请遵循 [SECURITY.md](SECURITY.md) 中的私有流程。

## 从这里开始

- **README.md** —— quickstart 与核心 CRD 表面摘要。
- **docs/operator-guide/** —— 运行时运维 (`deployment.md`, `cross-validation-cnpg.md`, `ha-election.md`, `pooler-monitoring.md`)。
- **docs/releases/release-process.md** —— 发布与升级流程。
- **CHANGELOG.md** —— 各版本变更历史。

## 问题 / 讨论

- **GitHub Discussions**:
  https://github.com/keiailab/postgres-operator/discussions
  适合用法咨询、设计原理、运维场景以及 RFC 草拟。

## Bug 报告 / 功能请求

- **GitHub Issues**:
  https://github.com/keiailab/postgres-operator/issues
  请使用 `bug_report.yaml` / `feature_request.yaml` 模板。提供复现步骤、operator 版本、Kubernetes 版本、kind/云环境、`kubectl get postgrescluster -oyaml` 输出以及 operator-manager Pod 日志摘录,可以显著加快分诊速度。

## Pull Request

请遵循 [CONTRIBUTING.md](CONTRIBUTING.md):安装 lefthook、为提交进行 DCO 签署、在 PR 正文中附上通过本地 4-layer 网关的证据 (`pre-commit run --all-files`、`make test`、`make audit`)。PR 模板会引导你完成这些。

## 安全漏洞

通过 [SECURITY.md](SECURITY.md) 的私有渠道进行报告。请勿将漏洞描述写入公开 issue 或 discussion。

## 商业支持 / SLA

本项目为 Apache-2.0 开源项目,没有官方商业支持。若您需要针对生产集群的事件响应 SLA,可能需要单独的咨询合作 —— 请通过邮件 `support@keiailab.io` 联系。

## 响应预期

- Issues / Discussions 首次响应: **3 个工作日**。
- 安全报告首次响应: **48 小时** (依 SECURITY.md)。
- Pull-request 评审: 受维护者可用性影响,**5 个工作日**。

以上为维护者的尽力而为目标,并非 SLA。

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
