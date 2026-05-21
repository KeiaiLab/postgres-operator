# ADR-0023: v3.x-stable baseline 인정 (audit ❌ 0 충족)

| Meta | Value |
|---|---|
| Status | Accepted |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | (none) |
| Related | commons-ADR/0013 (audit SSOT), CLAUDE.md §7 (v3.x-stable 정의), postgres-ADR/0022 (GHA narrow exception), postgres-ADR/0021 (RFC-0002 GHA block hook) |

## Context

CLAUDE.md §7: "본 규약은 **상용 제품 수준**의 다중 프로젝트 일관성을 목표로 한다 — `standards/enforcement.md`의 P0+P1+P2 자동화 모두 충족 시 *v3.x-stable* 선언."

본 repo (postgres-operator) 는 다음 두 축으로 *v3.x-stable* 진입 조건을 충족했다.

### 1. audit ❌ 0 측정 — 2026-05-21 15:30

`commons/scripts/audit-production-grade.sh` (commons-ADR/0013 SSOT) 가 5 repo (postgres / mongodb / valkey / commons / forgewise) 의 P0 (기본 안전) + P1 (품질 게이트) + P2 (거버넌스) + OP (운영) + C (커뮤니티) 50+ 항목을 자동 측정. 본 repo 의 결과:

- P0 (기본 안전): ✅ pre-commit / pre-push / secrets / 한국어 검사 모두 통과
- P1 (품질 게이트): ✅ lint (`golangci-lint`) / test / typecheck / build / audit / import-graph 통과
- P2 (거버넌스): ✅ ADR coverage (0001~0022) / RFC-0002 GHA block hook 강제 / 표준 모듈 정합
- OP (운영): ✅ release.sh 자동화 / chart .tgz publish / OCI image multi-stage build
- C (커뮤니티): ✅ ADOPTERS.md / CONTRIBUTING / CODE_OF_CONDUCT / SECURITY / GOVERNANCE (i18n 4-lang) 정합

audit 시계열 기록: 내부 audit-history (외부 도구 의존) → "🎉 2026-05-21 15:30 — audit ❌ 0 달성" 상태 기록.

### 2. 거버넌스 baseline

- **RFC-0002 정합** (GitHub Actions 영구 금지) — 본 repo 의 lefthook pre-commit hook 이 `.github/workflows/` 추가 자동 차단 (postgres-ADR/0021). 예외 3종 (Pages 정적 배포 + Dependabot/Renovate + release tag → Release body) 은 postgres-ADR/0022 로 명시.
- **i18n 4-lang** (en/ko/ja/zh) README + AGENTS + GOVERNANCE + CONTRIBUTING + CODE_OF_CONDUCT + SECURITY + ADOPTERS — supercycle 2026-05-21 Wave 4 완료.
- **operator-commons** 의존성 정합: `github.com/keiailab/operator-commons` 적합 버전 import (Sprint 1 의 pkg/pvc + pkg/topology 채택 — commons-ADR/0016).

## Decision

본 repo (`keiailab/postgres-operator`) 를 **v3.x-stable** 로 인정한다.

- *외부 사용자 대상 운영 등급* 으로 공개 가능.
- 후속 release tag `vX.Y.Z` 권장 — 구체 버전 (v0.3.0 alpha vs v1.0.0 GA) 은 별 사용자 결정 + 별 ADR 로 추적 (CHANGELOG 정합 + semver 판단).
- 본 ADR 자체는 *baseline 인정* 만 — 실 tag 행위는 사용자가 별도 명시.

## Consequences

### Positive

- **외부 신뢰** — audit 자동 측정 (commons-ADR/0013) + 본 baseline ADR + 거버넌스 4종 (CONTRIBUTING / CODE_OF_CONDUCT / SECURITY / GOVERNANCE) + i18n 4-lang 의 4 축이 *상용 등급* 신뢰 신호로 작용.
- **거버넌스 정합** — RFC-0002 위반 / standards/* 일탈은 ADR 부재 시 §5 실패로 자동 차단. 회귀 시 본 baseline 무효화.
- **커뮤니티 진입** — ADOPTERS 갱신 / Discussion 활성화 / external contributor 의 PR 수용 기준 명시.

### Negative / 회귀 차단 조건

- **audit ❌ ≥ 1 회귀 시** — v3.x-stable 인정 *유지 불가*. 본 ADR 갱신 + commons audit-history 시계열 기록 필수.
- **standards/* 일탈 시** — ADR 부재면 §5 실패. 일탈 자체는 ADR 동반 시 허용 (§7 우선순위 사용자 명시 > Tier-3 > Tier-2 > Tier-1).
- **i18n drift** — 4-lang README / 거버넌스 문서 중 하나라도 외부적 의미 변경 시 sync 필요 (readme-i18n-sync hook 통과 기준).

### Trade-offs

- *v3.x-stable 본 선언* (본 ADR) vs *RFC-0005 글로벌 선언 대기* — 본 repo 는 baseline 만 인정하고 글로벌 RFC-0005 는 별 사용자 결정 영역으로 분리. 글로벌 선언 부재 시에도 본 repo 의 audit ❌ 0 자체가 *측정 가능한 운영 등급 신호*.
- *현 alpha 단계 인정* (v0.x.y) vs *GA 격상* (v1.0.0) — 본 ADR 은 격상 강제 안 함. CHANGELOG 정합 + 사용자 결정.

## 후속 (v3.1+)

본 baseline 후 v3.1+ 진화 후보:
- P3 성능 게이트 (benchmark + budget) — 별 RFC
- P4 DR 게이트 (backup + restore + chaos) — 별 RFC
- P5 커뮤니티 KPI (이슈 응답 SLA + adopter 성장) — 별 RFC
- audit 자동 측정 cron (월 1회) + audit-history 자동 갱신 — commons 측 별 ADR

## 참조

- 외부 audit 도구: `audit-production-grade.sh` (외부 측정 SSOT)
- 글로벌 거버넌스 규약 v3.x-stable 정의 (외부 standards, private)
- [postgres-ADR/0021](0021-rfc-0002-gha-block-hook.md): RFC-0002 GHA block hook
- [postgres-ADR/0022](0022-gha-narrow-exception-3-workflows.md): GHA narrow exception 3 workflows
