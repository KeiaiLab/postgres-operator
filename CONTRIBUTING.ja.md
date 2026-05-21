<p align="center">
  <a href="CONTRIBUTING.md">English</a> |
  <a href="CONTRIBUTING.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="CONTRIBUTING.zh.md">中文</a>
</p>

# keiailab/postgres-operator へのコントリビュート (日本語)

> 英語原文: [CONTRIBUTING.md](CONTRIBUTING.md) — canonical / 正本

コントリビューションありがとうございます! issue、ディスカッション、PR 本文では英語と韓国語のいずれも歓迎しますが、プロジェクトドキュメント自体は英語で維持されます。

## 基本ルール

1. **テストなしで機能はマージされない。** すべての PR は unit test または e2e test を含めること。
2. **DCO サインオフ必須。** すべての commit に `Signed-off-by: Your Name <you@example.com>` の trailer が必要 (`git commit -s` を利用)。
3. **Apache 2.0**: コントリビュートすることで、自分の成果物を本プロジェクトのライセンスで提供することに同意する。
4. **Commit message の言語**: 韓国語または英語 OK。クロスチームのコラボレーションのため英語を推奨。

## はじめに

### 前提

- Go 1.23+
- Docker (buildx 有効)
- kubectl、kind、kubebuilder v4
- make
- [lefthook](https://github.com/evilmartians/lefthook) (pre-commit hook マネージャー)

### ローカル開発

```bash
git clone https://github.com/keiailab/postgres-operator.git
cd postgres-operator
brew install lefthook    # または: go install github.com/evilmartians/lefthook@latest
make hooks-install       # `lefthook install` ラッパー (pre-commit / commit-msg / pre-push)
make hooks-check         # hook が正しく接続されているか確認 (DCO + Conventional Commits 強制)
make test                # envtest + unit テスト
make lint                # golangci-lint
make e2e                 # kind ベースの e2e (5〜10 分)
make build               # オペレーターバイナリのビルド
make docker-build        # コンテナイメージのビルド (docker buildx デフォルトビルダー)
```

## PR ワークフロー

1. **新機能はまず issue を作成** してメンテナーと方向性をすり合わせること。些細なバグ修正 / ドキュメント微修正は直接 PR でも可。
2. **ブランチ名**: `feat/<short>`、`fix/<short>`、`docs/<short>`、`refactor/<short>`。
3. **Commit message**: [Conventional Commits](https://www.conventionalcommits.org/) を推奨 (`feat:`、`fix:`、`docs:`、`chore:`)。
4. **サインオフ**: `git commit -s -m "feat: ..."`。
5. **PR 本文**: テンプレートに従って記入し、関連 issue を `Closes #N` でリンクする。
6. **ローカルゲートが green であること**: `pre-commit run --all-files` と `make lint test validate` の双方が通る必要があります (RFC-0002 により GitHub Actions は使用しません)。
7. **レビュー**: CODEOWNERS が自動アサインされます。通常の変更には 1 名のメンテナーの LGTM、アーキテクチャ変更には 2 名が必要です。

## 大規模な変更には RFC が必要

アーキテクチャ変更 — CRD の追加/変更、新規 reconciler の導入、セキュリティモデルの変更、外部依存の追加 — は、まず [`docs/rfcs/`](docs/rfcs/) に RFC を提出する必要があります。

- ファイル名: `NNNN-short-title.md`。
- 7 日間のコメントウィンドウ。
- 合意形成後、ステータスを `Accepted` に切り替え、PR を進めます。

## テストポリシー

- **Unit test**: `internal/**/*_test.go`、必要に応じて envtest を使用。
- **e2e**: `test/e2e/`、kind クラスター上の Ginkgo + chainsaw。
- **chaos**: `test/chaos/`、chaos-mesh シナリオ (Phase 3+)。
- **bench**: `test/bench/`、pgbench (Phase 6、8)。
- 行カバレッジ ≥ 80% (codecov でゲート)。

## コードスタイル

- `gofmt` / `goimports` (`make fmt` を実行)。
- `golangci-lint` 違反 0 件 (`make lint` を実行)。
- コメントは **なぜ** を説明し、**何を** は名前で表現する — 名前が意図を伝えること。
- 新規の外部ライブラリ / フレームワークを導入する前に、公式ドキュメントまたは [context7 MCP](https://github.com/upstash/context7) で現行 API を再確認すること。

## セキュリティ脆弱性

[SECURITY.md](SECURITY.md) を参照し、プライベートチャネルを利用してください。脆弱性を公開 issue に書かないこと。

## 行動規範

[Contributor Covenant v2.1](CODE_OF_CONDUCT.md) に従います。

## ライセンス

すべてのコントリビューションは [Apache 2.0](LICENSE) でライセンスされます。

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
