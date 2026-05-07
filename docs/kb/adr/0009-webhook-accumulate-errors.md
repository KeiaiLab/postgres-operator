# ADR-0009: Webhook validate — immediate-return → accumulate-errors 변환

- Date: 2026-05-07
- Status: Accepted
- Authors: @eightynine01
- Refs: ADR-0008 (operator-commons 채택), valkey iteration 31 (`14be0db`) 패턴

## Context

`internal/webhook/v1alpha1/postgrescluster_webhook.go` 의 `validate(c)` 함수가
*4 invalid case* 를 *순차 if chain* 으로 *첫 발견 시 즉시 NewInvalid 반환*:

```go
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster)
    (admission.Warnings, error) {
    // 1. postgresVersion ∈ matrix
    if _, ok := version.IsSupported(pgVersion, w.FeatureGates); !ok {
        return nil, apierrors.NewInvalid(...)  // ← 첫 발견 시 종료
    }
    // 2. autoSplit.enabled + triggers
    if as.Enabled && !hasAnyTrigger(...) {
        return nil, apierrors.NewInvalid(...)  // ← 다음 invalid 미보고
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

K8s ecosystem convention (valkey / mongodb / 대부분 operator):
*모든 validation error 를 ErrorList 로 accumulate 후 일괄 NewInvalid 반환*.
이는 사용자 (kubectl 사용자 / GitOps 운영자) 가 *모든 invalid 를 한 번에* 보게 함:

```go
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid:
* spec.postgresVersion: Unsupported value "99": ...
* spec.autoSplit.triggers: at least one trigger ... must be > 0
* spec.backup.schedule: must be non-empty when backup.enabled=true
```

vs 본 *immediate-return* 패턴:
```go
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid: spec.postgresVersion: ...

# postgresVersion fix 후 재시도:
$ kubectl apply -f cluster.yaml
The PostgresCluster "x" is invalid: spec.autoSplit.triggers: ...
# ← 사용자가 *3 cycle 의 apply* 로 모든 invalid 발견
```

본 패턴이 *intentional design* 인지 *unconscious choice* 인지 git history 검토:
- `internal/webhook/v1alpha1/postgrescluster_webhook.go` 의 *validate 함수 commit
  message* (Plan 의 F01a / RFC 0001 §4 도출) 에 *immediate-return* 의 명시 reasoning
  부재.
- ADR-0001 ~ ADR-0008 에서도 webhook error semantics 언급 없음.
- 즉 *unconscious choice*. K8s convention 정합 fix 권장.

## Decision

`validate` 함수의 *순차 if chain + immediate return* → *errs ErrorList accumulate +
일괄 NewInvalid* 변환:

```go
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster)
    (admission.Warnings, error) {
    var errs field.ErrorList
    // 1. postgresVersion (commons.ValidateWithPredicate 위임 — ADR-0008)
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
    // 2-4. autoSplit / backup / extensions (각자 errs append, immediate return 없음)
    ...
    if len(errs) > 0 {
        return nil, apierrors.NewInvalid(gv, c.Name, errs)
    }
    return nil, nil
}
```

### Test 영향 매트릭스

| 기존 test | accumulate 후 동작 |
|---|---|
| `TestValidate_VersionRejected_NotInMatrix` (1 invalid) | 동등 — error message 가 ErrorList 로 단일 entry. `strings.Contains` 가 keyword 매칭. |
| `TestValidate_AutoSplitEnabled_RequiresAtLeastOneTrigger` (1 invalid) | 동등 — keyword 매칭. |
| `TestValidate_BackupEnabled_RequiresSchedule` (1 invalid) | 동등 — keyword 매칭. |
| `TestValidate_Happy` (모두 valid) | 동등 — errs empty, nil 반환. |

각 test 가 *단일 invalid case* 만 set — accumulate 결과 *errs len=1* 로 동일. 회귀
가드 자연 PASS.

### commons.ValidateWithPredicate 위임 (ADR-0008 deepening)

`version.IsSupported(v, gates)` 의 *2-arg 시그너처* 를 *closure* 로 wrap 후
`commons.ValidateWithPredicate(path, value, predicate, allowed)` 호출. *FeatureGates
는 closure scope 내 capture* — commons API 변경 불필요.

postgres operator-commons 채택률 변화: 2/6 (security/labels) → **3/6 (+ webhook)**.

## Consequences

### Positive
- 사용자 / kubectl 출력에서 *모든 invalid 한 번에 발견* — apply 반복 cycle 감소.
- 3 operator (mongodb / valkey / postgres) webhook 동일 *accumulate 패턴* —
  cross-operator drift 차단.
- commons.ValidateWithPredicate 의 3rd 사용처 — operator-commons API stability ↑.

### Negative
- `validate` 함수 LoC 증가 (~10 줄 — errs 변수 + 마지막 NewInvalid block).
- *복합 invalid 시나리오* test 추가 가능성 (다음 iteration 의 *separate concern*).

### Trade-offs
- *K8s convention 정합* (본 ADR) vs *immediate-return 보존*: convention 정합이
  *사용자 경험 ↑* + *cross-operator 패턴 통일*. 보존은 *기존 test 100% bit-equal
  output* 만 이점 (현실적 가치 zero).

## Alternatives Considered

1. **immediate-return 보존** — 거절: K8s ecosystem 의 deviation. 사용자 경험 ↓.
2. **commons 미사용 + 자체 ErrorList** — 거절: ADR-0008 의 commons 채택 기조 위반.
   commons.ValidateWithPredicate 가 *closure 로 FeatureGates 수용* — 단순.
3. **commons API 확장 (FeatureGates 매개변수 추가)** — 거절: postgres-specific
   매개변수가 commons API 비대화. closure 가 더 깨끗.

## Implementation

```go
// supportedPostgresList — version.matrix.go 의 string-only view 추출.
// commons.ValidateWithPredicate 의 4th 인자 (allowed []string) 용.
func supportedPostgresList() []string {
    return []string{"16", "17", "18"}  // matrix.go 의 PostgresMajor 만 추출
}
```

또는 matrix.go 에 `func SupportedMajors() []string` 추가 — 본 ADR 후속.

## Verification

```bash
go test ./internal/webhook/v1alpha1/ -count=1 -v
# 9+ test sub 모두 PASS — 단일 invalid case 검증 자연 통과.

go test ./... -count=1
# 전 패키지 PASS — controller / version / webhook 영향 없음.
```

## Refs

- valkey iteration 31 (`14be0db`) — webhook → commons.ValidateWithPredicate 위임 (예시 commit)
- mongodb ADR-0013 (`3345f85`) — conditions LastTransitionTime 패턴 fix (deviation 인정 + upstream 위임)
- HANDOFF iteration 32 — 3-way boundary 분석 (commons / upstream / 자체 보존)
- ADR-0008 (operator-commons 채택)
- k8s.io/apimachinery/pkg/util/validation/field (ErrorList 패턴)
