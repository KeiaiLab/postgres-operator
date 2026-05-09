# ADR-0013: OperatorHub.io bundle scaffold (PR-B9 cross-cut)

- Date: 2026-05-10
- Status: Accepted
- Authors: @eightynine01

## Context

valkey-operator PR-B9 (ADR-0037 in valkey repo) 가 OperatorHub.io 등록 기술적
전제를 갖췄다. cross-cut 통일 — postgres-operator + mongodb-operator 도 동일
bundle scaffolding 으로 외부 OperatorHub 발견성을 갖춘다. ADR-0016 (Cross-cut
Audit Pattern, mongodb 출처) 정합.

## Decision

valkey 패턴 byte-identical 이식:
1. `config/manifests/bases/postgres-operator.clusterserviceversion.yaml` —
   2 CRD owned (PostgresCluster, BackupJob), 메타데이터 (description / keywords
   / maintainers / provider / maturity=alpha / minKubeVersion=1.26.0).
2. `config/manifests/kustomization.yaml` — CSV + crd + rbac + manager + samples.
   webhook 은 kustomization.yaml 부재 (`config/webhook/manifests.yaml` 단일 파일)
   로 제외 — OLM 이 webhook deployment 자동 처리.
3. Makefile `bundle` / `bundle-build` 타겟 (operator-sdk 1.42 + kustomize).
4. alm-examples — 2 sample (dev + prod) inline JSON.

`config/manager/kustomization.yaml` 의 image tag 갱신은 본 PR 에서는 *생략* —
postgres release pipeline 이 이미 image push 시점에 kustomize edit set image
를 처리.

## Consequences

긍정:
- 3 operator (valkey + postgres + mongodb 후속) cross-cut 통일.
- `make bundle VERSION=...` 재현 가능 — release 자동화 후속 작업 진입점.
- 2 CRD 의 `customresourcedefinitions.owned` 명시 — OLM 카탈로그 정확.

부정:
- BackupJob 의 alm-examples 부재 (sample 파일 부재) — operator-sdk warning.
  후속 PR-B9.2.1 에서 BackupJob sample 추가.
- valkey 와 비교 시 `containerImage` 가 `0.3.0-alpha.15` (alpha) — community-
  operators PR 시점에 stable channel 분리 결정 필요.

## Alternatives Considered

1. **valkey 와 별개 다른 패턴**: 거절. cross-cut 통일성 ↑.
2. **webhook 포함**: 거절. config/webhook 의 kustomization.yaml 추가는 별 작업
   (kubebuilder regenerate 영향). OLM 이 webhook deployment 자동 처리 가능.

## References

- valkey ADR-0037 (OperatorHub bundle scaffold).
- ADR-0016 (mongodb): Cross-cut Audit Pattern.
- operator-sdk 1.42: <https://sdk.operatorframework.io/docs/olm-integration/>.
- 후속: PR-B9.2.1 BackupJob sample 추가, PR-B9.3 community-operators PR 제출.
