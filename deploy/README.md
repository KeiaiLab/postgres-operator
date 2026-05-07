# deploy/ — GitOps 배포 디렉터리

본 디렉터리는 ArgoCD (또는 동등 GitOps tool) 가 git → cluster 단방향 동기를 수행하기 위한 매니페스트 진입점이다. **`config/` 와 별개 경로** — `make deploy` 등 단발성 푸시는 `config/default` 를 사용한다.

ADR-0006 의 결정에 따라 mongodb-operator / valkey-operator 와 동일 구조로 정합화되었다.

## 구조

```
deploy/
├── overlays/prod/                 # ArgoCD application path: operator 자체 (envName=prod, ns=data)
│   ├── kustomization.yaml         # config/{crd,rbac,manager} → namespace=data
│   └── delete-namespace.yaml      # 자동 생성 Namespace 제거
└── postgres-cluster.yaml          # ArgoCD application path: workload (CR 인스턴스, ns=data)
```

운영 모델: argos 클러스터 ns 통합 정책 (2026-05-06 cycle: 5 차트 모두 `data` ns 단일) 에 따라 operator 와 CR 이 *동일 data ns* 를 공유한다. envName 분리 (`overlays/prod`) 는 환경 식별자로만 유지.

## 현 운영 상태 (2026-05-08)

`keiailab/postgres-operator` 의 기존 미배포 원인은 argos-platform-data 의 ApplicationSet (`platform/data/application.yaml`) directories 목록에 operator path 가 없었던 것이다. 현재 production GitOps 진입점은 argos-platform-data 의 `postgres-operator/` Helm wrapper chart 이다.

2026-05-08 live 검증 기준으로 ArgoCD Application `platform-data-postgres-operator` 는 `Synced/Healthy` (revision `cc662773f1a286d6b11a768af151db0ccd47b63f`) 이고, `data` namespace 의 `platform-data-postgres-operator-controller-manager` Deployment 는 `1/1` 로 실행 중이다. live image 는 `ghcr.io/keiailab/postgres-operator:0.3.0-alpha.4` (`sha256:394ec5eb4aa09d316d957a3c751bb7283f21bfa71f19a9d2871ccbc7ec974f2f`) 이며 `PostgresCluster/argos-postgres` 는 `Ready=True` 이다.

본 디렉터리는 **대체 Kustomize 배포 진입점** (RFC-0004 §3) 으로 유지한다. argos production 은 `platform/data/postgres-operator` 경로를 우선 source of truth 로 사용한다.

⚠️ **범위 경계** — 위 상태는 Day-0 alpha-deployable single-shard 배포 완료를 뜻한다. HA replica, backup/restore drill, PITR, 장기 soak 가 남아 있으므로 0.4.0 single-shard production-ready 또는 GA 로 표기하지 않는다.

## 사전 조건 (cluster)

- [x] `data` namespace 사전 생성 (argos 2026-05-06 cycle 으로 Active).
- [x] StorageClass `ceph-rbd` (default) 이용 가능 — argos 클러스터 검증.
- [ ] (선택) `pg-admin-creds` Secret (data ns) — postgres-operator 가 자동 생성하지 않는 경우 ExternalSecret 으로 주입. RFC 0001 v2 schema 는 internal bootstrap 동작 가능.
- [ ] Prometheus Operator (monitoring.serviceMonitor.enabled=true 사용 시).
- [ ] PrometheusRule CRD 가용 (monitoring.prometheusRule.enabled=true 사용 시).

## 적용 (수동 검증)

```fish
# 1) 렌더 검증
kustomize build deploy/overlays/prod | head
kustomize build deploy/overlays/prod | grep -c "kind: Namespace"   # 0

# 2) operator 적용
kustomize build deploy/overlays/prod | kubectl apply -f -
kubectl -n data rollout status deploy/controller-manager

# 3) workload 적용
kubectl apply -f deploy/postgres-cluster.yaml
kubectl -n data get postgrescluster postgres-cluster \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

## 변경 절차

본 디렉터리 변경은 ADR 작성 후 진행 (`docs/kb/adr/`). 매번 `kustomize build deploy/overlays/prod` 렌더 검증.
