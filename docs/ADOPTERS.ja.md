<p align="center">
  <a href="ADOPTERS.md">English</a> |
  <a href="ADOPTERS.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="ADOPTERS.zh.md">中文</a>
</p>

# postgres-operator のアダプター (日本語)

> 英語原文: [ADOPTERS.md](ADOPTERS.md) — canonical / 正本

本ドキュメントは、本番環境または評価環境で `keiailab/postgres-operator` を利用している組織およびプロジェクトの *公開* リストです。自己登録歓迎 — 行を追加するには PR を作成してください。

> 本オペレーターは現在 **0.4.0-beta** 段階です。本番デプロイの際はまず SLA ガイド (ROADMAP.md) をご確認ください。

## Production ユーザー

postgres-operator を *production-grade SLA* で運用しているユーザー。

| ユーザー | コンポーネント | 利用パターン | 初回バージョン | 現行バージョン | 登録日 |
|---|---|---|---|---|---|
| _まだなし (alpha 段階)_ | — | G1 マイルストーン (single-shard production) 達成後に追加予定。 | — | — | — |

## Evaluator (評価者)

PoC / evaluation / Day-0 環境でオペレーターを運用しているユーザー。

| ユーザー | コンポーネント | フェーズ | 登録日 |
|---|---|---|---|
| **platform-data** ([keiailab](https://github.com/keiailab)) | PostgresCluster (single-shard, PG18) | Day-0 デプロイ。PG18 failover smoke 通過、RTO 21 秒。HA replicas / backup-restore drill は未通過 — 本番化前に ROADMAP G1 が必要。 | 2026-05-07 |

## 自己追加方法

PR を作成して、上の表に 1 行追加してください:

```markdown
| **<組織 / プロジェクト>** ([profile](<URL>)) | <コンポーネント + トポロジー> | <フェーズ: Day-0 / G1 / G2 / G3> | <登録日 YYYY-MM-DD> |
```

非公開または匿名化された記載を希望する場合は SECURITY.md のチャネルでご連絡ください。メンテナーが *組織匿名化* 行を追加します。

## ROADMAP gate

本番移行の各フェーズは ROADMAP.md に定義された G1〜G4 マイルストーンに従います:

- **G1** — Single-shard production (HA replica + failover drill + backup/restore/PITR + upgrade rollback)。
- **G2** — Native multi-shard (router + auto-split + cross-shard transactions)。
- **G3** — pgBackRest 統合 GA。
- **G4** — chaos-mesh による検証 + 複数組織のアダプター。

## CNCF Sandbox 参照

本 ADOPTERS リストは、CNCF graduation 基準 "≥ 1 public adopter (または意向を表明した evaluator)" を満たすための公開参照としても利用されます。

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
