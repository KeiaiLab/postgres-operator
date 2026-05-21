<p align="center">
  <a href="AGENTS.md">English</a> |
  <a href="AGENTS.ko.md">한국어</a> |
  <a href="AGENTS.ja.md">日本語</a> |
  <b>中文</b>
</p>

# postgres-operator - AI Agent 指南 (中文)

> 英文原文: [AGENTS.md](AGENTS.md) — canonical / 正本

## 项目结构

**Single-group 布局 (默认):**
```
cmd/main.go                    Manager 入口 (注册 controller/webhook)
api/<version>/*_types.go       CRD schema (+kubebuilder 标记)
api/<version>/zz_generated.*   自动生成 (请勿编辑)
internal/controller/*          Reconciliation 逻辑
internal/webhook/*             Validation/defaulting (如有)
config/crd/bases/*             生成的 CRD (请勿编辑)
config/rbac/role.yaml          生成的 RBAC (请勿编辑)
config/samples/*               示例 CR (编辑这里)
Makefile                       Build/test/deploy 命令
PROJECT                        Kubebuilder 元数据 自动生成 (请勿编辑)
```

**Multi-group 布局** (适用于具有多个 API group 的项目):
```
api/<group>/<version>/*_types.go       按 group 划分的 CRD schema
internal/controller/<group>/*          按 group 划分的 controller
internal/webhook/<group>/<version>/*   按 group 与 version 划分的 webhook (如有)
```

Multi-group 布局按 group 名 (例如 `batch`、`apps`) 组织 API。请检查 `PROJECT` 文件中是否有 `multigroup: true`。

**转换为 multi-group 布局:**
1. 运行: `kubebuilder edit --multigroup=true`
2. 移动 API: `mkdir -p api/<group> && mv api/<version> api/<group>/`
3. 移动 controller: `mkdir -p internal/controller/<group> && mv internal/controller/*.go internal/controller/<group>/`
4. 移动 webhook (如有): `mkdir -p internal/webhook/<group> && mv internal/webhook/<version> internal/webhook/<group>/`
5. 更新所有文件中的 import 路径
6. 修正 `PROJECT` 中每个 resource 的 `path`
7. 更新测试套件的 CRD 路径 (相对路径多加一个 `..`)

## 关键规则

### 切勿编辑这些文件 (自动生成)
- `config/crd/bases/*.yaml` —— 由 `make manifests` 生成
- `config/rbac/role.yaml` —— 由 `make manifests` 生成
- `config/webhook/manifests.yaml` —— 由 `make manifests` 生成
- `**/zz_generated.*.go` —— 由 `make generate` 生成
- `PROJECT` —— 由 `kubebuilder [OPTIONS]` 生成

### 切勿删除 Scaffold 标记
请勿删除 `// +kubebuilder:scaffold:*` 注释。CLI 会在这些标记处注入代码。

### 保持项目结构
请勿移动文件。CLI 期望文件位于特定位置。

### 始终使用 CLI 命令
使用 `kubebuilder create api` 和 `kubebuilder create webhook` 来 scaffold。切勿手动创建文件。

