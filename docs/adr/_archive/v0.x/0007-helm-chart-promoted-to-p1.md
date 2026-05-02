# ADR 0007 — Helm chart을 P14에서 P1 트랙으로 분리

- **상태**: Accepted
- **날짜**: 2026-04-30
- **결정자**: @keiailab/maintainers
- **관련**: roadmap.md (14 Pillar), Bitnami PostgreSQL Helm Chart 비교 (`/Users/phil/.claude/plans/1-https-artifacthub-io-packages-helm-bit-sunny-wozniak.md` §5 P1-4)

## 컨텍스트

`docs/roadmap.md`는 Helm chart을 **P14 Distribution**에 배치 — install.yaml, OLM bundle, multi-arch image와 묶음. P14는 *v1.0 GA 마지막 단계*로 정의되어, 다른 모든 Pillar(P1~P13) M3 통과 후 진입.

문제: alpha/beta 단계에서 사용자가 operator를 *어떻게 설치*하는가? 현재는 Kustomize manifests만 제공 (`config/default`). 실제 사용자 채널은 *Helm chart가 사실상 표준*이라:

- alpha 단계 사용자가 시도조차 못 함 → feedback loop 차단
- "PGO-class" 패리티 약속에 *배포 채널 패리티*도 포함되나 P14까지 부재
- Bitnami는 chart가 *유일한 채널*이며 매우 mature — 같은 시장 사용자에게 "Kustomize 외 없음"은 진입 마찰

## 결정

Helm chart을 *P14에서 분리*하여 P1 Core Lifecycle 트랙의 후속 task(P1-T5)로 재배치한다. P14에는 *나머지* distribution 산출물만 남긴다:

| 변경 전 (P14) | 변경 후 |
|---|---|
| Helm chart | **P1 트랙으로 이동** (alpha 사용자 채널) |
| install.yaml | P14 유지 |
| OLM bundle | P14 유지 |
| multi-arch image | P14 유지 |

### chart 분리 모델

`charts/` 아래 두 chart 별도 패키징:

- `charts/postgresql-operator/` — operator 자체 (Deployment + RBAC + CRD + NetworkPolicy + ServiceAccount)
- `charts/postgrescluster/` — PostgresCluster CR 인스턴스 (선택, P1-T5 sub-task)

이는 Bitnami의 `postgresql` (CR 인스턴스) + 별도 operator chart 패턴의 *역배치* — operator chart가 *상위*, CR chart가 *옵션*. 사유: 사용자가 operator는 한 번 설치, CR은 namespace당 N개.

## 근거

### 왜 P14 전체 이동이 아닌 *분리*인가
P14의 install.yaml, OLM, multi-arch는 *후행 산출물* — 모든 CRD가 동결된 후에 생성하는 것이 안전. 그러나 Helm chart은 *현재 CRD 상태*를 패키징하면 되므로 *지금* 가능하다. 무리해서 OLM/multi-arch까지 앞당기면 *모든* CRD가 unstable한 alpha 상태에서 매번 재패키징.

### 왜 chart 두 개로 분리인가
- *operator*는 cluster-scope 한 번 설치
- *PostgresCluster CR*은 namespace당 N개 — chart가 다중 인스턴스를 지원하려면 helm release 별로 분리 필요
- Bitnami가 `postgresql` (CR 인스턴스 chart)을 메인으로 두는 이유와 동일

### 왜 *지금*인가 — 기존 P14 유지의 비용

| 비용 | 영향 |
|---|---|
| alpha 사용자 부재 | 실 사용 feedback 0, 회귀 발견 늦음 |
| Bitnami로 사용자 이탈 | "PGO-class" 약속의 인지도 손실 |
| chart 작성을 v1.0 직전에 모아서 처리 시 burden | 각 CRD별 template + values default 동시 결정 → 회귀 위험 |

## 트레이드오프

- **두 경로 동시 유지**: Kustomize(`config/default`) + Helm(`charts/postgresql-operator`) 동시 운영 → 변경 시 두 곳 갱신 부담. 완화: chart template이 `config/`의 manifests를 generate (Makefile 타겟에서 `kustomize build config/default | helmify`로 자동화 검토).
- **chart 안정성 약속**: alpha chart는 `Chart.yaml: appVersion`이 v0.x이므로 breaking change 가능. 사용자가 이를 인지해야 함. 완화: chart README에 "alpha 단계, breaking 가능" 명시.
- **OLM bundle 분리 유지**: OperatorHub 사용자는 여전히 P14까지 대기 — 사용자 세그먼트 차이를 인정하고 P14 우선순위는 유지.

## 결과

- `docs/roadmap.md`에서 P14 정의 갱신 — Helm chart을 *P1 트랙*으로 이동.
- 신규 디렉토리 `charts/postgresql-operator/` (P1-4 권장 implementation 시).
- Makefile에 `chart-package`, `chart-lint` 타겟 추가.
- TASKS.md에 P1-4 권장 등록.
- 본 ADR은 v1.0 GA 시점에 *재평가* — Helm chart 안정성과 OLM bundle 통합 시점 검토.

## 강제 메커니즘

| 메커니즘 | 위치 | 도입 시점 |
|---|---|---|
| roadmap.md 갱신 | `docs/roadmap.md` §14 Pillar | 본 ADR 동시 |
| chart 골격 | `charts/postgresql-operator/` | P1-4 |
| chart lint CI 단계 | Makefile + 로컬 hook | P1-4 |
| `helm install --dry-run` 회귀 | e2e | P1-4 |
