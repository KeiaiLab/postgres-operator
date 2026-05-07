# Postgres Operator Helm Chart

`postgres-operator` chart는 `keiailab/postgres-operator`의 operator manager, RBAC, CRD, NetworkPolicy를 배포한다.

## 전제 조건

- Kubernetes 1.26+
- Helm 3.8+
- `kubectl`이 대상 클러스터를 바라보는 상태

## 설치

```bash
helm install postgres-operator ./charts/postgres-operator \
  --namespace postgres-operator-system \
  --create-namespace
```

CRD는 `crds/` 디렉터리에 포함되어 Helm 설치 시 기본 적용된다. CRD lifecycle을 별도로 관리하는 환경에서는 Helm 표준 옵션을 사용한다.

```bash
helm install postgres-operator ./charts/postgres-operator \
  --namespace postgres-operator-system \
  --create-namespace \
  --skip-crds
```

## 주요 값

| 값 | 설명 | 기본값 |
|---|---|---|
| `image.repository` | operator image repository | `ghcr.io/keiailab/postgres-operator` |
| `image.tag` | operator image tag. 비우면 chart `appVersion` 사용 | `""` |
| `replicas` | manager replica 수 | `1` |
| `rbac.create` | ClusterRole/ClusterRoleBinding 생성 여부 | `true` |
| `networkPolicies.enabled` | 데이터플레인 NetworkPolicy 생성 여부 | `true` |
| `metrics.enabled` | manager metrics port 노출 여부 | `true` |

## 사용자 시점 검증

[기능명] Helm chart 설치

사용자 시나리오:
1. 사용자는 chart를 대상 namespace에 설치한다.
2. 사용자는 CRD가 등록됐는지 확인한다.
3. 사용자는 operator Deployment와 RBAC가 생성됐는지 확인한다.
4. 사용자는 dev 샘플 `PostgresCluster`를 적용한다.

기대 결과:
- `postgresclusters.postgres.keiailab.io`, `backupjobs.postgres.keiailab.io` CRD가 표시된다.
- manager Deployment가 생성된다.
- RBAC와 NetworkPolicy가 렌더링된다.
- 샘플 CR 적용 시 schema 에러가 발생하지 않는다.

```bash
helm lint --strict ./charts/postgres-operator
helm template --include-crds gate ./charts/postgres-operator
kubectl get crd postgresclusters.postgres.keiailab.io backupjobs.postgres.keiailab.io
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
```

## 제거

```bash
helm uninstall postgres-operator -n postgres-operator-system
```

Helm은 CRD를 uninstall 시 자동 삭제하지 않는다. CRD까지 제거하려면 명시적으로 삭제한다.

```bash
kubectl delete crd postgresclusters.postgres.keiailab.io
kubectl delete crd backupjobs.postgres.keiailab.io
```
