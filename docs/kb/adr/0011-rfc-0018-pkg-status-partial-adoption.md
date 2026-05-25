# ADR-0011: Partial adoption of RFC-0018 — pkg/status (Ready type only), preserve asymmetry on pkg/finalizer

- Date: 2026-05-09
- Status: Accepted (PR-A7 first cut — Ready type only; domain types + Progressing/Degraded/Available are follow-up)
- Authors: @eightynine01
- Refs: RFC-0018 (status-finalizer-standard), ADR-0003 (commons), v0.x ADR-0008 (Finalizer avoidance), Plan §2 D11

## Context

Adoption of operator-commons v0.6.0's `pkg/status`. RFC-0018 §3.1 standardizes
the 4 generic ConditionTypes (Ready / Progressing / Degraded / Available) +
a 6-Reason catalog. postgres-operator has domain-specific ConditionTypes
(ShardsReady / RouterReady / BackupHealthy / AutoSplitEligible) — this ADR
decides the *partial adoption* pattern.

Also *preserves the asymmetry on pkg/finalizer*: per the *Finalizer avoidance
policy (Cascade Delete via OwnerReference)* of postgres v0.x ADR-0008, and the
BackupCleanupJob CRD's separate handling of external-resource cleanup — this
is the *intended asymmetry* of RFC-0018 §3.2. Even though other operators
adopt finalizer.Add, postgres preserves non-adoption.

## Decision

1. **`setCondition` wrapper in `internal/controller/status.go`** delegates
   *only the Ready type* to `commonsstatus.SetReady`. Domain types
   (ShardsReady / RouterReady / BackupHealthy / AutoSplitEligible) and
   Progressing still call `meta.SetStatusCondition` directly from this
   wrapper (preserves current behavior).

2. **observedGeneration=0 temporarily** — the 5th argument to
   `commonsstatus.SetReady`. Changing the caller signature (passing
   cluster.Generation) is a *separate PR* (PR-A7.2). This PR is the first cut.

3. **Reason catalog unification**: postgres's `ReasonReconciling` /
   `ReasonAvailable` / `ReasonNotApplicable` are wire-level identical to
   the equivalent string literals in commons (each "Reconciling" /
   "Available" / "NotApplicable"). Explicit const aliasing is deferred to a
   separate PR (after deciding the preservation scope for domain reasons).

4. **pkg/finalizer not adopted**: the *cascade-delete-by-OwnerReference*
   decision in v0.x ADR-0008 is *kept*. postgres's finalizer asymmetry is
   the *intended variant* of RFC-0018 §3.2 Migration stage 2. This is the
   postgres-specific path justified by the BackupCleanupJob CRD design.

## Consequences

### Positive

- Alignment of the *Ready type* begins — the `kubectl describe postgrescluster/...`
  output's `Reason="Available"` / `"Reconciling"` etc. now uses the standard
  operator-commons catalog.
- Domain ConditionTypes (ShardsReady, etc.) are preserved — zero learning cost
  for postgres operators.
- Asymmetry preservation is preserved as an *explicit decision* — follow-up
  contributors will not reinterpret the intent of v0.x ADR-0008.

### Negative

- observedGeneration=0 temporary — the `observedGeneration` field in the
  Conditions output of `kubectl get postgrescluster -o yaml` will always be 0.
  Resolved in PR-A7.2.
- Progressing / Degraded / Available types are not delegated — commons alignment
  is *partial*.

### Trade-offs

- *Ready type only as first cut* (this PR) vs. *delegate all 4 generic types
  at once* — the latter requires signature changes at 11+ caller sites and
  domain reason mapping. This PR prioritizes a surgical change.

## Alternatives Considered

1. **Delegate the entire commons.setCondition wrapper + add observedGeneration** — rejected.
   - Caller signature changes at 11+ sites — large review burden.
   - Split into PR-A7.2.

2. **Replace postgres reason catalog with commons const aliases** — deferred.
   - Const cannot reference a var like `commonsstatus.ReasonAvailable` (Go
     const grammar constraint).
   - Equivalent string literals are wire-equivalent, so this PR does not
     introduce aliases.

3. **Adopt pkg/finalizer (supersede v0.x ADR-0008)** — rejected.
   - cascade-delete-by-OwnerReference is *designed in separation* from the
     BackupCleanupJob CRD. Introducing finalizer would let *two cleanup paths*
     coexist → operational accident risk.
   - RFC-0018 §3.2 explicitly permits asymmetry.

## Refs

- RFC-0018 §3.1 (pkg/status standard), §3.2 (postgres asymmetry preservation).
- v0.x ADR-0008: Finalizer avoidance policy (cascade delete).
- Active ADR-0008: operator-commons adoption (2026-05-07).
- Plan §2 D11 (postgres status migration).
- Follow-up PR-A7.2: extend setCondition caller signature + delegate Progressing/Degraded/Available.
