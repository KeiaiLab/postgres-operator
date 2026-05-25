# ADR-0019: GitHub Actions 유지 — v2.0 통합 정합 (postgres-operator)

| Meta | Value |
|---|---|
| Status | Accepted |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | ADR-0018 (GHA 전면 제거 → 로컬 4계층 단일 운영, RFC-0002 strict) |
| Related | ADR-0017 (이전 retention rationale — Superseded by ADR-0018) |

## Context

ADR-0018 (Accepted, 2026-05-21) 은 S7 cycle 에서 `.github/workflows/` 14 파일의 *전면 제거* + 로컬 4계층 단일 운영을 결정했다. 그 근거는 2026-04-28 GHA billing incident (24h+ 전 repo merge freeze) 의 SPOF risk 회피였다.

그러나 2026-05-21 사용자 (maintainer) 는 후속 결정을 명시:

- **v2.0 정책 통일**: GHA 유지 + 로컬 4계층 보강 + dual-track 운영 + 통합 ADR
- 본 결정의 동인:
  1. All operators should follow the same GHA policy to reduce maintenance overhead from internal policy divergence
  2. public OSS operator 의 *external trust gate* (Scorecard 배지, CodeQL Security 탭, Artifact Hub trust badge) 가치를 재평가 — strict 적용 시 손실 비용이 SPOF risk 회피 이득보다 큼
  3. PR #88 (로컬 4계층 보강 — kube-linter + go-licenses + markdown-link-check) 가 이미 머지됨으로써 *dual-defense* 가 자연스러운 구조 — 두 노선이 상호 배타적 아님
  4. PR #87 (scripts/helm-publish.sh + scripts/release.sh) 가 GHA fallback 으로 작동 — GHA 장애 시에도 release 절차 정상 동작 보장

따라서 본 ADR 은 ADR-0018 의 strict 노선을 폐기하고, ADR-0017 의 retention 노선을 *통합 형태로 재수립*한다. 단순 ADR-0017 복원이 아니라, ADR-0018 phase 2/3 의 보강 인프라 (lefthook 3종 + scripts/) 를 *유지하면서* GHA 도 함께 유지하는 dual-track 형태이다.

## Decision

`.github/workflows/` 14 파일을 **유지**하면서 ADR-0018 phase 2/3 의 보강 인프라도 **함께 유지**한다 (dual-track 운영). RFC-0002 strict 정신과의 거리는 ADR-0017 의 분류표 + 본 통합 ADR 으로 정당화한다.

### Workflow Classification (14 files, ADR-0017 표 계승)

| Category | Workflows | Rationale |
|---|---|---|
| **External Trust Gate** | `codeql.yml`, `scorecard.yml`, `dco.yml`, `dependency-review.yml`, `kube-linter.yml`, `security-scan.yml`, `go-licenses.yml` | External-recognized security/compliance signals; downstream consumers verify the Scorecard badge; CodeQL deep dataflow 분석 (local `gosec` 한계 초과); DCO Signed-off-by trail (community-operators upstream 요구); `go-licenses` AGPL/BUSL/CSL/SSPL 재유입 차단 (ADR-0003). |
| **Auto Deploy** | `helm-publish.yml`, `release.yml` | RFC-0002 §7 Exception ① (GitHub Pages) + Exception ③ (release tag → GitHub Release body). cosign + SLSA L2 supply-chain attestation 자동화. community-operators sync job 도 `release.yml` 내 (ADR-0014). |
| **Local 4-Tier Backup** | `ci.yml` (lint+test+build), `helm-lint.yml`, `helm-install-test.yml`, `markdown-link-check.yml` | 동일 검사가 pre-commit / pre-push / Makefile 에서도 강제 (lefthook L1/L2 + Makefile L3 + 리뷰어 L4). GHA primary + local depth-defense. |
| **Ops Tools** | `stale.yml` | Issue / PR lifecycle automation; merge gate 아님. GHA 장애 시 손실 허용. |

