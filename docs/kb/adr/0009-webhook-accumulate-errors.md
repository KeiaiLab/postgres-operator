# ADR-0009: Webhook validate — immediate-return → accumulate-errors

- Date: 2026-05-07
- Status: Accepted
- Authors: @eightynine01
- Refs: ADR-0008 (operator-commons adoption), valkey iteration 31 (`14be0db`) pattern

## Context

The `validate(c)` function in
`internal/webhook/v1alpha1/postgrescluster_webhook.go` evaluates *four
invalid cases* in a *sequential if-chain* and *returns `NewInvalid`
immediately on the first hit*:

```go
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster)
    (admission.Warnings, error) {
    // 1. postgresVersion ∈ matrix
    if _, ok := version.IsSupported(pgVersion, w.FeatureGates); !ok {
        return nil, apierrors.NewInvalid(...)  // ← stops at the first miss
    }
    // 2. autoSplit.enabled + triggers
    if as.Enabled && !hasAnyTrigger(...) {
        return nil, apierrors.NewInvalid(...)  // ← next invalid is not reported
    }
    // 3. backup.schedule
    if b.Enabled && b.Schedule == "" {
        return nil, apierrors.NewInvalid(...)
    }
    // 4. extensions
    if missing := w.Plugins.EnabledExtensions(...); len(missing) > 0 {
        return nil, apierrors.NewInvalid(...)
    }
}
```

The K8s ecosystem convention (valkey / mongodb / most operators) is to
*accumulate every validation error into a `field.ErrorList` and return
a single `NewInvalid`*. The user (kubectl users / GitOps operators)
then sees *every invalid in one go*:

```text
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid:
* spec.postgresVersion: Unsupported value "99": ...
* spec.autoSplit.triggers: at least one trigger ... must be > 0
* spec.backup.schedule: must be non-empty when backup.enabled=true
```

vs the *immediate-return* pattern:

```text
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid: spec.postgresVersion: ...

# Fix postgresVersion and retry:
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid: spec.autoSplit.triggers: ...
# ← the user needs *3 apply cycles* to discover every invalid
```

Git history check for whether the existing pattern was an *intentional
design* or an *unconscious choice*:

- The `validate` function commit message (derived from Plan's F01a /
  RFC 0001 §4) does not state any reasoning for immediate-return.
- ADR-0001 through ADR-0008 do not mention webhook error semantics.
- Therefore it is an *unconscious choice*. Aligning with the K8s
  convention is the right fix.

## Decision

Convert the `validate` function from *sequential if-chain + immediate
return* to *`errs` ErrorList accumulate + single `NewInvalid`*:

```go
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster)
    (admission.Warnings, error) {
    var errs field.ErrorList
    // 1. postgresVersion (delegated to commons.ValidateWithPredicate — ADR-0008)
    predicate := func(v string) bool {
        _, ok := version.IsSupported(v, w.FeatureGates)
        return ok
    }
    if err := commonswebhook.ValidateWithPredicate(
        field.NewPath("spec", "postgresVersion"), pgVersion,
        predicate, supportedPostgresList(),
    ); err != nil {
        errs = append(errs, err)
    }
    // 2-4. autoSplit / backup / extensions (each appends to `errs`, no immediate return)
    ...
    if len(errs) > 0 {
        return nil, apierrors.NewInvalid(gv, c.Name, errs)
    }
    return nil, nil
}
```

### Test impact matrix

| Existing test | Behaviour after accumulate |
|---|---|
| `TestValidate_VersionRejected_NotInMatrix` (1 invalid) | Equivalent — error message becomes a single ErrorList entry. `strings.Contains` keyword match still hits. |
| `TestValidate_AutoSplitEnabled_RequiresAtLeastOneTrigger` (1 invalid) | Equivalent — keyword match. |
| `TestValidate_BackupEnabled_RequiresSchedule` (1 invalid) | Equivalent — keyword match. |
| `TestValidate_Happy` (all valid) | Equivalent — `errs` empty, returns nil. |

Each test sets a *single invalid case*; after accumulation `len(errs)=1`
is unchanged. The regression guard naturally passes.

### `commons.ValidateWithPredicate` delegation (deepening ADR-0008)

The 2-arg signature of `version.IsSupported(v, gates)` is wrapped in a
closure and then passed to `commons.ValidateWithPredicate(path, value,
predicate, allowed)`. *FeatureGates is captured in the closure scope*
— no commons-API change is required.

postgres operator-commons adoption count: 2/6 (security/labels) →
**3/6 (+ webhook)**.

## Consequences

### Positive

- Users and `kubectl` see *every invalid at once* — fewer apply
  iterations.
- The 3 operators (mongodb / valkey / postgres) all share the same
  *accumulate pattern* — cross-operator drift is blocked.
- This is the 3rd consumer of `commons.ValidateWithPredicate` —
  raises operator-commons API stability.

### Negative

- `validate` grows ~10 LoC (the `errs` variable + the final
  `NewInvalid` block).
- *Composite invalid scenarios* may need a new test (a *separate
  concern* for a later iteration).

### Trade-offs

- *K8s-convention alignment* (this ADR) vs *preserving immediate
  return*: convention alignment gives *better UX* + *cross-operator
  pattern unification*. The only benefit of preservation is "existing
  tests keep their bit-equal output" — practical value is zero.

## Alternatives Considered

1. **Keep immediate return** — Rejected: deviates from the K8s
   ecosystem; degraded UX.
2. **Don't use commons; build a local ErrorList** — Rejected: violates
   the ADR-0008 commons-adoption stance. `commons.ValidateWithPredicate`
   *accepts FeatureGates as a closure capture* — simpler.
3. **Extend the commons API to take a FeatureGates parameter** —
   Rejected: a postgres-specific parameter bloats the commons API. The
   closure is cleaner.

## Implementation

```go
// supportedPostgresList — extract the string-only view from
// version.matrix.go for use as the 4th argument of
// commons.ValidateWithPredicate (allowed []string).
func supportedPostgresList() []string {
    return []string{"16", "17", "18"}  // PostgresMajor entries from matrix.go only
}
```

Alternatively, add `func SupportedMajors() []string` to `matrix.go` as
a follow-up to this ADR.

## Verification

```bash
go test ./internal/webhook/v1alpha1/ -count=1 -v
# All 9+ subtests pass — the single-invalid cases keep passing naturally.

go test ./... -count=1
# Full-package PASS — controller / version / webhook unaffected.
```

## Refs

- valkey iteration 31 (`14be0db`) — webhook → `commons.ValidateWithPredicate` delegation (example commit).
- mongodb ADR-0013 (`3345f85`) — conditions `LastTransitionTime` pattern fix (deviation acknowledged + upstream delegation).
- HANDOFF iteration 32 — 3-way boundary analysis (commons / upstream / kept locally).
- ADR-0008 (operator-commons adoption).
- `k8s.io/apimachinery/pkg/util/validation/field` (ErrorList pattern).
