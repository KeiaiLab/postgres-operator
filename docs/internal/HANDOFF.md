# HANDOFF — postgres-operator

> The next session must be able to resume *without conversation
> context*. Reading order on startup: this file → `TASKS.md` → the
> latest commit log.

## Current state (2026-05-21, 0.3.0-alpha.18)

- Release: `0.3.0-alpha.18` — GHCR image + Helm chart published.
  Chart `version` and `appVersion` plus `config/manager/kustomization.yaml`
  `newTag` and the `dist/install.yaml` image tag are all aligned (a
  `make validate` assertion blocks future drift).
- OLM bundle: `bundle/manifests/` is aligned to **8 owned CRDs**
  (PostgresCluster, BackupJob, ScheduledBackup, Pooler,
  PostgresDatabase, PostgresUser, ImageCatalog, ClusterImageCatalog),
  with alm-examples for 7 of them and CSV descriptions for all 8.
  `operator-sdk bundle validate --select-optional suite=operatorframework`
  is clean. Bundle package name: `keiailab-postgres-operator`.
- community-operators draft PR:
  [k8s-operatorhub/community-operators#8109](https://github.com/k8s-operatorhub/community-operators/pull/8109).
  Remaining: maintainer-owned bundle image push to
  `ghcr.io/keiailab/postgres-operator-bundle:0.3.0-alpha.18` (requires a
  PAT with `write:packages` scope) → CI green → flip to Ready → merge.
- production cluster: ArgoCD `platform-data-postgres-operator`
  Synced/Healthy, `PostgresCluster/postgres` Ready=True (Day-0
  single-shard, no HA replicas yet).

## Active work

| Task | Stage / % | Notes |
|---|---|---|
| T26 OSS/OLM standards alignment | Complete 100% | spdystream CVE fix, OLM bundle, CHANGELOG/SUPPORT, README, 4 new `make validate` gates, lefthook DCO enforced. |
| T27 Live kind smoke + Pooler built-in auth + password rotation | Complete 100% | PG18 iter#8 → 5/5 PASS + PG17 → 5/5 PASS across all five new-CRD smoke scenarios (Pooler 4-test, PostgresDatabase 2-test, PostgresUser 2-test, ScheduledBackup 2-test, ImageCatalog 2-test). Six latent issues were root-caused and fixed end-to-end across iter#3..#8 (PostgresDatabase/User finalizer race + statusUpdate IsConflict swallow, Pooler missing Watches(PostgresCluster), eval SQL re-tokenisation, psql -U postgres OS-user mismatch, smoke.sh missing rollout-restart after image load, PostgresUser missing DROP ROLE finalizer). All fixed with unit-test regression guards. |
| T28 community-operators PR | Implementation 60% | Draft PR opened (#8109). Awaiting bundle image push + CI. |
| T29 Pooler TLS auto-issuance | Complete 100% | cert-manager `Certificate` CR auto-issuance via `spec.pgbouncer.autoTLS` (stages 1+2 spec + controller). **Stage 3** cert-manager kind drill → 6/6 PASS. **Stage 4 self-signed fallback** (`spec.pgbouncer.autoTLS.selfSigned: true`) — operator generates in-process RSA-2048 + x509 self-signed cert (1-year validity, 30-day renewal skew), writes the same `tls.crt`/`tls.key`/`ca.crt` Secret layout cert-manager produces, so no end-user manifest change is needed when migrating between environments. **Stage 5 rotation observability** — `Pooler.Status.AutoTLSClientCertNotAfter` / `AutoTLSServerCertNotAfter` and `priority=1` printer columns. T29 complete. |

| T30 HA bootstrap fence race | Complete 100% | Final fix shipped: (i) `IsStandby(dataDir)` short-circuit, (ii) `promotedAtLeastOnce` guard, (iii) **standby-pod election downgrade** — pods that boot with `standby.signal` on disk take Follower election, never contest the lease, and (iv) `handleStoppedLeading` is now side-effect-free. Failover is exclusively operator-driven (`executeClusterPromotion`). Live PG18 SHARD_REPLICAS=1 5/5 PASS + streaming, PG17 SHARD_REPLICAS=1 5/5 PASS + streaming, SHARD_REPLICAS=0 both PG18 / PG17 5/5 regression-free. |
| T31 G1 rejoin/sync 라이브 drill 자동화 | Complete 90% | `hack/smoke.sh` 에 `SMOKE_REJOIN` (basebackup + pg_rewind) + `SMOKE_SYNC` (RPO=0 + opt-in kill) 두 환경변수 단계 추가. 라이브 evidence (2026-05-17, fresh kind PG18 SHARD_REPLICAS=1): **B.1~B.3 RPO=0 PASS** (`commit_lsn=0/3DA43A0 / flush_lsn=0/3DA43A0 / pg_wal_lsn_diff=0`, drill_sync commit dca3fa0); **A.1 basebackup rejoin PASS** (`quickstart-shard-0-1` standby PVC delete → fresh basebackup → `streaming sync_state=async lag=0`). ROADMAP G1 `Replica rejoin` + `Synchronous replication` 양쪽 `[~]→[x]`. **A.2 pg_rewind 라이브 drill** + **SMOKE_FAILOVER operator-driven promotion 라이브 trigger** 회귀 = 별 task (`docs/g1-ha-election-fact-fix` 영역 위임). |
| T32 Gate 진척 turn 2026-05-19 (~26 sub-task / 21 commit) | Complete 100% | **G1**: D.1.1 PVC fence runbook (L76 `[~]→[x]`, pure 함수 + 158-line runbook + 5 sub-test), D.2.3 Upgrade runbook (L92 stub→complete 36→206 lines), D.3.1 WAL-G + Barman plugin (L89 `[~]→[x]`, 양 plugin 13+12 sub-test). **G2**: D.5.2 PrometheusRule alert count verify (L106 `[~]→[x]`, 8 alerts ≥8), D.5.8 object grants DSL (L133 `[~]→[x]`, `internal/postgres/grants.go` 13 sub-test), D.6.1 built-in TLS auto-issuance (L126 `[ ]→[x]`, RSA-2048 self-signed + ShouldRenew 30d skew + 9 sub-test), D.6.4 PSA + default-deny NetworkPolicy (L135 `[ ]→[x]`, 4-5 정책 renderer + 5 sub-test). **G3**: D.8.2 vindex policy branching (L150 `[~]→[x]`, 4 vindex 분기 + 자체 murmur3 + overlap detection + 9 sub-test), D.8.3 metadata store Postgres catalog (L151 `[ ]→[x]`, Store interface + PostgresStore + 2-version SchemaMigrations + 9 sqlmock sub-test), D.8.8 placement + drift guard (L156+L157 `[ ]→[x]`, 6 PlacementDriftReason + 9 sub-test). **G5**: D.10.1 scatter-gather 실 구현 (L180 `[~]→[x]`, ShardExecutor pluggable + FailFast/BestEffort + MergeConcat/OrderBy + 9 sub-test), D.10.2 2PC coordinator real state machine (L181 `[~]→[x]`, Begin/Enlist/Prepare/Commit/Rollback + 5 state + InDoubt + 8 sub-test). 본 turn worktree: `.claude/worktrees/postgres-operator-gates` (T33 P1.2 squash 통합 완료). |
| T33 supercycle ship readiness | In progress | `~/.claude/plans/postgres-operator-supercycle-T33.md` 추적. P1 (git hygiene + T32 통합) 완료. P2 (root 정책 = 사용자 actual 정합, root 유지) 폐기 / P3 (legacy keyword 0 + i18n sync) / P4 (lint + OLM bundle validate) / P5 (release sanity + release.yml multi-arch fix) / P6 (cleanup). Codex review `019e4aa5-55a6-74c2-b003-d595e7232c55` 8 challenge 반영. |

## Local 4-layer gate

The release gate is enforced locally (GitHub Actions is permanently
forbidden per ADR-0009 / RFC-0002).

```bash
make hooks-install       # lefthook pre-commit / commit-msg / pre-push
make gate                # lint + test + audit + validate (one-shot)
make validate            # CRD count + 18 monitoring grep + version drift + sdk validate + kube-linter
make audit               # govulncheck + trivy fs HIGH/CRITICAL + gosec
```

`make validate` enforces (among others):

- `bundle/manifests/postgres.keiailab.io_*.yaml` count ≥ 8.
- `operator-sdk bundle validate ./bundle` (default + operatorframework suite).
- Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag drift.
- `.github/workflows/` absence (RFC-0002 / ADR-0009).
- kube-linter on `dist/install.yaml` and the helm-template output.

## 최종 차단점 (single-session ceiling, 2026-05-19 최종)

본 turn ~26 sub-task 마감 (G1: D.1.1+D.2.3+D.3.1+D.3.2+D.4.1 / G2: D.5.2+D.5.4+D.5.5+D.5.6+D.5.7+D.5.8+D.5.9+D.5.10+D.5.11+D.6.1+D.6.3+D.6.4+D.6.5 / G3: D.8.2+D.8.3+D.8.8 / G4: D.9.1+D.9.2+D.9.3-9+D.9.10 + D.1.2+D.1.3+D.2.2 e2e / G5: D.10.1+D.10.2 / G6: D.11.4+D.11.5+D.11.6+D.11.7). ROADMAP 기준 [x] 83건 / [ ] 9건 / [~] 12건. 잔여 9 [ ] 항목 분석:

| Plan ID | 항목 | 차단 근거 | 최소 소요 |
|---|---|---|---|
| D.11.1 | G6 7-day soak | ROADMAP L191 "NON-GOAL single session — 7-day wall clock required" | 7 일 + 측정 |
| D.11.2 | G6 chaos (pod kill / netpart / disk) | ROADMAP L192 "multi-day chaos drill required" | multi-day |
| D.11.3 | G6 restore rehearsal cron | ROADMAP L193 "monthly cron drill — out of single session" | monthly + 7 PASS |
| D.9.* (11 sub-task) | G4 ShardSplitJob 7-step e2e | 수개월 분산 DB 엔지니어링 (Snapshot+WAL / bootstrap / initial copy / CDC catch-up / cutover / routing / cleanup) | 수개월 |
| D.8.4-7 | pg-router SQL parser + libpq passthrough | wire protocol v3 직접 구현, prepared statement / cursor edge case | 수개월 |
| D.5.3 / D.5.5 / D.5.10 | live Grafana / Prometheus / cross-cluster drill | 라이브 클러스터 + observability stack 접근 | 라이브 의존 |
| D.2.4 / D.10.4 | RTO/RPO + benchmark 실 측정 | 라이브 클러스터 + cluster mesh 복원 | 라이브 의존 |

진정한 진척: 본 turn 3 sub-task (D.1.1 + D.2.3 + D.8.2) 코드+런북+테스트 마감 — *세션 내 closeable 영역의 leverage* 가 한도. 잔여 51 sub-task 는 *time/live-resource bound*. 사용자 의사결정 필요: ① 다음 turn 별도 closeable item (D.6.4 PSA hardening / D.5.8 object grants / D.10.2 2PC 실 구현 등) 진행 ② 라이브 클러스터 mesh 복원 후 D.2.4/D.5.3/D.10.4 진행 ③ G4/G5 multi-month roadmap 별도 sprint 분리.

## Next-session entry points

### To finish T28 (community-operators)

1. Maintainer obtains a PAT carrying `write:packages` scope and authenticates:
   `gh auth refresh -s write:packages` or
   `echo $PAT | docker login ghcr.io -u eightynine01 --password-stdin`.
2. `make bundle-build VERSION=0.3.0-alpha.18` (already passes; just rebuild if needed).
3. `docker push ghcr.io/keiailab/postgres-operator-bundle:0.3.0-alpha.18`.
4. On PR [#8109](https://github.com/k8s-operatorhub/community-operators/pull/8109)
   wait for the community-operators CI; flip the PR to Ready when green.

### To finish T29 (Pooler TLS)

- Stage 3 — live cert-manager kind drill: run
  `hack/smoke-cert-manager.sh` after a successful
  `./hack/smoke.sh --keep`. The helper installs cert-manager,
  creates a self-signed `Issuer`, applies a
  `Pooler` with `spec.pgbouncer.autoTLS.clientEnabled=true`, and
  verifies the operator emits a cert-manager `Certificate` CR
  whose issued Secret is mounted onto the Pooler Deployment.
- Stage 4 — self-signed fallback for cert-manager-less environments.
- Stage 5 — automatic rotation observability (status condition that tracks
  cert-manager `Certificate.status.notAfter`).

### To finish T27 (live kind smoke)

- Run the 5 scenarios × PG17 / PG18 matrix:
  ```bash
  SMOKE_DATABASE=1 SMOKE_USER=1 SMOKE_POOLER=1 \
  SMOKE_SCHEDULEDBACKUP=1 SMOKE_IMAGECATALOG=1 \
  CR_NAME=quickPG18 SHARD_REPLICAS=0 \
  ./hack/smoke.sh
  ```
  Repeat with `PG_MAJOR=17 POSTGRES_VERSION=17 CR_NAME=quickPG17`.
- Record the result in `docs/operator-guide/cross-validation-cnpg.md`
  (move the matching rows from ⚠️ → ✅).

### To rotate the Pooler built-in auth password (T27 ⑥)

```bash
kubectl annotate pooler <name> postgres.keiailab.io/rotate-pooler-password=true --overwrite
# The operator runs ALTER ROLE, updates the userlist.txt Secret in place,
# strips the annotation, and records status.builtinAuthLastRotation.
```

## Audit cleanup cycle (2026-05-21)

본 cycle 은 2026-04-30 ~ 05-21 동안 누락된 잔재 항목을 정리한다 (main HEAD
변화: 38670f3 → 10f8339 → b0b0e22 → …):

- **#101** — ADR 0006/0007 orphan ID 중복 정리 (`0025-repmgr-pgbouncer-barman-integration.md` +
  `0026-operatorhub-io-version-sync.md` 로 안전 renumber, PR #98/#96 시도 후
  잔존하던 0006/0007 stale 파일을 INDEX 정합). ARCHITECTURE.md ADR cross-link
  본문도 17 → 24 ADR 정합 + ARCHITECTURE.{ko,ja,zh}.md 다국어 3 파일 동시 추가
  (markdown-link-check 정합).
- **#102** — Renovate 제거 (Issue #18 13일째 미해결 closes). Dependabot 단일
  운영 (RFC-0002 narrow exception ② 정합).
- **다국어 33 파일 PR** (별 PR) — root-level + `docs/UPGRADING` 의 ko/ja/zh
  누락 33 파일 작성 (CODE_OF_CONDUCT/SECURITY/MAINTAINERS/ADOPTERS/SUPPORT/
  GOVERNANCE/CONTRIBUTING/BRANDING/ROADMAP/AGENTS/docs/UPGRADING × 3 lang).
- **본 PR** — `docs/superpowers/{specs,plans}/2026-05-17-g1-rejoin-sync-drill-*.md`
  완전 삭제 (T31 진행 산출물, ROADMAP G1 `[x]` 마감 후 dead artifact) +
  TASKS.md / HANDOFF.md 인용 정합.
- **stable FF** (별 task) — main HEAD 로 stable 정합.

## Reference

- `README.md` — quickstart and the 8 CRD surface table.
- `ROADMAP.md` and `docs/roadmap.md` — Gates G0–G6 with sub-task checklists.
- `TASKS.md` — full P1 task table (F01a–T32) and the next-phase preview.
- `CHANGELOG.md` — Keep-a-Changelog history through 0.1.1-alpha → 0.3.0-alpha.18.
- `docs/operator-guide/cross-validation-cnpg.md` — feature matrix vs CloudNativePG.
- `docs/operator-guide/community-operators-onboarding.md` — community-operators PR procedure.
- `SUPPORT.md` / `SECURITY.md` / `CONTRIBUTING.md` / `GOVERNANCE.md` / `MAINTAINERS.md` — community policy surface.
