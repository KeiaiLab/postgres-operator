# ADR-0037: I26 — 인스턴스 매니저·pg 이미지 변경은 ADR 트레일러를 달고 착지한다

- **Date**: 2026-11-07
- **Status**: Accepted
- **Authors**: @claude (설계)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.11 FX-P0 · 불변식 I26 · KEIAILAB.md 게이트 ·
  스킬 `keiailab-postgres-operator-land-verify-image-ship-log-blocks`

## Context

- land 표 5번째 줄 `git diff --exit-code c7608427 -- cmd/instance internal/instance Dockerfile.pg` 은 기점 beta.11 대비 무변을
  강제했다.
- 17369c33(INC-2026-10-10 PGDATA 권한·리스)과 322546d6(rewind 폴백 basebackup)이 그 경로를 바꿨다. 둘 다 사고 근치라
  필요한 변경이었지만 가드를 우회해 착지했다. 지금 이 줄은 **모든** 착지에서 붉다 — 가드가 판별력을 잃었다.
- 가드를 지우면 I26 의 목적(데이터 플레인 변경을 의도한 커밋으로만)이 사라진다. 기점을 손으로 옮기면 같은 일이 되풀이된다.

## Decision

1. 기점 = 착지 기준(`origin/stable`). 가드는 `origin/stable..HEAD` 의 커밋만 본다 — stable 은 귀납적으로 승인된 I26 변경만
   담는다. 17369c33·322546d6 은 이 ADR 이 소급 승인한다.
2. 그 범위에서 `cmd/instance/` · `internal/instance/` · `Dockerfile.pg` 를 건드린 커밋마다 트레일러 `I26-ADR: NNNN` 이 있고,
   HEAD 트리에 `docs/kb/adr/NNNN-*.md` 가 있으며 그 파일의 `**Status**: Accepted` 줄이 있어야 한다. 하나라도 어기면 rc 1 과
   커밋·경로·사유를 낸다. git 을 못 읽거나 `--base` 를 못 풀면 rc 2(fail-closed). 빈 범위는 "검사한 커밋 0" 을 출력하고 rc 0.
3. 구현 = `internal/i26guard`(순수 판정 — 커밋·경로·트레일러·ADR 상태 입력) + `cmd/i26guard`(git 읽기). 이미지 test 단계가
   판정 시험을 돈다. land 표 5번째 줄 = `go run ./cmd/i26guard --base origin/stable`.
4. land-direct 의 트레일러 추가(amend)는 기존 트레일러를 보존한다 — 가드는 착지 전 커밋에서 돈다.
5. kind 훈련장 pg-drill 기점(`pgDrillBase`)은 운영 pg 이미지가 굽는 트리와 같아야 한다 — 이 결정은 그 값을 바꾸지 않는다.
   운영 pg 이미지의 출처 확인은 위험 목록에 둔다.

## Consequences

- I26 변경은 사람 승인이 아니라 기계 판정이다 — ADR 은 심판(design-judge)이 반박 우선으로 본 설계다.
- 가드가 다시 판별력을 갖는다(무변 트리 rc 0 · 트레일러 없는 I26 커밋 rc 1).

## Alternatives Considered

- 가드 삭제: I26 의 강제 지점이 없어진다.
- 기점 sha 만 최신으로 갱신: 다음 I26 변경에서 같은 우회가 생긴다. 승인 근거가 남지 않는다.
- 저장소 안 기점 파일: 같은 커밋이 기점을 옮겨 스스로 승인한다.

## Status

Accepted. 슬라이스 G1(가드 코드) · G2(등록부 5번째 줄).
