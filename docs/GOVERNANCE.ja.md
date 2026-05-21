<p align="center">
  <a href="GOVERNANCE.md">English</a> |
  <a href="GOVERNANCE.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="GOVERNANCE.zh.md">中文</a>
</p>

# ガバナンス (日本語)

> 英語原文: [GOVERNANCE.md](GOVERNANCE.md) — canonical / 正本

本ドキュメントは、keiailab/postgres-operator プロジェクトにおける意思決定の方式を定義します。

## 原則

1. **オープン (Open)**: あらゆる意思決定は公開チャネル (GitHub issues / PRs / RFCs) 上で行います。
2. **Lazy consensus**: 日常的な変更は異議がなければ進行します。
3. **Explicit consensus**: アーキテクチャ変更、CRD 変更、セキュリティモデル変更、ライセンス変更には RFC およびメンテナーの **2/3 supermajority** が必要です。より小さい RFC (単一コンポーネント / ツール採用 / ポリシー強化) は **単純多数 (>50%)** で可とします。GOVERNANCE そのものの改訂 (後述の "Amendments") は常に 2/3 supermajority を必要とします。
4. **共同責任 (Shared accountability)**: メンテナーはコード品質、ユーザー安全性、コミュニティ健全性について共同で責任を負います。

## 決定クラス

### Routine 変更 (lazy consensus)

- バグ修正、ドキュメント改善、テスト追加、依存パッケージの minor/patch アップグレード、および public API を変更しないリファクタリング。
- プロセス: PR → 1 名以上のメンテナーが LGTM → マージ。
- Window: 不要 (ローカルゲートが通れば即マージ可能。RFC-0002 に従い GitHub Actions は使用しない — pre-commit / pre-push フックおよび Makefile がゲートを担う)。

### Medium 変更 (explicit consensus)

- 新規 CRD フィールド、新規 reconciler、依存パッケージの major アップグレード、public API の変更。
- プロセス: issue で提案 → 7 日間のコメントウィンドウ → メンテナー多数の LGTM → マージ。
- 反対意見が 1 件でもあればメンテナー会議へエスカレーション。

### Architectural 変更 (RFC 必須)

- 新規コンポーネント導入、セキュリティモデル変更、ライセンス変更、または backward-incompatible 変更。
- プロセス:
  1. `docs/rfcs/NNNN-title.md` に RFC を提出。
  2. 14 日間のコメントウィンドウ。
  3. メンテナーの 2/3 以上が賛成。
  4. RFC のステータスを `Draft → Accepted` に切り替え、実装 PR を開く。
- 却下された RFC は `Rejected` ステータスのまま保存 (履歴コンテキストの維持)。

## 役割

### Contributor

誰でも可能。PR と issue を提出できます。

### Reviewer

継続的にレビューを行う contributor。CODEOWNERS に追加されることがあります。マージ権限はありません。

### Maintainer

[MAINTAINERS.md](MAINTAINERS.md) を参照。マージ / 承認権限を保持します。

### Lead maintainer

keiailab 組織の代表者。ライセンス、ガバナンス、セキュリティポリシーに対する最終意思決定権を保持します。

## メンテナー会議

- 毎月の定例会議 (必要に応じて臨時セッションを実施)。
- 議事録は `docs/meetings/` 配下に公開。
- アジェンダ: 紛争解決、RFC ディスカッション、ロードマップレビュー。

## 紛争解決

1. まず PR/issue のコメントで議論。
2. 未解決の場合、メンテナー会議のアジェンダに追加。
3. メンテナー多数決で決定。
4. 票が割れた場合は lead maintainer が決裁する。

## ライセンス / 知的財産

- すべてのコントリビューションは Apache 2.0 でライセンスされます。
- DCO サインオフは必須です。
- ライセンス変更にはすべてのコントリビューターの全会一致の同意が必要です (実質的に不変)。

## Amendments (改訂)

本ドキュメントはメンテナーの 2/3 supermajority があるときのみ改訂可能です。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
