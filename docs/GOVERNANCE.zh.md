<p align="center">
  <a href="GOVERNANCE.md">English</a> |
  <a href="GOVERNANCE.ko.md">한국어</a> |
  <a href="GOVERNANCE.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 治理 (中文)

> 英文原文: [GOVERNANCE.md](GOVERNANCE.md) — canonical / 正本

本文档定义 keiailab/postgres-operator 项目的决策方式。

## 原则

1. **开放 (Open)**: 所有决策都在公共渠道 (GitHub issues / PRs / RFCs) 上进行。
2. **Lazy consensus**: 常规变更在无人反对时即可推进。
3. **Explicit consensus**: 架构变更、CRD 变更、安全模型变更和许可证变更需要 RFC 以及维护者的 **2/3 supermajority** 通过。更小范围的 RFC (单一组件 / 工具采用 / 策略强化) 仅需 **简单多数 (>50%)**。GOVERNANCE 自身的修订 (见下文 "Amendments") 始终需要 2/3 supermajority。
4. **共同责任 (Shared accountability)**: 维护者对代码质量、用户安全和社区健康共同负责。

## 决策类别

### Routine 变更 (lazy consensus)

- 错误修复、文档改进、增加测试、依赖 minor/patch 升级,以及不修改 public API 的重构。
- 流程: PR → 至少 1 名维护者 LGTM → 合并。
- Window: 不强制 (本地网关通过后即可合并;依据 RFC-0002 不使用 GitHub Actions —— pre-commit / pre-push 钩子与 Makefile 充当网关)。

### Medium 变更 (explicit consensus)

- 新增 CRD 字段、新增 reconciler、依赖 major 升级、public API 变更。
- 流程: 通过 issue 提议 → 7 天评论窗口 → 维护者多数 LGTM → 合并。
- 任何 1 个反对意见都会升级至维护者会议。

### Architectural 变更 (必须 RFC)

- 引入新组件、修改安全模型、变更许可证,或任何向后不兼容的变更。
- 流程:
  1. 在 `docs/rfcs/NNNN-title.md` 提交 RFC。
  2. 14 天评论窗口。
  3. 2/3 以上的维护者赞成。
  4. 将 RFC 状态从 `Draft → Accepted`,然后开启实现 PR。
- 被拒绝的 RFC 保留 `Rejected` 状态 (保存历史上下文)。

## 角色

### Contributor

任何人。可提交 PR 和 issue。

### Reviewer

经常进行代码评审的 contributor。可能被加入 CODEOWNERS。无合并权限。

### Maintainer

参见 [MAINTAINERS.md](MAINTAINERS.md)。持有合并 / 批准权。

### Lead maintainer

keiailab 组织代表。对许可证、治理和安全策略具有最终决策权。

## 维护者会议

- 月度节奏 (必要时召开临时会议)。
- 会议纪要发布在 `docs/meetings/` 下。
- 议程: 争议解决、RFC 讨论、路线图评审。

## 争议解决

1. 先在 PR/issue 评论中讨论。
2. 未解决则加入维护者会议议程。
3. 由维护者多数表决决定。
4. 票数相同时,由 lead maintainer 决断。

## 许可证 / 知识产权

- 所有贡献均以 MIT 授权。
- DCO 签署必不可少。
- 许可证变更需要所有贡献者一致同意 (实际上不可变)。

## Amendments (修订)

本文档仅在获得维护者 2/3 supermajority 同意时方可修订。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
