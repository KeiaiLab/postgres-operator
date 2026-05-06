# ADR 0008 — Finalizer 회피 정책 (Cascade Delete via OwnerReference)

- **상태**: Accepted
- **날짜**: 2026-04-30
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0002 (Patroni 미사용 — K8s API as DCS), Crunchy PGO 비교 (`/Users/phil/.claude/plans/1-https-artifacthub-io-packages-helm-bit-sunny-wozniak.md` §4 P0-4)

## 컨텍스트

본 프로젝트는 PostgresCluster reconciler가 모든 하위 자원(StatefulSet, Service, ConfigMap, PVC)에 `controllerutil.SetControllerReference`를 호출 (`internal/controller/postgrescluster_controller.go:262`). 이는 K8s GC가 PostgresCluster 삭제 시 *자동 cascade delete*하는 표준 패턴.

그러나:
- *Finalizer 도입 유혹*이 향후 PR에서 발생 가능 — "외부 자원(backup repo, S3 prefix, certificate) cleanup도 해야 하지 않나?"
- Finalizer는 *추가 reconcile loop* + *K8s API 의존도 증가* + *deletion 지연* 비용
- ADR 0002 "K8s API as DCS" 원칙이 *분산 합의 단순성*을 보존 — Finalizer는 그 원칙과 충돌 가능
- Crunchy PGO도 동일 패턴(controllerutil + GC TTL)을 ADR로 못박음 — 본 ADR이 그 결정을 *명시적으로 기록*

## 결정

본 프로젝트는 **Finalizer를 신규 도입하지 않는다.** 다음 두 메커니즘만 사용:

1. **OwnerReference + K8s GC**: 하위 자원에 `controllerutil.SetControllerReference`로 OwnerReference 설정. PostgresCluster 삭제 시 K8s GC가 cascade delete.
2. **외부 자원 cleanup은 별도 Job CRD**: backup repo cleanup, S3 prefix 정리 등 외부 자원이 PostgresCluster 삭제와 *별도 lifecycle*을 가질 때, 별도 `BackupCleanupJob` 같은 CRD에서 처리. PostgresCluster는 외부 자원을 *알지 못하는 상태*로 삭제 가능.

### 강제 회귀 테스트

`test/e2e/cascade_delete_test.go`에 다음 시나리오 추가:
1. PostgresCluster 생성 → StatefulSet/Service/ConfigMap 생성 확인
2. PostgresCluster 삭제
3. **60초 내** 모든 하위 자원이 GC 됨을 검증

`internal/controller/postgrescluster_controller_test.go`에 envtest 어셔션:
- 모든 하위 자원이 `controllerutil.HasControllerReference == true`

## 근거

### 왜 Finalizer가 *기본*에서 제외되는가
Finalizer는 다음 4가지 비용을 만든다:

1. **Deletion 지연**: 사용자가 `kubectl delete` 후 *Finalizer가 처리될 때까지* PostgresCluster 자원이 K8s API에 남음. 운영자 혼란.
2. **Stuck 위험**: Finalizer 처리 중 reconciler 다운 → 자원 영구 stuck. 강제 제거 시 외부 자원 leak.
3. **Reconcile 복잡도**: 모든 reconcile cycle에서 `if obj.DeletionTimestamp != nil { ... } else { ... }` 분기 추가.
4. **분산 합의 충돌**: ADR 0002 "K8s API as DCS"는 etcd가 단일 진실. Finalizer는 그 진실을 *지연* — 분산 모델에서 timing 가정 추가.

### 왜 외부 자원은 별도 Job CRD인가
PostgresCluster 삭제 ≠ backup repo 삭제. 사용자는 PostgresCluster를 삭제해도 *backup은 보존*하고 싶을 수 있음. 또는 *cluster 재생성*을 위해 backup만 보존. 이 분리를 강제하면:

- PostgresCluster lifecycle 단순화 (외부 의존 0)
- 사용자가 의도적으로 cleanup CRD 생성해야 외부 자원 삭제 → *명시적 의도 표현*
- Finalizer 부재로 deletion 즉시 완료

### 왜 *지금* ADR을 작성하는가
P0-4 회귀 테스트 추가 시점에 *왜 이 테스트가 존재하는가*의 근거를 ADR로 못박음. 향후 PR이 "외부 자원 cleanup 위해 Finalizer 추가"를 시도하면 본 ADR이 Reject 근거.

## 트레이드오프

- **외부 자원 leak 가능성**: Finalizer 부재 → PostgresCluster 삭제 후 외부 backup repo가 leak될 수 있음. 완화: 운영 가이드에서 *별도 cleanup CRD* 사용 권장. 또한 cloud provider 측 lifecycle policy (S3 expiration 등) 활용.
- **kubectl-cnpg 같은 도구의 *graceful drain* 부재**: PGO는 Finalizer로 PG가 graceful shutdown 후 삭제되도록 보장. 본 프로젝트는 fail-fast (ADR 0002 + RFC 0003 부록 A) 모델이라 graceful drain은 *재기동 신호로 운영자가 처리* — 동일 분산 합의 원칙.

## 결과

- 본 ADR은 *향후 모든 PR*의 Finalizer 도입을 reject할 근거.
- P0-4 권장 implementation 시 회귀 테스트 추가.
- 외부 자원 cleanup 필요한 시점(P4 Backup, P7 Security)에 *별도 Job CRD*로 설계.
- 본 ADR 변경(Finalizer 예외 도입)은 RFC 필수 — *광범위 영향* 관점에서 RFC 의무.

## 강제 메커니즘

| 메커니즘 | 위치 | 도입 시점 |
|---|---|---|
| 회귀 테스트 (e2e cascade delete) | `test/e2e/cascade_delete_test.go` | P0-4 implementation |
| envtest 어셔션 (OwnerReference) | `internal/controller/postgrescluster_controller_test.go` | P0-4 |
| golangci-lint 정책 (가능 시 — Finalizer import 차단) | `.custom-gcl.yml` | P13-T2 후속 |
| PR 리뷰 체크리스트 | `standards/checklist.md §3` | 본 ADR 채택과 동시 |
