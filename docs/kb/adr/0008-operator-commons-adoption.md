# ADR-0008: operator-commons 채택 + Container SecurityContext invariant 강화

- Date: 2026-05-07
- Status: Accepted
- Authors: @eightynine01
- Refs: keiailab/operator-commons ADR-0001 (charter)

## Context

3 keiailab Kubernetes operators (`mongodb-operator`, `valkey-operator`,
`postgres-operator`) 가 동일한 PodSecurity *restricted* SecurityContext invariant
를 *각자 인라인* 으로 정의하고 있었음. 드리프트 방지 목적으로 `keiailab/operator-commons`
shared library v0.1.2 가 도입됨 (operator-commons ADR-0001).

iteration 8 ship-4 시점 본 repo 의 `dataplaneContainerSecurityContext`
(internal/controller/builders.go) 는 다음 invariant 가 *container-level 에서*
누락되어 있었음:

- `RunAsNonRoot` 미설정 — Pod-level inherit 에 의존.
- `SeccompProfile` 미설정 — Pod-level inherit 에 의존.

아카이브된 `_archive/v0.x/0006-security-defaults-rationale.md` 의 정책이
*active* 상태가 아니므로, 본 ADR 이 새 정책 수립.

## Decision

`dataplaneContainerSecurityContext` 를 `operator-commons/pkg/security.RestrictedContainer`
호출로 위임. functional option `WithReadOnlyRootFilesystem(true)` 로 postgres
특화 옵션 유지.

결과 invariant (commons 가드 + postgres-specific):
- `runAsNonRoot=true` (commons 강제 — *이전 누락, 본 ADR 로 명시 도입*)
- `seccompProfile.type=RuntimeDefault` (commons 강제 — *이전 누락, 본 ADR 로 명시 도입*)
- `allowPrivilegeEscalation=false` (commons 가드)
- `capabilities.drop=[ALL]` (commons 가드)
- `readOnlyRootFilesystem=true` (postgres-specific, WithReadOnlyRootFilesystem 옵션)

`dataplanePodSecurityContext` (Pod-level) 은 *본 ADR 범위 외* — 별 iteration
에서 commons.RestrictedPod 확장 (RunAsUser/Group functional option 추가) 후 위임 검토.

## Consequences

### Positive
- PodSecurity restricted invariant 가 commons 100% line coverage 단위 test
  로 영구 회귀 가드. 3 operator 가 동일 보장.
- container-level 명시 정의 — Pod-level inherit 가정 제거. PodSecurity admission
  의 *명시 검사* 가 통과 (이전엔 inherit 경로로 우회).

### Negative
- `RunAsNonRoot=true` 강제 → postgres image (postgres user uid 70) 가 root
  가 아니므로 OK. 그러나 *custom image* 가 root 로 시작하는 경우 admission
  거부 — `dataplanePodSecurityContext` 의 RunAsUser=70 이 fallback 이지만,
  custom image 가 RunAsUser override 시 root 가능.
- commons v0.x 동안 API breaking 가능 — replace directive 또는 SemVer pin 필수.

### Trade-offs
- *invariant 강화 + 명시화* (본 ADR) vs *Pod-level inherit 의존* (이전): 명시화가
  PodSecurity admission 정합성 ↑. inherit 의존은 *추적 어려움* + *invariant 누설
  위험*.

## Alternatives Considered

1. **Pod-level 만 commons 위임 + Container-level 유지** — 거절: 사용자 명시 결정
   (iteration 8 plan AskUserQuestion 응답).
2. **commons 채택 보류 + 자체 함수 그대로** — 거절: operator-commons ADR-0001
   의 공통화 정책 + 3 operator 드리프트 방지 목표.
3. **container-level 새 함수 추가 (delegation 없이)** — 거절: 3번째 인라인
   복제. operator-commons 도입 의의 무효화.

## Verification

```bash
$ go test ./internal/controller/... -count=1
ok  github.com/keiailab/postgres-operator/internal/controller  8.578s
```

container-level invariant 강화 후 회귀 검증 — Pod 의 admission/runtime 검사
시 commons.RestrictedContainer 의 모든 가드 (capabilities/seccomp/runAsNonRoot/
allowPrivilegeEscalation) 가 명시 적용.

## Refs

- operator-commons v0.1.2 (github.com/keiailab/operator-commons)
- iteration 8 plan: ~/.claude/plans/iridescent-squishing-locket.md
- archived: docs/kb/adr/_archive/v0.x/0006-security-defaults-rationale.md

<!-- live-verified: 2026-05-09 -->
