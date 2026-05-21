<p align="center">
  <a href="MAINTAINERS.md">English</a> |
  <a href="MAINTAINERS.ko.md">한국어</a> |
  <a href="MAINTAINERS.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 维护者 (中文)

> 英文原文: [MAINTAINERS.md](MAINTAINERS.md) — canonical / 正本

本文档跟踪 keiailab/postgres-operator 的维护者列表。维护者拥有本项目的决策权和合并权限。

## 当前维护者

| 姓名 / 团队 | GitHub | 角色 | 领域 |
|---|---|---|---|
| keiailab maintainers | [@keiailab/maintainers](https://github.com/orgs/keiailab/teams/maintainers) | Lead | 整个项目 |

GitHub team `@keiailab/maintainers` 在所有领域均拥有合并和审批权限。个人维护者将按照以下流程添加。

## 维护者资格

满足以下条件 *至少 6 个月* 的贡献者可被提名为维护者:

- ≥ 20 个已合并的 PR (有实质性的代码或文档贡献)。
- ≥ 30 个 PR 评审 (含建设性反馈)。
- 遵守本项目的 [GOVERNANCE.md](GOVERNANCE.md) 与 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- 对至少一个核心领域 (controller、instance manager、backup、sharding、文档等) 具备深入理解。

## 添加维护者

1. 由现任维护者或候选人本人发起提案 (issue 或 RFC)。
2. `@keiailab/maintainers` 团队在 7 天评论窗口内应用 lazy consensus。
3. 若无异议,将新维护者加入 GitHub team,并通过 PR 更新 MAINTAINERS.md。

## 非活跃维护者

连续 6 个月不活跃的维护者将转入 emeritus 状态 (撤销权限,姓名保留在 emeritus 名册中)。回归流程与新增维护者相同。

## 领域所有权 (与 CODEOWNERS 一致)

参见 `/.github/CODEOWNERS`。每个目录的自动审查者由该文件指定。

## Emeritus

(暂无)

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
