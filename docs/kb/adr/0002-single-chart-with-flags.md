# ADR-0002: Helm 단일 chart + 컴포넌트 flag 정책

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

기존 ADR 0007 (Helm chart P1) 와 RFC 0002 (no GitHub Actions) 흐름 위에서 Helm 패키징 전략을 재설계한다. 이전 0.2.0-alpha 단계에서는 Citus (AGPLv3) 를 격리하기 위한 보조 chart 분리가 검토되었으나, 2026-05-02 사용자 결정으로 자체 분산 SQL 레이어를 채택하면서 AGPL 의존 자체가 제거되었다 (ADR-0001 참조). 따라서 *라이선스 격리용 보조 chart* 라는 분리 동기는 소멸했다. 동시에 1인 maintainer 가 6년 timeline 동안 다중 chart 를 lockstep 으로 유지하는 비용은 현실적으로 감당 불가능하다. router / resharder / rebalancer / KEDA glue / backup / monitoring 등 컴포넌트는 *선택적 활성화* 가 필요하지만, 이는 chart 분리 대신 `values.yaml` flag 로 충분히 모델링된다.

## Decision

`charts/postgres-operator/` 단일 chart 로 모든 operator 컴포넌트를 패키징하고, 선택 컴포넌트는 `values.yaml` 의 boolean flag 로 토글한다.

핵심 매개변수:

- chart 개수: **1** (`postgres-operator`).
- chart 버전 정책: SemVer + appVersion 일치 (operator 이미지와 chart version lockstep).
- 컴포넌트 토글 위치: `values.yaml` 최상위 키 (`router.enabled`, `resharder.enabled`, `rebalancer.enabled`, `autoscale.keda.enabled`, `backup.enabled`, `monitoring.serviceMonitor.enabled`, `monitoring.prometheusRule.enabled`, `security.networkPolicies.enabled`).
- 스키마 검증: `values.schema.json` 을 *strict top-level* (`additionalProperties: false`) 로 작성하여 오타·미지원 키를 install/upgrade 시 즉시 거부.
- conditional 렌더링: 각 `templates/<component>.yaml` 은 `{{- if .Values.<component>.enabled }} ... {{- end }}` 가드.
- umbrella sample chart 는 본 chart 에 포함하지 않고 별도 repo (`postgres-operator-samples`) 로 분리하여 운영 chart 와 데모용 의존성을 격리.
- Helm 호환성: 3.18+ 필수, 4.0 readiness 확보 (Wasm plugin 미사용, SSA 기본화 호환 검증).
- ArtifactHub: `artifacthub-repo.yml` + signed `.prov` 로 verified publisher 신청.

## Consequences

긍정:

- 운영 단순화 — 사용자는 `helm install` 한 번으로 전체 스택 배포, flag 만 조정하여 컴포넌트 선택.
- 1인 maintainer 가 lockstep 유지해야 할 chart 수가 1 로 고정되어 릴리스 부담 감소.
- ArtifactHub 등재·검색·signing 흐름이 단일 패키지 기준으로 단순화.
- Helm dependency 그래프 부재 → install/upgrade 시점 의존 충돌 회피.

부정:

- `values.yaml` 스키마가 비대화될 가능성. 특히 P2 (router) ~ P6 (분산 트랜잭션) 진행 중 키가 누적된다.
- 사용자가 일부 컴포넌트만 원해도 chart 전체를 받아야 함 (template 파일은 lazy 렌더되므로 런타임 비용은 없으나 chart 크기 증가).
- 컴포넌트별 독립 릴리스 사이클 불가 — router 만 패치하려면 chart 전체 version bump.

트레이드오프:

- `values.schema.json` strict 모드 의무화로 비대화에 대응. 모든 PR 에서 schema 갱신 누락 시 `helm lint --strict` 가 차단.
- 컴포넌트 추가 시 ADR 으로 신규 flag 와 default 값을 정당화 (사용자 가시 default 변경은 minor bump).
- chart 크기 증가 대비 운영 단순화의 가치가 1인 maintainer 환경에서 더 크다고 판단.

## Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| (a) 3-chart 분리 (`postgres-operator-lib` + `postgres-operator` + `postgres-operator-sample`) | 1인 maintainer 가 3개 chart 를 lockstep 유지하는 비용 과다. Library chart 의 추상화 가치는 다중 consumer 가 있을 때 발현되지만 본 프로젝트는 단일 consumer (자기 자신). |
| (b) 단일 chart + 외부 sample repo + library chart | library chart 부분 거절. sample repo 분리는 *부분 채택*. operator chart 는 자체 완결성을 가지며 외부 library 에 의존하지 않는다. |
| (c) operator-sdk / OLM bundle 단독 패키징 (Helm 미지원) | K8s 직접 사용자 (OpenShift 비사용자) 배제. ArtifactHub 의 Helm 패키지 channel 도 활용 불가. |
| (d) Kustomize overlay 단독 | 버전 관리·릴리스·dependency 표현이 Helm 대비 약함. 사용자는 이미 Helm 생태계를 기대. |
| (e) chart 1개 + 컴포넌트별 sub-chart (`charts/charts/router/`) | sub-chart 의 values 전파 규칙이 복잡하고 `values.schema.json` strict 검증과 충돌. flag 로 충분히 표현 가능한 단일 컴포넌트 토글에는 과도. |

## References

- ADR-0001 (자체 분산 SQL 결정 — AGPL Citus 의존 제거의 직접 원인)
- 이전 ADR 0007 (Helm chart P1, archive 됨) — 본 ADR 이 재정의 후 supersede
- RFC 0002 (no GitHub Actions, archive 됨) — 로컬 게이트 일원화 정책은 helm lint 도 pre-push 로 위임
- standards/linting.md — `helm lint --strict` 는 L2 pre-push hook 에서 강제
- standards/ci.md — 4 계층 게이트, helm template + kustomize build 는 L2/L3 단계
- standards/adr.md — 본 문서의 형식 표준
