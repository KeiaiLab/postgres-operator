<p align="center">
  <a href="MAINTAINERS.md">English</a> |
  <a href="MAINTAINERS.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="MAINTAINERS.zh.md">中文</a>
</p>

# メンテナー (日本語)

> 英語原文: [MAINTAINERS.md](MAINTAINERS.md) — canonical / 正本

本ドキュメントは keiailab/postgres-operator のメンテナー一覧を追跡します。メンテナーは本プロジェクトに対する意思決定権およびマージ権を保持します。

## 現在のメンテナー

| 名前 / チーム | GitHub | 役割 | 領域 |
|---|---|---|---|
| keiailab maintainers | [@keiailab/maintainers](https://github.com/orgs/keiailab/teams/maintainers) | Lead | プロジェクト全体 |

GitHub team `@keiailab/maintainers` がすべての領域に対してマージおよび承認権限を保持します。個別メンテナーは以下のプロセスに従って追加されます。

## メンテナー資格

以下の基準を *少なくとも 6 か月* 満たした contributor がメンテナーに推薦され得ます:

- 20 件以上のマージ済み PR (意味のあるコードまたはドキュメント貢献)。
- 30 件以上の PR レビュー (建設的なフィードバックを伴うもの)。
- 本プロジェクトの [GOVERNANCE.md](GOVERNANCE.md) と [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) を遵守していること。
- 少なくとも 1 つのコア領域 (controller、instance manager、backup、sharding、docs など) に対する深い理解。

## メンテナーの追加

1. 既存メンテナー、または候補者自身が提案を起票 (issue または RFC) します。
2. `@keiailab/maintainers` チームが 7 日間のコメントウィンドウで lazy consensus を適用します。
3. 異議がなければ、新メンテナーを GitHub team に追加し、PR で MAINTAINERS.md を更新します。

## 非アクティブメンテナー

6 か月連続で非アクティブなメンテナーは emeritus ステータスに移行されます (権限は剥奪、名前は emeritus ロスターに保存)。復帰には新規追加と同じ手順が必要です。

## 領域オーナーシップ (CODEOWNERS と整合)

`/.github/CODEOWNERS` を参照してください。ディレクトリ別の自動レビュー担当者は同ファイルから割り当てられます。

## Emeritus

(まだなし)

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
