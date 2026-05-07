# ADR-0005: 릴리스 채널 (alpha/beta/stable) 과 CRD apiVersion 진화

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

본 operator 는 P0~P7 약 6년 timeline 으로 v1.0 에 도달한다. 이 기간 동안 사용자가 *production 채택* 을 점진적으로 시작할 수 있어야 하고, 동시에 alpha 단계의 빠른 반복은 보호되어야 한다. 단일 채널 rolling 모델은 alpha 사용자에게 breaking change 를 강제하고, semver pre-1.0 (`0.x.y`) 만으로는 *어느 0.x 가 안정한가* 를 사용자에게 명확히 전달할 수 없다. 또한 CRD apiVersion 은 K8s 표준 진화 경로 (v1alpha1 → v1beta1 → v1) 를 따라야 하며 (ADR-0004 의 operator-managed CRD 모델 위에서), 각 단계는 conversion webhook 을 통한 안전한 마이그레이션이 보장되어야 한다.

## Decision

3개 릴리스 채널 (alpha / beta / stable) 을 운영하고, CRD apiVersion 을 phase 와 lockstep 으로 진화시킨다.

핵심 매개변수:

- **채널**:
  - `alpha` — P0~P3 (chart `0.3.x`~`0.6.x`). breaking change 자유. 호환성 보장 없음. release 빈도 높음 (월 1~2회).
  - `beta` — P4~P5 (chart `0.7.x`~`0.8.x`). field deprecation 시 최소 6개월 + 1 minor 사전 통지. backward compat 노력. release 빈도 중간 (분기 1회).
  - `stable` — P6~ (chart `0.9.x`, `1.x.y`). LTS 24개월. semver patch/minor 호환. major (1.x → 2.x) 는 12개월 사전 RFC + 마이그레이션 가이드 의무.
- **CRD apiVersion 진화**:
  - `v1alpha1` — P0~P3 단계. 모든 CRD (PostgresCluster, ShardRange, ShardSplitJob, BackupJob).
  - `v1beta1` — P4 진입 시. v1alpha1 도 동시 served (storage version 은 v1beta1). conversion webhook 배포.
  - `v1` — P6 진입 시. v1beta1 served (deprecated, 24개월 후 제거). storage version 은 v1.
- **이미지 / chart 태그 매핑**:
  - alpha: `quay.io/postgres-operator/manager:v0.X.Y-alpha.N` + chart `version: 0.X.Y-alpha.N`.
  - beta: `:v0.X.Y-beta.N` + chart `0.X.Y-beta.N`.
  - stable: `:v1.Y.Z` + chart `1.Y.Z`. (0.9.x 는 stable 의 RC 단계로 별도 명시.)
- **Helm repo 인덱스**: 각 채널은 별도 repo URL 또는 동일 repo 내 `Chart.yaml` annotation 으로 분리 (`artifacthub.io/channel: alpha|beta|stable`).
- **Breaking change 정책**:
  - alpha → beta promotion: 모든 alpha 사용자에게 마이그레이션 가이드 (`docs/migrations/alpha-to-beta.md`) 의무.
  - beta → stable promotion: deprecated 필드 제거 시점이 stable 진입 시. 이후 24개월 LTS.
  - 채널 내부 (예: alpha → 다음 alpha) 도 breaking 발생 시 CHANGELOG `BREAKING CHANGE:` 명시 + 마이그레이션 노트.
- **Conversion webhook**:
  - operator 가 manager 와 동일 pod 에서 호스팅 (별도 Deployment 분리 안 함, P4 단계 단순화).
  - cert-manager 발급 인증서 사용 (ADR 별도 X — 본 ADR 에 통합).
  - v1alpha1 ↔ v1beta1 ↔ v1 모든 페어 변환 의무 (storage version 으로의 양방향).
- **deprecation 표기**:
  - CRD field: `// +kubebuilder:deprecatedversion:warning="..."` annotation.
  - values.yaml key: `values.schema.json` 의 `deprecated: true` + NOTES.txt 경고.