### E2E 测试需要隔离的 Kind 集群
e2e 测试旨在隔离环境 (类似 GitHub Actions CI) 中验证方案。请在专用的 [Kind](https://kind.sigs.k8s.io/) 集群 (而非真实 dev/prod 集群) 中运行。

## 变更后的操作

**编辑 `*_types.go` 或标记后:**
```
make manifests  # 从标记重新生成 CRD/RBAC
make generate   # 重新生成 DeepCopy 方法
```

**编辑 `*.go` 文件后:**
```
make lint-fix   # 自动修复代码风格
make test       # 运行单元测试
```

## CLI 命令速查

### 创建 API (自己的类型)
```bash
kubebuilder create api --group <group> --version <version> --kind <Kind>
```

### Deploy Image Plugin (为任意容器镜像生成部署/管理的 scaffold)

为部署/管理任意容器镜像 (nginx、redis、memcached、自有应用等) 的 controller 进行生成:

```bash
# 例: 部署 memcached
kubebuilder create api --group example.com --version v1alpha1 --kind Memcached \
  --image=memcached:alpine \
  --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

scaffolds 良好实践的代码: reconciliation 逻辑、status condition、finalizer、RBAC。可作为参考实现。


### 创建 Webhook
```bash
# Validation + defaulting
kubebuilder create webhook --group <group> --version <version> --kind <Kind> \
  --defaulting --programmatic-validation

# Conversion webhook (用于多版本 API)
kubebuilder create webhook --group <group> --version v1 --kind <Kind> \
  --conversion --spoke v2
```

### 监听核心 Kubernetes 类型的 Controller
```bash
# 监听 Pod
kubebuilder create api --group core --version v1 --kind Pod \
  --controller=true --resource=false

# 监听 Deployment
kubebuilder create api --group apps --version v1 --kind Deployment \
  --controller=true --resource=false
```

### 监听外部类型的 Controller (例如来自其他 operator)

监听外部 API (cert-manager、Argo CD、Istio 等) 的资源:

```bash
# 例: 监听 cert-manager Certificate 资源
kubebuilder create api \
  --group cert-manager --version v1 --kind Certificate \
  --controller=true --resource=false \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

**提示:** 仅在需要特定版本时使用 `--external-api-module=<module>@<version>`。否则省略 `@<version>` 即可使用 go.mod 中的版本。

### 监听外部类型的 Webhook

```bash
# 例: 校验外部资源
kubebuilder create webhook \
  --group cert-manager --version v1 --kind Issuer \
  --defaulting \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

## 测试 & 开发

```bash
make test              # 运行单元测试 (使用 envtest: 真实 K8s API + etcd)
make run               # 本地运行 (使用当前 kubeconfig context)
```

测试采用 **Ginkgo + Gomega** (BDD 风格)。请查看 `suite_test.go` 了解配置。

## 部署工作流

```bash
# 1. 重新生成 manifest
make manifests generate

# 2. 构建 & 部署
export IMG=<registry>/<project>:tag
make docker-build docker-push IMG=$IMG  # 或: kind load docker-image $IMG --name <cluster>
make deploy IMG=$IMG

# 3. 测试
kubectl apply -k config/samples/

# 4. 调试
kubectl logs -n <project>-system deployment/<project>-controller-manager -c manager -f
```

### API 设计

**`api/<version>/*_types.go` 的关键标记:**

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// 字段上:
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern="^[a-z]+$"
// +kubebuilder:default="value"
```

- **status 字段请使用 `metav1.Condition`** (而非自定义 string 字段)
- **使用预定义类型**: 日期请使用 `metav1.Time` 而非 `string`
- **遵循 K8s API 规范**: 标准字段名 (`spec`、`status`、`metadata`)

### Controller 设计

**`internal/controller/*_controller.go` 的 RBAC 标记:**

```go
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
```

**实现规则:**
- **幂等的 reconciliation**: 可安全多次执行
- **更新前先重新读取**: 在 `r.Update` 前执行 `r.Get(ctx, req.NamespacedName, obj)`,避免冲突
- **结构化日志**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner reference**: 启用自动 garbage collection (`SetControllerReference`)
- **监听次级资源**: 使用 `.Owns()` 或 `.Watches()`,而不仅仅是 `RequeueAfter`
- **Finalizer**: 清理外部资源 (bucket、VM、DNS entry 等)

### 日志

**遵循 Kubernetes 日志消息风格指南:**

- 以大写字母开头
- 不以句点结束
- 主动语态: 含主语 (`"Deployment could not create Pod"`) 或省略 (`"Could not create Pod"`)
- 过去时: `"Could not delete Pod"`,而不是 `"Cannot delete Pod"`
- 明示对象类型: `"Deleted Pod"`,而不是 `"Deleted"`
- 平衡的键-值对

```go
log.Info("Starting reconciliation")
log.Info("Created Deployment", "name", deploy.Name)
log.Error(err, "Failed to create Pod", "name", name)
```

**Reference:** https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

### Webhook
- **同时创建所有类型**: `--defaulting --programmatic-validation --conversion`
- **使用 `--force` 时**: 先备份自定义逻辑,scaffold 后再恢复
- **针对多版本 API**: 使用 hub-and-spoke 模式 (`--conversion --spoke v2`)
  - Hub version: 通常为最早的稳定版本 (v1)
  - Spoke version: 与 hub 互转的新版本 (v2、v3)
  - 例: `--group crew --version v1 --kind Captain --conversion --spoke v2` (v1 为 hub,v2 为 spoke)

### 从示例学习

**deploy-image plugin** 会按良好实践生成完整的 controller。可作为参考实现使用:

```bash
kubebuilder create api --group example --version v1alpha1 --kind MyApp \
  --image=<your-image> --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

生成的代码包括: status condition (`metav1.Condition`)、finalizer、owner reference、event、幂等的 reconciliation。

## 分发选项

### Option 1: YAML 包 (Kustomize)

```bash
# 从 Kustomize manifest 生成 dist/install.yaml
make build-installer IMG=<registry>/<project>:tag
```

**关键点:**
- `dist/install.yaml` 由 Kustomize manifest (CRD、RBAC、Deployment) 生成
- 提交该文件到仓库以便分发
- 用户只需 `kubectl` 即可安装 (无需额外工具)

**示例:** 用户可通过单条命令安装:
```bash
kubectl apply -f https://raw.githubusercontent.com/<org>/<repo>/<tag>/dist/install.yaml
```

### Option 2: Helm Chart

```bash
kubebuilder edit --plugins=helm/v2-alpha                      # 生成 dist/chart/ (默认)
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts  # 生成 charts/chart/
```

**开发:**
```bash
make helm-deploy IMG=<registry>/<project>:<tag>          # 通过 Helm 部署 manager
make helm-deploy IMG=$IMG HELM_EXTRA_ARGS="--set ..."    # 使用自定义 value 部署
make helm-status                                         # 查看 release 状态
make helm-uninstall                                      # 删除 release
make helm-history                                        # 查看 release 历史
make helm-rollback                                       # 回滚至上一版本
```

**最终用户/生产:**
```bash
helm install my-release ./<output-dir>/chart/ --namespace <ns> --create-namespace
```

**注意:** 初始 chart 生成后若添加 webhook 或修改 manifest:
1. 备份 `<output-dir>/chart/values.yaml` 与 `<output-dir>/chart/manager/manager.yaml` 中的自定义内容
2. 重新执行: `kubebuilder edit --plugins=helm/v2-alpha --force` (若曾自定义,请使用同一个 `--output-dir`)
3. 从备份手动还原自定义值

### 发布容器镜像

```bash
export IMG=<registry>/<project>:<version>
make docker-build docker-push IMG=$IMG
```

## 参考资料

### 必读
- **Kubebuilder Book**: https://book.kubebuilder.io (综合指南)
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md (常见模式与问题)
- **Good Practices**: https://book.kubebuilder.io/reference/good-practices.html (为何 reconciliation 是幂等的、status condition 等)
- **Logging Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines (消息风格、verbosity 级别)

### API 设计与实现
- **API Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- **Operator Pattern**: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html

### 工具与库
- **controller-runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **controller-tools**: https://github.com/kubernetes-sigs/controller-tools
- **Kubebuilder Repo**: https://github.com/kubernetes-sigs/kubebuilder

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
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
