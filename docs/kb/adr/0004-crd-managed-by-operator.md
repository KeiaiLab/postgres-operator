# ADR-0004: CRD 라이프사이클은 operator manager 가 소유

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

Helm 의 `crds/` 디렉토리는 *install-only* 동작을 가진다. `helm install` 시 한 번 적용되고 `helm upgrade` 와 `helm uninstall` 시점에는 무시된다. 이는 CRD 진화 (필드 추가, validation 강화, conversion webhook 도입) 를 chart upgrade 흐름에서 끊어버려, 사용자가 별도 절차 (`kubectl apply -f crds/`) 를 수동 실행해야 하는 운영 부채를 만든다. 본 operator 는 6년 timeline (P0~P7) 동안 CRD 가 v1alpha1 → v1beta1 → v1 으로 진화하며 (ADR-0005 참조), Helm `crds/` 모델로는 안전한 진화를 제공할 수 없다. 동시에 OLM (Operator Lifecycle Manager) 은 operator 가 CRD 를 *직접 소유* 하는 모델을 권장하므로, 본 결정은 OLM 통합 친화성도 동시 확보한다.

## Decision

Operator manager (cmd/main.go) 가 startup 시점에 CRD 를 server-side apply (SSA) 하여 라이프사이클을 직접 소유한다. Helm chart 의 `crds/` 디렉토리는 폐기한다.

핵심 매개변수:

- **CRD 소스**: `config/crd/bases/*.yaml` (controller-gen 생성) 을 operator 이미지에 embed (`//go:embed config/crd/bases/*.yaml`).
- **적용 시점**: `cmd/main.go` 의 manager.Start() 직전, leader election 획득 후 1회.
- **적용 방식**: server-side apply (`fieldManager: postgres-operator`, `force: true`). 다른 fieldManager 가 추가한 필드는 보존.
- **owner annotation**: `postgres-operator.io/managed-by: operator`. CRD 변경 추적·debugging 지원.
- **버전 conflict 처리**: 운영자 image 가 *downgrade* (예: v0.5.0 → v0.4.0) 될 때 CRD apply 를 *skip* (storage version 보존). `kubectl annotate` 로 강제 override 옵션 (`postgres-operator.io/allow-crd-downgrade: "true"`) 제공.
- **Helm chart 변경**:
  - `charts/postgres-operator/crds/` 디렉토리 **제거**.
  - `templates/NOTES.txt` 에 "CRD 는 operator pod 기동 후 약 5초 내 자동 등장 — `kubectl get crd | grep postgres-operator` 로 확인" 명시.
  - `helm uninstall` 시 CRD 는 *남는다* (의도적). 데이터 보호. 명시적 정리는 사용자가 `kubectl delete crd -l app.kubernetes.io/managed-by=postgres-operator` 로 수행.
- **개발자 도구**: `make sync-crds` 는 *유지*. controller-gen → `config/crd/bases` → `docs/api-reference` 동기화 검증용. operator 가 embed 하는 CRD 와 docs 가 일치하지 않으면 CI L2 게이트에서 차단.
- **OLM 통합**: 본 결정은 OLM `installModes` 의 `OwnNamespace` / `SingleNamespace` / `AllNamespaces` 모두와 호환. CSV (ClusterServiceVersion) 의 `customresourcedefinitions.owned` 항목은 operator-sdk 가 자동 생성.

## Consequences

긍정:

- `helm upgrade` 시 CRD 자동 진화 — 사용자 수동 단계 0.
- `helm uninstall` 시 CRD/CR 데이터 자동 보존 — 운영 사고 위험 감소.
- OLM 마켓플레이스 (OperatorHub.io) 등재 친화 — operator 가 CRD owner 인 표준 패턴.
- conversion webhook 배포 (P5 이후) 가 operator 와 lockstep 으로 묶여 *부분 배포* 사고 차단.
- CRD validation 강화 시 operator 만 upgrade 하면 즉시 적용.

부정:

- `helm install` 직후 `kubectl get crd` 가 비어있을 수 있음 — operator pod 기동 (~5초) 까지 대기 필요. NOTES.txt 명시로 완화.
- operator pod 가 CRD apply 권한 (cluster-scoped: `apiextensions.k8s.io/customresourcedefinitions:create,update,patch`) 필요. RBAC 폭이 약간 확장.
- `helm uninstall` 후 CRD 가 남아있어 *재설치* 시 기존 CR 인스턴스가 보임 — 의도된 동작이나 사용자가 놀랄 수 있음. uninstall NOTES 명시.

트레이드오프:

- "Helm 단독 사용자" 의 즉시성 ↔ "OLM/장기 운영" 사용자의 안전한 진화. 후자가 우선 (1인 maintainer 가 6년 운영).
- RBAC 확장 ↔ CRD 라이프사이클 자동화. RBAC 은 `ClusterRole` 명시로 투명성 확보 가능.

## Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| (a) Helm `crds/` 디렉토리 유지 (현재 chart 모델) | upgrade 시점 CRD 진화 불가. 6년 timeline 의 v1alpha1 → v1 전이 시 사용자 수동 단계 폭발. |
| (b) `crd-install` Helm hook 사용 | install 시 CRD 적용은 가능하나 upgrade hook 은 ordering 보장이 약하고 helm 4 deprecation 후보. |
| (c) OLM 전용 (Helm 미제공) | K8s 직접 사용자 (vanilla cluster, on-prem, edge) 배제. ArtifactHub Helm channel 활용 불가. |
| (d) helm-charts repo 의 `pre-install` Job 으로 `kubectl apply` | Job 권한·sa 분리·실패 처리 복잡. operator 자체에 통합하는 것이 단순. |
| (e) Static CRD bundle (`postgres-operator-crds-1.0.0.yaml`) 별도 배포 | 사용자가 chart 와 별도로 받아야 함. lockstep 보장 부재. |
| (f) operator 가 CRD 를 *생성만 하고 update 안 함* | 진화 단절. ADR 의도에 정면 충돌. |

## References

- ADR-0002 (Helm 단일 chart) — `crds/` 폐기는 chart 정책의 일부
- ADR-0005 (versioning + channels) — CRD apiVersion 진화는 operator 소유 모델에서만 안전
- standards/adr.md — 본 문서 형식
- Helm 공식 문서 — chart best practices, CRD limitations
- Kubernetes API conventions — server-side apply, fieldManager 규칙
- OLM 공식 문서 — operator-managed CRD 패턴
