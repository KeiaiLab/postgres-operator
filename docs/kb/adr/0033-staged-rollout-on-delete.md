# ADR-0033: 샤드 멤버는 OnDelete 로 두고 오퍼레이터가 하나씩, primary 는 마지막에 교체한다

- **Date**: 2026-09-29
- **Status**: Accepted
- **Authors**: @claude (PL3 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.16 · 불변식 I8·I20·I24 · ADR-0030(런타임 ConfigMap) · ADR-0031(재구축 엔진) · ADR-0032(제자리 switchover)

## Context

- 샤드 StatefulSet 은 UpdateStrategy 를 두지 않아 RollingUpdate(OrderedReady · partition 0)였다. 템플릿이
  바뀌면 STS 컨트롤러가 높은 서수부터 primary 까지 재시작한다 — 복제 상태를 보지 않고, primary 는 파드 삭제로
  멈춘다(인스턴스 매니저가 SIGKILL — 스트리밍 안 된 꼬리 유실). 2026-09-24 루프의 한 고리였다.
- 파드를 지우면 postgres 는 곧바로 죽는다. 계획된 primary 교체는 제자리 switchover(ADR-0032)뿐이다.
- 수렴은 readyz 로 못 본다 — 끊긴 standby 도 Ready 다. 복제 그래프(pg_stat_replication)가 본다.

## Decision

1. 샤드 STS(리샤드 타깃 포함)는 `UpdateStrategy = OnDelete`. `copySpec` 이 전략을 통째로 옮겨 템플릿과 같은
   Update 한 요청에 싣는다 — 따로 오면 템플릿이 먼저 바뀐 순간 STS 컨트롤러가 재시작을 시작한다. OnDelete 는
   `rollingUpdate`(서버 기본값 partition 0)와 함께 검증을 통과하지 못해 필드를 비운다.
2. `advanceRollout`(샤드마다, 그래프·재구축·switchover 다음): 옛 멤버 = `controller-revision-hash` ≠
   `status.updateRevision`(STS 상태가 현재 generation 을 봤을 때만).
   - 옛 replica 가 있으면 하나를 지운다(UID 조건부) — streaming 아닌 것 먼저, 같으면 서수가 높은 것. STS 가 새
     revision 으로 다시 만든다.
   - 옛 replica 가 없고 primary 가 옛이면 `startSwitchover(Rollout)` — 옛 primary 는 제자리 standby 가 되고
     옛 revision replica 로 같은 규칙에 교체된다. 멤버가 하나면 switchover 할 standby 가 없어 지운다.
3. 선결: 샤드에 재구축·switchover 0 · 그래프 known ∧ CM primary 가 실제 primary · 대상 밖 replica 전부
   Ready ∧ streaming(primary 단계는 standby 전원 streaming — 수렴) · 첫 단계 전 primary `wal_keep_size` =
   렌더 값 · 렌더한 hot standby 필수 키(max_connections · max_worker_processes · max_wal_senders ·
   max_prepared_transactions · max_locks_per_transaction) ≥ primary 현재값. primary 설정은 exec 한 번
   (`pg_settings`)으로 읽는다.
4. 선결이 `memberConvergeBudget`(= staleStandbyReseedTimeout) 넘게 안 서면 condition `RolloutBlocked`
   (MemberNotConverged · RebuildActive · ParameterDecrease · PrimaryConfigPending · ImagePull · ConnectionSlots)
   와 Warning `ShardRolloutBlocked` 한 번. 다음 단계를 실행하거나 끝나면 `RolloutClear`. exec 가 연결 슬롯이
   없어 실패하면 모름(ConnectionSlots)이다. 이미지를 못 받는 멤버는 ImagePull 이다.
5. 완료 = 전 파드 해시 = updateRevision ∧ `updatedReplicas` = replicas — OnDelete 에서 안 움직일 수 있는
   `currentRevision` 은 보지 않는다. `ShardRolloutCompleted` 한 번.
6. 지표 `rollout_steps_total{step=replica|primary, outcome=started|blocked}` ·
   `rollout_pending_members`. 이벤트 `ShardRolloutStep`.

## Consequences

- 멤버를 지우는 주체는 오퍼레이터 하나이고 한 번에 하나다(I20). STS 컨트롤러는 멤버를 재시작하지 않는다(I8).
  PodManagementPolicy·RBAC 는 그대로다.
- primary 는 언제나 마지막이고 파드 삭제가 아니라 제자리 switchover 로 바뀐다 — 쓰기 공백은 switchover 한 번.
- switchover 가 primary 를 못 바꾸고 끝나면(중단) `memberConvergeBudget` 뒤 다시 연다. 재구축 예산은 쓰지 않는다.
- 막힘의 시계·Warning 한 번·완료 한 번은 메모리 기억이다 — 오퍼레이터가 다시 뜨면 예산 시계를 새로 잰다.
- 노드 사망·재구축이 지운 파드도 새 revision 으로 다시 생긴다(OnDelete 의 정의) — 롤아웃은 그 멤버를 교체된
  것으로 본다.
- 렌더하지 않은 설정은 보지 않는다 — wal_keep_size·max_connections 렌더는 OP5·PL4 가 싣는다.

## Alternatives Considered

- RollingUpdate + partition: STS 컨트롤러가 여전히 순서대로 지우고 복제 상태를 보지 않는다. primary 도 파드
  삭제(SIGKILL)로 멈춘다.
- readyz 로 수렴 판정: 끊긴 standby 도 Ready 라 다음 멤버를 지워 streaming standby 0 을 만든다.
- 하나뿐인 멤버를 막아 둔다: 사람이 지워야 풀린다 — 사람 게이트다.

## Status

Accepted (PL3). SEC1 은 hba 를 바꾼 뒤 옛 revision 멤버의 재접속 실패를 이 롤아웃이 먼저 교체한다고 본다
(streaming 아닌 옛 replica 먼저).
