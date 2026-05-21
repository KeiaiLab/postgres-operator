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
| [ADR-0006](0006-gitops-deploy-overlay.md) | Introduce the GitOps deploy overlay (3-repo alignment) | Accepted | 2026-05-06 |
| [ADR-0007](0007-pre-commit-instead-of-lefthook.md) | Hook tooling — pre-commit instead of lefthook (diverging from the org-wide lefthook standard) | Accepted | 2026-05-06 |
| [ADR-0008](0008-operator-commons-adoption.md) | Adopt operator-commons + harden the container `SecurityContext` invariant | Accepted | 2026-05-07 |
| [ADR-0009](0009-webhook-accumulate-errors.md) | Webhook validate — immediate-return → accumulate-errors (delegate to `commons.ValidateWithPredicate`) | Accepted | 2026-05-07 |
| [ADR-0010](0010-rfc-0017-tooling-unification-adoption.md) | Adopt RFC-0017 operator tooling unification (introduce lefthook + EventRecorder + HEALTHCHECK) | Proposed | 2026-05-09 |
| [ADR-0011](0011-rfc-0018-pkg-status-partial-adoption.md) | Partial RFC-0018 adoption — `pkg/status` (`Ready` type only) + asymmetric `pkg/finalizer` preserved (PR-A7 first cut) | Accepted | 2026-05-09 |
| [ADR-0012](0012-pkg-version-matrix-commons-delegation.md) | Delegate `pkg/version Matrix[Combo]` to commons (Plan §2 D12, PR-B3 — `go.mod` commons v0.7.0 bump) | Accepted | 2026-05-09 |
| [ADR-0013](0013-operatorhub-bundle-scaffold.md) | OperatorHub.io bundle scaffold cross-cut — operator-sdk 1.42 + kustomize, 2 owned CRDs, Makefile `bundle` / `bundle-build` (PR-B9, valkey ADR-0037 port) | Accepted | 2026-05-10 |
| [ADR-0014](0014-community-operators-sync-automation.md) | community-operators sync automation | Accepted | 2026-05-10 |
| [ADR-0015](0015-distributed-tx.md) | 분산 트랜잭션 — 2PC primary + saga deferred (G5 §D.10.2, RFC-0005 정합) | Accepted | 2026-05-16 |
| [ADR-0016](0016-former-adr-0015-force-reset-history.md) | 옛 ADR-0015 (RFC-0002 OSS CI 일탈) force-reset history codify — Option A: Accepted / Option B: Withdrawn (사용자 confirmation 대기) | Proposed | 2026-05-20 |
| [ADR-0017](0017-gha-retention-for-public-oss.md) | GitHub Actions Retention — Public OSS Operator External Trust Gate (S7 cycle 폐기, 본 문서는 history 보존 용) | Superseded by ADR-0018 | 2026-05-21 |
| [ADR-0018](0018-gha-to-local-4-layer.md) | GHA 전면 제거 → 로컬 4계층 단일 운영 (RFC-0002 strict, 14 workflow 전면 제거 + scripts/helm-publish.sh + scripts/release.sh + 3종 보강) | Superseded by ADR-0019 | 2026-05-21 |
| [ADR-0019](0019-gha-retention-for-public-oss.md) | GitHub Actions 유지 — operator family v2.0 통합 정합 (14 workflow 복원 + ADR-0018 phase 2/3 인프라 유지 + dual-track 운영) | Accepted | 2026-05-21 |
| [ADR-0020](0020-sprint-1-commons-pvc-topology-adoption.md) | Sprint 1 — operator-commons pkg/pvc + pkg/topology 채택 (-375 LOC, postgres callsite 2 + pvc 1 교체) | Accepted | 2026-05-21 |
| [ADR-0021](0021-rfc-0002-gha-block-hook.md) | RFC-0002 GitHub Actions Block — lefthook pre-commit hook 자동 강제 (.github/workflows/ 신규 파일 추가 차단, modify 허용, ADR-0019 dual-track 정합, commons ADR-0012 패턴 sync) | Accepted | 2026-05-21 |
| [ADR-0022](0022-gha-narrow-exception-3-workflows.md) | GHA Narrow Exception — 3 Workflow 보존 (helm-publish + release + scorecard, RFC-0002 §7 narrow exception, ADR-0019 amendment) | Accepted | 2026-05-21 |
| [ADR-0023](0023-repmgr-pgbouncer-barman-integration.md) | Repmgr / PgBouncer / Barman 통합 — bitnami parity (orphan number recovery from duplicate 0006) | Proposed | 2026-05-14 |
| [ADR-0024](0024-operatorhub-io-version-sync.md) | OperatorHub.io 최신 버전 자동 sync (orphan number recovery from duplicate 0007) | Proposed | 2026-05-14 |

