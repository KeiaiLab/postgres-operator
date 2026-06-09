<p align="center">
  <a href="CONTRIBUTING.md">English</a> |
  <a href="CONTRIBUTING.ko.md">한국어</a> |
  <a href="CONTRIBUTING.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 为 keiailab/postgres-operator 做贡献 (中文)

> 英文原文: [CONTRIBUTING.md](CONTRIBUTING.md) — canonical / 正本

感谢您的贡献! issue、discussion 和 PR 正文中欢迎使用英文与韩文;但项目文档本身以英文维护。

## 基本规则

1. **未带测试的功能不会合并。** 每个 PR 都必须包含 unit test 或 e2e test。
2. **DCO 签署必不可少。** 每个 commit 都必须带有 `Signed-off-by: Your Name <you@example.com>` trailer (使用 `git commit -s`)。
3. **MIT**: 通过贡献,您同意以本项目许可证授权自己的成果。
4. **Commit message 语言**: 韩文或英文均可;为了跨团队协作,推荐英文。

## 入门

### 前置条件

- Go 1.23+
- Docker (启用 buildx)
- kubectl、kind、kubebuilder v4
- make
- [lefthook](https://github.com/evilmartians/lefthook) (pre-commit hook 管理器)

### 本地开发

```bash
git clone https://github.com/keiailab/postgres-operator.git
cd postgres-operator
brew install lefthook    # 或: go install github.com/evilmartians/lefthook@latest
make hooks-install       # `lefthook install` 的封装 (pre-commit / commit-msg / pre-push)
make hooks-check         # 确认 hook 已正确接入 (强制 DCO + Conventional Commits)
make test                # envtest + 单元测试
make lint                # golangci-lint
make e2e                 # 基于 kind 的 e2e (5–10 分钟)
make build               # 构建 operator 二进制
make docker-build        # 构建容器镜像 (docker buildx 默认 builder)
```

## PR 工作流

1. **新功能请先开 issue** 与维护者对齐方向。简单的 bug 修复 / 文档微调可直接发 PR。
2. **分支命名**: `feat/<short>`、`fix/<short>`、`docs/<short>`、`refactor/<short>`。
3. **Commit message**: 推荐使用 [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`、`fix:`、`docs:`、`chore:`)。
4. **签署**: `git commit -s -m "feat: ..."`。
5. **PR 正文**: 按模板填写,并通过 `Closes #N` 链接相关 issue。
6. **本地网关必须为绿色**: `pre-commit run --all-files` 与 `make lint test validate` 都必须通过 (依据 RFC-0002,GitHub Actions 不可用)。
7. **评审**: CODEOWNERS 自动指派。常规变更需要 1 名维护者 LGTM;架构变更需要 2 名。

## 重大变更需 RFC

架构变更 —— 新增/变更 CRD、引入新 reconciler、修改安全模型、新增外部依赖 —— 必须先在 [`docs/rfcs/`](rfcs/) 中提交 RFC。

- 文件名: `NNNN-short-title.md`。
- 7 天评论窗口。
- 达成共识后,将状态切换为 `Accepted`,然后推进 PR。

## 测试策略

- **Unit test**: `internal/**/*_test.go`,适当处使用 envtest。
- **e2e**: `test/e2e/`,在 kind 集群上的 Ginkgo + chainsaw。
- **chaos**: `test/chaos/`,基于 chaos-mesh 的场景 (Phase 3+)。
- **bench**: `test/bench/`,使用 pgbench (Phase 6、8)。
- 行覆盖率 ≥ 80% (codecov 把关)。

## 代码风格

- `gofmt` / `goimports` (运行 `make fmt`)。
- `golangci-lint` 0 违规 (运行 `make lint`)。
- 注释应解释 **为什么**,而 **是什么** 应由命名传达 —— 名字应承载意图。
- 引入任何新的外部库/框架前,请通过官方文档或 [context7 MCP](https://github.com/upstash/context7) 再次确认当前 API。

## 安全漏洞

参见 [SECURITY.md](SECURITY.md),使用私有渠道。请勿将漏洞写入公开 issue。

## 行为准则

我们遵循 [Contributor Covenant v2.1](CODE_OF_CONDUCT.md)。

## 许可证

所有贡献以 [MIT](../LICENSE) 授权。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
