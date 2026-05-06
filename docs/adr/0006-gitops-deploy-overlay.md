# ADR-0006: GitOps deploy 오버레이 도입 (3-repo 정합)

- Date: 2026-05-06
- Status: Accepted
- Authors: @eightynine01

## Context

`keiailab/{mongodb,postgresql,valkey}-operator` 3 repo 는 Operator SDK / kubebuilder 로 부트스트랩 되어 모두 `config/{crd,rbac,manager,default,...}` kustomize 트리를 가진다. `config/default` 는 namespace 를 `<op>-operator-system` 으로, namePrefix 를 `<op>-operator-` 로 강제한다. 이는 `make deploy` 같은 *단발성 클러스터 푸시* 에 적합하지만 GitOps (ArgoCD 가 git → cluster 단방향 동기) 시나리오에서는 다음 문제가 있다:

1. ArgoCD Application 의 `destination.namespace` 가 `prod` 로 운영되는데 `config/default` 의 `namespace: <op>-operator-system` 와 어긋남 → drift 가 영구화.
2. `config/default` 가 자동 생성하는 Namespace 리소스 (`<op>-operator-system`) 를 ArgoCD 가 매번 만들려 함 → prod 클러스터의 *사전 생성된 prod ns* 정책과 충돌.
3. 3 repo 중 mongodb-operator 만 `deploy/overlays/prod/` 진입점이 있어 정합성 불일치.

### 현 운영 상태 (2026-05-06 인벤토리)

본 ADR 작성 시점의 클러스터 라이브 상태:

- **mongodb**: `keiailab/argos-platform-data/mongodb` umbrella chart 가 ArgoCD ApplicationSet path 로 운영 중 (`platform/data/application.yaml` revision=stable). umbrella 가 keiailab/mongodb-operator helm chart 1.4.5 를 dependency 로 흡수.
- **valkey**: `keiailab/argos-platform-data/valkey` 가 *bitnami/valkey* 5.6.1 (replication 1+1) 로 운영 중. keiailab/valkey-operator 는 *클러스터 미배포*.
- **postgresql**: ApplicationSet path 에 postgresql 없음 (cnpg 만 등록). keiailab/postgresql-operator 는 *클러스터 미배포* — 본 deploy/ 가 **Day-0 GitOps 첫 배포 후보 진입점**.

본 ADR 의 deploy/overlays/prod 는 따라서 *유일한 ArgoCD source 가 되도록 강제하는 것이 아니라*, argos-platform-data umbrella chart 가 본 path 를 직접 가리키게 마이그레이션 가능한 *대체/예비 진입점* 으로 정의된다. RFC-0004 §3 "Day-0 GitOps 첫 배포" 시나리오 적용 가능.

## Decision

각 operator repo 에 mongodb-operator 와 동일 구조의 GitOps 오버레이 계층을 도입한다.

```
deploy/
├── overlays/prod/
│   ├── kustomization.yaml      # config/{crd,rbac,manager} 를 prod ns 로 묶음
│   └── delete-namespace.yaml   # 자동 생성 Namespace 를 strategic-merge 로 제거
└── <workload>.yaml             # CR 인스턴스 (db ns, ArgoCD 별도 application)
```

- `kustomization.yaml` 의 `namespace: prod` 가 모든 namespaced 리소스에 적용된다.
- `patches.target.name` 은 *namePrefix 적용 전 raw name* (`system`) 으로 잡는다 — overlay 가 `config/default` 가 아닌 `config/manager` 를 직접 import 하므로.
- CR 인스턴스는 `db` namespace 를 사용하며 별개 ArgoCD application 으로 동기화한다 (operator 와 workload 의 라이프사이클 분리).

## Consequences

긍정:
- ArgoCD application source 후보가 `deploy/overlays/prod` 로 명시화 — argos-platform-data 의 umbrella chart 가 본 path 를 dependency 로 흡수하거나, 또는 본 path 를 *직접* ApplicationSet generator path 로 등록 가능.
- `config/default` 는 *로컬 개발* 용도로 보존되어 `make deploy` 워크플로 회귀 없음.
- 3 repo 가 동일 구조를 가져 운영자 인지 부하 감소.

부정:
- `config/manager/manager.yaml` 의 raw name 이 `system` 인 것에 의존. kubebuilder scaffold 가 향후 변경되면 patch target 도 갱신 필요.
- mongodb-operator 의 `config/manager/manager.yaml` 은 full name (`mongodb-operator-system`) 으로 수동 변경되어 있어 patch target name 만 1 줄 비대칭. 본 repo 는 kubebuilder scaffold 를 그대로 두는 쪽을 택함 (재생성 안전성 우선).

## Alternatives Considered

1. **`config/default` 를 직접 ArgoCD source 로 사용** — namespace 강제 변경 어렵고 자동 생성 Namespace 리소스 이슈 그대로. 거절.
2. **mongodb-operator 처럼 `config/manager/manager.yaml` 의 Namespace name 을 full name 으로 수동 변경** — 재생성 시 매번 패치 필요. operator-sdk regenerate 호환성 저하. 거절.
3. **Helm chart (`charts/`) 을 GitOps source 로 사용** — argos-platform-data 의 mongodb umbrella chart 가 이미 본 패턴 (operator chart 를 dependency 로 흡수). postgresql-operator 도 동일 방식 가능. 본 ADR 은 *그것과 별개 진입점* 도입을 결정하는 것이지 helm 경로를 부정하지 않는다. 두 진입점 (helm wrapper / kustomize overlay) 은 동일 cluster state 를 산출하도록 향후 parity invariant (valkey ADR-0028 격) 가 도입돼야 한다. 후속 ADR 에서 다룬다.
