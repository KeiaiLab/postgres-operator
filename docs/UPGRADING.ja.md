<p align="center">
  <a href="UPGRADING.md">English</a> |
  <a href="UPGRADING.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="UPGRADING.zh.md">中文</a>
</p>

# postgres-operator のアップグレード (日本語)

> 英語原文: [UPGRADING.md](UPGRADING.md) — canonical / 正本

本ドキュメントは、postgres-operator の minor/major バージョンアップグレード時に必要なマイグレーション作業をまとめます。Helm ユーザーは chart のアップグレードのみですべての変更が適用されますが、静的 manifest (`kubectl apply -f`) ユーザーは RBAC など一部の項目を手動で patch する必要があります。

## 0. バージョンポリシー (semver)

| 変更タイプ | semver bump | 例 |
|---|---|---|
| 新規 controller / CR / API 追加 | minor (v1.X → v1.X+1) | PostgresPooler を新設 |
| 既存 API シグネチャ変更 (breaking) | major (v1.X → v2.0) | PostgresCluster.spec.storage 構造の変更 |
| バグ修正 / 依存 bump | patch (v1.X.Y → v1.X.Y+1) | controller-runtime 0.19→0.20 |
| operator-commons 依存 bump | minor (commons v0.X → v0.X+1) | pkg/reconcile を新規採用 |

## 1. v0.1.x → v0.2.x

### Helm ユーザー

```bash
helm repo update
helm upgrade postgres-operator <repo>/postgres-operator \
  --namespace postgres-operator-system \
  --version 0.2.x
```

chart 自体が RBAC、CRD、Deployment を同期します。追加作業は不要です。

### 静的 manifest ユーザー — RBAC マイグレーション

`make build-installer` の結果である `dist/install.yaml` の差分を確認:

```bash
kubectl diff -f dist/install.yaml
kubectl apply -f dist/install.yaml
```

既存 ClusterRole への新規権限 (現時点ではなし — 本 minor で RBAC 変更なし):

| API group | Resource | 理由 | 追加時期 |
|---|---|---|---|
| (なし) | — | — | — |

## 2. v0.2.x → v0.3.x (予定)

### operator-commons v0.9.0 採用 (Sprint 1 + S5)

```bash
# go.mod の operator-commons 依存を bump した後
go mod tidy
```

- **新規 import**: `github.com/keiailab/operator-commons/pkg/pvc`、`pkg/topology` (Sprint 1)
- **追加 import 予定**: `pkg/reconcile`、`pkg/resources` (S5 後続)
- **重複コードの除去**: `internal/controller/` の自前 helper を operator-commons の helper に置換。挙動は同一。

マイグレーションの影響:
- Reconcile 挙動同一 (リファクタリングのみ、外部挙動の変更なし)
- CRD spec 変更なし (v1alpha2 conversion は別 cycle)
- Helm chart への影響なし

## 3. v0.3.x → v1.0.0 (予定 — v3.x-stable 宣言時点)

CLAUDE.md §7 の *商用製品レベル* (P0+P1+P2+OP+C すべて ✅) に到達した時点。

- すべての CR の API stability を `Stable` (v1) に昇格
- breaking change なし (v0.x → v1.0 は *命名のみ* の変更)
- 5 repo の一貫性を保証: `commons/docs/quality/production-grade-checklist.md` を参照

詳細: operator-commons ADR-0013 (audit-production-grade.sh)

## 4. GHA dual-track ポリシー (ADR-0019)

本 repo は RFC-0002 (GitHub Actions 永久禁止) の *例外* — public OSS operator の external trust gate のために GHA 14 workflow を維持しつつ、ローカル 4 階層 (lefthook) とのデュアルトラック運用 (ADR-0019)。

アップグレード時の GHA workflow 変更は `dependabot/github_actions/*` PR で自動化されます。*人手の PR* で `.github/workflows/` に新規ファイルを追加するには *別 ADR* + ユーザー承認が必要です。

## 5. 一般的なマイグレーションチェックリスト

アップグレード前:
- [ ] CRD 変更 (`api/v1alpha1/` の ObjectMeta が v1alpha2 と互換)
- [ ] `make verify` (lint + test + build + audit) が通る
- [ ] 既存の e2e スイートが PASS (`make integration-test`)
- [ ] dependabot 依存 bump PR の統合確認

アップグレード後:
- [ ] Helm chart の `dependencies:` (keiailab-commons library chart) を更新
- [ ] 各 CR の spec 互換性を検証 (特に storage、resources)
- [ ] reconcile 結果を確認 (`kubectl get postgrescluster -A`)
- [ ] 運用メトリクス (`Reconcile{Total,Latency,Errors}`) が正常

## 6. 非互換変更の通知ポリシー

- **Deprecation**: 新 minor で `// Deprecated:` コメント + 2 minor 後に削除
- **Breaking**: major bump + 本 UPGRADING.md の専用セクション + ADR を作成
- **事後通知は行わない**: あらゆる breaking 変更には *最小 1 minor* の事前 deprecation

## 参考

- ADR 一覧: `docs/kb/adr/INDEX.md`
- operator-commons UPGRADING: https://github.com/keiailab/operator-commons/blob/main/docs/UPGRADING.md
- audit: `make audit-quality` (5 repo を測定、commons ADR-0013)
- i18n: `commons/docs/i18n/README.md`

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
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
