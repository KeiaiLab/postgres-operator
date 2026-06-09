<p align="center">
  <a href="SECURITY.md">English</a> |
  <a href="SECURITY.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="SECURITY.zh.md">中文</a>
</p>

# セキュリティポリシー (日本語)

> 英語原文: [SECURITY.md](SECURITY.md) — canonical / 正本

## サポートバージョン

| Version | セキュリティパッチサポート |
|---|---|
| 最新 minor (v0.x) | ✅ |
| ひとつ前の minor | ✅ (60 日) |
| それ以前 | ❌ |

long-term-support (LTS) ポリシーは、v1.0.0 GA リリース後に別途公表されます。

## 脆弱性の報告

**セキュリティ脆弱性については公開 issue を作成しないでください。** プライベートチャネルをご利用ください:

- メール: `security@keiailab.io`
- PGP 鍵フィンガープリント: `89A4 0947 6828 CB99 2338  C378 651E 51AF 520B CB78`
  (keiailab Helm chart 署名鍵 — gh-pages 上で公開されている `artifacthub-repo.yml` のフィンガープリントと同一)。

## 対応プロセス

1. 受領後 **48 時間以内に確認**。
2. **7 日以内に影響度・深刻度を評価** (CVSS v3.1)。
3. **合意されたパッチタイムライン** を共有 (通常 14〜30 日)。
4. 公開ディスクロージャまでの **90 日 embargo** (交渉可)。
5. **CVE の割り当て** および GitHub Security Advisory の公開。
6. **報告者へのクレジット** (任意)。

## ディスクロージャポリシー

- アドバイザリはパッチリリースと同時に公開。
- 必要に応じて CVE を割り当て。
- 報告者へのクレジット (任意、opt-in)。
- 影響を受けるユーザーには移行ガイドを提供。

## 安全な運用のための推奨事項

本オペレーターを運用する際の推奨:

- TLS を必須化: `network.tls.mode=required`。
- 推奨: `network.networkPolicy.enabled=true` で Network Policy を有効化。
- SCRAM-SHA-256 認証を強制 (デフォルト)。
- Secret ローテーションのために cert-manager 連携を利用。
- サポートマトリクスに記載された最新パッチ版 PostgreSQL を追跡。
- コンテナイメージを `cosign verify` で検証。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
