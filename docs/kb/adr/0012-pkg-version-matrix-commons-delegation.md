# ADR-0012: pkg/version Matrix[Combo] commons delegation (Plan §2 D12, PR-B3)

- Date: 2026-05-09
- Status: Accepted
- Authors: @eightynine01
- Refs: commons-ADR-0004 (Matrix[E] generic), ADR-0005 (versioning-and-channels)

## Context

keiailab-commons v0.7.0 added a generic `Matrix[E MatrixEntry]` under
`pkg/version` (commons PR-B1 / ADR-0004). This PR-B3 delegates postgres's
`[]Combo` slice + ad-hoc loop pattern in `internal/version/matrix.go` to
commons `Matrix[Combo]` — *4-repo pkg/version adoption rises from 67% to 100%*.

The existing external contract (IsSupported / All / Stable / SupportedMajors)
signatures are unchanged — only the *internal storage* is delegated to
commons.

## Decision

1. **Bump `go.mod`** keiailab-commons v0.6.0 → v0.7.0.

2. **Changes in `internal/version/matrix.go`**:
   - Add `Combo.PrimaryKey() string { return c.PostgresMajor }` method
     — implements the `MatrixEntry` interface.
   - `var supported []Combo` → `var supported = commonsversion.MustMatrix(Combo{...}, ...)`
     — *init-time panic* on duplicate PostgresMajor.
   - `IsSupported`: change `for _, c := range supported` loop to `supported.Find(pgMajor)`.
     Gate verification branches are preserved within the function.
   - `All`: change direct slice copy to delegation via `supported.Entries()`
     (defensive copy preserved).
   - `Stable`: changed to `for _, c := range supported.Entries()`.
   - `SupportedMajors`: change ad-hoc dedup loop to delegation via
     `supported.Keys()`. Guarantee an *init-time MustMatrix duplicate panic*
     instead of *runtime dedup*.

3. **External contract unchanged**: callers of webhook (`commonsversion.NewSupportedAllOf`)
   + matrix_test.go are unmodified. 5 test cases (PG18/17/16 + Unknown +
   Stable_AllVanilla) pass.

4. **Updating ADR-0005 (versioning-and-channels)** is *not required* — the
   *decision itself* (Stable/Beta/Preview channel policy) is unchanged. Only
   the *implementation* is delegated to commons. This ADR-0012 separately
   records the implementation change.

## Consequences

### Positive

- 4-repo pkg/version adoption rate is 100% (postgres joins last).
- *runtime dedup* (SupportedMajors's ad-hoc map) → forced *init-time panic*.
  Blocks silent acceptance of duplicate PostgresMajor.
- `supported.Find(pgMajor)` is explicit — readability of intent is improved
  compared to the previous `for c := range` loop.

### Negative

- Dependency surface +1 (keiailab-commons v0.7.0's `Matrix[E]`). v0.6.0 +
  pkg/version.List is also in use — dependency change is minor.
- Use of generics enforces Go 1.18+ (already on go 1.25.10).

### Trade-offs

- *commons Matrix delegation* (this ADR) vs. *keep internal own implementation*
  — the latter violates the 4-repo cross-cut lint (RFC-0017 §3.3). This ADR's
  delegation is aligned.
- *Update ADR-0005* vs. *new separate ADR* — chose the latter. ADR-0005
  preserves the *decision itself*; this ADR preserves the *implementation
  change*.

## Alternatives Considered

1. **Different field for `Combo`'s PrimaryKey** — rejected.
   - PostgresMajor is the unique identifier — Image / Channel are mutable.
   - For duplicate Channel entries (e.g., PG18 stable + PG18 beta), the
     duplicate PrimaryKey panic is intended — enforce single channel per major.

2. **`Matrix[Combo]` + `Matrix[Backup]` etc. multi-matrix** — outside this PR.
   - This PR is Combo only. Backup matrix is a follow-up cycle.

3. **Do not use commons `Matrix`, use only List** — rejected.
   - List is string only — cannot represent the Combo struct.

## Refs

- commons-ADR-0004: Add Generic Matrix[E] to pkg/version.
- Plan §2 D12: delegate postgres matrix.go → commons.
- ADR-0005 (versioning-and-channels): parent decision for the *implementation*
  of this ADR.
- ADR-0009 (webhook accumulate-errors, delegation to commons.ValidateWithPredicate):
  preserves the webhook caller contract.
