# ADR-0023: OperatorHub.io 최신 버전 자동 sync

- **Date**: 2026-05-14
- **Status**: Proposed
- **Authors**: @phil

## Context

keiailab/{valkey,mongodb,postgres}-operator 3 repo 모두 OLM bundle scaffold (bundle/manifests + metadata) 적용됨 (valkey ADR-0037 + PR-B9 / mongodb + postgres 동일 패턴). 그러나 *bundle CSV (ClusterServiceVersion) 의 version field 가 chart version 과 drift*:

| Repo | Chart.yaml | Bundle CSV | Latest tag |
|---|---|---|---|
| valkey | 1.0.12 | 1.0.9 | v1.0.13 |
| mongodb | 1.4.23 | 1.4.19 | v1.5.0 |
| postgres | 0.3.0-alpha.18 | (TBD, 신규 file) | v0.3.0-alpha.16 |

OperatorHub.io 의 *community-operators submission* (k8s-operatorhub/community-operators repo 의 PR) 시 bundle CSV 가 사용자 가시 — *stale 버전* 등록되면 *manual update 의무*.

## Decision

**release.yml workflow 의 별 job 으로 *bundle 자동 sync*** 추가. release tag 시 다음 chain 실행:

1. **preflight** (기존): Chart.yaml ↔ tag parity
2. **bundle-regen** (신규):
   - `make bundle VERSION=<tag>` — operator-sdk + kustomize 통해 CSV regen
   - bundle CSV 의 *version + spec.replaces + image tag* 모두 *최신* 으로 sync
3. **bundle-commit** (신규):
   - regen 된 bundle 변경을 *별 branch* (release/bundle-vX.Y.Z) 으로 push
   - main 으로 PR 자동 생성 (gh pr create)
4. **community-operators-submission** (신규, optional):
   - bundle 변경을 *k8s-operatorhub/community-operators* fork 의 별 PR 으로 submission
   - 별 GitHub PAT (community_operators_submit_token) 필요 — 사용자 영역

## Consequences

### 긍정
- bundle CSV 가 *항상 최신 chart version 과 sync*
- OperatorHub.io 등록 시 *manual update 의무 0*
- 3 repo 모두 *동일 mechanism* — consistency

### 부정
- release.yml 의 *job 수 +2~3* — release pipeline 길어짐
- *operator-sdk + kustomize* dependency (현재 valkey Makefile 에 이미 의존, 다른 repo 도 동일 추가)

### Trade-off
- *bundle CSV 별 PR* vs *release commit 안에 inline* — PR 분리가 review 용이.

## Alternatives Considered

- **A1. Manual bundle update**: 매 release 시 maintainer 가 *make bundle* 수동 실행 — Rejected (drift 위험).
- **A2. Renovate bundle bump rule**: Renovate config 의 *custom regex manager* 으로 bundle CSV version 추적 — 복잡, 부분만 sync.
- **A3. 본 ADR**: release.yml chain 자동화 — **Accepted**.

## Implementation Path

1. **Phase A** (각 repo 별 PR):
   - Makefile 의 `make bundle VERSION=$(VERSION)` target 검증 (valkey 이미 활성, mongodb + postgres 추가)
   - release.yml 에 `bundle-regen + bundle-commit` job 추가
2. **Phase B**: community-operators submission token 환경 변수 등록 (사용자 admin 영역)
3. **Phase C**: 첫 release (v1.5.1 / v1.0.14 / v0.3.0-alpha.19) 시 mechanism 검증

## References

- valkey ADR-0037 OperatorHub.io bundle scaffold
- valkey PR-B9 (#21) + PR-B9.2 (#22) — alm-examples + bundle 초기 setup
- OperatorHub.io community-operators repo: https://github.com/k8s-operatorhub/community-operators
- operator-sdk bundle docs: https://sdk.operatorframework.io/docs/olm-integration/quickstart-bundle/

## Related ADRs

- ADR-0001 self-built distributed SQL keystone (postgres)
- ADR-0006 Repmgr/PgBouncer/Barman integration (postgres bitnami parity 100%)
