# ADR-0011: RFC-0018 부분 채택 — pkg/status (Ready type only), pkg/finalizer 비대칭 보존

- Date: 2026-05-09
- Status: Accepted (PR-A7 first cut — Ready type 만, 도메인 type + Progressing/Degraded/Available 후속)
- Authors: @eightynine01
- Refs: RFC-0018 (operator-commons/docs/kb/rfc/0018-status-finalizer-standard.md), ADR-0003 (commons), v0.x ADR-0008 (Finalizer 회피), Plan §2 D11

## Context

operator-commons v0.6.0 의 `pkg/status` 채택. RFC-0018 §3.1 의 generic
4종 ConditionType (Ready / Progressing / Degraded / Available) + 6 Reason
카탈로그 표준화. postgres-operator 는 도메인 특이 ConditionType 보유
(ShardsReady / RouterReady / BackupHealthy / AutoSplitEligible) — 본
ADR 은 *부분 채택* 패턴을 결정한다.

또한 *pkg/finalizer 비대칭 보존*: postgres v0.x ADR-0008 의 *Finalizer
회피 정책 (Cascade Delete via OwnerReference)* 와 BackupCleanupJob CRD
가 외부 자원 cleanup 분리 처리 — RFC-0018 §3.2 의 *의도된 비대칭*.
mongodb / valkey 가 finalizer.Add 채택해도 postgres 는 미채택 보존.

## Decision

1. **`internal/controller/status.go` 의 `setCondition`** wrapper 가 *Ready
   type 만* `commonsstatus.SetReady` 로 위임. 도메인 type (ShardsReady /
   RouterReady / BackupHealthy / AutoSplitEligible) + Progressing 은 본
   wrapper 가 직접 `meta.SetStatusCondition` 호출 (현 동작 보존).

2. **observedGeneration=0** 임시 — `commonsstatus.SetReady` 의 5 번째
   인자. 호출자 시그니처 변경 (cluster.Generation 전달) 은 *별 PR*
   (PR-A7.2). 본 PR 은 first cut.

3. **Reason 카탈로그 통일**: postgres 의 `ReasonReconciling` /
   `ReasonAvailable` / `ReasonNotApplicable` 는 commons 의 동등 string
   literal 과 wire-level identical (각 "Reconciling" / "Available" /
   "NotApplicable"). 명시적 const alias 는 별 PR (도메인 reason 보존
   범위 결정 후).

4. **pkg/finalizer 미채택**: v0.x ADR-0008 의 *cascade-delete-by-
   OwnerReference* 결정 *유지*. postgres 의 finalizer 비대칭은 RFC-0018
   §3.2 Migration 단계 2 의 *의도된 변형*. mongodb/valkey 와 다른 길.

## Consequences

### Positive

- *Ready type* 의 4-repo 정합성 시작 — `kubectl describe postgrescluster/...`
  출력의 `Reason="Available"` / `"Reconciling"` 등이 mongodb/valkey/commons
  와 동일 카탈로그.
- 도메인 ConditionType (ShardsReady 등) 보존 — postgres 운영자 학습
  비용 0.
- 비대칭 보존이 *명시 결정* 으로 보존 — 후속 작업자가 v0.x ADR-0008
  의도 재해석 안 함.

### Negative

- observedGeneration=0 임시 — `kubectl get postgrescluster -o yaml` 의
  Conditions 출력에서 `observedGeneration` 필드 항상 0. PR-A7.2 에서
  해소.
- Progressing / Degraded / Available type 미위임 — 4-repo 정합성 *부분*.

### Trade-offs

- *Ready type 만 first cut* (본 PR) vs *4 generic type 일괄 위임* —
  후자는 호출자 11+ 위치 시그니처 변경 + 도메인 reason 매핑. 본 PR 은
  외과 수술적 변경 우선.

## Alternatives Considered

1. **commons.setCondition wrapper 전체 위임 + observedGeneration 추가** — 거부.
   - 호출자 11+ 위치 시그니처 변경 — 큰 review 부담.
   - PR-A7.2 로 분리.

2. **postgres reason 카탈로그를 commons const alias 로 교체** — 보류.
   - const 가 `commonsstatus.ReasonAvailable` 같은 var 참조 불가
     (Go const 문법 제약).
   - 동등 string literal 은 wire 동등하므로 본 PR 은 alias 미진행.

3. **pkg/finalizer 채택 (v0.x ADR-0008 supersede)** — 거부.
   - cascade-delete-by-OwnerReference 가 BackupCleanupJob CRD 와 *분리
     설계*. finalizer 도입 시 *2개 cleanup path* 공존 → 운영 사고 위험.
   - RFC-0018 §3.2 가 명시 비대칭 허용.

## Refs

- RFC-0018 §3.1 (pkg/status 표준), §3.2 (postgres 비대칭 보존).
- v0.x ADR-0008: Finalizer 회피 정책 (cascade delete).
- 활성 ADR-0008: operator-commons 채택 (2026-05-07).
- Plan §2 D11 (postgres status migration).
- 후속 PR-A7.2: setCondition 호출자 시그니처 확장 + Progressing/Degraded/Available 위임.
