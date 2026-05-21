<p align="center">
  <a href="SUPPORT.md">English</a> |
  <a href="SUPPORT.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="SUPPORT.zh.md">中文</a>
</p>

# サポート (日本語)

> 英語原文: [SUPPORT.md](SUPPORT.md) — canonical / 正本

keiailab/postgres-operator の利用中に問題が発生した場合は、以下のチャネルをご利用ください。セキュリティ脆弱性は **ここで報告しないでください** — [SECURITY.md](SECURITY.md) のプライベートプロセスに従ってください。

## まずはこちらから

- **README.md** — quickstart と中核となる CRD サーフェスの概要。
- **docs/operator-guide/** — 実行時の運用 (`deployment.md`, `cross-validation-cnpg.md`, `ha-election.md`, `pooler-monitoring.md`)。
- **docs/releases/release-process.md** — リリースおよびアップグレード手順。
- **CHANGELOG.md** — リリースごとの変更履歴。

## 質問 / ディスカッション

- **GitHub Discussions**:
  https://github.com/keiailab/postgres-operator/discussions
  利用方法に関する質問、設計上の理由、運用シナリオ、RFC ドラフトの作成に最適。

## バグ報告 / 機能要望

- **GitHub Issues**:
  https://github.com/keiailab/postgres-operator/issues
  `bug_report.yaml` / `feature_request.yaml` テンプレートをご利用ください。再現手順、operator のバージョン、Kubernetes のバージョン、kind/cloud 環境、`kubectl get postgrescluster -oyaml` の出力、operator-manager Pod ログの抜粋を含めていただくと、トリアージが非常に速くなります。

## Pull Request

[CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。lefthook をインストールし、DCO サインオフし、ローカル 4-layer ゲートが通過した evidence (`pre-commit run --all-files`、`make test`、`make audit`) を PR 本文に添付します。PR テンプレートが流れをガイドします。

## セキュリティ脆弱性

[SECURITY.md](SECURITY.md) のプライベートチャネルを通じて報告してください。脆弱性の内容を公開 issue や discussion に書かないでください。

## 商用サポート / SLA

本プロジェクトは Apache-2.0 オープンソースプロジェクトであり、公式の商用サポートはありません。production クラスター向けのインシデント対応 SLA が必要な場合は、別途コンサルティング契約が必要となる場合があります — `support@keiailab.io` までメールでご連絡ください。

## 応答目安

- Issues / Discussions の一次応答: **3 営業日**。
- セキュリティ報告の一次応答: **48 時間** (SECURITY.md に基づく)。
- Pull-request レビュー: メンテナーの稼働状況により **5 営業日**。

これらはメンテナーのベストエフォート目標であり、SLA ではありません。

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
