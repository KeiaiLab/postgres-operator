# Router CPU HPA Design

> 작성일: 2026-06-29  
> 범위: `PostgresCluster.spec.router.autoscale` 의 1차 운영 구현  
> 대상 브랜치: `chore/ha-pitr-e2e-consolidation`

## 1. 요약

Router autoscaling 은 현재 API 스키마만 있고 실제 reconciler 구현이 없다. 이번 설계는
`spec.shardingMode=native` 이고 `spec.router.enabled=true`, `spec.router.autoscale.enabled=true` 인
클러스터에 Kubernetes `autoscaling/v2.HorizontalPodAutoscaler` 를 생성한다.

1차 범위는 **CPU 기반 HPA** 로 제한한다. `targetActiveConnections` 는 API 필드로 남기되, router metric
서버와 custom metrics adapter 가 아직 없으므로 이번 단계에서는 HPA metric 으로 사용하지 않는다.

## 2. 핵심 개념 / 기술용어 설명

- **HPA**: Kubernetes `HorizontalPodAutoscaler`. 대상 `Deployment` 의 replica 수를 CPU 같은 metric 에 따라
  조정한다.
- **Router Deployment**: `PostgresCluster` 가 native sharding 모드에서 생성하는 stateless `pg-router`
  Deployment. 현재는 `spec.router.replicas` 로 수동 scale 한다.
- **Controller ownership**: HPA 는 `PostgresCluster` 의 자식 리소스로 생성한다. OwnerReference 를 붙이면
  cluster 삭제 시 garbage collection 되고, `SetupWithManager().Owns()` 로 HPA 변경도 reconcile trigger 가 된다.

## 3. 현재 상태

코드 기준 확인 결과:

- `api/v1alpha1/postgrescluster_types.go` 에 `RouterAutoscaleSpec` 이 있다.
- `internal/controller/postgrescluster_controller.go` 는 router `ConfigMap` / `Service` / `Deployment` 만 만든다.
- `internal/controller/builders.go` 에 HPA builder 가 없다.
- RBAC marker 에 `autoscaling` 그룹 권한이 없다.
- `copySpec()` 은 `HorizontalPodAutoscaler` 를 지원하지 않는다.
- `cmd/pg-router` 는 Prometheus `/metrics` endpoint 를 노출하지 않는다.

## 4. 설계 결정

### 4.1 1차 구현은 CPU HPA 만 지원

`spec.router.autoscale.targetCPU` 를 `Resource` metric 으로 반영한다.

```yaml
metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

`targetActiveConnections` 는 이번 구현에서 무시하지 않고, **reserved 필드** 로 문서화한다. 사용자가 값을 넣어도
HPA 에 반영되지 않는다. 이유는 실제 router metric 과 custom metrics adapter 가 없어서 동작한다고 표현하면
운영자를 오도하기 때문이다.

### 4.2 `maxReplicas` 는 autoscale 활성 시 필수

`autoscale.enabled=true` 인데 `maxReplicas=0` 이면 webhook 에서 거부한다. 무한 또는 불명확한 scale 상한은
운영 리스크가 크다.

`minReplicas` 기본값:

- `autoscale.minReplicas > 0` 이면 그 값을 사용한다.
- 아니면 `router.replicas` 를 사용한다.
- 둘 다 0 이면 schema default 기대와 별개로 builder 에서 1로 방어한다.

`targetCPU` 기본값:

- `autoscale.targetCPU > 0` 이면 그 값을 사용한다.
- 아니면 API default 와 일치하게 70을 사용한다.

### 4.3 HPA 활성 시 operator 는 Deployment replicas 와 싸우지 않는다

현재 reconciler 는 매번 router Deployment 의 `spec.replicas` 를 desired 값으로 덮어쓴다. HPA 를 붙인 뒤에도
이 동작을 유지하면 HPA 가 scale out 해도 다음 reconcile 에서 다시 `spec.router.replicas` 로 되돌아간다.

따라서 HPA 활성 시에는 다음 규칙을 적용한다.

- 새 Deployment 생성 시 초기 replicas 는 HPA min 값으로 둔다.
- 기존 Deployment update 시 `copySpec()` 이 `Deployment.spec.replicas` 를 덮어쓰지 않는다.
- template, securityContext, image, resources 등 pod spec 은 계속 operator desired state 로 동기화한다.

이 동작은 `buildRouterDeployment(..., preserveReplicas bool)` 또는 router 전용 update helper 로 표현한다.
파일 변경을 작게 유지하기 위해 1차 구현은 `buildRouterDeployment` 에 `preserveReplicas` 인자를 추가하고,
`copySpec()` 은 `Deployment` 자체가 `replicas=nil` 인 경우 기존 replicas 를 보존하도록 처리한다.

### 4.4 autoscale 비활성 또는 router 비활성 시 HPA 삭제

사용자가 `autoscale.enabled=false` 로 바꾸거나 router 를 끄면 기존 HPA 는 삭제한다. 그래야 이후 수동
`spec.router.replicas` 가 다시 단일 제어권을 가진다.

삭제는 `apierrors.IsNotFound` 를 정상으로 처리한다.

### 4.5 HPA 상태는 1차 범위에서 status 에 추가하지 않는다

`ClusterRouterStatus` 에 HPA 상태 필드를 추가하면 CRD/DeepCopy/문서 범위가 커진다. 이번 단계는 HPA 리소스
생성과 충돌 방지에 집중한다. 운영자는 `kubectl get hpa` 와 Events 로 상태를 확인한다.

## 5. 동작 흐름

1. `PostgresClusterReconciler` 가 `PostgresCluster` 를 읽는다.
2. `shardingMode=native && router.enabled=true` 이면 router `ConfigMap`, `Service`, `Deployment` 를 upsert 한다.
3. `router.autoscale.enabled=true` 이면 router `Deployment` 를 대상으로 하는 HPA 를 upsert 한다.
4. HPA 활성 상태에서는 router Deployment replicas 를 HPA 가 관리한다.
5. `autoscale.enabled=false` 또는 router inactive 이면 기존 HPA 를 삭제한다.
6. cluster 삭제 시 OwnerReference 로 HPA 도 함께 정리된다.

## 6. 오류 처리

- HPA upsert 실패: 기존 `handleUpsertErr()` 경로를 사용해 conflict 는 requeue, 그 외 실패는 Warning Event 를 남긴다.
- HPA delete 실패: NotFound 는 무시하고, 그 외 오류는 reconcile 실패로 반환한다.
- invalid autoscale spec: webhook 에서 거부한다.

## 7. 테스트 / 검증 방법

단위/통합 테스트:

- `buildRouterHPA` 가 `scaleTargetRef`, min/max, CPU target 을 정확히 만든다.
- autoscale enabled cluster 가 router HPA 를 생성한다.
- autoscale disabled 로 전환하면 HPA 를 삭제한다.
- autoscale enabled 상태에서 기존 Deployment replicas 를 HPA 값처럼 임의 변경해도 reconcile 이 되돌리지 않는다.
- webhook 이 `maxReplicas < minReplicas`, `enabled=true && maxReplicas=0` 을 거부한다.

검증 명령:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Package ./internal/webhook/v1alpha1 -Run "TestValidate_.*Autoscale"
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -Run "TestBuildRouterHPA|TestBuildRouterDeployment"
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -GinkgoFocus "router autoscale"
```

