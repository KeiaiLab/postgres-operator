<p align="center">
  <a href="BRANDING.md">English</a> |
  <a href="BRANDING.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# ブランディングガイド — `postgres-operator` (日本語)

> 英語原文: [BRANDING.md](BRANDING.md) — canonical / 正本

> keiailab operator family の視覚的 identity、voice、tone。

本ドキュメントは `postgres-operator` のブランディング決定に対する正本 (canonical) リファレンスです。README、リリースノート、マーケティング資料、およびプロジェクトを代表するあらゆる third-party コミュニケーションに適用されます。

## 1. Identity

**組織**: [keiailab](https://keiailab.com) — Kubernetes ネイティブのデータプラットフォームオペレーター (Apache-2.0、license-clean、vanilla upstream 互換)。

**プロジェクト**: `postgres-operator` — Kubernetes 向け Apache-2.0 PostgreSQL Operator — vanilla PG18+、license-clean、K8s ネイティブの自動シャーディングロードマップ。

## 2. ロゴ & ビジュアルアセット

| Asset | URL | 用途 |
|---|---|---|
| Primary logo (SVG) | `https://keiailab.com/assets/logo.svg` | README ヘッダー、スライド |
| Mono mark | `https://keiailab.com/assets/mark.svg` | favicon、SNS カード |
| Wordmark | `https://keiailab.com/assets/wordmark.svg` | Footer、暗背景 |

**ロゴ配置**: README 上部中央、width 120px。常に https://keiailab.com にリンクします。

**Clear space**: ロゴ周りの最小 padding はロゴ幅の 25%。

**禁止事項**:
- ロゴの再カラーリング
- ドロップシャドウやフィルター追加
- コントラスト不足の背景上に配置
- keiailab ブランド承認なしの他ロゴとの組合せ

## 3. カラーパレット

| 役割 | Hex | 用途 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | ヘッダー、primary action、リンク |
| Secondary (deep navy) | `#0F172A` | 暗背景、コードブロック |
| Accent (warm amber) | `#F59E0B` | ハイライト、バッジアクセント |
| Neutral grey | `#64748B` | 明背景上の本文 |
| Background light | `#F8FAFC` | ドキュメントページ背景 |
| Background dark | `#020617` | コードエディタテーマ、dark mode |

GitHub README の shield.io バッジは上記 hex の使用を推奨。

## 4. タイポグラフィ

- **見出し**: System default (GitHub の default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **本文**: 同上 (GitHub ネイティブと整合)
- **コード**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub の default monospace)

別途の webfont は使用しません (GitHub README レンダリングと整合)。

## 5. Voice & Tone

**読者**: Kubernetes プラットフォームエンジニア / DBA / SRE。

**Voice 原則**:
- **ダイレクト (Direct)** — 可能な限り段落より bullet
- **エビデンスベース (Evidence-based)** — 主張にはベンチマーク / SLA / リンクを伴う
- **ベンダーニュートラル (Vendor-neutral)** — upstream (PostgreSQL、MongoDB、Valkey) を参照しつつ third-party operator を embed/wrap しない
- **License-aware** — Apache-2.0 + BSD/MIT/PG-license 依存のみ

**避ける表現**:
- マーケティング最上級表現 ("blazing fast"、"revolutionary"、"best-in-class")
- 漠然とした比較 ("X-class quality") — *具体的メトリクスまたはベンチマークで補強*
- ロードマップにおける日付ベースの締切 (代わりに `standards/roadmap.md §1.1` の feature checklist を使用)

## 6. README ヘッダー標準

すべての README 最初の段落は次の形式 (Wave 3 標準):

```markdown
<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator

> **Apache-2.0 PostgreSQL Operator for Kubernetes — vanilla PG18+, license-clean, K8s-native auto-sharding roadmap**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <!-- 既存の shield.io バッジを維持 + 整合 -->
</p>

<p align="center">
  <b>English</b> |
  <a href="README.ko.md">한국어</a> |
  <a href="README.ja.md">日本語</a> |
  <a href="README.zh.md">中文</a>
</p>
```

## 7. README Footer 標準

すべての README + ポリシー .md ファイルの末尾に次の footer を付与:

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges 標準順序

README の shield.io バッジ並び順 (左→右):

1. License (Apache-2.0)
2. Go Version (1.25+)
3. Database (例: PostgreSQL 18+ / MongoDB 7.0+ / Valkey 8.0+)
4. Kubernetes Version (1.26+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/postgres-operator/discussions` — 機能アイデア、Q&A
- **Issues**: バグ報告 + 利用シーン付きの具体的な機能要望
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md` 標準 (ユーザーシナリオ + 検証コマンド引用が必須、`standards/checklist.md §3`)

## 10. Social & External

- **Web サイト**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-postgres-operator
- **GHCR** (Container): https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator

## 11. ライセンス & 出典

- License: [Apache-2.0](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party 出典: 該当する場合は [NOTICE](../NOTICE) を参照

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
