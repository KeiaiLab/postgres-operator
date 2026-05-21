<p align="center">
  <b>English</b> |
  <a href="GOVERNANCE.ko.md">한국어</a> |
  <a href="GOVERNANCE.ja.md">日本語</a> |
  <a href="GOVERNANCE.zh.md">中文</a>
</p>

# Governance

This document defines how decisions are made in the keiailab/postgres-operator
project.

## Principles

1. **Open**: every decision happens on a public channel (GitHub issues / PRs /
   RFCs).
2. **Lazy consensus**: routine changes proceed when no one objects.
3. **Explicit consensus**: architectural changes, CRD changes, security-model
   changes, and license changes require an RFC and a **2/3 supermajority** of
   maintainers. Smaller RFCs (single component / tool adoption / policy
   reinforcement) require a **simple majority (>50%)**. Changes to GOVERNANCE
   itself (see "Amendments" below) always require a 2/3 supermajority.
4. **Shared accountability**: maintainers are jointly responsible for code
   quality, user safety, and community health.

## Decision classes

### Routine changes (lazy consensus)

- Bug fixes, documentation improvements, additional tests, dependency
  minor/patch upgrades, and refactors that do not change a public API.
- Process: PR → at least 1 maintainer LGTM → merge.
- Window: none required (a PR may merge as soon as the local gate passes;
  per RFC-0002 we do not use GitHub Actions — pre-commit / pre-push hooks
  plus the Makefile provide the gate).

### Medium changes (explicit consensus)

- New CRD fields, new reconcilers, dependency major upgrades, public API
  changes.
- Process: propose via issue → 7-day comment window → maintainer majority
  LGTM → merge.
- Single objection escalates to a maintainer meeting.

### Architectural changes (RFC required)

- Introducing a new component, changing the security model, changing the
  license, or any backward-incompatible change.
- Process:
  1. Submit an RFC at `docs/rfcs/NNNN-title.md`.
  2. 14-day comment window.
  3. ≥ 2/3 of maintainers in favor.
  4. Flip the RFC status `Draft → Accepted`, then open the implementation PR.
- A rejected RFC keeps the status `Rejected` (preserved for historical
  context).

## Roles

### Contributor

Anyone. May submit PRs and issues.

### Reviewer

A contributor who reviews regularly. May be added to CODEOWNERS. No merge
rights.

### Maintainer

See [MAINTAINERS.md](MAINTAINERS.md). Holds merge / approval rights.

### Lead maintainer

The keiailab organization representative. Final decision authority on
license, governance, and security policy.

## Maintainer meetings

- Monthly cadence (with ad-hoc sessions as needed).
- Minutes published under `docs/meetings/`.
- Agenda: dispute resolution, RFC discussion, roadmap review.

## Dispute resolution

1. Discuss in the PR/issue comments first.
2. If unresolved, add to the maintainer-meeting agenda.
3. Decided by a maintainer-majority vote.
4. The lead maintainer breaks ties.

## Licensing / intellectual property

- All contributions are licensed under Apache 2.0.
- DCO sign-off is mandatory.
- License changes require unanimous contributor consent (so they are
  effectively immutable).

## Amendments

This document can only be amended with a ≥ 2/3 maintainer supermajority.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
