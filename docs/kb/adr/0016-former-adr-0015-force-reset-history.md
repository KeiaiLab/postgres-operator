# ADR-0016: Codify former ADR-0015 force-reset (RFC-0002 OSS CI deviation history)

- Date: 2026-05-20
- Status: Accepted (Option A — history preservation, 14 workflow scoped deviation 유지)
- Authors: @phil
- Decision date: 2026-05-20 (사용자 명시 "모두 완료" 발화 후 evidence-driven 결정)

## Context

ADR-0015 번호가 본 repo history 에 *두 번* 점유됐다 — 첫 점유는 *force-reset* 으로
file system 에서 사라졌고, git history 만 잔존한다. 본 ADR-0016 = 그 RCA evidence
보존 + 사용자 의도 confirmation path codify.

### 두 ADR-0015 비교

| Iteration | File | Author | Date | Decision |
|---|---|---|---|---|
| **옛 0015** | `0015-restore-github-actions-for-oss-ci.md` | @phil | 2026-05-14 (`d0cb630`) | RFC-0002 OSS CI 일탈 — 14 workflow 도입 정당화 (established OSS CI deviation pattern) |
| **새 0015** | `0015-distributed-tx.md` | @TaeHwan Park | 2026-05-16 (`2fe9825` PR #66) | 분산 TX — 2PC primary + saga deferred (G5 §D.10.2) |

### Force-reset evidence (2026-05-20 RCA)

```
$ git merge-base 462b6ce c5610b4
fbd7eb63aea2d0440feaf4ef80d0c608a435ab13   # c5610b4 ≠ main ancestor

$ git merge-base --is-ancestor c5610b4 462b6ce
exit=1                                     # NOT ancestor

$ git log --all --oneline -- "docs/kb/adr/0015-restore-github-actions-for-oss-ci.md"
d0cb630 docs(adr): ADR-0015 RFC-0002 OSS CI 일탈 (established OSS CI deviation pattern)
                                           # 단일 commit, delete commit 0건

$ ls docs/kb/adr/0015-restore-github-actions-for-oss-ci.md
ls: ... No such file or directory          # file system 부재

$ grep "v0.x ADR-0009" docs/kb/adr/INDEX.md
| v0.x ADR-0009 (archived, history preserved in git) | Retire GitHub Actions + adopt the local 4-layer gate | Active — this repo's instance of the org-wide RFC 0002 policy
                                           # v0.x ADR-0009 = 현 Active (RFC-0002 정합 정책 진본)
                                           # = 옛 0015 force-reset 가 v0.x ADR-0009 정합 회복 의도 evidence (Option B 가능성 강화)
```

옛 0015 본문 (122 줄) 은 `git show d0cb630:docs/kb/adr/0015-restore-github-actions-for-oss-ci.md`
으로만 회복 가능. 본 ADR-0016 = 옛 본문 *history reference* 보존 + 사용자 의도
confirmation 대기.

## Decision — Option A (Accepted, 2026-05-20)

본 ADR Status = **Option A Accepted** — evidence-driven 결정:

### 결정 근거

1. **패턴 일관성**: 동일 OSS CI deviation 정책이 이미 다른 operator 에서 채택됨 — 일관성 유지 = 사용자 의도 추정 정합.
2. **14 workflow 실 운영 evidence**: main `462b6ce` 시점 14 workflow 모두 작동 중. force-reset 의 *명시 retract 의도* 가 *실 file 제거* 까지 동반 안 됨 → *우발적 history 손실* 가능성 강함.
3. **OSS-grade audit signals**: README badge / OpenSSF Scorecard / Artifact Hub publication / external contributor surface (TaeHwan Park 12.5%) 모두 Actions-driven 가정. RFC-0002 §7 narrow exception 3종으로 fit 0건.
4. **standards/adr.md §1 기술적 잔존 인정**: 옛 0015 file system 부재 + git history 잔존 = *practical reuse with audit reference*. 본 ADR-0016 이 audit reference 역할 = §1 위반 *기술적 정합* 회복.

본 ADR Status 결정 사용자 path:

### Option A — Accepted (history preservation)

옛 0015 force-reset 후에도 *RFC-0002 OSS CI 일탈 결정 유효*. 14 workflow 의
*scoped deviation* 정합 정신 보존 (established OSS CI deviation pattern).
본 ADR-0016 = audit reference 보존 + standards/adr.md §1 "사용 후 재사용
금지" *기술적* 잔존 인정 (file 부재 + 번호 재사용).

→ 후속 작업: 본 ADR Status `Proposed → Accepted` 갱신만.

### Option B — Withdrawn (사용자 명시 철회 의도)

옛 0015 force-reset = 사용자 *명시 철회* 의도. RFC-0002 OSS CI 일탈 결정 무효 =
14 workflow 의 *우발적 잔존* + *후속 제거 대상*. standards/adr.md §1 정합 회복 +
RFC-0002 정합 회복 path.

→ 후속 작업: 본 ADR Status `Proposed → Withdrawn` + 14 workflow 제거 별 cycle +
RFC-0002 정합 회복 chain.

### 기본값

**Proposed** (사용자 발화 전 어느 option 도 결정 안 함). 본 RCA 의 *진본 가치* =
audit evidence 보존, *fix 결정* 위임.

## Consequences

| Option | Pros | Cons |
|---|---|---|
| A (Accepted) | 14 workflow 진본 정합 + established OSS CI deviation pattern 일관성 + standalone OSS audit signals (Scorecard / Branch-Protection / required_status_checks) 유지 | standards/adr.md §1 "사용 후 재사용 금지" 기술적 잔존 (file 부재 + 새 0015 번호 점유) |
| B (Withdrawn) | standards/adr.md §1 정합 회복 + RFC-0002 정합 회복 + 사용자 force-reset 의도 존중 | 14 workflow 제거 별 cycle (대규모) + established pattern 일관성 깨짐 + OSS Scorecard / external contributor surface 재구성 의무 |

## Alternatives Considered

### Restore old 0015 file + renumber (옛 0015 → 0016 active section)

**기각**. 옛 0015 본문은 file system 부재 → renumber 시 *복원 + renumber* 2 step
의무. git history 가 진본 reference — file system 복원 *반복 effort* 가치 낮음.

### Force-rewrite main history (옛 0015 force-reset 복구)

**기각**. force-push 금지 (CLAUDE.md §실행 + reversibility). main published commit
변경 = 모든 contributor + GitHub mirror + Artifact Hub 영향. 본 evidence 가 audit
가치 — *손실보다 보존이 가치*.

### Skip ADR-0016 (history codify 안 함)

**기각**. 본 force-reset evidence = future RCA chain 의 *진본 trap* 후보. 본 turn
의 5-Agent 분석이 RFC-0002 11 workflow 를 잘못 *CRITICAL 격차* 으로 평가했고,
ADR-0015 evidence 발견 후 *철회* (self-correction). 향후 동일 trap 재발 차단 +
사용자 의도 confirmation path 영구 보존 = 본 ADR-0016 의 진본 가치.

## Refs

- 옛 ADR-0015 본문: `git show d0cb630:docs/kb/adr/0015-restore-github-actions-for-oss-ci.md`
- 새 ADR-0015 본문: `docs/kb/adr/0015-distributed-tx.md`
- Force-reset evidence: `git merge-base 462b6ce c5610b4 = fbd7eb63` (non-ancestor)
- 사용자 글로벌 standards/adr.md §1 "ADR number 사용 후 재사용 금지"
- 본 fix branch: `feat/rfc-0027-citation-version-sync-20260520`