## Archived (v0.x — decisions from before the redesign)

The v0.x cycle aimed at a "PGO-class full-stack + first-class Citus +
Plugin SDK" outcome. After the 2026-04-30 keystone redesign (new
ADR-0001), the v0.x ADRs are preserved *for historical reference only*.
New code / new decisions are *not* required to follow the conclusions of
this archive. The current principle is "external designs may be
referenced, embedding / wrapping external systems is forbidden, and we
implement as new services." That said, some archived ADRs (e.g. v0.x
ADR-0008 cascade delete, v0.x ADR-0009 no-github-actions-rfc-0002)
remain *consistent with current operational policy* — see the Notes
column.

| Number | Title | Notes |
|--------|-------|-------|
| [v0.x ADR-0001](_archive/v0.x/0001-stateless-query-router-on-citus.md) | Mission restated: PGO-class full-stack + first-class Citus + Plugin SDK | Superseded by active ADR-0001 |
| [v0.x ADR-0002](_archive/v0.x/0002-no-patroni-instance-manager.md) | No Patroni; instance manager + K8s API as DCS | Active — instance-manager pattern retained |
| [v0.x ADR-0003](_archive/v0.x/0003-queryrouter-stateless-design.md) | Stateless QueryRouter design | Active — `pg-router` follows the same principle |
| [v0.x ADR-0004](_archive/v0.x/0004-build-not-fork-or-layer.md) | Build from scratch (both PGO fork and soft-layer rejected) | Active — keystone policy of this repo |
| [v0.x ADR-0005](_archive/v0.x/0005-plugin-sdk-interface-model.md) | Plugin SDK interface model (in-process + gRPC) | Superseded — Plugin SDK retired (active ADR-0001) |
| [v0.x ADR-0006](_archive/v0.x/0006-security-defaults-rationale.md) | Dataplane PodSecurityContext defaults | Active — Pod security defaults retained |
| [v0.x ADR-0007](_archive/v0.x/0007-helm-chart-promoted-to-p1.md) | Move the Helm chart from P14 to the P1 track | Superseded by active ADR-0002 (single chart + flags) |
| [v0.x ADR-0008](_archive/v0.x/0008-finalizer-avoidance-policy.md) | Finalizer-avoidance policy (cascade delete via OwnerReference) | Active — cascade-delete pattern in operation |
| [v0.x ADR-0009](_archive/v0.x/0009-no-github-actions-rfc-0002.md) | Retire GitHub Actions + adopt the local 4-layer gate | Active — this repo's instance of the org-wide RFC 0002 policy |
| [v0.x ADR-0010](_archive/v0.x/0010-license-and-sharding-strategy.md) | License + sharding strategy (Citus AGPL isolation, vanilla PG default) | Superseded by active ADR-0001 (0 lines of Citus dependency) |

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

## Org-wide references

- Org-wide ADR standard: `~/Documents/ai-dev/standards/adr.md`.
- ADR-coverage gate: `scripts/check-adr-coverage.sh` (org-wide).
- Enforcement standard: `~/Documents/ai-dev/standards/enforcement.md §2.1`.
