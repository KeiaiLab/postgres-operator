# ADR-0020: Sprint 1 — keiailab-commons pkg/pvc + pkg/topology 채택 (-375 LOC)

- Date: 2026-05-21
- Status: Accepted
- Authors: @eightynine01 (Codex Major #7 — Sprint 1 Phase 2)
- Refs: keiailab-commons ADR-0012 (commons-side decisions), Sprint 1 분석 (~495 LOC cross-operator 중복 식별)

## Context

postgres-operator 의 `internal/controller/pvc_resize.go` (~120 LOC) +
`internal/controller/topology_spread.go` (~48 LOC) + 각각의 테스트
(146 + 62 LOC) 가 다른 operators 와 거의 동일 (~495 LOC cross-operator
중복). keiailab-commons Sprint 1 (commons ADR-0012) 에서 `pkg/pvc`
+ `pkg/topology` 신규 추출.

본 ADR 은 postgres-operator 측 consumer migration 을 보존한다.

## Decision

1. **pkg/pvc 어댑션** — `internal/controller/pvc_resize.go` (120 LOC) +
   `internal/controller/pvc_resize_test.go` (146 LOC) 전체 삭제.
   `postgrescluster_controller.go:347` 의 `expandDataPVCs(...)` 호출을
   `commonspvc.ExpandDataPVCs(...)` 로 1줄 교체.

2. **pkg/topology 어댑션** — `internal/controller/topology_spread.go`
   (47 LOC) + `internal/controller/topology_spread_test.go` (62 LOC) 전체
   삭제. 2개 callsite 교체:
   - `builders.go:1022` (PostgresCluster Shard STS) — postgres 의 "추가
     복제본 수" 의미론 보존: `commonstopology.Defaulted(..., WithMinReplicas(1))`.
   - `pooler_controller.go:1594` (PgBouncer Pooler Deployment) — 동일
     `WithMinReplicas(1)` (replicas-1 < 1 → spread 미주입).

3. **go.mod**: `keiailab-commons v0.8.0 → v0.8.1-0.20260521045707-85a46ba80952`
   (commons PR #52 pre-merge commit). v0.9.0 tag 후 본 ADR 갱신.

4. **Beta tier 어댑션 위험 인지**: commons pkg/pvc + pkg/topology 가 현재
   Beta. All consuming operators 동시 회귀 통과 후 Stable 격상 트리거.

## Consequences

### Positive

- LOC 감축: -375 LOC (3 deletion + 2 dependency + 5 line modification).
- 단일 SSOT: PVC expansion + TSC default 로직이 commons 에서만 갱신됨 —
  drift 방지.
- 함수형 옵션 패턴 (WithMinReplicas) 으로 postgres 의 "additional copies"
  semantic 을 *명시적으로* 표현 — 이전 코드의 implicit `replicas < 1`
  체크보다 의도가 더 명확.

### Negative

- Beta tier 채택 — commons API breaking 위험이 *원칙적으로* 존재 (실제로는
  ADR-0012 의 alternative 거부 사유로 안정성 확보됨).
- commit hash 의존 — v0.9.0 tag 후 본 ADR 의 §Decision §3 갱신 필요.

### Trade-offs

- *commit hash 의존 (즉시 머지 가능)* vs *v0.9.0 tag 대기 (지연)* — 본 ADR
  은 전자 채택. 후속 cleanup 으로 tag 의존 회복 (단순 go.mod 갱신).

## Alternatives Considered

1. **부분 채택 (pkg/pvc 만)** — 거부. 두 패키지가 같은 sprint 결과물 +
   동시 회귀 통과가 Stable 격상의 트리거이므로 분리 무의미.
2. **Phase 2 e2e 통과 후 머지** — 차후 사용자 결정. 본 PR 은 *생성 + 로컬
   회귀 통과* 까지만, 머지는 사용자 e2e 검토 후.

## Refs

- keiailab-commons PR #52 — `pkg/pvc` + `pkg/topology` 신규.
- keiailab-commons ADR-0012 — commons-side 결정 근거.
- 삭제된 원본 파일:
  - `internal/controller/pvc_resize.go` (-120 LOC)
  - `internal/controller/pvc_resize_test.go` (-146 LOC)
  - `internal/controller/topology_spread.go` (-47 LOC)
  - `internal/controller/topology_spread_test.go` (-62 LOC)
- 수정된 callsite:
  - `internal/controller/postgrescluster_controller.go:347` — `expandDataPVCs` → `commonspvc.ExpandDataPVCs`.
  - `internal/controller/builders.go:1022` — `defaultedTopologySpread` → `commonstopology.Defaulted(..., WithMinReplicas(1))`.
  - `internal/controller/pooler_controller.go:1594` — 동일 패턴.
