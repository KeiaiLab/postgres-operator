# ADR-0018: GHA 전면 제거 → 로컬 4계층 단일 운영 (RFC-0002 strict)

| Meta | Value |
|---|---|
| Status | Superseded by ADR-0019 |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | ADR-0017 (GHA Retention for Public OSS) |
| Superseded by | ADR-0019 (GitHub Actions 유지 — v2.0 통합 정합) |
| Related | RFC-0002 (GHA 영구 금지), ADR-0007 (pre-commit instead of lefthook → lefthook), ADR-0014 (community-operators sync automation), ADR-0016 (former ADR-0015 force-reset history) |

> **2026-05-21 supersede**: v2.0 통일 결정 (사용자 maintainer) 으로 본 ADR 의 *strict 제거* 노선은 폐기되고, ADR-0019 (GHA 유지 + dual-track 운영 + 로컬 4계층 보강 유지) 로 대체되었다. 본 문서는 history 보존 용도로만 유지된다. phase 2/3 의 인프라 (scripts/helm-publish.sh, scripts/release.sh, lefthook 3종 보강) 는 ADR-0019 에서도 dual-track fallback 으로 *유지*된다.

## Context

ADR-0017 (Proposed, 2026-05-21) 은 14개 `.github/workflows/` 의 *유지* 결정을 시도했다. 그 근거는:

1. External contributor 의 trusted automated gate
2. Scorecard / CodeQL 등 external trust signal
3. Helm chart auto-publish + release supply-chain (cosign + SLSA)
4. dependabot/renovate 의 GHA `package-ecosystem` 의존

그러나 사용자 (maintainer) 는 2026-05-21 S7 cycle 에서 다음 결정을 명시:

- 본 repo 는 **RFC-0002 strict** 적용 — GHA 전면 제거 + 로컬 4계층 단일 운영.
- SPOF risk 최소화 우선 — 2026-04-28 incident (org billing → 전 repo 24h+ merge freeze) 의 재발 회피가 external trust signal 손실보다 우선.

이로써 ADR-0017 의 *유지* 노선은 폐기되고, 본 ADR 이 strict 적용을 SSOT 화한다.

## Decision

`.github/workflows/` 전면 제거 (14 파일) + 모든 게이트를 **로컬 4계층** 으로 일원화:

| Layer | 메커니즘 | 본 repo 의 구체 도구 |
|---|---|---|
| L1 pre-commit hook | `.lefthook.yml pre-commit` | gofmt, govet, golangci-lint (`--new-from-rev`), helm-lint (chart 변경 시), adr-phantom-check, orphan-plan-files-block |
| L2 pre-push hook | `.lefthook.yml pre-push` | unit-test, full-lint, helm-lint, helm-template, govulncheck, gitleaks, platforms-amd64-guard, version-sync, go-mod-tidy, **kube-linter** (NEW), **go-licenses** (NEW), **markdown-link-check** (NEW) |
| L3 Makefile | `Makefile` target | `make lint test build audit validate gate release helm-publish kube-lint go-licenses md-link-check` |
| L4 리뷰어 증거 확인 | PR description | 로컬 `lefthook run pre-push` + `make gate` 실행 로그 첨부 |

### 제거된 14 workflow + 대체 위치

| 제거 workflow | 대체 |
|---|---|
| `ci.yml` (lint+test+build) | L1+L2 (golangci-lint + unit-test) + L3 (`make lint test build`) |
| `codeql.yml` | L2 (gosec via `make audit` HIGH severity) + L3 (`make audit`) — CodeQL deep-dataflow 손실 인정 (RFC-0002 trade-off) |
| `dco.yml` | L1 commit-msg hook (`dco-signoff`, `DCO_STRICT=1` 기본) |
| `dependency-review.yml` | L2 (`go-mod-tidy` drift 차단) + L3 (`make audit` govulncheck + trivy) |
| `go-licenses.yml` | L2 (`go-licenses` hook, NEW) + L3 (`make go-licenses`, NEW) |
| `helm-install-test.yml` | L2 (`helm-template` render 검증) — 실 install test 는 사용자 release 시 수동 |
| `helm-lint.yml` | L1 (`helm-lint` chart 변경 시) + L2 (`helm-lint`) |
| `helm-publish.yml` | `scripts/helm-publish.sh` (수동) + `Makefile helm-publish` target |
| `kube-linter.yml` | L2 (`kube-linter` hook, NEW) + L3 (`make kube-lint`) |
| `markdown-link-check.yml` | L2 (`markdown-link-check` hook, NEW) + L3 (`make md-link-check`) |
| `release.yml` | `scripts/release.sh` (수동) + `Makefile release` target |
| `scorecard.yml` | 외부 trust signal 손실 인정 — Artifact Hub Scorecard 배지 사라짐. RFC-0002 trade-off. |
| `security-scan.yml` (trivy) | L3 (`make audit` trivy fs --severity HIGH,CRITICAL) |
| `stale.yml` | 손실 인정 — issue/PR lifecycle 은 maintainer 수동 정리 |

