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
4. land-direct 순서 = fetch → rebase → squash → check → 트레일러 amend. squash 는 `origin/stable..HEAD` 의 **첫 커밋** 메시지만
   남긴다 — `I26-ADR` 트레일러는 첫 커밋에 둔다(또는 `LAND_KEEP_COMMITS=1` 로 I26 커밋마다 트레일러). amend 는
   `Landed-By`·`Gate-Tree`·`Gate-Log` 만 바꾸므로(`ifexists=replace`) `I26-ADR` 은 남는다.
5. kind 훈련장 pg-drill 기점(`pgDrillBase`)은 운영 pg 이미지가 굽는 트리와 같아야 한다 — 이 결정은 그 값을 바꾸지 않는다.
   운영 pg 이미지의 출처 확인은 위험 목록에 둔다.
6. 상류 동기(GitHub main → stable)는 land-direct rebase 가 평탄화해 트레일러 없는 I26 경로 변경을 들여온다(744af8c #298 이
   오늘 `Dockerfile.pg` 를 건드렸다). 동기는 `I26-ADR: 0037` 을 단 squash 커밋 하나로 착지한다 — 이 ADR 이 "상류 동기 = 의도한
   I26 변경" 을 승인한다. 그 커밋이 `Dockerfile.pg`·`cmd/instance` 를 바꾸면 `pgDrillBase` 검토가 위험 목록에 든다.

## Consequences

- I26 변경은 사람 승인이 아니라 기계 판정이다 — ADR 은 심판(design-judge)이 반박 우선으로 본 설계다.
- 가드가 다시 판별력을 갖는다(무변 트리 rc 0 · 트레일러 없는 I26 커밋 rc 1).
- 가드는 HEAD 트리에서 ADR 을 읽는다 — 한 커밋이 Accepted ADR 과 그 트레일러를 함께 넣을 수 있다. 기계가 보장하는 것은
  fail-closed(모르는 ADR·불일치 → rc 1/2)이고, 승인의 실질은 ADR 심판이다.
- `LAND_SKIP_CHECK`·`LAND_CHECK_FILE` 은 등록부 검사를 건너뛴다 — 가드도 함께 빠진다(ship-tools 후속).
- 이미지 test 단계는 `docs/`·`.git` 을 빼고 굽는다 — 이미지는 판정 시험만 돌고 실제 가드는 돌지 않는다.

## Alternatives Considered

- 가드 삭제: I26 의 강제 지점이 없어진다.
- 기점 sha 만 최신으로 갱신: 다음 I26 변경에서 같은 우회가 생긴다. 승인 근거가 남지 않는다.
- 저장소 안 기점 파일: 같은 커밋이 기점을 옮겨 스스로 승인한다.

## Status

Accepted. 슬라이스 G1(가드 코드) · G2(등록부 5번째 줄).
