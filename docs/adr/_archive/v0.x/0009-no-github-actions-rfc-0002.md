# ADR 0009 — GitHub Actions 폐기 + 로컬 4 계층 게이트 적용 (RFC 0002 적용)

- **상태**: Accepted
- **날짜**: 2026-04-30
- **결정자**: @keiailab/maintainers
- **관련**: 글로벌 CLAUDE.md §2 (GitHub Actions 영구 금지), 글로벌 RFC 0002 (2026-04-29), `standards/ci.md` v1.0
- **트리거**: 글로벌 §2 사고 (2026-04-28) — organization billing 1건 실패 → 전 저장소 전 PR의 모든 workflow runner 4초 만에 fail → 머지 불가 24시간+. 단일 외부 SaaS 의존이 SPOF.

## 컨텍스트

본 프로젝트는 `.github/workflows/{ci,upstream-watch}.yml` 두 워크플로를 *현재 활성*. 글로벌 §2가 *RFC 0002에 따라 GitHub Actions 영구 금지*를 명문화한 시점(2026-04-29) 이후 본 프로젝트는 *마이그레이션 미적용 잔재*. 본 ADR이 그 적용 결정을 기록.

또한 PR #1 머지 직전 발견된 *e2e fail*은 *기존 main의 cert-manager 통합 미완*이 원인이며, GH Actions 자체가 *PR 머지 차단의 단일 SPOF*로 작용하는 위험을 노출.

## 결정

### 폐기 대상

- `.github/workflows/ci.yml` — 5 jobs (lint, test, matrix-build, e2e, scan) 모두 *로컬 4 계층*으로 마이그레이션 후 삭제.
- `.github/workflows/upstream-watch.yml` — Citus 신 릴리스 감지 cron. *RemoteTrigger 또는 사용자 schedule*로 대체 (본 ADR 후속 작업).

### 4 계층 매핑 (글로벌 `ci.md` §1 표준)

| 기존 ci.yml job | 새 위치 | 명령 |
|---|---|---|
| lint (golangci + .custom-gcl) | **L1 pre-commit** | `make lint-config && make lint` |
| test (Unit + envtest + go mod tidy drift) | **L2 pre-push** | `make test`, `go mod tidy && git diff --exit-code go.mod go.sum` |
| scan (trivy fs HIGH+CRITICAL) | **L2 pre-push** | `make audit` (신규 타겟, trivy fs 호출) |
| matrix-build (PG×Citus 3 조합) | **L3 Makefile (수동)** | release tag 시점 또는 `make build-pg-images` 수동 |
| e2e (kind, PG 16/17, p1) | **L3 Makefile (수동)** | `make test-e2e` (kind 7-9분, pre-push 부적합) |
| upstream-watch (cron) | **RemoteTrigger 또는 사용자 schedule** | 본 PR에서는 폐기만, 대체는 후속 |

### 예외 3종

본 프로젝트는 글로벌 §2의 예외 3종 *어디에도 해당하지 않음*:
- ① GitHub Pages 정적 배포 — 사용 안 함
- ② Dependabot/Renovate 도구 자체 — `.github/dependabot.yml` 부재 (필요 시 별도 추가)
- ③ release tag → GitHub Release 본문 자동 생성 — 현재 release tag 워크플로 부재

### 도구 설치 + 강제

- 개발자 환경: `pre-commit install --hook-type pre-commit --hook-type pre-push` 1회 실행 강제 (README 안내).
- `.pre-commit-config.yaml`이 본 PR과 함께 도입.
- 우회(`--no-verify`)는 *사고 보고 의무* (`incident-kb.md`).

### PR 머지 증거 (글로벌 `ci.md` §2)

PR 본문 또는 첫 commit 메시지에 다음 블록 포함 강제 (PR 리뷰어가 확인):

```
로컬 게이트 PASS:
- pre-commit run --all-files: PASS
- pre-push hooks: PASS  (또는 N/A if no hook)
- make test: PASS  (or specific subset)
- make audit: PASS  (high+ vulnerabilities = 0)
```

부재 시 리뷰어가 머지 차단.

## 근거

### 왜 *지금* 마이그레이션인가

