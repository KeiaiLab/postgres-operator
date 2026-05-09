# ADR-0012: pkg/version Matrix[Combo] commons 위임 (Plan §2 D12, PR-B3)

- Date: 2026-05-09
- Status: Accepted
- Authors: @eightynine01
- Refs: Plan §2 D12 (`~/.claude/plans/1-https-artifacthub-io-packages-helm-clo-synthetic-gem.md`), commons-ADR-0004 (Matrix[E] generic), ADR-0005 (versioning-and-channels)

## Context

operator-commons v0.7.0 의 `pkg/version` 에 generic `Matrix[E MatrixEntry]`
추가 (commons PR-B1 / ADR-0004). 본 PR-B3 는 postgres `internal/version/
matrix.go` 의 `[]Combo` slice + ad-hoc loop 패턴을 commons `Matrix[Combo]`
로 위임 — *4-repo pkg/version 채택률 67% → 100%*.

기존 외부 contract (IsSupported / All / Stable / SupportedMajors) 시그
니처 불변 — *내부 storage* 만 commons 위임.

## Decision

1. **`go.mod`** 의 operator-commons v0.6.0 → v0.7.0 bump.

2. **`internal/version/matrix.go` 변경**:
   - `Combo.PrimaryKey() string { return c.PostgresMajor }` 메소드 추가
     — `MatrixEntry` interface 구현.
   - `var supported []Combo` → `var supported = commonsversion.MustMatrix(Combo{...}, ...)`
     — duplicate PostgresMajor 시 *init-time panic*.
   - `IsSupported`: `for _, c := range supported` 루프 → `supported.Find(pgMajor)`.
     gate 검증 분기는 본 함수가 보존.
   - `All`: 직접 slice 복사 → `supported.Entries()` 위임 (방어 복사 보존).
   - `Stable`: `for _, c := range supported.Entries()` 으로 변경.
   - `SupportedMajors`: ad-hoc dedup loop → `supported.Keys()` 위임. *runtime
     dedup* 대신 *init-time MustMatrix duplicate panic* 보장.

3. **외부 contract 불변**: webhook (`commonsversion.NewSupportedAllOf`)
   호출부 + matrix_test.go 무수정. 5 test case (PG18/17/16 + Unknown +
   Stable_AllVanilla) 통과 보장.

4. **ADR-0005 (versioning-and-channels)** 본문 갱신 *불필요* — *결정
   자체* (Stable/Beta/Preview 채널 정책) 그대로. *implementation* 만
   commons 위임. 본 ADR-0012 가 implementation 변경을 별도 보존.

## Consequences

### Positive

- 4-repo pkg/version 채택률 100% (postgres 마지막 합류).
- *runtime dedup* (SupportedMajors 의 ad-hoc map) → *init-time panic*
  으로 강제. duplicate PostgresMajor 의 silent acceptance 차단.
- `supported.Find(pgMajor)` 가 명시적 — 기존 `for c := range` 루프보다
  intent 가독성 향상.

### Negative

- 의존성 표면 +1 (operator-commons v0.7.0 의 `Matrix[E]`). v0.6.0 +
  pkg/version.List 도 사용 중 — 의존 변화는 minor.
- generic 사용으로 Go 1.18+ 강제 (이미 go 1.25.10).

### Trade-offs

- *commons Matrix 위임* (본 ADR) vs *내부 자체 유지* — 후자는 4-repo
  cross-cut lint (RFC-0017 §3.3) 위반. 본 ADR 의 위임이 정합.
- *ADR-0005 갱신* vs *별 ADR 신규* — 후자 채택. ADR-0005 가 *결정 자체*
  보존, 본 ADR 가 *implementation 변경* 보존.

## Alternatives Considered

1. **`Combo` 의 PrimaryKey 다른 필드** — 거부.
   - PostgresMajor 가 unique 식별자 — Image / Channel 은 가변.
   - Channel 중복 entry 시 (예: PG18 stable + PG18 beta) duplicate
     PrimaryKey panic. 의도 — 단일 channel per major 강제.

2. **`Matrix[Combo]` + `Matrix[Backup]` 등 multi-matrix** — 본 PR 외.
   - 본 PR 은 Combo 만. Backup matrix 는 후속 사이클.

3. **commons `Matrix` 미사용 + List 만** — 거부.
   - List 는 string only — Combo struct 표현 불가.

## Refs

- commons-ADR-0004: pkg/version Generic Matrix[E] 추가.
- Plan §2 D12: postgres matrix.go → commons 위임.
- ADR-0005 (versioning-and-channels): 본 ADR 의 *implementation* 부모 결정.
- ADR-0009 (webhook accumulate-errors, commons.ValidateWithPredicate 위임):
  webhook 호출부 contract 보존.
