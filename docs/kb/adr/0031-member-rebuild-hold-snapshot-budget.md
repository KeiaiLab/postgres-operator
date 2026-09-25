# ADR-0031: 멤버 재구축을 hold · 스냅숏 · 예산 뒤로

- **Date**: 2026-09-25
- **Status**: Accepted
- **Authors**: @claude (OP4b 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.5·§3.6·§3.9 · ADR-0030(런타임 ConfigMap) · #220

## Context

- v0.4.0-beta.11 의 재시드는 파드와 **PVC 를 지운다**(`reseedStandby`). 승격 경로·끊긴 standby·rogue 가
  쿨다운·예산 없이 불러 2026-09-24 에 Retain PV 386 개가 새고 nvme 가 찼다. 지우는 순간 옛 primary 가
  가졌을 수 있는 앞선 WAL 도 사라진다.
- 되감기(pg_rewind)도 파괴다 — 밀려난 primary 가 다시 뜨면 부트스트랩이 standby 로 강등하고 인스턴스
  매니저가 분기 뒤 WAL 을 버린다.
- 파괴를 막을 자리는 컨테이너가 뜨기 전(부트스트랩)과 파괴를 허가하는 오퍼레이터 코드 둘이다.

## Decision

1. 런타임 ConfigMap 에 두 키를 더한다(늘 씀, 비면 빈 문자열 — 템플릿이 Optional=false 로 참조한다):
   `MEMBER_HOLD`(pod,pod) · `RESEED_REQUESTS`(pod=token,…). init 컨테이너만 받는다. 승격·채택 발행은
   승격 키만 쓰고 이 두 키는 재구축 엔진만 쓴다(낙관적 잠금 — 낡은 캐시가 목록을 덮지 못한다). 옛(3키)
   CM 에는 빈 값으로 채운다.
2. 부트스트랩(§3.6): hold 목록에 자기가 있으면 어떤 파일도 건드리기 전에 exit 1. 재시드 토큰이 PVC 루트
   `.keiailab-reseed-applied` 와 다르면 다른 primary 가 있을 때만 `.keiailab-reseed-pending`=token 을 쓰고
   PGDATA(상수 경로)를 비운다 — 자기가 primary 거나 비었으면 exit 1. basebackup 이 끝나면 pending 을 applied
   로 바꾼다. pending 이 있으면 빈 ordinal-0 도 initdb 하지 않는다(I6).
3. 엔진(`member_rebuild.go`) — 상태 = CR 애노테이션 `postgres.keiailab.io/member-rebuild`(JSON v1
   `{active, ledger}`):
   - 요청(`requestRebuild`, 사유 Demoted·FencedPrimary·Rogue·Stale)은 **즉시 hold**(예산·쿨다운·동시성 무관).
     Stale 은 파드 나이 ≥ staleStandbyReseedTimeout 이고 이미지를 못 받는 중이 아닐 때만 받는다.
   - Holding: 전파 대기(runtimePropagationWait) → 멤버 쿨다운(마지막 끝 + reseedCooldown)·샤드당 1 → 재검증
     → 끝 위치 → 파드 삭제. 다음 단계는 **관측**으로 — 새 파드가 없거나 bootstrap 이 비0 종료했고 postgres
     가 돈 적 없을 때. 돈 적 있으면 다시 지우고 스냅숏은 뜨지 않는다.
   - Snapshotting: `VolumeSnapshot <pod>-rebuild-<token>`(unstructured, 소유 참조 0, class =
     `--member-snapshot-class` / env `MEMBER_SNAPSHOT_CLASS`, 라벨 member-snapshot·cluster·shard-id·
     rebuild-reason·data-bearing). 해제 허가는 그 순간 **직접 읽은** readyToUse 만 믿는다(I3).
   - 재검증(멈추기 전과 해제 직전): 대상이 CM primary 이거나 in recovery 로 CM primary 의 복제 그래프에
     붙어 있으면 RebuildCancelled(hold 해제, 파괴 0).
   - 예산: 1h 창 해제 ≤ replicas(최소 1), 원장이 CR 에 있어 재기동한 오퍼레이터도 같다. 소진이면
     MemberRebuildBlocked(BudgetExhausted). 가용성 예외: 샤드 그래프가 streaming standby 0 을 알고 옛
     primary(Demoted·FencedPrimary)면 창 안 샤드당 1건을 예산 밖으로(outcome availability).
   - Releasing: 원장이 먼저다 — 한 pass 가 CR 에 해제를 적고 다음 걸음이 hold 해제와 재시드 토큰
     (Rogue·Stale)을 **한 CM 쓰기**로 한 뒤 전파 대기 → 파드 재삭제.
   - Verifying: 복제 그래프 연결 ∧ (재시드면 applied == token) → RESEED 제거 · unfence · 원장 끝 ·
     MemberRebuildCompleted.
   - data-bearing = lead > 0 ∨ 모름(lead = 끝 위치 − 샤드 promotionLSN). standby exec 는 정확하다. primary
     exec 와 상태 애노테이션은 하한이라 분기점을 넘을 때만 증거로 쓰고, 아니면 모름(보존)이다. 양성 증거면
     outcome data_bearing + Warning RebuildDiscardsNewerData(가용성 예외보다 먼저 — 경보를 가리지 않는다).
   - 스냅숏 실패: 클래스 미설정·API 없음·status.error·snapshotReadyTimeout 초과 = Blocked. 옛 primary·Rogue
     는 hold 를 둔 채 reseedCooldown 뒤 새 토큰으로 다시 뜬다. Stale 은 파괴 없이 해제·종료(outcome
     abandoned) — 클래스가 비었으면 멈추지도 않는다.
4. GC: 라벨(member-snapshot=true · cluster)과 이름 접두 `<cluster>-` 로 이 클러스터 것만, data-bearing
   라벨이 `false` 인 것만, 진행 중 항목의 스냅숏은 빼고 7일 또는 상한(2 × members) 밖을 지운다.
   data-bearing(라벨이 없는 것 포함)은 상한을 넘어도 자동으로 지우지 않는다.
5. 관측: `member_rebuilds_total{shard,reason,outcome}`(샤드마다 0 으로 초기화) ·
   `member_rebuild_budget_remaining` · `members_held{shard}`(CM 기준) · `member_snapshots{data_bearing}` ·
   condition MemberRebuildBlocked(BudgetExhausted · SnapshotClassUnset · SnapshotUnavailable · SnapshotFailed
   · SnapshotTimeout · NotRejoined / RebuildClear). 엔진 끄기 스위치는 두지 않는다(사람 게이트 금지) —
   비상 정지는 pg-stop 이다.

## Consequences

- 멤버 데이터를 버리는 모든 길이 hold 와 readyToUse 스냅숏 뒤로 간다. 요청자를 이 엔진으로 옮기고 PVC
  삭제를 걷는 것은 다음 단계(OP4c)다 — 이 결정만으로는 요청자가 없어 운영 행동이 그대로다.
- 템플릿이 바뀐다(init env 두 키 · 부트스트랩) — 운영 반영(L1) 한 번에 실린다. 키가 비면 부트스트랩
  결과는 그대로다.
- hold 는 컨테이너를 만들 때만 읽힌다 — 도는 멤버는 엔진이 지워야 멈춘다. 그 사이 전파가 늦어 postgres
  가 뜨면(되감기 포함) 관측이 다시 지우지만 그 사이 버린 WAL 은 되찾지 못한다.
- 샤드당 1 · 쿨다운 동안 도는 held 멤버는 계속 돈다. 예산 소진이면 멤버가 최대 1h hold 된다(가용성
  예외가 streaming 0 을 막는다).
- Verifying 에는 시한이 없다 — 끝내 붙지 않으면 NotRejoined 가 보이고 그 샤드의 다음 재구축이 기다린다.
- 알려진 한계: lead 는 타임라인을 보지 않는다 — 새 타임라인을 따라가다 끊긴 standby 도 분기점을 넘어
  data-bearing 이 된다(보존 쪽 오류: 스냅숏이 남고 critical 경보가 뜬다).
- data-bearing 스냅숏은 쌓인다 — 게이지·경보(O1) · 복구·정리 절차는 후속이다.

## Alternatives Considered

- PVC 삭제 유지 + 쿨다운만: 누수와 앞선 WAL 손실이 그대로다.
- 시간(대기)만으로 hold 효과 판정: 전파 지연·재기동 백오프를 못 본다 — 관측(init 비0 ∧ postgres 돈 적
  없음)을 쓴다.
- 해제를 CM 먼저 쓰고 원장을 나중에: CR 쓰기가 실패하면 예산이 덜 세어 초과할 수 있다.
- 엔진 on/off 플래그: 사람이 켜야 도는 관문이라 두지 않는다.

## Status

Accepted (OP4b). 요청자(aggregate·승격 경로)와 PVC 삭제 제거는 OP4c, RBAC(volumesnapshots)와 입장
정책(data-bearing 삭제 거부)은 OP4c·D1 에서 더한다.