- **사용자 채택 가이드 (`docs/channels.md` 신설)**:
  - "production 사용 가능 시점: P1 (single-shard, alpha) 부터. 단 alpha 채널은 breaking change 가능 — beta 진입 시 마이그레이션 필요."
  - "단일 클러스터에서 채널 혼용 금지. 클러스터당 한 채널."

## Consequences

긍정:

- 사용자 신뢰 — "어느 버전을 production 에 써도 되는가" 가 채널명으로 즉시 전달.
- alpha 사용자의 빠른 반복 보호 — beta/stable 사용자에게 영향 없이 실험 가능.
- CRD apiVersion 진화가 K8s 표준 패턴 그대로 — kubectl, CRD-aware 도구 (Argo CD, Flux) 와 호환.
- conversion webhook 으로 *클러스터 내 혼합 버전 CR* 안전 처리.
- ArtifactHub 의 channel 분리로 사용자가 의도치 않게 alpha 를 잡는 사고 방지.

부정:

- 다중 채널 유지 비용 — 1인 maintainer 가 alpha + beta + stable 동시 운영 시 backport 부담. P6 진입 전까지는 stable 채널이 비어있으므로 부담은 점진적.
- conversion webhook 운영 부담 — cert 회전, webhook 가용성, P4 시점 단순 single-pod 호스팅 채택으로 완화.
- LTS 24개월 약속 — stable 진입 후 12개월 전부터 다음 major 의 RFC 시작 의무.

트레이드오프:

- 채널 분리로 인한 운영 복잡도 ↔ 사용자 신뢰 + production 채택 가능성. 사용자 신뢰가 우선 (operator 채택률은 신뢰의 함수).
- conversion webhook 의 추가 컴포넌트 ↔ CRD 진화 안전성. 진화 안전성이 우선 (데이터 손실 회피).
- 다중 채널 backport 부담 ↔ alpha 사용자의 반복 속도. 반복 속도가 우선 (alpha 의 본질).

## Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| (a) 단일 채널 rolling — semver 만 (`0.x.y` → `1.0.0` → `1.x.y`) | pre-1.0 의 안정성 표현 모호. alpha 사용자 보호 부재 — `0.4.0` 사용자가 `0.5.0` 의 breaking change 에 노출. |
| (b) 2채널 (stable + edge) | edge 가 alpha + beta 를 모두 흡수해야 하나, alpha 와 beta 의 호환 약속 차이를 표현 불가. |
| (c) 4채널+ (nightly + alpha + beta + stable + LTS) | 1인 maintainer 환경에서 과도. nightly 는 commit-id 기반 image 로 대체 가능 (`:sha-abc1234`). |
| (d) CRD apiVersion 진화 없이 v1alpha1 영구 유지 | K8s 컨벤션 위배. CRD-aware 도구 (kubectl explain, Argo CD diff) 가 alpha 를 production 으로 표시 안 함. |
| (e) Conversion webhook 없이 v1alpha1 → v1beta1 즉시 전환 (storage version 변경만) | 클러스터 내 잔존 v1alpha1 CR 의 read 실패. 데이터 손실 위험. |
| (f) 채널 라벨만 분리 (이미지 태그 동일) | helm repo / ArtifactHub 가 채널 인식 불가. 사용자가 의도치 않게 alpha 잡는 사고. |
| (g) date-based versioning (`2026.05.0`) | semver 의 breaking/feature/patch 의미 손실. K8s 생태계 컨벤션 위배. |

## References

- ADR-0001 (자체 분산 SQL) — phase 정의의 출처
- ADR-0004 (operator-managed CRD) — 본 ADR 의 CRD 라이프사이클 전제
- standards/adr.md — 형식 표준
- standards/commits.md — Conventional Commits 의 `BREAKING CHANGE:` footer 활용
- Kubernetes API versioning conventions (https://kubernetes.io/docs/reference/using-api/#api-versioning)
- ArtifactHub channel annotations 명세
- Operator SDK conversion webhook 가이드
