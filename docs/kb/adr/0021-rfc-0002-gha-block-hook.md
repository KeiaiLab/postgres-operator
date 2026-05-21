# ADR-0021: RFC-0002 GitHub Actions Block — lefthook pre-commit hook 자동 강제

| Meta | Value |
|---|---|
| Status | Accepted |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | (none) |
| Related | RFC-0002 (GitHub Actions Permanent Ban), ADR-0019 (GHA 유지 + operator family v2.0 dual-track), commons ADR-0012 (gha-block hook 패턴 SSOT) |

## Context

`~/.codex/CLAUDE.md` §2 RFC-0002 (2026-04-29): "GitHub Actions 영구 금지 — `.github/workflows/` 디렉토리 추가 / 보존 **금지**".

5 repo audit (2026-05-21) 측정 결과 P2-2 "GHA block hook" 가 모든 operator repo 에서 ❌:

| repo | P2-2 |
|---|---|
| postgres-operator | ❌ (본 ADR) |
| mongodb-operator | ❌ (sister ADR-0035) |
| valkey-operator | ❌ |
| operator-commons | ✅ (ADR-0012 SSOT) |
| forgewise | ✅ |

즉 *정책* (RFC-0002) 은 있지만 *자동 강제* (lefthook hook) 이 부재. 사람이 의도하지 않은 `.github/workflows/` 신규 추가 시 차단 불가.

본 ADR 은 operator-commons ADR-0012 패턴을 postgres-operator 에 sync 한다.

### v2.0 정합 고려 (ADR-0019 dual-track)

ADR-0019 (Accepted, 2026-05-21) 결정: postgres-operator 는 v2.0 = GHA *유지* (`.github/workflows/` 14 파일) + 로컬 4계층 보강 + dual-track 운영.

따라서 본 hook 은:
- 신규 파일 추가 (`--diff-filter=A`) 만 차단
- 기존 파일 변경 (dependabot 의 actions 버전 bump 등) 은 허용
- 우회: `PLAN_BYPASS=1 git commit` (PR 본문 사유 + ADR 인용 의무)

이는 commons ADR-0012 의 정합 그대로다 — operator-commons 가 GHA 0 인 것과 무관하게, hook 자체는 *신규 추가 차단* 만 수행하므로 GHA 유지 노선 (ADR-0019) 과 모순 없음.

## Decision

`.lefthook.yml` 의 `pre-commit.commands.gha-block` 신설 (commons ADR-0012 패턴):

```yaml
gha-block:
  run: |
    if [ "${PLAN_BYPASS:-0}" = "1" ]; then exit 0; fi
    added=$(git diff --cached --name-only --diff-filter=A | grep "^\.github/workflows/" || true)
    if [ -n "$added" ]; then
      echo "❌ RFC-0002 §2 위반: .github/workflows/ 신규 파일 추가 금지 (modify 는 허용 — ADR-0019 v2.0 dual-track)"
      echo "   파일: $added"
      echo "   대체: 로컬 4계층 (lefthook + Makefile + 리뷰어 증거)"
      echo "   예외 (helm-publish/release/scorecard 등) ADR 작성 후 사용자 명시 승인 필요"
      echo "   우회: PLAN_BYPASS=1 git commit ..."
      exit 1
    fi
```

## Consequences

- ✅ 신규 GHA workflow 의도치 않은 도입 자동 차단 (P2-2 ❌ → ✅)
- ✅ dependabot/renovate 의 기존 14 workflow 갱신 정상 (modified 만 — 차단 안 함, ADR-0019 정합)
- ✅ commons 의 PLAN_BYPASS 패턴 일관 적용 — `lefthook.yml` 안 기존 hook 들의 우회 메커니즘과 동일
- ✅ ADR-0019 의 dual-track 운영 (GHA 유지 + 로컬 4계층) 과 정합 — 본 hook 은 *신규* 차단만, 기존 14 workflow 와 무관
- ⚠️ 향후 신규 workflow 추가가 필요한 경우 (예: 신규 trust gate 도입) ADR 신설 + `PLAN_BYPASS=1` 우회 + PR 본문 사유 명시 의무

## Alternatives Considered

### Alt 1 — server-side rule (branch protection)

GitHub branch protection 의 push rule 에서 `.github/workflows/**` 차단.

기각:
- branch protection 의 file path rule 은 GHA Enterprise 등급에서만 사용 가능
- local-first 정책 (RFC-0002 → 로컬 4계층) 과 모순
- 우회 시 audit trail 부재

### Alt 2 — pre-push hook 으로 위치 이동

본 hook 을 pre-commit 대신 pre-push 로 배치.

기각:
- pre-commit 이 *조기 차단* — 잘못 추가된 파일이 local history 에 남지 않음
- commons ADR-0012 패턴이 pre-commit — sister consistency

### Alt 3 — modify 도 차단 (strict mode)

`--diff-filter=AM` 으로 추가 + 수정 모두 차단.

기각:
- ADR-0019 dual-track 정합 위반 — 14 workflow 의 정상 유지/갱신 차단됨
- dependabot/renovate PR 이 머지 불가 상태에 빠짐
- commons ADR-0012 도 add-only — 일관

## Verification

### 정상 차단 동작

```bash
mkdir -p .github/workflows
echo "name: test" > .github/workflows/test-gha-block.yml
git add .github/workflows/test-gha-block.yml
lefthook run pre-commit --command gha-block
# 출력: "❌ RFC-0002 §2 위반..." + exit status 1

git restore --staged .github/workflows/test-gha-block.yml
rm .github/workflows/test-gha-block.yml
```

### 기존 파일 modify 허용

```bash
# 기존 workflow 에 주석 추가
echo "# test modify" >> .github/workflows/helm-publish.yml
git add .github/workflows/helm-publish.yml
lefthook run pre-commit --command gha-block
# 출력: ✔️ gha-block (차단 없음)

git restore --staged .github/workflows/helm-publish.yml
git checkout -- .github/workflows/helm-publish.yml
```

### 우회 동작

```bash
PLAN_BYPASS=1 git commit -m "test bypass"
# gha-block 통과 (PLAN_BYPASS=1 정합)
```

## Status

Accepted — 2026-05-21.

## References

- RFC-0002: `~/.codex/CLAUDE.md` §2 (GitHub Actions 영구 금지)
- ADR-0019: `0019-gha-retention-for-public-oss.md` (GHA 유지 + dual-track)
- commons ADR-0012: `~/Workspace/keiailab/operator-commons/docs/kb/adr/0012-rfc-0002-gha-block-hook.md` (SSOT 패턴)
- audit script: `~/Workspace/keiailab/operator-commons/scripts/audit-production-grade.sh` (P2-2)
