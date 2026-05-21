<p align="center">
  <a href="AGENTS.md">English</a> |
  <a href="AGENTS.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="AGENTS.zh.md">中文</a>
</p>

# postgres-operator - AI Agent ガイド (日本語)

> 英語原文: [AGENTS.md](AGENTS.md) — canonical / 正本

## プロジェクト構成

**Single-group レイアウト (デフォルト):**
```
cmd/main.go                    Manager エントリ (controller/webhook 登録)
api/<version>/*_types.go       CRD スキーマ (+kubebuilder マーカー)
api/<version>/zz_generated.*   自動生成 (編集禁止)
internal/controller/*          Reconciliation ロジック
internal/webhook/*             Validation/defaulting (存在する場合)
config/crd/bases/*             生成された CRD (編集禁止)
config/rbac/role.yaml          生成された RBAC (編集禁止)
config/samples/*               サンプル CR (こちらを編集)
Makefile                       Build/test/deploy コマンド
PROJECT                        Kubebuilder メタデータ 自動生成 (編集禁止)
```

**Multi-group レイアウト** (複数 API group プロジェクト向け):
```
api/<group>/<version>/*_types.go       group ごとの CRD スキーマ
internal/controller/<group>/*          group ごとの controller
internal/webhook/<group>/<version>/*   group + version ごとの webhook (存在する場合)
```

Multi-group レイアウトは group 名 (例: `batch`、`apps`) で API を整理します。`PROJECT` ファイルの `multigroup: true` を確認してください。

**Multi-group レイアウトへの変換:**
1. 実行: `kubebuilder edit --multigroup=true`
2. API を移動: `mkdir -p api/<group> && mv api/<version> api/<group>/`
3. Controller を移動: `mkdir -p internal/controller/<group> && mv internal/controller/*.go internal/controller/<group>/`
4. Webhook を移動 (ある場合): `mkdir -p internal/webhook/<group> && mv internal/webhook/<version> internal/webhook/<group>/`
5. すべてのファイルの import path を更新
6. `PROJECT` ファイル内の各リソースの `path` を修正
7. テストスイートの CRD path を更新 (相対パスに `..` を 1 段追加)

## クリティカルなルール

### 編集禁止 (自動生成)
- `config/crd/bases/*.yaml` - `make manifests` から
- `config/rbac/role.yaml` - `make manifests` から
- `config/webhook/manifests.yaml` - `make manifests` から
- `**/zz_generated.*.go` - `make generate` から
- `PROJECT` - `kubebuilder [OPTIONS]` から

### Scaffold マーカーを削除しない
`// +kubebuilder:scaffold:*` コメントを削除しないこと。CLI がこのマーカー位置にコードを注入します。

### プロジェクト構造を維持
ファイルを移動しないこと。CLI は特定の場所にファイルがあることを期待します。

### CLI コマンドを必ず使用
スキャフォールドには `kubebuilder create api` と `kubebuilder create webhook` を使ってください。ファイルを手動で作成しないこと。

