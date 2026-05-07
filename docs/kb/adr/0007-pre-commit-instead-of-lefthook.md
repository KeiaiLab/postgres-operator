# ADR-0007: Hook 도구로 pre-commit 채택 (글로벌 lefthook 표준 분기)

- Date: 2026-05-06
- Status: Accepted
- Authors: @eightynine01

## Context

글로벌 표준 `~/Documents/ai-dev/standards/enforcement.md §1.1` 은 git hook 관리 도구로 **lefthook** (Go 단일 바이너리, 언어 중립) 을 명시한다. 본 repo 는 `.pre-commit-config.yaml` 기반의 **pre-commit** (Python 기반) 을 운영 중이며, 표준과 분기한 상태로 `mongodb-operator` / `postgres-operator` 두 repo 가 동일 패턴을 사용하고 있다 (3-repo 중 valkey-operator 만 lefthook).

본 repo 의 `.pre-commit-config.yaml` 은 RFC 0002 의 4 계층 게이트 (`standards/ci.md §1`) 를 명시 매핑 중이며 (L1 pre-commit → golangci-lint, L2 pre-push → test/audit/secrets/go-mod-tidy-drift), `_archive/v0.x/0009-no-github-actions-rfc-0002.md` 가 이를 docs 화한 상태다.

## Decision

본 repo 는 **pre-commit 을 유지**한다. 마이그레이션 일정 미정 (트리거 조건은 Consequences 절 참조).

## Rationale

1. **RFC 0002 게이트 매핑 적용 완료** — `.pre-commit-config.yaml` 이 L1/L2 stages 를 명시 사용 중 (`stages: [pre-commit]` / `stages: [pre-push]`). lefthook 마이그레이션 시 동등 매핑 재작성 필요.
2. **pre-commit 은 GitHub-recognized 표준** — 광범위한 hook 생태계 (trailing-whitespace 등 built-in) + autofix_prs CI 통합.
3. **기능 동일** — 둘 다 동일한 git hook 메커니즘. lefthook 의 강점 (Go 단일 바이너리, 언어 중립) 은 본 *Go 프로젝트* 에서 결정적 이점이 아님.
4. **마이그레이션 비용 vs 가치 낮음** — 동작 중인 인프라 교체의 정당화 부족.

## Consequences

### 긍정
- 기존 hook 인프라 재사용 — 회귀 위험 0.
- L1/L2 매핑 명시적, RFC 0002 4 계층 게이트 정합 ✓.

### 부정
- 글로벌 `enforcement.md §1.1` 과 분기 — `governance-report` 의 P0 정합 컬럼에서 분기 표시 가능.
- 3-repo 중 valkey 만 lefthook → 신규 기여자가 양쪽 도구 학습 필요.

### 마이그레이션 트리거 (lefthook 으로 전환 시점)

다음 중 하나 발생 시 본 ADR 을 *Superseded* 로 변경하고 lefthook 마이그레이션:

1. valkey-operator 의 lefthook 운영이 6개월 이상 안정적이고 *명백한 우위* 발견.
2. pre-commit 자체에 보안 이슈 / 유지보수 중단 시그널.
3. 글로벌 RFC 가 lefthook 강제 적용으로 갱신.
4. 신규 hook 추가 시 lefthook 만 지원하는 기능이 필요해짐.

## Alternatives Considered

### A. lefthook 으로 즉시 마이그레이션
- pros: 글로벌 표준 100% 정합.
- cons: RFC 0002 매핑 재작성 + 회귀 위험.
- 거절 사유: 가치 < 비용.

### B. (채택) ADR 로 분기 명시 + 마이그레이션 트리거 정의
- pros: 명시적 추적성, 향후 통일 결정의 근거.
- cons: 단기 도구 분기 잔존.

## 글로벌 참조

- 표준: `~/Documents/ai-dev/standards/enforcement.md §1.1`
- 정합 사례: `valkey-operator/.lefthook.yml`
- 본 repo 운영: `.pre-commit-config.yaml`
- 관련 ADR: `_archive/v0.x/0009-no-github-actions-rfc-0002.md` (4 계층 게이트 매핑 history)
