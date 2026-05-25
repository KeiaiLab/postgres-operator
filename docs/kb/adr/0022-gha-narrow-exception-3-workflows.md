# ADR-0022: GHA Narrow Exception — 3 Workflow 보존 (RFC-0002 §7 적용)

| Meta | Value |
|---|---|
| Status | Accepted |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | (none) |
| Related | postgres-ADR/0019 (GHA 유지 v2.0), postgres-ADR/0021 (gha-block hook), RFC-0002 §7 (narrow exception) |

## Context

postgres-operator/0019 의 *원래 결정* = "14 workflow 모두 유지" (v2.0 정합, 2026-05-21 사용자 결정).

그 후 다른 자동화 (PR #93) 가 RFC-0002 §7 narrow exception 노선 적용 — 14 → 3 workflow:
- `helm-publish.yml` (RFC-0002 §7 예외 ① — GitHub Pages 정적 배포)
- `release.yml` (RFC-0002 §7 예외 ③ — release tag → GH Release 본문 자동 생성)
- `scorecard.yml` (보안 메타데이터 — 외부 신뢰 신호)

본 ADR 은 *현재 main 상태* (3 workflow) 와 ADR-0019 (14 workflow) 의 불일치를 해소.

### 변경 history

| Commit | 작업 | workflow 수 |
|---|---|---|
| (baseline 2026-05-21) | 14 (ADR-0017 GHA 유지 noted) | 14 |
| PR #86 (S7 cycle) | RFC-0002 strict 적용 — 14 제거 | 0 |
| PR #89 (S7 cycle) | postgres-ADR/0018 Accepted (strict) | 0 |
| **PR #90 (S7 revert)** | 사용자 v2.0 결정 적용 — 14 복원 | 14 |
| **PR #92 (S7 revert)** | postgres-ADR/0019 Accepted (GHA 유지) | 14 |
| **PR #93 (§7 narrow auto)** | 11 workflow 제거, 3 보존 (helm-publish + release + scorecard) | **3** |
| (현재) | — | 3 |

## Decision

postgres-ADR/0019 의 "14 workflow" 결정을 *현 상태* (3 workflow) 로 *amendment*:

- `helm-publish.yml` 유지 (RFC-0002 §7 예외 ① — gh-pages 정적 배포)
- `release.yml` 유지 (RFC-0002 §7 예외 ③ — release tag → GH Release body 자동)
- `scorecard.yml` 유지 (OpenSSF Scorecard 외부 신뢰 신호 — postgres-ADR/0017 의 핵심 사유 일부)
- 나머지 11 workflow (ci, codeql, dco, dependency-review, go-licenses, helm-install-test, helm-lint, kube-linter, markdown-link-check, security-scan, stale) → 로컬 4계층 (lefthook + Makefile) 에 *전부 이관 완료* (S7 cycle PR #88 의 lefthook 보강)
- postgres-ADR/0021 (gha-block hook) 의 *신규 파일 추가 차단* 동작은 유지 (modify 는 허용)

본 ADR 은 postgres-ADR/0019 의 amendment — *Supersede* 가 아닌 *clarification* (실 상태 ↔ ADR 정합 회복).

## Consequences

- ✅ postgres-ADR/0019 ↔ 실 main 상태 정합 (14 → 3 명시)
- ✅ RFC-0002 §7 narrow exception 패턴 docs/kb/adr/ 에 영구 기록
- ✅ helm-publish + release + scorecard 3 workflow 의 *유지 사유* 각각 ADR 본문에 명시 (감사 추적성)
- ⚠️ 다른 operator 와 GHA workflow 수가 *비대칭* 할 수 있음 — per-operator trade-off 차이 인정. 다음 cycle 의 RFC 로 통합 검토 가능.
- ⚠️ scorecard.yml 은 *예외 ①/③ 가 아닌* "보안 메타데이터" — RFC-0002 §7 의 *공식 예외 목록* 에 없음. 본 ADR 이 *4번째 예외* (보안 외부 신뢰) 신설.

## Verification

```bash
# 1. 현 workflow 3
test $(ls .github/workflows/*.yml 2>/dev/null | wc -l) -eq 3

# 2. 3 specific workflows
for f in helm-publish.yml release.yml scorecard.yml; do
  test -f .github/workflows/$f || exit 1
done

# 3. ADR-0019 + ADR-0022 모두 Accepted
grep -q "Status.*Accepted" docs/kb/adr/0019-gha-retention-for-public-oss.md
grep -q "Status.*Accepted" docs/kb/adr/0022-gha-narrow-exception-3-workflows.md

# 4. gha-block hook 활성 (postgres-ADR/0021)
grep -q "gha-block" .lefthook.yml
```

## Migration

본 ADR 채택 후 다음 단계:
- postgres-ADR/0019 본문의 "14 workflow" 언급에 `(주: 2026-05-21 PR #93 으로 11 제거 → 3 잔존, postgres-ADR/0022 amendment)` annotation 추가 (선택)
- audit 의 postgres P0-6 ✅ 유지 (gha-retention ADR 첨부 인정)
- 후속 RFC 검토: RFC-0002 §7 의 *공식 예외 목록* 에 보안 메타데이터 (scorecard) 추가
