# CNPG → Keiailab Postgres Operator: Gap分析

> **言語**: [English](GAP-ANALYSIS.md) | [한국어](GAP-ANALYSIS.ko.md) | [日本語](GAP-ANALYSIS.ja.md) | [中文](GAP-ANALYSIS.zh.md)

## 概要

本ドキュメントはkeiailabプロダクションクラスタで現在運用中のCloudNativePG (CNPG) v1.29と
keiailab/postgres-operator v0.3.0-alpha.18の機能を比較します。
目標: CNPGを本番で置き換える前に解決すべき機能ギャップの特定。

**主な結果:**
- 16コアコンポーネント中12個が完全実装済み
- 5つのP0（必須）ギャップがプロダクション置き換えをブロック
- 3つの追加P1ギャップが運用信頼性に必要
- 見積もり: 8スプリント
- デプロイ準備度: 7.5/10

---

## Gapマトリクス

### P0 — プロダクションブロッカー

| # | ギャップ | CNPG相当 | 工数 | スプリント |
|---|---------|---------|------|-----------|
| 1 | WALアーカイブ + オブジェクトストア | barmanObjectStore | 2週間 | S3 |
| 2 | オブジェクトストアからPITR | spec.backup.recovery | 1週間 | S4 |
| 3 | TLS Phase 3 (マウント + ssl=on) | デフォルト動作 | 3日 | S1 |
| 4 | postgresql.confホットリロード | pg_reload_conf() | 2日 | S2 |
| 5 | バックアップ保持クリーンアップ | retentionPolicy | 3日 | S5 |

### P1 — 運用信頼性

| # | ギャップ | CNPG相当 | 工数 | スプリント |
|---|---------|---------|------|-----------|
| 6 | Switchover | cnpg promote | 3日 | S5 |
| 7 | Fencing | cnpg fencing | 3日 | S6 |
| 8 | 同期レプリケーション | syncReplicas | 2日 | S6 |
| 9 | pg_hba.confリロード | Configリロード | 1日 | S2 |
| 10 | カスタムPGパラメータ | spec.postgresql.parameters | 2日 | S2 |

---

## MVP定義

**CNPG置き換え最小条件:** P0 (5) + P1の6, 7, 10 = **合計8機能**

---

## マイグレーションロードマップ

| スプリント | フォーカス | E2E検証 |
|-----------|----------|---------|
| S1 | TLS Phase 3 | psql sslmode=verify-full成功 |
| S2 | Configホットリロード | SHOW結果にspec変更反映 |
| S3 | WALアーカイブ + バックアップ | WALセグメントがストアに到着 |
| S4 | PITRリストア | タイムスタンプ復元 + データ検証 |
| S5 | 保持 + Switchover | クリーンアップ実行 + primary切替 |
| S6 | Fencing + 同期複製 | Split-brainシミュレーション通過 |
| S7 | 統合テスト | keiailabクラスタ全体e2e |
| S8 | CNPG置き換え | ゼロダウンタイム移行 |
