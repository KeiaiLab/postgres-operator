# ADR-0027: GitOps overlay + ArtifactHub 검증 파이프라인 표준화

- Date: 2026-06-02
- Status: Accepted
- Authors: @phil

## Context

keiailab operator 4종(mongodb-operator / postgres-operator / valkey-operator + operator-commons
라이브러리)의 cross-repo 표준이 비일치 상태였다:

- **GitOps overlay 경로 drift**: mongodb는 `examples/gitops/`, postgres/valkey는
  `deploy/overlays/prod/` — 동일 GitOps 패턴이 서로 다른 경로에 분산.
- **ArtifactHub 검증 자동화 부재**: ArtifactHub Signed badge 전제 조건인 PGP
  signingKey(`89A409476828CB992338C378651E51AF520BCB78`) 메타데이터 검증이 수동에만
  의존.
- **CI 게이트 비대칭**: postgres의 PR 게이트가 0종이었으며(Phase 6 이전), valkey가 가장
  성숙한 reference 구현(ADR-0024 수기 chart 패턴 + ADR-0044 Signed/Official trust
  badge).

postgres-operator는 이 표준화 작업에서 특히 **PR 게이트 부재**가 핵심 문제였다:
signingKey 메타데이터, `artifacthub-verify.yml` 워크플로, CI 8종 보강이 동시에
필요했다.

## Decision

**2-레이어 분리**를 전 4종에 적용한다:

- **Layer 1 — ArtifactHub publish** (4종 모두): helm chart(`charts/<name>/`) → gh-pages
  → ArtifactHub Signed badge. 공통 PGP signingKey fingerprint
  `89A409476828CB992338C378651E51AF520BCB78`를 `charts/artifacthub-repo.yml`에 등록.
- **Layer 2 — GitOps 배포 overlay** (operator 3종만, operator-commons 제외):
  kustomize(`deploy/overlays/prod/`), namespace=`data`, base namespace delete patch 적용.
  operator-commons는 `type: library`로 배포 대상이 아니므로 Layer 2에서 제외.

**ArtifactHub 검증 파이프라인**:
- `.github/workflows/artifacthub-verify.yml`: `ah lint`(메타데이터 린트) + smoke 테스트
  (gh-pages 인덱싱 확인 + ArtifactHub REST 등록 확인 + `.tgz.prov` 도달성 검증).

**서명 구분**:
- `charts/artifacthub-repo.yml` PGP signingKey → ArtifactHub `Signed` badge.
- cosign(`release.yml`) → GitHub Release `Verified` 레이블.
- 두 서명은 **완전히 별개**다 — 혼동 금지.

**postgres-operator 특이사항**:
- Phase 6 이전 PR 게이트 0종 → CI 8종 보강(`golangci-lint`, `govulncheck`,
  `envtest`, `helm-lint`, `trivy`, `scorecard`, `license-check`,
  `artifacthub-verify`) 추가.
- `charts/artifacthub-repo.yml`에 signingKey 신규 등록.

**전파 방식**: Approach A(self-contained) — valkey reference를 각 repo에 복사+적응.
org-level reusable workflow(`uses:`) 방식은 배제. 이유: OSS fork 가능성 +
`keiailab/.github` org repo 2026-05-27 제거됨.

**GH Actions 사용 정당화**: RFC-0002(GitHub Actions 영구 금지)는 GitLab/인프라
closed-source org billing SPOF(2026-04-28 트리거) 컨텍스트의 결정이다. 본 대상은
**GitHub OSS public repo** + **사용자 명시 지시**("GHActions 통해서 artifacthub.io
파이프라인 검증"). 거버넌스 우선순위(사용자 명시 > Tier-1 글로벌)상 OSS public repo의
GH Actions 사용은 정책 위반이 아니다. ADR-0022(GHA Narrow Exception) 및 ADR-0019
(GHA retention for public OSS)와 정합.

## Consequences

**긍정적**:
- operator 4종 ArtifactHub 메타데이터 + GitOps overlay 표준 통일.
- `ah lint` + smoke 자동 검증으로 ArtifactHub 등록 회귀 방지.
- cosign ↔ ArtifactHub Signed badge 혼동 제거 — 각 badge 역할 명확화.
- postgres PR 게이트 0 → 8종 보강으로 CI 대칭 달성.

**부정적 / 트레이드오프**:
- 4종 repo 각각 `artifacthub-verify.yml` 유지 필요(self-contained overhead).
- `.tgz.prov` 생성은 현재 로컬 `helm-publish.sh --sign`에서만 동작 — CI 자동화는
  GPG private key secret 결정 후 후속 적용.
- ArtifactHub REST smoke는 gh-pages publish → ArtifactHub 인덱싱 지연(수 분)으로
  flaky 가능 → 재시도 로직 필요.

## Alternatives Considered

**org-level reusable workflow(`uses:` 호출)**: 배제. `keiailab/.github` org repo가
2026-05-27 제거됨. OSS repo는 self-contained를 선호(fork 시 의존성 없음). valkey
ADR-0024가 이미 self-contained manual pattern 확립.

**GH Actions 완전 배제(로컬 4계층만)**: ArtifactHub smoke는 gh-pages publish 후
원격 상태를 확인해야 하므로 로컬에서 실행 불가. ADR-0019/0022 narrow exception
패턴 범위 내에서 GH Actions를 유지.

## Refs

- ADR-0019: GitHub Actions 유지 — Public OSS Operator External Trust Gate
- ADR-0022: GHA Narrow Exception (helm-publish + release + scorecard)
- valkey-operator ADR-0024: Helm chart manual pattern + ArtifactHub
- valkey-operator ADR-0044: ArtifactHub Signed + Official trust badges
- RFC-0002: GitHub Actions 영구 금지(GitLab/인프라 closed-source 한정)