### Dual-Track 운영 — ADR-0018 phase 2/3 인프라 유지

ADR-0018 supersede 에도 불구하고 phase 2/3 의 인프라는 **유지**한다 — GHA 장애 시 fallback 으로 작동:

| 구성 요소 | 출처 PR | 본 ADR 에서 역할 |
|---|---|---|
| `scripts/helm-publish.sh` | PR #87 | GHA 장애 시 maintainer 수동 helm chart publish |
| `scripts/release.sh` | PR #87 | GHA 장애 시 maintainer 수동 release 절차 |
| lefthook pre-push `kube-linter` | PR #88 | `kube-linter.yml` workflow 의 로컬 대체 가능 (L2) |
| lefthook pre-push `go-licenses` | PR #88 | `go-licenses.yml` workflow 의 로컬 대체 가능 (L2) |
| lefthook pre-push `markdown-link-check` | PR #88 | `markdown-link-check.yml` workflow 의 로컬 대체 가능 (L2) |
| Makefile target `kube-lint`, `go-licenses`, `md-link-check` | PR #88 | L3 — `make verify` 등에서 호출 가능 |

이는 ADR-0017 (Superseded) 의 단순 GHA retention 보다 *진화한* 구조다 — local 4-tier 가 명시적으로 보강되었으므로 GHA SPOF risk 가 ADR-0017 시점보다 실질적으로 더 낮다.

### Branch protection

`main` branch protection 의 `required_status_checks` = 본 ADR 머지 시점 기준 0 (ADR-0018 시점에 정렬됨 — 신규 workflow 등록 시 운영 결정으로 갱신). 본 ADR 은 branch protection 갱신을 강제하지 않는다 — maintainer 가 trust gate 별 필요에 따라 별도 결정.

### 운영 변경 사항

- **dependabot/renovate**: GHA 의존 *없음* — config 만 유지하면 PR 생성 정상 동작 (RFC-0002 §7 Exception ②).
- **release 절차**: 1순위 GHA `release.yml` (cosign keyless + SLSA L2); 장애 시 `scripts/release.sh vX.Y.Z` 수동 fallback.
- **helm chart 배포**: 1순위 GHA `helm-publish.yml` (`gh-pages` push 자동); 장애 시 `scripts/helm-publish.sh` 수동 fallback.
- **로컬 검증**: maintainer 는 push 전 `lefthook run pre-push` 또는 `make verify` 로 GHA 와 동등한 검증 가능.

## Consequences

**Positive**:

