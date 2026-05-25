# ADR-0010: Adopt RFC-0017 operator tooling unification

- Date: 2026-05-09
- Status: Proposed
- Authors: @eightynine01
- Tags: tooling, ci, hook, lint, event-recorder

## Context

RFC-0017 (operator tooling unification) proposes tooling unification across operator repos. This ADR records the adoption decision on the postgres-operator side and the changes scoped to this repo.

Current state of this repo (2026-05-09 audit):
- Hook: `.pre-commit-config.yaml` (not lefthook) — violates RFC-0017 §3.1
- `.golangci.yml`: present (18 linters) ★ **the source-of-truth for RFC-0017 §3.2**
- `.custom-gcl.yml`: present (logcheck plugin) ★ **source-of-truth**
- Makefile: lint/test/validate/audit all present — ✓
- Dockerfile: distroless static base — RFC-0017 §3.5 (HEALTHCHECK) was withdrawn, so N/A. Need to verify the helm chart probe alignment.
- **EventRecorder: ✗ completely unimplemented** — Recorder field / GetEventRecorderFor / Eventf calls are *all absent* (2026-05-09 grep verification). It is simply unimplemented, not an intentional test isolation. A direct target of RFC-0017 §3.4.

## Decision

Adopt RFC-0017 as **Accepted** and in this repo:

1. Add a new `.lefthook.yml` (standard lefthook pattern + this repo's postgres-specific hook integration)
2. Remove `.pre-commit-config.yaml` (DAY 2)
3. No changes to `.golangci.yml` / `.custom-gcl.yml` (already the standard)
4. **Introduce the EventRecorder** — `mgr.GetEventRecorderFor("postgres-cluster-controller")` + Eventf calls inside the Reconciler to eliminate the FakeRecorder TODO
5. ~~Add HEALTHCHECK to Dockerfile~~ — withdrawn (RFC-0017 §3.5 incompatible with distroless). Instead, verify helm chart probe coverage.

## Consequences

### Positive
- This repo's linter is promoted to the standard source → the existing ADR-0005 (depguard rule blocking Plugin SDK bypass) is elevated to *part of the cross-operator standard*
- With the EventRecorder introduced, the K8s standard operational signal (Events in kubectl describe) is exposed — eases operator debugging
- govulncheck / gitleaks / go-mod-tidy drift enter pre-push

### Negative / Trade-offs
- If introducing the EventRecorder, *if FakeRecorder's intent was intentional (test isolation)* there will be a test impact — needs verification (RFC §7 open question)
- Existing contributors must run `lefthook install` once

### Follow-up
- [x] ~~AI-PG10-1: confirm FakeRecorder's original intent~~ → 2026-05-09 complete: Recorder code itself is absent (simple omission). Safe to introduce.
- [ ] AI-PG10-2: EventRecorder introduction PR — add struct field `Recorder record.EventRecorder` to 2 Reconcilers (PostgresClusterReconciler, BackupJobReconciler) + `r.Recorder = mgr.GetEventRecorderFor("<name>-controller")` in SetupWithManager + add Eventf calls in the core Reconcile branches (create/fail/delete) + inject a fake recorder in suite_test.go + verify zero regression in envtest (Owner: @eightynine01, Due: 2026-05-15)
- [ ] AI-PG10-3: Event reason catalog patch — depends on the commons-extraction RFC follow-up to RFC-0017 §3.4 (Owner: @eightynine01, Due: 2026-05-26)
- [ ] AI-PG10-4: verify helm chart probe alignment (Owner: @eightynine01, Due: 2026-05-12)

## Alternatives Considered

| Alternative | Reason for rejection |
|-------------|----------------------|
| Keep FakeRecorder | No K8s standard operational signal, kubectl describe output is sparse |
| Implement EventRecorder directly (record.New, etc.) | The controller-runtime standard manager.GetEventRecorderFor integrates with metrics / leader election — rolling our own is inappropriate |

## References

- RFC-0017: operator tooling unification.
- Related ADR: ADR-0005 (Plugin SDK bypass blocking — depguard rule preserved)
