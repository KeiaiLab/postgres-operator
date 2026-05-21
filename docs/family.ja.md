<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# keiailab operator family

> 共有基盤の上に構築された 4 つの姉妹 Kubernetes operator — `operator-commons` (Go ライブラリ) + Helm partials + Apache-2.0 スタック。

このページは **`postgres-operator`** リポジトリから読まれており、ファミリー全体の正規 cross-link ページです。

## Family 概要

| プロジェクト | データベース | ステータス | リポジトリ |
|---|---|---|---|
| **`postgres-operator`** | PostgreSQL 18+ | active | https://github.com/keiailab/postgres-operator |
| **`mongodb-operator`** | MongoDB 7.0+ | active | https://github.com/keiailab/mongodb-operator |
| **`valkey-operator`** | Valkey 8.0+ (Redis fork, BSD-3) | active | https://github.com/keiailab/valkey-operator |
| **`operator-commons`** | 共有 Go ライブラリ | v0.7.0 | https://github.com/keiailab/operator-commons |

## 共有しているもの

4 つのプロジェクトすべてが同じ運用原則に収束しています:

- **Apache-2.0** エンドツーエンド — SSPL なし、SaaS 表面の copyleft なし
- **`operator-commons`** 共有 Go ライブラリ (v0.7.0+) — finalizer、labels、status sugars、security context builders、NetworkPolicy / ServiceMonitor partials
- **Helm chart skeleton** — RFC-0027 `default` falsy-toggle 防止、RFC-0026 component-keyed values、cycle 26 hardening 6 markers (priorityClassName / lifecycle / SA / minReadySeconds / automount / revisionHistoryLimit)
- **OLM bundle parity** — scorecard v1alpha3 6-test matrix
- **i18n** — README + 11 canonical docs を English / 한국어 / 日本語 / 中文 (cleanup supercycle 2026-05-21 Wave 4)

## やらないこと

- ❌ **upstream operator の embed/wrap** (PGO, CloudNativePG, MongoDB Community Operator, Sentinel) — license-clean、copyleft 義務なし
- ❌ **release gate 用 GitHub Actions** — local 4-layer + GitLab CI L5 (RFC-0002, RFC-0043)
- ❌ **時間ベースの roadmap deadline** — 機能チェックリスト + 完成度 % (`standards/roadmap.md §1.1`)
- ❌ **Bitnami chart / image** — registry deprecation リスク、Broadcom 買収 (ADR-0136 / ADR-0057)

## 出発点

| タスク | エントリーポイント |
|---|---|
| Kubernetes に `postgres-operator` をデプロイ | [README.md](../README.md) Quickstart セクション |
| アーキテクチャを読む | [ARCHITECTURE.md](../ARCHITECTURE.md) |
| イシューまたは機能リクエストを起票 | https://github.com/keiailab/postgres-operator/issues |
| 設計または roadmap を議論 | https://github.com/keiailab/postgres-operator/discussions |
| コードに貢献 | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| セキュリティイシュー報告 | [SECURITY.md](../SECURITY.md) |
| ブランド / ボイス学習 | [BRANDING.md](../BRANDING.md) |
| Adopter / 使用ケース追跡 | [ADOPTERS.md](../ADOPTERS.md) |
| メンテナーを探す | [MAINTAINERS.md](../MAINTAINERS.md) |
| Governance モデル確認 | [GOVERNANCE.md](../GOVERNANCE.md) |
| 今後の作業確認 | [ROADMAP.md](../ROADMAP.md) |

## Cross-family 互換性 (operator-commons)

3 つのデータベース operator はすべて `github.com/keiailab/operator-commons` を同じバージョン (現在 `v0.7.0+`) で import します:

```go
import (
    "github.com/keiailab/operator-commons/pkg/version"
    "github.com/keiailab/operator-commons/pkg/security"
    "github.com/keiailab/operator-commons/pkg/labels"
    "github.com/keiailab/operator-commons/pkg/monitoring"
    "github.com/keiailab/operator-commons/pkg/finalizer"
    "github.com/keiailab/operator-commons/pkg/status"
)
```

`operator-commons` の breaking change には 3 つのデータベース operator 全体で同期 bump が必要 — supercycle Wave 5 の `make cross-validation` target で検証されます。

## i18n

このページ (およびすべての正規プロジェクトドキュメント) は 4 つの言語で提供されています:

- [English](family.md) (canonical)
- [한국어](family.ko.md)
- **日本語** (本 file)
- [中文](family.zh.md)

技術的内容については英語版が権威ある情報源であり、各言語版は native 表現で同じ決定を反映します。

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