- Operator 정책 통일 — cross-operator maintainer 인지 비용 감소.
- External trust signal (CodeQL, Scorecard, DCO, Artifact Hub badge) 복원 — public OSS contributor PR 검증 부담 감소.
- ADR-0018 의 phase 2/3 보강 (lefthook 3종 + scripts/) 이 *낭비되지 않고* dual-track fallback 으로 재사용 — RFC-0002 strict 노선 폐기로 인한 작업 sunk cost 없음.
- GHA SPOF risk 감소 — local 4-tier 가 ADR-0017 시점보다 실질적으로 더 두꺼움 (PR #88 의 3종 보강 덕분).
- 2026-04-28 같은 GHA 장애 발생 시 `scripts/release.sh` + `lefthook run pre-push` 로 즉시 우회 가능 — ADR-0017 시점에는 부재했던 fallback.

**Negative**:

- RFC-0002 strict 노선 폐기 — global 정책과의 거리 증가. 본 ADR ��� 그 일탈을 정당화하지만, RFC-0002 의 "GitHub Actions 영구 금지" 문언과 정면 충돌��� 남는다 (mitigation: 본 ADR �� dual-track 구조가 RFC-0002 의 SPOF 회피 의도는 달성).
- ADR-0018 의 *전면 제거* 결정을 1일 (2026-05-21) ���에 다�� 뒤집음 — governance 안정성 측면���서 흔들림 신호. mitigation: v2.0 통일 결정으로 향후 안정성 ↑.
- `ci.yml`, `helm-lint.yml`, `kube-linter.yml`, `go-licenses.yml`, `markdown-link-check.yml` 등 5개 workflow 가 lefthook hook 과 *중복* — dual maintenance 비용. Accepted (depth-defense 가치 인정).
- Branch protection `required_status_checks` 가 0 인 상태로 머지 — workflow 가 *runs* 하지만 *blocks* 하지 않음. maintainer 가 별도 결정으로 등록 필요.

**Neutral**:

- Consistent GHA policy across all operators — 정합 회복.
- ADR-0017 (Superseded by ADR-0018) 의 분류 (External Trust Gate / Auto Deploy / Local 4-Tier Backup / Ops Tools) 가 본 ADR 에서 재차 *유효해짐* — 단 본 ADR 은 ADR-0017 의 단순 복원이 아니라 dual-track 진화판.

## Alternatives Considered

1. **ADR-0017 단순 복원** — Rejected. ADR-0017 의 dual-track 구조가 명시되지 않았고, PR #88 의 lefthook 3종 보강이 반영되지 않은 형태. 본 ADR 은 ADR-0017 의 분류표를 *계승*하면서 ADR-0018 의 보강 인프라를 *통합* 형태로 재구성.
2. **ADR-0018 유지 (strict 노선 견지)** — Rejected. v2.0 통일 결정에 위반. 정책 분기 유지 시 maintainer 인지 비용 ↑.
3. **부분 유지 (External Trust Gate + Auto Deploy 만, Local 4-Tier Backup 제거)** — Rejected. `ci.yml` 등이 사라지면 PR 별 자동 lint+test 가 GHA 에서 사라짐. 외부 contributor 가 local 검증을 수행해야 하는 부담은 strict 노선의 단점을 그대로 가짐. dual-track 의 depth-defense 가치 손실.
4. **GHA + branch protection required_status_checks 즉시 등록** — Deferred. 본 ADR 의 범위 외 — branch protection 정책은 별도 ADR 또는 운영 결정으로 처리.

## References

- **RFC-0002** (2026-04-29) — Global GHA 영구 금지 (internal-infra intent).
- **ADR-0017** (Superseded by ADR-0018) — 이전 retention rationale.
- **ADR-0018** (Superseded by 본 ADR) — RFC-0002 strict 적용 / 전면 제거.
- **Incident KB**: I-2026-04-28 (GHA billing outage; RFC-0002 trigger). 본 ADR 의 dual-track 구조로 mitigation.
- **관련 PR (postgres-operator)**:
  - PR #86 (RFC-0002 strict 제거) → PR #90 (revert 복원)
  - PR #87 (scripts/helm-publish.sh + scripts/release.sh) — 본 ADR 에서 dual-track fallback 으로 유지
  - PR #88 (lefthook 3종 보강) — 본 ADR 에서 Local 4-Tier Backup 강화로 유지
  - PR #89 (ADR-0018 Accepted) → 본 ADR 로 supersede
- **관련 repo policy**:
  - ADR-0003 — license policy (`go-licenses.yml` workflow 가 enforcement).
  - ADR-0007 — pre-commit → lefthook 전환 (Local 4-Tier 인프라).
  - ADR-0014 — community-operators sync automation (`release.yml` 내 job).

## Implementation

- **본 ADR PR**: ADR-0019 Accepted, ADR-0018 Superseded by ADR-0019, INDEX.md 갱신.
- **선행 PR #90 (2026-05-21 머지)**: `.github/workflows/` 14 파일 복원 (revert of PR #86).
- **유지 인프라 (변경 없음)**:
  - PR #87 산출물: `scripts/helm-publish.sh`, `scripts/release.sh`
  - PR #88 산출물: lefthook pre-push 3종 (kube-linter, go-licenses, markdown-link-check) + Makefile target

Status: Proposed → Accepted (본 ADR PR 머지 시점).

## 변경 이력

- **2026-05-21 Accepted**: v2.0 통일 결정 (사용�� maintainer). ADR-0018 supersede.