### E2E テストは隔離された Kind クラスターが必要
e2e テストは隔離環境 (GitHub Actions CI と同様) でソリューションを検証するように設計されています。専用の [Kind](https://kind.sigs.k8s.io/) クラスター (実際の dev/prod クラスターではなく) で実行してください。

## 変更後の手順

**`*_types.go` またはマーカーを編集した後:**
```
make manifests  # マーカーから CRD/RBAC を再生成
make generate   # DeepCopy メソッドを再生成
```

**`*.go` ファイルを編集した後:**
```
make lint-fix   # コードスタイルを自動修正
make test       # ユニットテスト実行
```

## CLI コマンドチートシート

### API 作成 (自前の型)
```bash
kubebuilder create api --group <group> --version <version> --kind <Kind>
```

### Deploy Image Plugin (任意のコンテナイメージを deploy/管理するスキャフォールド)

任意のコンテナイメージ (nginx、redis、memcached、独自アプリ等) を deploy/管理する controller を生成:

```bash
# 例: memcached をデプロイ
kubebuilder create api --group example.com --version v1alpha1 --kind Memcached \
  --image=memcached:alpine \
  --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

よい慣習のコードを scaffolds: reconciliation ロジック、status condition、finalizer、RBAC。参考実装として活用してください。


### Webhook 作成
```bash
# Validation + defaulting
kubebuilder create webhook --group <group> --version <version> --kind <Kind> \
  --defaulting --programmatic-validation

# Conversion webhook (multi-version API 用)
kubebuilder create webhook --group <group> --version v1 --kind <Kind> \
  --conversion --spoke v2
```

### Core Kubernetes 型用 Controller
```bash
# Pod を監視
kubebuilder create api --group core --version v1 --kind Pod \
  --controller=true --resource=false

# Deployment を監視
kubebuilder create api --group apps --version v1 --kind Deployment \
  --controller=true --resource=false
```

### 外部型用 Controller (例: 他の operator から)

外部 API (cert-manager、Argo CD、Istio など) のリソースを監視:

```bash
# 例: cert-manager Certificate リソースを監視
kubebuilder create api \
  --group cert-manager --version v1 --kind Certificate \
  --controller=true --resource=false \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

**注意:** 特定バージョンが必要な場合のみ `--external-api-module=<module>@<version>` を使用。それ以外は `@<version>` を省略すると go.mod のバージョンが使われます。

### 外部型用 Webhook

```bash
# 例: 外部リソースの validation
kubebuilder create webhook \
  --group cert-manager --version v1 --kind Issuer \
  --defaulting \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

## テスト & 開発

```bash
make test              # ユニットテスト実行 (envtest 使用: 実 K8s API + etcd)
make run               # ローカル実行 (現在の kubeconfig context を使用)
```

テストは **Ginkgo + Gomega** (BDD スタイル) を使用します。セットアップは `suite_test.go` を参照してください。

## デプロイワークフロー

```bash
# 1. manifest を再生成
make manifests generate

# 2. ビルド & デプロイ
export IMG=<registry>/<project>:tag
make docker-build docker-push IMG=$IMG  # または: kind load docker-image $IMG --name <cluster>
make deploy IMG=$IMG

# 3. テスト
kubectl apply -k config/samples/

# 4. デバッグ
kubectl logs -n <project>-system deployment/<project>-controller-manager -c manager -f
```

### API 設計

**`api/<version>/*_types.go` の主要なマーカー:**

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// フィールドに:
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern="^[a-z]+$"
// +kubebuilder:default="value"
```

- **status には `metav1.Condition` を使用** (custom string フィールドではなく)
- **定義済み型を使用**: 日付には `string` ではなく `metav1.Time`
- **K8s API 規約に従う**: 標準フィールド名 (`spec`、`status`、`metadata`)

### Controller 設計

**`internal/controller/*_controller.go` の RBAC マーカー:**

```go
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
```

**実装ルール:**
- **冪等な reconciliation**: 複数回実行しても安全
- **更新前に再取得**: `r.Update` の前に `r.Get(ctx, req.NamespacedName, obj)` を実行し conflict を回避
- **構造化ロギング**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner reference**: 自動 garbage collection を有効化 (`SetControllerReference`)
- **副次リソースの watch**: `RequeueAfter` だけでなく `.Owns()` または `.Watches()` を使用
- **Finalizer**: 外部リソースをクリーンアップ (bucket、VM、DNS entry 等)

### ロギング

**Kubernetes のロギングメッセージスタイルガイドラインに従う:**

- 大文字で開始
- メッセージを `.` で終わらせない
- 能動態: 主語あり (`"Deployment could not create Pod"`) または省略 (`"Could not create Pod"`)
- 過去形: `"Cannot delete Pod"` ではなく `"Could not delete Pod"`
- オブジェクト型を明示: `"Deleted"` ではなく `"Deleted Pod"`
- キー・値ペアのバランス

```go
log.Info("Starting reconciliation")
log.Info("Created Deployment", "name", deploy.Name)
log.Error(err, "Failed to create Pod", "name", name)
```

**Reference:** https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

### Webhooks
- **すべての型を一緒に作成**: `--defaulting --programmatic-validation --conversion`
- **`--force` を使うとき**: まずカスタムロジックをバックアップし、スキャフォールド後に復元
- **Multi-version API の場合**: hub-and-spoke パターン (`--conversion --spoke v2`)
  - Hub version: 通常は最古の安定版 (v1)
  - Spoke version: hub に変換される新規バージョン (v2、v3)
  - 例: `--group crew --version v1 --kind Captain --conversion --spoke v2` (v1 が hub、v2 が spoke)

### サンプルから学ぶ

**deploy-image plugin** は良い慣習に従う完全な controller を scaffold します。参考実装として利用してください:

```bash
kubebuilder create api --group example --version v1alpha1 --kind MyApp \
  --image=<your-image> --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

生成されるコード: status condition (`metav1.Condition`)、finalizer、owner reference、event、冪等な reconciliation。

## 配布オプション

### Option 1: YAML バンドル (Kustomize)

```bash
# Kustomize manifest から dist/install.yaml を生成
make build-installer IMG=<registry>/<project>:tag
```

**ポイント:**
- `dist/install.yaml` は Kustomize manifest (CRD、RBAC、Deployment) から生成
- 配布を容易にするためにこのファイルをリポジトリに commit
- ユーザーは `kubectl` だけで導入可能 (追加ツール不要)

**例:** ユーザーは単一コマンドでインストール:
```bash
kubectl apply -f https://raw.githubusercontent.com/<org>/<repo>/<tag>/dist/install.yaml
```

### Option 2: Helm Chart

```bash
kubebuilder edit --plugins=helm/v2-alpha                      # dist/chart/ を生成 (デフォルト)
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts  # charts/chart/ を生成
```

**開発用:**
```bash
make helm-deploy IMG=<registry>/<project>:<tag>          # Helm 経由で manager をデプロイ
make helm-deploy IMG=$IMG HELM_EXTRA_ARGS="--set ..."    # カスタム値でデプロイ
make helm-status                                         # release ステータスを表示
make helm-uninstall                                      # release を削除
make helm-history                                        # release 履歴を表示
make helm-rollback                                       # 前バージョンへロールバック
```

**エンドユーザー/本番:**
```bash
helm install my-release ./<output-dir>/chart/ --namespace <ns> --create-namespace
```

**重要:** 初期チャート生成後に webhook を追加したり manifest を修正したりした場合:
1. `<output-dir>/chart/values.yaml` および `<output-dir>/chart/manager/manager.yaml` のカスタマイズをバックアップ
2. 再実行: `kubebuilder edit --plugins=helm/v2-alpha --force` (カスタマイズした場合は同じ `--output-dir` を使用)
3. バックアップから手動でカスタム値を復元

### コンテナイメージを publish

```bash
export IMG=<registry>/<project>:<version>
make docker-build docker-push IMG=$IMG
```

## References

### 必読資料
- **Kubebuilder Book**: https://book.kubebuilder.io (包括的なガイド)
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md (共通パターンと質問)
- **Good Practices**: https://book.kubebuilder.io/reference/good-practices.html (reconciliation が冪等な理由、status condition など)
- **Logging Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines (メッセージスタイル、verbosity レベル)

### API 設計 & 実装
- **API Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- **Operator Pattern**: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html

### ツール & ライブラリ
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
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
