# ADR-0006: GitOps deploy 오버레이 도입 (3-repo 정합)

- Date: 2026-05-06
- Status: Accepted
- Authors: @eightynine01

## Context

`keiailab/{mongodb,postgresql,valkey}-operator` 3 repo 는 Operator SDK / kubebuilder 로 부트스트랩 되어 모두 `config/{crd,rbac,manager,default,...}` kustomize 트리를 가진다. `config/default` 는 namespace 를 `<op>-operator-system` 으로, namePrefix 를 `<op>-operator-` 로 강제한다. 이는 `make deploy` 같은 *단발성 클러스터 푸시* 에 적합하지만 GitOps (ArgoCD 가 git → cluster 단방향 동기) 시나리오에서는 다음 문제가 있다:

1. ArgoCD Application 의 `destination.namespace` 가 `prod` 로 운영되는데 `config/default` 의 `namespace: <op>-operator-system` 와 어긋남 → drift 가 영구화.
2. `config/default` 가 자동 생성하는 Namespace 리소스 (`<op>-operator-system`) 를 ArgoCD 가 매번 만들려 함 → prod 클러스터의 *사전 생성된 prod ns* 정책과 충돌.
3. 3 repo 중 mongodb-operator 만 `deploy/overlays/prod/` GitOps 진입점이 있어 정합성 불일치.

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
- ArgoCD application path 가 `deploy/overlays/prod` 로 고정 → drift 0.
- `config/default` 는 *로컬 개발* 용도로 보존되어 `make deploy` 워크플로 회귀 없음.
- 3 repo 가 동일 구조를 가져 운영자 인지 부하 감소.

부정:
- `config/manager/manager.yaml` 의 raw name 이 `system` 인 것에 의존. kubebuilder scaffold 가 향후 변경되면 patch target 도 갱신 필요.
- mongodb-operator 의 `config/manager/manager.yaml` 은 full name (`mongodb-operator-system`) 으로 수동 변경되어 있어 patch target name 만 1 줄 비대칭. 본 repo 는 kubebuilder scaffold 를 그대로 두는 쪽을 택함 (재생성 안전성 우선).

## Alternatives Considered

1. **`config/default` 를 직접 ArgoCD source 로 사용** — namespace 강제 변경 어렵고 자동 생성 Namespace 리소스 이슈 그대로. 거절.
2. **mongodb-operator 처럼 `config/manager/manager.yaml` 의 Namespace name 을 full name 으로 수동 변경** — 재생성 시 매번 패치 필요. operator-sdk regenerate 호환성 저하. 거절.
3. **Helm chart (`charts/`) 을 GitOps source 로 사용** — chart 는 별도 배포 경로 (Artifact Hub) 로 운영 중. GitOps 와 chart 배포가 동일 prod ns 에 중복 적용될 위험. 거절.
