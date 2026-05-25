# ADR-0007: Adopt pre-commit as the hook tool (divergence from the global lefthook standard)

- Date: 2026-05-06
- Status: Accepted
- Authors: @eightynine01

## Context

The global standard `standards/enforcement.md §1.1` specifies **lefthook** (Go single-binary, language-neutral) as the git-hook management tool. This repo operates **pre-commit** (Python-based) via `.pre-commit-config.yaml`, diverging from the standard.
<!-- live-verified: 2026-05-09 -->

This repo's `.pre-commit-config.yaml` is explicitly mapped to the 4-tier gates of RFC 0002 (`standards/ci.md §1`) (L1 pre-commit → golangci-lint, L2 pre-push → test/audit/secrets/go-mod-tidy-drift); see the active GHA-removal ADRs ([0018](0018-gha-to-local-4-layer.md), [0021](0021-rfc-0002-gha-block-hook.md)) for the canonical record.

## Decision

This repo **keeps pre-commit**. Migration schedule is undecided (see the Consequences section for trigger conditions).

## Rationale

1. **RFC 0002 gate mapping already applied** — `.pre-commit-config.yaml` explicitly uses L1/L2 stages (`stages: [pre-commit]` / `stages: [pre-push]`). A lefthook migration would require rewriting the equivalent mapping.
2. **pre-commit is a GitHub-recognized standard** — broad hook ecosystem (built-ins such as trailing-whitespace) + autofix_prs CI integration.
3. **Functionally equivalent** — both use the same git-hook mechanism. lefthook's strengths (Go single-binary, language-neutral) are not decisive advantages in this *Go project*.
4. **Migration cost vs. value is low** — insufficient justification for replacing working infrastructure.

## Consequences

### Positive
- Reuse of existing hook infrastructure — zero regression risk.
- L1/L2 mapping is explicit, aligned with the RFC 0002 4-tier gates ✓.

### Negative
- Divergence from the global `enforcement.md §1.1` — divergence may be flagged in the P0 alignment column of `governance-report`.
- Divergence from the org-wide lefthook standard → new contributors may need to learn both tools.

### Migration trigger (when to switch to lefthook)

Switch this ADR to *Superseded* and migrate to lefthook if any of the following occurs:

1. The org-wide lefthook operations have been stable for 6+ months and a *clear advantage* is found.
2. pre-commit itself develops a security issue / maintenance-discontinuation signal.
3. The global RFC is updated to mandate lefthook.
4. When adding a new hook, a feature only supported by lefthook becomes required.

## Alternatives Considered

### A. Immediate migration to lefthook
- pros: 100% alignment with the global standard.
- cons: requires rewriting RFC 0002 mappings + regression risk.
- Reason for rejection: value < cost.

### B. (Adopted) Document divergence via ADR + define migration triggers
- pros: explicit traceability, basis for future unification decisions.
- cons: short-term tooling divergence remains.

## Global references

- Standard: `standards/enforcement.md §1.1`
- Operation in this repo: `.pre-commit-config.yaml`
- Related ADRs: [ADR-0018](0018-gha-to-local-4-layer.md), [ADR-0021](0021-rfc-0002-gha-block-hook.md) (current canonical records for the 4-tier gate mapping)

<!-- live-verified: 2026-05-09 -->
