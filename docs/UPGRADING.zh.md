<p align="center">
  <a href="UPGRADING.md">English</a> |
  <a href="UPGRADING.ko.md">한국어</a> |
  <a href="UPGRADING.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 升级 postgres-operator (中文)

> 英文原文: [UPGRADING.md](UPGRADING.md) — canonical / 正本

本文档汇总在 postgres-operator 进行 minor/major 版本升级时所需的迁移操作。Helm 用户只需升级 chart 即可应用所有变更;但静态 manifest (`kubectl apply -f`) 用户需手动 patch RBAC 等部分项目。

## 0. 版本策略 (semver)

| 变更类型 | semver bump | 示例 |
|---|---|---|
| 新增 controller / CR / API | minor (v1.X → v1.X+1) | 新增 PostgresPooler |
| 修改既有 API 签名 (breaking) | major (v1.X → v2.0) | 修改 PostgresCluster.spec.storage 结构 |
| bug 修复 / 依赖 bump | patch (v1.X.Y → v1.X.Y+1) | controller-runtime 0.19→0.20 |
| operator-commons 依赖 bump | minor (commons v0.X → v0.X+1) | 新引入 pkg/reconcile |

## 1. v0.1.x → v0.2.x

### Helm 用户

```bash
helm repo update
helm upgrade postgres-operator <repo>/postgres-operator \
  --namespace postgres-operator-system \
  --version 0.2.x
```

chart 本身会同步 RBAC、CRD、Deployment。无需额外操作。

### 静态 manifest 用户 —— RBAC 迁移

查看 `make build-installer` 产物 `dist/install.yaml` 的差异:

```bash
kubectl diff -f dist/install.yaml
kubectl apply -f dist/install.yaml
```

既有 ClusterRole 的新增权限 (本 minor 暂无 —— 此次 minor 无 RBAC 变更):

| API group | Resource | 原因 | 加入时间 |
|---|---|---|---|
| (无) | — | — | — |

## 2. v0.2.x → v0.3.x (计划中)

### 采纳 operator-commons v0.9.0 (Sprint 1 + S5)

```bash
# 在 go.mod 中 bump operator-commons 依赖之后
go mod tidy
```

- **新引入 import**: `github.com/keiailab/operator-commons/pkg/pvc`、`pkg/topology` (Sprint 1)
- **计划新增 import**: `pkg/reconcile`、`pkg/resources` (S5 后续)
- **去除重复代码**: 用 operator-commons 的 helper 替换 `internal/controller/` 自有 helper。行为不变。

迁移影响:
- Reconcile 行为不变 (仅重构,无外部行为变化)
- CRD spec 无变更 (v1alpha2 conversion 为另一个 cycle)
- 不影响 Helm chart

## 3. v0.3.x → v1.0.0 (计划中 —— 商用产品级别里程碑)

到达 CLAUDE.md §7 的 *商用产品级别* (P0+P1+P2+OP+C 全部 ✅) 时执行。

- 将所有 CR 的 API stability 升级到 `Stable` (v1)
- 无 breaking change (v0.x → v1.0 仅是 *命名* 变更)
- 通过 `make audit-quality` 验证商用产品级别质量

## 4. GHA dual-track 策略 (ADR-0019)

本 repo 属于 RFC-0002 (GitHub Actions 永久禁止) 的 *例外* —— 公开 OSS operator 的 external trust gate 需要 GHA 14 workflow,与本地 4 层 (lefthook) 形成 dual-track 运营 (ADR-0019)。

升级时的 GHA workflow 变更将由 `dependabot/github_actions/*` PR 自动完成。*人工 PR* 若要在 `.github/workflows/` 新增文件,需 *另起 ADR* 并获得用户批准。

## 5. 通用迁移清单

升级前:
- [ ] CRD 变更 (`api/v1alpha1/` 的 ObjectMeta 与 v1alpha2 兼容)
- [ ] `make verify` (lint + test + build + audit) 通过
- [ ] 既有 e2e 套件 PASS (`make integration-test`)
- [ ] 确认已整合 dependabot 依赖 bump PR

升级后:
- [ ] 更新 Helm chart 的 `dependencies:` (keiailab-commons library chart)
- [ ] 验证各 CR 的 spec 兼容性 (特别是 storage、resources)
- [ ] 验证 reconcile 结果 (`kubectl get postgrescluster -A`)
- [ ] 运营指标 (`Reconcile{Total,Latency,Errors}`) 正常

## 6. 不兼容变更通告策略

- **Deprecation**: 在新 minor 中加 `// Deprecated:` 注释,2 个 minor 后移除
- **Breaking**: major bump + 本 UPGRADING.md 的专用章节 + 撰写 ADR
- **不做事后通告**: 任何 breaking 变更需 *至少提前 1 个 minor* 完成 deprecation

## 参考

- ADR 列表: `docs/kb/adr/INDEX.md`

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