API/RBAC 생성 파일 검증:

```bash
make manifests generate
make sync-crds
```

최종 수용 검증은 Docker/kind 또는 Dev Container 에서 묶어서 실행하고, 완료 후 `cleanup-test-e2e` 로 자원을
반납한다.

## 8. 대안 비교

### A. CPU HPA 먼저 구현

장점:

- Kubernetes 기본 metric 경로라 외부 adapter 없이 동작한다.
- 기존 API 필드 일부를 실제 운영 기능으로 연결한다.
- blast radius 가 작고 envtest 로 검증 가능하다.

단점:

- connection 수 기반 scale 보다 DB router 특성 반영이 약하다.

### B. active connections HPA 까지 동시에 구현

장점:

- router 부하 모델에 더 가깝다.

단점:

- `pg-router` `/metrics`, ServiceMonitor, Prometheus Adapter 또는 KEDA 설계가 필요하다.
- 한 번에 바뀌는 컴포넌트가 많아 live gate 없이 신뢰하기 어렵다.

### C. AutoSplit 먼저 구현

장점:

- shard 확장이라는 제품 가치가 크다.

단점:

- metric source, threshold window, `ShardSplitJob` 생성 정책, approval UX, rollback 정책이 모두 필요하다.
- online reshard live gate 가 아직 남아 있어 자동화부터 붙이면 위험하다.

추천은 **A안** 이다.

## 9. ★ Insight

이 기능의 핵심은 HPA 객체를 만드는 것이 아니라 **제어권 충돌을 제거하는 것**이다. operator 가 계속
`Deployment.spec.replicas` 를 강제하면 HPA 는 존재해도 실제 운영에서는 scale 이 불안정해진다. 따라서 HPA
활성 시 replicas 필드만 HPA 에 위임하고, Pod template 과 security/resource/image 는 operator 가 계속 관리하는
역할 분리가 필요하다.

실무에서 가장 자주 생기는 문제는 다음이다.

- `maxReplicas` 누락으로 scale 상한이 불명확해지는 문제
- CPU request 가 없어서 HPA utilization 계산이 불가능한 문제
- operator 와 HPA 의 replicas field fight
- custom metric 이 준비되지 않았는데 Pods metric 을 HPA 에 넣어 HPA 가 `Unknown` 상태가 되는 문제

이번 설계는 이 중 replicas field fight 와 custom metric 미준비 문제를 먼저 닫는다. CPU request 는 router
resource defaults 또는 sample manifests 보강에서 별도 확인이 필요하다.

## 10. 범위 밖

- `targetActiveConnections` 기반 autoscale
- `pg-router` Prometheus metrics endpoint
- Prometheus Adapter / KEDA 연동
- AutoSplit 자동 `ShardSplitJob` 생성
- HPA 상태를 `PostgresCluster.status.router` 에 반영
- live e2e 자동화

## 11. 한 줄 결론

Router autoscaling 1차 구현은 CPU HPA 로 시작하되, HPA 활성 시 Deployment replicas 를 HPA 에 위임하는 것이
설계의 핵심이다.