### Branch protection

`main` branch protection 의 `required_status_checks` = 0 (이미 정합 — workflow 제거 시점에 별도 갱신 불필요).

### 운영 변경 사항

- **dependabot/renovate**: GHA 의존 *없음* — config 만 유지하면 PR 생성은 정상 동작 (RFC-0002 §7 Exception ②).
- **release 절차**: maintainer 가 `bash scripts/release.sh vX.Y.Z` 또는 `make release VERSION=vX.Y.Z` 수동 실행.
- **helm chart 배포**: `scripts/helm-publish.sh` 수동 실행 → `gh-pages` push.

## Consequences

**Positive**:

- 2026-04-28 GHA billing incident 같은 외부 SaaS SPOF 재발 risk 0.
- 로컬 4계층 보강 (Phase 3 — kube-linter + go-licenses + markdown-link-check 3종 추가) 완료.
- maintainer 가 GHA 장애 시에도 정상 머지 가능 (모든 게이트가 로컬에서 재실행 가능).
- Internal-infra repo 와 정합 (이미 RFC-0002 strict 적용).

**Negative**:

- External contributor 의 PR 검증 부담 증가 — 로컬 lefthook 설치 + `make gate` 실행 필요.
- 외부 trust signal 손실:
  - CodeQL Security 탭 비어짐 (deep dataflow 분석 손실)
  - Artifact Hub Scorecard 배지 사라짐 (OpenSSF Scorecard signal 손실)
  - DCO 자동 검증 → commit-msg hook (sign-off 강제) 로 대체. `DCO_STRICT=0` 우회 가능하나 기본 strict.
- release supply-chain (cosign + SLSA) 는 maintainer 가 로컬에서 cosign keyfile 로 실행 (RFC-0002 OIDC keyless 불가).
- ADR-0017 (Proposed) 의 분류 (External Trust Gate / Auto Deploy / Local 4-Tier Backup / Ops Tools) 자체는 유효 — 그러나 본 ADR 은 *전부 제거* 노선이므로 분류는 ADR-0017 폐기와 함께 무효화.

**Neutral**:

- Other operators may follow different GHA policies — per-operator trade-off differences accepted. 향후 SPOF risk 평가 결과에 따라 노선 재정렬 가능.

## Alternatives Considered

1. **ADR-0017 (GHA 14개 유지 + dual operation)** — Proposed → Superseded by this ADR.
   maintainer 결정으로 폐기. external trust signal 손실보다 SPOF risk 회피 우선.
2. **Partial removal (External Trust Gate 만 유지)** — Rejected.
   `required_status_checks` 가 0 인 상태에서 부분 유지의 실질 이득 부족. dual maintenance 비용만 발생.
3. **전부 유지 (GHA retention)** — Rejected.
   postgres-operator 는 internal-use 비중이 더 크고 external contributor PR cadence 가 낮음.
   SPOF risk 회피 우선 결정.

## References

- **RFC-0002** (2026-04-29) — Global GHA 영구 금지.
- **ADR-0017** (Superseded) — Public OSS retention 노선 (폐기).
- **ADR-0007** — pre-commit → lefthook 전환 (L1/L2 인프라).
- **ADR-0014** — community-operators sync automation (현재 수동 수행).
- **ADR-0016** — former ADR-0015 force-reset history.
- **Incident KB**: I-2026-04-28 (GHA billing outage; RFC-0002 trigger).

## Implementation

- **Phase 1 (PR #86, 머지)**: `.github/workflows/` 14 파일 `git rm -r`.
- **Phase 2 (PR #87, 머지)**: `scripts/helm-publish.sh` + `scripts/release.sh` 신규.
- **Phase 3 (PR #88, 머지)**: lefthook pre-push 3종 보강 + Makefile `kube-lint`/`go-licenses`/`md-link-check` target 추가.
- **Phase 4 (본 ADR PR)**: ADR-0018 Accepted, ADR-0017 Superseded.

Status: Proposed → Accepted (본 ADR PR 머지 시점).

## 변경 이력

- **2026-05-21 Accepted**: S7 cycle 4 phase 완료. ADR-0017 supersede. RFC-0002 strict
  적용 완료 — 로컬 4계층 단일 운영 + 14 workflow 제거 + 3종 보강 (kube-linter,
  go-licenses, markdown-link-check) 완료.
