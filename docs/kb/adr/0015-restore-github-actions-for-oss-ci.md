---
adr: 0015
title: Restore GitHub Actions workflows for OSS CI (deviation from RFC-0002)
status: Accepted
date: 2026-05-14
deciders: keiailab/maintainers
deviates_from: ai-dev/rfcs/0002-no-github-actions.md
sister_adr: keiailab/valkey-operator ADR-0045 (canonical, 2026-05-12)
---

# ADR-0015: Restore GitHub Actions workflows for OSS CI

## Status

Accepted — 2026-05-14

## Context

`postgres-operator` 는 ghcr.io 와 Artifact Hub 에 publish 되는 공개 OSS
Kubernetes operator 다. 본 repo 가 RFC-0002 (GitHub Actions permanently
banned) 의 *infra repo 전제* 와 다른 4 차이는 valkey-operator ADR-0045
§Context 와 동일하다. 본 ADR 은 그 sister pattern 으로서 postgres-operator
의 14 workflow 도입을 *명시 일탈* 로 봉인한다.

차이점 요약 (sister of ADR-0045 §Context 1~4):

1. **External contributor surface** — fork PR 가 lefthook 미설치 환경에서도
   동일 게이트 통과해야 함.
2. **Trust signals** — OpenSSF Scorecard `Branch-Protection` /
   `CI-Tests` / `Token-Permissions` / `Signed-Releases` 가 Actions 기반.
3. **Already-installed conventions** — README badge, CODEOWNERS routing,
   `.github/PULL_REQUEST_TEMPLATE.md`, Artifact Hub publication 모두
   Actions-driven 가정.
4. **Required status checks** — branch protection 의 `required_status_checks`
   집합이 비면 dependabot sweep 회귀 hole 재발.

RFC-0002 §7 narrow exception 3종 (Pages / Renovate-Dependabot / release tag
1-step) 은 본 14 workflow 중 *0건* 완전 fit (라이브 audit 2026-05-14
mongodb-operator sister 검증과 동일 패턴).

## Decision

14 workflow 를 *scoped deviation* 으로 유지. 주요 workflow:

- `ci.yml` (PR + push) — golangci-lint + go test + manager build
- `helm-install-test.yml` — helm install smoke
- `helm-lint.yml` / `helm-publish.yml` — chart 검증 + gh-pages
- `security-scan.yml` / `codeql.yml` — trivy + CodeQL
- `release.yml` — image + sbom + cosign + chart-tgz + community-operators sync

`main` branch protection 의 `required_status_checks` 최소 집합:
- `golangci-lint`
- `go test` (envtest 포함)
- `helm-lint`
- `helm-install-test`
- `govulncheck` / `trivy-fs`
- `dco`

내부 인프라 repo (`force-*`, `keiailab/platform-*`) 는 RFC-0002 §1 그대로
유지 — 본 일탈은 *공개 OSS operator surface* 에 한정한다.

## Scope of the deviation

valkey-operator ADR-0045 §Scope sister repo 매트릭스 중 본 repo (postgres-
operator) 에 해당한다:

- `keiailab/valkey-operator` (ADR-0045, canonical)
- `keiailab/mongodb-operator` (ADR-0028, 동일 cycle 작성 2026-05-14)
- `keiailab/postgres-operator` (본 ADR)
- `keiailab/operator-commons` (no workflow yet, ADR 불필요)

## Consequences

### Positive

- 외부 fork PR 즉시 게이트 통과 신호.
- `required_status_checks` 강제 가능 → dependabot sweep 회귀 hole 차단.
- OpenSSF Scorecard score `CI-Tests` / `Branch-Protection` /
  `Signed-Releases` 회복.
- SLSA-3 provenance + cosign keyless (`release.yml` 의 `sbom` + `image` job)
  자연 통합.
- 글로벌 governance-report 의 `gha_workflow_count` 메트릭 = 14, *명시
  audit 대상*.

### Negative

- RFC-0002 가 차단했던 *GitHub Actions billing SPOF* 재진입. 완화:
  - 모든 workflow 는 *반드시* `make` target 으로도 실행 가능해야 함
    (lefthook + Makefile parity — RFC-0002 §2 evidence pattern 동일).
  - Actions 장애 시 PR 본문에 `make verify` 출력 인용 + maintainer 승인
    으로 머지 가능 (valkey ADR-0045 §Negative mitigation 정합).

### Trade-offs explicitly considered

| Alternative | Rejected because |
|---|---|
| Keep `.github/workflows/` removed, lefthook only | fork PR 게이트 신호 0 |
| Mirror to GitLab CI | OSS contribution surface 분기, 유지비 2x |
| Only restore `release.yml` (§7 ③ argue) | required_status_checks 강제 불가 |
| Self-host runners | PR volume 대비 비용 정당화 안 됨 |

## Compliance

- 14 workflow 중 *cross-merge-queue dependency 없음*.
- `make test` + `make lint` + `make audit` + `make validate` 로컬 동등.
- governance-report 갱신 시 `gha_workflow_count: 14` + `gha_adr_link: 0015`
  pair 로 *exception 명시* 추적.

## Follow-ups

- 본 ADR 머지 후 `governance-report` 에 `gha_workflow_count` 컬럼 추가.
- ai-dev/rfcs/0002 본문 *clarification footnote* — "public OSS operator
  repositories" scope 외 명시.
- postgres-operator alpha (v0.3.0-alpha.18) → beta tagging 시 본 ADR
  reference 갱신.

## References

- valkey-operator ADR-0045 (canonical sister, 2026-05-12)
- mongodb-operator ADR-0028 (sister, 2026-05-14)
- ai-dev/rfcs/0002-no-github-actions.md §7 (narrow exceptions)
- ai-dev/standards/adr.md §2 (글로벌 standards 일탈 시 ADR 필수)
