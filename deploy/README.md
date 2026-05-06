# deploy/ — GitOps 배포 디렉터리

본 디렉터리는 ArgoCD (또는 동등 GitOps tool) 가 git → cluster 단방향 동기를 수행하기 위한 매니페스트 진입점이다. **`config/` 와 별개 경로** — `make deploy` 등 단발성 푸시는 `config/default` 를 사용한다.

ADR-0006 의 결정에 따라 mongodb-operator / valkey-operator 와 동일 구조로 정합화되었다.

## 구조

```
deploy/
├── overlays/prod/                 # ArgoCD application path: operator 자체
│   ├── kustomization.yaml         # config/{crd,rbac,manager} → namespace=prod
│   └── delete-namespace.yaml      # 자동 생성 Namespace 제거
└── postgres-cluster.yaml          # ArgoCD application path: workload (CR 인스턴스, db ns)
```

운영 모델: **operator 와 workload 는 별개 ArgoCD application** — operator 는 prod ns, 데이터는 db ns.

## 현 운영 상태 (2026-05-06)

`keiailab/postgresql-operator` 는 *클러스터 미배포 상태* — argos-platform-data 의 ApplicationSet (`platform/data/application.yaml`) path 목록에 postgresql 항목이 없으며, postgresql 워크로드는 cnpg 가 별도 운영 중이다.

본 디렉터리는 따라서 **Day-0 GitOps 첫 배포 진입점** (RFC-0004 §3) 이다. 적용 시 argos-platform-data/application.yaml 의 ApplicationSet directories 에 postgresql path 추가 (또는 본 repo 의 deploy/overlays/prod 를 가리키는 별도 ApplicationSet) 가 선행 작업이다.

## 사전 조건 (cluster)

- [ ] `prod` namespace 사전 생성.
- [ ] `db` namespace 사전 생성.
- [ ] StorageClass `ceph-block` 이용 가능.
- [ ] (선택) `pg-admin-creds` Secret (db ns) — postgres-operator 가 자동 생성하지 않는 경우 ExternalSecret 으로 주입. RFC 0001 v2 schema 는 internal bootstrap 동작 가능.
- [ ] Prometheus Operator (monitoring.serviceMonitor.enabled=true 사용 시).
- [ ] PrometheusRule CRD 가용 (monitoring.prometheusRule.enabled=true 사용 시).

## 적용 (수동 검증)

```fish
# 1) 렌더 검증
kustomize build deploy/overlays/prod | head
kustomize build deploy/overlays/prod | grep -c "kind: Namespace"   # 0

# 2) operator 적용
kustomize build deploy/overlays/prod | kubectl apply -f -
kubectl -n prod rollout status deploy/postgresql-operator-controller-manager

# 3) workload 적용
kubectl apply -f deploy/postgres-cluster.yaml
kubectl -n db get postgrescluster postgres-cluster \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

## 변경 절차

본 디렉터리 변경은 ADR 작성 후 진행 (`docs/adr/`). 매번 `kustomize build deploy/overlays/prod` 렌더 검증.
