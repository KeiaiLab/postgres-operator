# ADR-0008: Adopt operator-commons + tighten the Container SecurityContext invariant

- Date: 2026-05-07
- Status: Accepted
- Authors: @eightynine01
- Refs: keiailab/operator-commons ADR-0001 (charter)

## Context

The keiailab Kubernetes operators were defining the same PodSecurity *restricted*
SecurityContext invariant *inline in each*. To prevent drift, the `keiailab/operator-commons`
shared library v0.1.2 was introduced (operator-commons ADR-0001).

At the iteration 8 ship-4 timepoint, this repo's `dataplaneContainerSecurityContext`
(internal/controller/builders.go) was missing the following invariants *at the
container level*:

- `RunAsNonRoot` not set — relied on Pod-level inherit.
- `SeccompProfile` not set — relied on Pod-level inherit.

The prior v0.x security-defaults rationale is no longer *active* (history
preserved in git), so this ADR establishes the new policy.

## Decision

Delegate `dataplaneContainerSecurityContext` to a call into
`operator-commons/pkg/security.RestrictedContainer`. Keep the postgres-specific
option via the functional option `WithReadOnlyRootFilesystem(true)`.

Resulting invariants (commons guards + postgres-specific):
- `runAsNonRoot=true` (enforced by commons — *previously missing, explicitly introduced by this ADR*)
- `seccompProfile.type=RuntimeDefault` (enforced by commons — *previously missing, explicitly introduced by this ADR*)
- `allowPrivilegeEscalation=false` (commons guard)
- `capabilities.drop=[ALL]` (commons guard)
- `readOnlyRootFilesystem=true` (postgres-specific, via the WithReadOnlyRootFilesystem option)

`dataplanePodSecurityContext` (Pod-level) is *outside this ADR's scope* — we
will consider delegating it after extending commons.RestrictedPod
(adding RunAsUser/Group functional options) in a separate iteration.

## Consequences

### Positive
- The PodSecurity restricted invariant is permanently regression-guarded by
  the 100% line-coverage unit tests in commons. All operators consuming commons
  have the same guarantee.
- Explicit container-level definition — eliminates the assumption of Pod-level
  inherit. The *explicit check* of PodSecurity admission now passes (previously
  it slipped through via the inherit path).

### Negative
- Forcing `RunAsNonRoot=true` → the postgres image runs as postgres user uid 70
  (not root), so OK. However, if a *custom image* starts as root, admission
  will be rejected — `dataplanePodSecurityContext`'s RunAsUser=70 is the
  fallback, but if a custom image overrides RunAsUser it may run as root.
- commons v0.x may have API breakage — a replace directive or SemVer pin is
  required.

### Trade-offs
- *Strengthen + make the invariant explicit* (this ADR) vs. *rely on Pod-level
  inherit* (previous): being explicit ↑ improves PodSecurity admission
  alignment. Reliance on inherit is *hard to trace* + carries *invariant leak
  risk*.

## Alternatives Considered

1. **Delegate Pod-level only to commons, keep Container-level as-is** — rejected:
   user-explicit decision (iteration 8 plan AskUserQuestion response).
2. **Defer commons adoption, keep our own function** — rejected: contradicts the
   commonization policy of operator-commons ADR-0001 + the goal of preventing
   drift across operators.
3. **Add a new container-level function (without delegation)** — rejected: a
   3rd inline copy. Negates the value of adopting operator-commons.

## Verification

```bash
$ go test ./internal/controller/... -count=1
ok  github.com/keiailab/postgres-operator/internal/controller  8.578s
```

Regression verification after tightening the container-level invariant — at
Pod admission/runtime checks, all guards of commons.RestrictedContainer
(capabilities/seccomp/runAsNonRoot/allowPrivilegeEscalation) are now
explicitly applied.

## Refs

- operator-commons v0.1.2 (github.com/keiailab/operator-commons)

<!-- live-verified: 2026-05-09 -->
