# ADR-0032: switchover 는 옛 primary 를 제자리에서 멈춘 뒤 승격한다

- **Date**: 2026-09-29
- **Status**: Accepted
- **Authors**: @claude (PL2 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.3·§3.16 · ADR-0030(런타임 ConfigMap) · ADR-0031(재구축 엔진)

## Context

- v0.4.0-beta.11 의 switchover(`switchover-target` 애노테이션)는 대상을 곧바로 `pg_promote` 한다. 옛 primary 는
  그 순간에도 쓰기를 받고 있어 스트리밍 안 된 꼬리를 가진 채 밀려나고, 다음 부팅에 되감기(pg_rewind)로 그 꼬리를
  버린다 — ack 된 커밋이 사라진다.
- 파드를 지워 멈추는 것도 답이 아니다. SIGTERM 은 인스턴스 매니저의 `signal.NotifyContext` 를 취소하고
  `exec.CommandContext` 가 postgres 를 곧바로 SIGKILL 한다(cmd/instance/main.go:274 · supervise.go:258). smart
  shutdown 은 이미 죽은 프로세스를 상대한다.
- postgres 가 스스로 끝나면(`pg_ctl stop`) 매니저가 exit 하고 컨테이너가 init 없이 다시 뜬다. `standby.signal` 이
  있으면 Follower 로 돈다(main.go:225-235). 인스턴스 매니저·pg 이미지는 바꾸지 않는다(I26).

## Decision

1. 상태 = CR 애노테이션 `postgres.keiailab.io/switchover-state`(JSON v1 — shard · oldPod · oldUID · oldRestarts ·
   target · reason · phase · since · stopAt · l0). 클러스터당 하나. 재기동한 오퍼레이터가 이어 간다.
2. 여는 길 둘 — `switchover-target` 애노테이션(Requested)과 단계화 롤아웃(Rollout, PL3) — 이 같은
   `startSwitchover` 를 부른다. `handleSwitchover` 는 승격하지 않는다.
3. Preparing(쓰기 계속): CM primary P 가 실제 primary 가 아니면 곧바로 중단한다. 샤드에 재구축이 있으면
   기다린다. 대상 t = P 의 `pg_stat_replication` 에서 streaming 인 멤버 중 수신 위치 최대(동률 → 낮은 서수).
   요청 대상이 그 최대가 아니면 `SwitchoverTargetBehind` 로 중단한다. 상태 기록 → P `CHECKPOINT`(공백 시계 밖).
4. Stopping: 상태(phase · stopAt · oldRestarts)를 먼저 쓰고 P 에 exec 한 번 — `ALTER SYSTEM SET primary_conninfo`
   (host = t, application_name = P) → `touch standby.signal` → 승격 마커 rm → L0 = `pg_current_wal_lsn()` →
   `pg_ctl stop -m fast -W`. fast shutdown 은 종료 체크포인트까지 연결된 스탠바이의 flush 확인을 받은 뒤
   끝난다 — ack 된 커밋은 전부 스탠바이에 있다. 파드 Delete 0.
   - `STOP_REQUESTED` 가 없고 P 가 아직 실제 primary 면 `standby.signal` rm · 마커 = CR epoch 로 되돌리고
     `SwitchoverAborted`.
   - P 컨테이너 재시작 수가 오르고(또는 파드가 바뀌고) `pg_is_in_recovery()=t` 가 되기 전엔 승격 exec 0.
     `switchoverRestartWait`(= statusStaleThresh) 안에 안 돌아오면 `SwitchoverAborted(OldPrimaryNotBack)`.
5. Promoting: 승자 = P 의 끝(재생 끝, L0 이상)을 가진 t → 그런 다른 standby(수신 최대 · 낮은 서수) → P
   (데이터를 전부 가졌다). t 가 아니면 `SwitchoverTargetBehind`. `promoteAndPublish(kind=switchover)` — hold 0
   (P 는 건강한 standby 로 새 타임라인을 따른다, 되감기 0). 발행을 확인하면 상태(요청이면 애노테이션도)를 지우고
   `SwitchoverCompleted` · `switchover_duration_seconds`(stopAt → 승격).
6. 자동 failover: 진행 중 샤드는 `shouldPromoteAfterDebounce` 에 실패를 그대로 넘겨 창을 누적하고 실행만
   보류한다. 중단되면 다음 판정이 누적된 창으로 곧바로 돈다(Demoted(P) hold 는 발행과 같은 CM 쓰기). switchover
   승격을 한 reconcile 은 failoverPending 을 지우고 requeue 로 끝난다. 진행 중엔 `pgPromotePoll` 로 다시 본다.
7. 이어받기: Preparing → 처음부터 · Stopping ∧ P 실제 primary → 되돌리고 중단 · Stopping ∧ P in recovery → 승자
   선정 · Promoting → 발행됐으면(채택 포함) 끝, 아니면 승자 다시 선정. 어느 단계든 `switchoverBudget`
   (= switchoverRestartWait + promotionExecBudget + 2 × statusPollInterval) 을 넘으면 중단한다.

## Consequences

- 계획된 primary 교체에 ack 된 커밋 유실·되감기·Demoted 재구축이 없다. 쓰기 공백 ≈ fast shutdown + 컨테이너
  재기동 + standby 기동 + 승격 + 라벨(설계 §3.16, 문턱 writeGapBudget 30 s).
- P 가 제때 안 돌아오면 정상 failover(승격 1 + Demoted)로 끝난다 — 루프가 아니다.
- 승자가 t 가 아니면 P 는 t(standby)를 따르고 다른 standby 는 P 를 따른다 — 복제 그래프가
  memberRetargetAfter 뒤 새 primary 로 재지정한다.
- 승격 exec 뒤 발행 전에 오퍼레이터가 죽으면 채택이 잇는다 — 채택은 옛 CM primary(P)를 hold 하므로 그 경로에선
  P 가 재구축을 탄다(보존 쪽).
- 요청 대상 판정은 상태 애노테이션의 수신 위치(5 s 주기)라 부하 중엔 근소하게 뒤처진 대상도 중단될 수 있다 —
  애노테이션을 다시 단다.

## Alternatives Considered

- 파드 삭제 → Stopping 관측 → fast stop(심판 DS-1): SIGTERM 이 곧 SIGKILL 이라 멈출 postgres 가 없다.
- read-only 펜스 + 백엔드 종료(심판 PR-4): `default_transaction_read_only` 는 클라이언트가 끌 수 있고 끝내
  SIGKILL 이라 옛 primary 가 되감기·Demoted 를 산다.
- 승격 먼저(beta.11): 옛 primary 꼬리 유실 — 이 ADR 이 고치는 결함이다.

## Status

Accepted (PL2). 단계화 롤아웃(PL3)이 `startSwitchover(Rollout)` 를 부르고 `switchoverActive` 로 멤버 삭제를 멈춘다.
