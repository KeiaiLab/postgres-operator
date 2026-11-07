# Architecture Decision Records — INDEX

This directory preserves the non-trivial decisions of `postgres-operator`
in Nygard 5-section form so that the *reason* behind each decision
outlives the code.

Standard path: `<repo>/docs/kb/adr/` (per the org-wide
`standards/adr.md §1`).

## Active ADRs

| Number | Title | Status | Date |
|--------|-------|--------|------|
| [ADR-0001](0001-self-built-distributed-sql.md) | Build a self-built distributed-SQL layer on PostgreSQL | Accepted | 2026-04-30 |
| [ADR-0002](0002-single-chart-with-flags.md) | Helm single chart + component flags policy | Accepted | 2026-04-30 |
| [ADR-0003](0003-license-policy-no-agpl-busl.md) | External-dependency license policy (AGPL / BUSL / CSL / SSPL forbidden) | Accepted | 2026-04-30 |
| [ADR-0004](0004-crd-managed-by-operator.md) | The CRD lifecycle is owned by the operator manager | Accepted | 2026-04-30 |
| [ADR-0005](0005-versioning-and-channels.md) | Release channels (alpha / beta / stable) + CRD apiVersion evolution | Accepted | 2026-04-30 |
| [ADR-0006](0006-gitops-deploy-overlay.md) | Introduce the GitOps deploy overlay | Accepted | 2026-05-06 |
| [ADR-0007](0007-pre-commit-instead-of-lefthook.md) | Hook tooling — pre-commit instead of lefthook (diverging from the org-wide lefthook standard) | Accepted | 2026-05-06 |
| [ADR-0008](0008-operator-commons-adoption.md) | Adopt keiailab-commons + harden the container `SecurityContext` invariant | Accepted | 2026-05-07 |
| [ADR-0009](0009-webhook-accumulate-errors.md) | Webhook validate — immediate-return → accumulate-errors (delegate to `commons.ValidateWithPredicate`) | Accepted | 2026-05-07 |
| [ADR-0010](0010-rfc-0017-tooling-unification-adoption.md) | Adopt RFC-0017 operator tooling unification (introduce lefthook + EventRecorder + HEALTHCHECK) | Proposed | 2026-05-09 |
| [ADR-0011](0011-rfc-0018-pkg-status-partial-adoption.md) | Partial RFC-0018 adoption — `pkg/status` (`Ready` type only) + asymmetric `pkg/finalizer` preserved (PR-A7 first cut) | Accepted | 2026-05-09 |
| [ADR-0012](0012-pkg-version-matrix-commons-delegation.md) | Delegate `pkg/version Matrix[Combo]` to commons (Plan §2 D12, PR-B3 — `go.mod` commons v0.7.0 bump) | Accepted | 2026-05-09 |
| [ADR-0013](0013-operatorhub-bundle-scaffold.md) | OperatorHub.io bundle scaffold — operator-sdk 1.42 + kustomize, 2 owned CRDs, Makefile `bundle` / `bundle-build` (PR-B9, standard OperatorHub scaffold pattern) | Accepted | 2026-05-10 |
| [ADR-0014](0014-community-operators-sync-automation.md) | community-operators sync automation | Accepted | 2026-05-10 |
| [ADR-0015](0015-distributed-tx.md) | 분산 트랜잭션 — 2PC primary + saga deferred (G5 §D.10.2, RFC-0005 정합) | Accepted | 2026-05-16 |
| [ADR-0016](0016-former-adr-0015-force-reset-history.md) | 옛 ADR-0015 (RFC-0002 OSS CI 일탈) force-reset history codify — Option A: Accepted / Option B: Withdrawn (사용자 confirmation 대기) | Proposed | 2026-05-20 |
| [ADR-0017](0017-gha-retention-for-public-oss.md) | GitHub Actions Retention — Public OSS Operator External Trust Gate (S7 cycle 폐기, 본 문서는 history 보존 용) | Superseded by ADR-0018 | 2026-05-21 |
| [ADR-0018](0018-gha-to-local-4-layer.md) | GHA 전면 제거 → 로컬 4계층 단일 운영 (RFC-0002 strict, 14 workflow 전면 제거 + scripts/helm-publish.sh + scripts/release.sh + 3종 보강) | Superseded by ADR-0019 | 2026-05-21 |
| [ADR-0019](0019-gha-retention-for-public-oss.md) | GitHub Actions 유지 — v2.0 통합 정합 (14 workflow 복원 + ADR-0018 phase 2/3 인프라 유지 + dual-track 운영) | Accepted | 2026-05-21 |
| [ADR-0020](0020-sprint-1-commons-pvc-topology-adoption.md) | Sprint 1 — keiailab-commons pkg/pvc + pkg/topology 채택 (-375 LOC, postgres callsite 2 + pvc 1 교체) | Accepted | 2026-05-21 |
| [ADR-0021](0021-rfc-0002-gha-block-hook.md) | RFC-0002 GitHub Actions Block — lefthook pre-commit hook 자동 강제 (.github/workflows/ 신규 파일 추가 차단, modify 허용, ADR-0019 dual-track 정합, commons ADR-0012 패턴 sync) | Accepted | 2026-05-21 |
| [ADR-0022](0022-gha-narrow-exception-3-workflows.md) | GHA Narrow Exception — 3 Workflow 보존 (helm-publish + release + scorecard, RFC-0002 §7 narrow exception, ADR-0019 amendment) | Accepted | 2026-05-21 |
| [ADR-0023](0023-v3x-stable-baseline.md) | v3.x-stable baseline 인정 (audit ❌ 0 충족, CLAUDE.md §7 v3.x-stable 조건) | Accepted | 2026-05-21 |
| [ADR-0024](0024-lefthook-pre-push-incremental-lint-envtest.md) | lefthook pre-push 3 hook incremental gate (full-lint + markdown-link-check --new-from-rev, envtest binary 자동 보장) + 22 dead link 가시화 (RFC-0002 후 push 차단 회피) | Accepted | 2026-05-21 |
| [ADR-0025](0025-repmgr-pgbouncer-barman-integration.md) | Repmgr / PgBouncer / Barman 통합 — bitnami parity (orphan recovery from duplicate 0006; PR #98/#96 attempted 0023/0024 but collided with concurrent merges, renumbered to next free 0025) | Proposed | 2026-05-14 |
| [ADR-0026](0026-operatorhub-io-version-sync.md) | OperatorHub.io 최신 버전 자동 sync (orphan recovery from duplicate 0007; PR #98/#96 attempted 0024 but collided with concurrent merges, renumbered to next free 0026) | Proposed | 2026-05-14 |
| [ADR-0027](0027-non-ordinal-reshard-target-shard-identity.md) | G3 online-resharding 의 비-ordinal target shard 식별 모델 (격리 label `reshard-target` + phase 별 mergeable 증분 + #220 identity 교훈 반영, #223 설계) | Proposed | 2026-06-05 |
| [ADR-0028](0028-postgres-first-then-commons-dedup.md) | PostgreSQL 우선 구현 → commons 중복 제거는 후행 일괄 (4 operator 독립 유지, mongo freeze, G4 가 Phase 2 trigger, 승격 3-test + 어댑터 패턴) | Proposed | 2026-06-26 |
| [ADR-0029](0029-reshard-target-promotion-identity-transition.md) | resharding target shard 영구 승격 — 정체성 전이 설계 (ordinal→명명 shard 일반화 `shard-id` label + fenced single-authority 승격 + source 폐기, ADR-0027 P6 상세화, #220 교훈 반영) | Proposed | 2026-06-28 |
| [ADR-0030](0030-runtime-config-out-of-pod-template.md) | 승격 상태를 파드 템플릿 밖 런타임 ConfigMap 으로 (CR 정본 epoch·initialized + `configMapKeyRef` — 승격이 템플릿을 안 바꿔 failover 루프 차단) | Accepted | 2026-09-25 |
| [ADR-0031](0031-member-rebuild-hold-snapshot-budget.md) | 멤버 재구축을 hold · 스냅숏 · 예산 뒤로 (`MEMBER_HOLD`·`RESEED_REQUESTS` · readyToUse 직접 확인 뒤 해제 · 1h 예산 CR 원장 · data-bearing 스냅숏 자동 삭제 0) | Accepted | 2026-09-25 |
| [ADR-0032](0032-switchover-stops-old-primary-in-place.md) | switchover 는 옛 primary 를 제자리에서 멈춘 뒤 승격한다 (fast shutdown → 같은 컨테이너가 standby 로 재기동 → 끝 LSN 이상 승자 승격 · 파드 삭제 0 · 진행 중 failover 는 실행만 보류) | Accepted | 2026-09-29 |
| [ADR-0033](0033-staged-rollout-on-delete.md) | 샤드 멤버는 OnDelete 로 두고 오퍼레이터가 하나씩, primary 는 마지막에 교체한다 (옛 replica 하나씩 — streaming 아닌 것 → 서수 높은 것 · 대상 밖 Ready ∧ streaming · 제자리 switchover 뒤 옛 primary 교체 · RolloutBlocked) | Accepted | 2026-09-29 |
| [ADR-0034](0034-no-network-trust.md) | pg_hba 는 어떤 네트워크 대역도 trust 하지 않는다 (local trust + host scram · postgres 비밀번호는 오퍼레이터가 Secret 에 맞춘다(SCRAM verifier 만 exec) · 확인 전 설정·템플릿 유지 · passfile + PGPASSFILE · 적재 기준 판정·낡은 멤버만 reload) | Accepted | 2026-09-29 |
| [ADR-0035](0035-pg-settings-owned-by-operator.md) | PG 설정은 spec 에서 렌더하고 오퍼레이터가 소유한다 (limits.memory → shared_buffers ÷4 · effective_cache_size ×3/4 · walKeepSize·maxConnections · 렌더 키의 auto.conf 항목은 파일 투영 뒤 값이 크지 않을 때만 RESET, 더 크면 남기고 AutoConfOverride) | Accepted | 2026-09-29 |
| [ADR-0036](0036-failover-fences-old-primary-and-cuts-standbys.md) | failover 는 옛 primary 를 멈추고 생존 standby 의 수신을 끊은 뒤 승격한다 (펜스 exec fast→immediate→SIGKILL · 생존자 primary_conninfo '' · 끝 최대 승자 · 같은 reconcile 재지정 · S6b = iptables 끊기 + primary 트리 SIGSTOP) | Accepted | 2026-11-07 |
| [ADR-0037](0037-i26-instance-manager-changes-carry-adr.md) | I26 — 인스턴스 매니저·pg 이미지 변경은 `I26-ADR:` 트레일러와 Accepted ADR 로 착지 (기점 = origin/stable · 17369c33·322546d6 소급 승인) | Accepted | 2026-11-07 |
| [ADR-0038](0038-pg-image-toolchain-security-patches.md) | pg 이미지 Go toolchain 보안 패치(소스 무변 · govulncheck GO-ID)는 `I26-ADR: 0038` 로 승인된 I26 변경 · golang 단계 ≥ go.mod toolchain | Accepted | 2026-11-07 |
| [ADR-0039](0039-loopdrill-host-stall-credit.md) | loopdrill J10 은 두 계측기가 함께 멈춘 시간만 공백에서 뺀다 (탐침 due·sent + 드라이버 정지 시계 교집합 · H1 관측 · `green-host-stalls` · 30 s 이상 정지는 `host-stalled` 재실행 · 예산 무변 · rejudge 는 기록만) | Accepted | 2026-11-08 |

## Archived (v0.x — decisions from before the redesign)

The v0.x cycle aimed at a production-grade PostgreSQL operator with a
third-party sharding extension and a Plugin SDK. After the 2026-04-30
keystone redesign (new ADR-0001), the v0.x ADRs are preserved *for
historical reference only*. New code / new decisions are *not* required
to follow the conclusions of this archive. The current principle is
"embedding / wrapping external systems is forbidden, and we implement as
new services." That said, some archived ADRs (e.g. v0.x ADR-0008 cascade
delete, v0.x ADR-0009 no-github-actions-rfc-0002) remain *consistent
with current operational policy* — see the Notes column.

| Archived slot | Topic | Notes |
|--------|-------|-------|
| v0.x ADR-0001 | Earlier mission statement (now retired) | Superseded by active ADR-0001 |
| v0.x ADR-0002 | Self-built instance manager + K8s API as DCS | Active — instance-manager pattern retained |
| v0.x ADR-0003 | Stateless QueryRouter design | Active — `pg-router` follows the same principle |
| v0.x ADR-0004 | Build from scratch (no fork or soft-layer) | Active — keystone policy of this repo |
| v0.x ADR-0005 | Plugin SDK interface model (in-process + gRPC) | Superseded — Plugin SDK retired (active ADR-0001) |
| v0.x ADR-0006 | Dataplane PodSecurityContext defaults | Active — Pod security defaults retained |
| v0.x ADR-0007 | Move the Helm chart from P14 to the P1 track | Superseded by active ADR-0002 (single chart + flags) |
| v0.x ADR-0008 | Finalizer-avoidance policy (cascade delete via OwnerReference) | Active — cascade-delete pattern in operation |
| v0.x ADR-0009 | Retire GitHub Actions + adopt the local 4-layer gate | Active — this repo's instance of the org-wide RFC 0002 policy |
| v0.x ADR-0010 | Earlier license + sharding strategy (AGPL third-party extension isolation, vanilla PG default) | Superseded by active ADR-0001 (0 lines of third-party extension dependency) |

## Authoring guide

- Format: Nygard 5 sections (Context / Decision / Consequences /
  Alternatives Considered / Status).
- Location: `docs/kb/adr/NNNN-<english kebab-case slug>.md` (org-wide
  standard).
- Numbering: 4-digit zero-padded; numbers are never reused.
- This `INDEX.md` must be updated by hand whenever a new ADR is added —
  see `standards/enforcement.md §2.1`.
- The v0.x archive is a history-preservation area; new decisions live in
  the active section only.
