# ADR-0017: GitHub Actions Retention — Public OSS Operator External Trust Gate

| Meta | Value |
|---|---|
| Status | Superseded by ADR-0018 |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | (none) |
| Superseded by | ADR-0018 (GHA 전면 제거 → 로컬 4계층 단일 운영) |
| Related | ADR-0014 (community-operators sync automation), ADR-0016 (former ADR-0015 force-reset history) |

> **2026-05-21 supersede**: maintainer 의 S7 cycle 결정으로 본 ADR 의 *유지* 노선은 폐기되고, ADR-0018 (RFC-0002 strict 적용 + 14 workflow 전면 제거) 로 대체되었다. 본 문서는 history 보존 용도로만 유지된다.

## Context

Global RFC-0002 (GitHub Actions Permanent Ban, 2026-04-29) was triggered by the
2026-04-28 organization billing outage which caused a 24h+ merge freeze across
all repos. The intent: avoid single-SaaS SPOF for *internal infrastructure
repos*.

However, public open-source K8s operators have *different* requirements:

1. External contributors need a *trusted, automated gate* to verify their PRs.
   They cannot run the maintainers' private lefthook profile or `make verify`
   on a non-Linux laptop in reasonable time.
2. Security scanners (CodeQL, OpenSSF Scorecard, Trivy) are *external trust
   signals* recognized by downstream consumers. The Scorecard badge on
   Artifact Hub / OperatorHub is part of the package metadata.
3. Helm chart auto-publish (GitHub Pages) and release artifact signing
   (cosign + SLSA) are *part of the public release workflow* — Pages
   distribution is the chart's only canonical URL.
4. dependabot/renovate require GHA-compatible `package-ecosystem` scanning
   to drive their PR cadence.

This ADR consolidates the partial exception ADRs already recorded in this repo
— **ADR-0014** (community-operators sync automation; RFC-0002 §7 Exception ③
extension) and **ADR-0016** (former ADR-0015 force-reset history; codifies the
14-workflow scoped deviation already in effect on `main`) — into a single
*integrated rationale* for the full `.github/workflows/` retention. The prior
partial ADRs each justified a slice of the deviation; this ADR is the
SSOT for the *whole* `.github/workflows/` directory in this repo.

## Decision

Retain `.github/workflows/` (14 workflow files) with **dual operation**:
GitHub Actions primary gate + local 4-tier (pre-commit, pre-push, Makefile,
PR reviewer evidence check) as fallback. This depth-defense pattern
mitigates the SPOF risk that motivated RFC-0002.

### Workflow Classification (14 files in this repo)

| Category | Workflows (this repo) | Rationale |
|---|---|---|
| **External Trust Gate** | `codeql.yml`, `scorecard.yml`, `dco.yml`, `dependency-review.yml`, `kube-linter.yml`, `security-scan.yml`, `go-licenses.yml` | External-recognized security/compliance signals; downstream consumers verify the Scorecard badge; CodeQL's deep static-analysis dataflow exceeds what local `gosec` can express; DCO maintains the Signed-off-by trail required for community-operators upstream; `go-licenses` blocks AGPL/BUSL/CSL/SSPL re-introduction (per ADR-0003). |
| **Auto Deploy** | `helm-publish.yml`, `release.yml` | RFC-0002 §7 Exception ① (GitHub Pages) and Exception ③ (release tag → GitHub Release body). Auto Helm chart publish to `gh-pages` plus cosign-signed release artifacts. `release.yml` also hosts the community-operators sync job per ADR-0014. |
| **Local 4-Tier Backup** | `ci.yml` (lint+test+build), `helm-lint.yml`, `helm-install-test.yml`, `markdown-link-check.yml` | Same checks also enforced by pre-commit / pre-push / Makefile (per ADR-0007). GHA is primary; local is depth-defense. If GHA is down, maintainers can still merge using `make verify` + local hooks. |
| **Ops Tools** | `stale.yml` | Issue / PR lifecycle automation; not a merge gate. Safe to lose during a GHA outage. |

### Branch protection alignment

`main` branch protection lists the GHA job names from the **External Trust
Gate** and **Local 4-Tier Backup** categories as `required_status_checks`.
Maintainers must keep this list in sync when renaming jobs in workflow files;
divergence is treated as an operational defect (the operational discipline
note in §Consequences applies).

## Consequences

**Positive**:

- External contributors see clear, automated PR gates without needing local
  setup parity.
- Downstream consumers verify external security signals (CodeQL findings on
  the Security tab, Scorecard badge, DCO compliance trail).
- Helm chart auto-publish to GitHub Pages keeps release velocity (cuts the
  manual `helm package` + `gh-pages` push step out of every release).
- dependabot/renovate operational without an extra runner SaaS.
- The two prior partial-exception ADRs (0014, 0016) now have a single
  upstream consolidated decision — easier to reference from PR reviews.

**Negative**:

- GHA SPOF risk remains. Mitigated by the local 4-tier fallback: every gate
  in the External Trust Gate and Local 4-Tier Backup categories has a local
  equivalent that maintainers can run when GHA is down.
- Some workflow files (notably `ci.yml`, `helm-lint.yml`) overlap with local
  hooks. Accepted for depth-defense; the marginal maintenance cost of
  keeping the workflow YAML in sync is small.
- Branch protection's `required_status_checks` list must stay in sync with
  workflow job names. Treated as operational discipline; a rename of a job
  in `ci.yml` without updating branch protection silently disables that
  gate, so renames go through PR review.

**Neutral**:

- All RFC-0002 §7 stated exceptions (Pages, dependabot, release) are already
  covered. This ADR is a *broader integrated rationale* that explains why
  the 14-file retention as a whole is correct for *this class of repo*
  (public OSS operator), not a request to add new exceptions.

## Alternatives Considered

1. **Strict RFC-0002 (remove all workflows)** — Rejected. External
   contributor trust loss; the Scorecard badge would disappear from
   Artifact Hub; CodeQL findings on the Security tab would empty; release
   automation regression. Once attempted by commit `3c69429` (per the
   per the parallel history in another operator); the consequences
   were severe enough that the workflows were restored.
2. **Partial removal (keep External Trust Gate only, remove `ci.yml` and
   `helm-lint.yml`)** — Rejected. Inconsistency with the established pattern
   across other operators which retain the full set; the
   local 4-tier duplicate would add maintenance burden without clear
   benefit. Depth-defense value is small but non-zero and the cost is low.
3. **GHA-only (drop the local 4-tier)** — Rejected. Re-introduces the
   exact SPOF that RFC-0002 was created to address; the 2026-04-28 incident
   already demonstrated this failure mode (24h+ org-wide merge freeze).

## References

- **RFC-0002** (2026-04-29) — Global GHA permanent ban (internal-infra
  intent).
- **Related ADRs (this repo)**:
  - [ADR-0014](0014-community-operators-sync-automation.md) — RFC-0002 §7
    Exception ③ extension for community-operators sync automation.
  - [ADR-0016](0016-former-adr-0015-force-reset-history.md) — codifies the
    14-workflow scoped deviation already in effect on `main` and its
    force-reset history.
- **Incident KB**: I-2026-04-28 (GHA billing outage; RFC-0002 trigger).
- **Related repo policy**: [ADR-0003](0003-license-policy-no-agpl-busl.md)
  is enforced in CI by the `go-licenses.yml` workflow listed in the
  External Trust Gate category.

## Implementation

No code changes. Status `Proposed` → `Accepted` upon merge of this ADR. The
existing 14 workflow files in `.github/workflows/` remain as-is; this ADR
documents the *why* of their retention. Branch protection's
`required_status_checks` list is unchanged.