1. **글로벌 §2 명시 위반** — 2026-04-29 이후 *비정합 상태*. 본 프로젝트가 다른 Kei* repo(force-infra-modules, force-tenant-house 등)와 *일관된 표준*을 유지하려면 즉시 적용.
2. **PR #1 e2e fail의 PR-blocking 효과 제거** — GH Actions 폐기 후에는 e2e fail이 *로컬 검증 차원*에 머무르며 *PR 머지 차단 SPOF 해소*. 단 e2e 자체 fix는 별도 PR.
3. **사고 트리거 회피** — organization billing 단일 SPOF가 다시 발생하면 본 프로젝트도 동일 영향. 마이그레이션 지연은 위험 수동 수용.

### 왜 e2e/matrix-build를 L2가 아닌 L3로 두는가

- e2e: kind cluster 부팅 + 7-9분 소요. 매 push 마다 실행은 *개발자 경험 저해*. PR 진입 직전 또는 release 시점에 명시 실행이 합리적.
- matrix-build: docker buildx로 PG image 3 조합 build. release tag 시점에만 필요.

이는 글로벌 `ci.md` §3 도구 카탈로그가 e2e/build를 별도 트랙으로 두는 패턴과 부합.

### 왜 lefthook 아닌 pre-commit인가

글로벌 `enforcement.md` §1.1은 lefthook 권장이지만, 본 프로젝트는 *Go 단일 언어 + Python(pre-commit) 사용자 친숙*이라 pre-commit이 더 자연스러움. 글로벌 §3 우선순위 "Tier-3 프로젝트 > Tier-2 standards"에 따라 본 ADR로 정당화. 향후 lefthook 채택은 별도 ADR.

## 트레이드오프

- **upstream-watch 자동화 손실**: cron 기반 Citus 신 릴리스 감지가 일시 중단. 완화: 후속 작업에서 RemoteTrigger 또는 사용자 schedule 도구로 대체.
- **개발자 환경 의존**: pre-commit + 보안 도구(gitleaks, trivy) 로컬 설치 강제. 완화: README에 `brew install` 또는 동등 명령 명시. macOS/Linux 환경 가정.
- **PR 리뷰어 부담 증가**: 로컬 게이트 PASS 증거 블록을 *수동* 확인. 완화: 글로벌 표준이 동일하므로 *전 Kei* repo 공통 부담* — 학습 곡선이 한 번.
- **e2e가 PR 자동 검증에서 빠짐**: 단 *e2e는 현재 이미 fail 상태* (cert-manager 미완)이므로 *기존 PR 차단을 막는 효과* 발생. 진짜 e2e fix(P7 cert-manager 통합 PR)에서 *L3 명시 실행*으로 검증.

## 결과

- `.github/workflows/{ci,upstream-watch}.yml` 두 파일 *삭제*.
- `.pre-commit-config.yaml` 신규 — L1 + L2 hook 정의.
- `Makefile`에 `audit` 타겟 신규 (`trivy fs` 호출).
- `README.md`에 "로컬 게이트 PASS 증거 블록" + 개발자 환경 설정 안내 추가.
- 본 PR 머지 후 *외부 단계*: GitHub branch protection의 "Required status checks" 제거 또는 로컬 hook 결과 마커로 교체 (admin이 별도 수행).

## 강제 메커니즘

| 메커니즘 | 위치 | 도입 시점 |
|---|---|---|
| `.pre-commit-config.yaml` | repo root | 본 ADR 동시 |
| `Makefile` audit 타겟 | `Makefile` | 본 ADR 동시 |
| 개발자 환경 안내 | `README.md` | 본 ADR 동시 |
| PR 본문 증거 블록 강제 | `commits.md §3` PR 체크리스트 | 글로벌 표준 |
| upstream-watch 대체 | RemoteTrigger / 사용자 schedule | 후속 작업 |

## 후속 작업

1. **upstream-watch 대체** — RemoteTrigger 또는 사용자 schedule 도구로 Citus 신 릴리스 감지 자동화 재구성.
2. **branch protection rule 갱신** — admin이 GitHub UI에서 Required status checks 제거.
3. **lefthook 마이그레이션 검토** (향후) — 글로벌 `enforcement.md` §1.1 권장과 정합 시 별도 ADR.
4. **e2e cert-manager 통합 완성 PR** — config/certmanager/ + Certificate CR + e2e BeforeAll wait. 본 PR과 별도 진행.
