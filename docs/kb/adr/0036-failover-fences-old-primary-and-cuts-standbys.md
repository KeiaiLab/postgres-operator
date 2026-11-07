# ADR-0036: failover 는 옛 primary 를 멈추고 생존 standby 의 수신을 끊은 뒤 승격한다

- **Date**: 2026-11-07
- **Status**: Accepted
- **Authors**: @claude (설계 — V1 판 1·3 S3 J9 · S6b J16)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.3·§3.4·§3.12 · 불변식 I6·I10·I26·I29 · 심판 FC-4 ·
  ADR-0030(런타임 CM) · ADR-0031(재구축 엔진) · ADR-0032(제자리 switchover) · 판정문 `test/e2e/loopdrill/verdicts/322546d6-17771de3.json` ·
  컨텍스트 `keiailab-postgres-operator-loopdrill-v1-round3-reds-2026-11-07`

## Context

kind V1 판 1·3 의 S3(primary 컨테이너 PID 1 = 인스턴스 매니저만 SIGSTOP, postgres 는 돈다)에서 J9 가 붉다.

```
07:28:22.47Z  오퍼레이터가 0-0 승격 — TL3 분기점 0/9976A88 (00000003.history)
07:28:25.64Z  옛 primary 0-1 이 tx 1495 커밋 (0/9976A88..0/9976B40) → 생존 standby 0-2 로 스트리밍
07:29:01Z     0-2 재지정 → 분기점을 넘어 TL3 을 못 따른다 → 8 분 붙지 못함
07:36:46Z     Stale 재시드(PVC 재구축)                        판 1 도 같다(0/99B3E68 vs 0/99B4028)
```

- 승격 경로(`executeClusterPromotion`)는 옛 primary 에 PVC fence 라벨만 단다. 라벨은 **부팅 때** 인스턴스 매니저가
  `VerifyNotFenced` 로 보는 것이라 이미 도는 postgres 를 멈추지 못한다.
- 리스는 펜스가 아니다. `handleStoppedLeading` 은 로그만 남기고(T30), 승격된 standby 의 매니저는 Follower 라 리스를 잡지
  않는다. S3 처럼 매니저가 얼면 매니저가 스스로 할 수 있는 일도 없다.
- `builders.go` #220 분기 주석 "the operator fence stops an illegitimate primary" 가 가정하는 정지 동작이 코드에 없다.
- 승격 대상은 상태 애노테이션(5 s 주기)의 수신 위치로 고른다. 옛 primary 가 살아 있는 동안 다른 standby 는 계속 받아
  후보를 넘어설 수 있다 — 분기점 뒤로 간 standby 는 재시드밖에 없다.
- 인스턴스 매니저·pg 이미지는 고치지 않는다(I26). exec 로 쓰는 도구는 ADR-0032 정지 exec 와 같다(sh · psql · pg_ctl).

## Decision

자동 failover 한 번 = 한 reconcile 안의 다섯 걸음. 상태 애노테이션은 두지 않는다 — 걸음마다 멱등이라 재기동한
오퍼레이터는 처음부터 다시 한다.

```
 F0 PVC fence 라벨(기존)      옛 primary 가 NotReady·부재면
 F1 펜스 exec(옛 primary)     standby.signal · 마커 rm → fast(-t 5s) → immediate(-t 5s) → SIGKILL → 확인
 F2 수신 끊기(생존자 전부, 동시) primary_conninfo = '' + reload → pg_stat_wal_receiver 0 행 확인
 F3 승자 = 끊긴 생존자 중 끝 위치 최대   동률 → 판정 후보 → 낮은 서수 (switchoverWinner 재사용)
 F4 promoteAndPublish(failover) 기존 — epoch 선기록 · 옛 primary hold · Demoted
 F5 나머지 생존자 → 승자로 재지정  MemberRetargeted · 재지정 기록(그래프가 다시 안 한다)
```

1. **F1 펜스** — 대상 = 기록된 primary(`shardPrimaryPod`, 기존 F0·hold 와 같은 파드)가 판정 후보와 다를 때. 파드 부재 →
   `Down`. 파드가 지워지는 중이거나 Running 이 아니면 exec 하지 않는다 → `Unreachable`(죽은 노드 exec 는 시한까지 붙잡힌다).
   exec 한 번(`fenceExecBudget`):
   - postgres 가 안 돌면 `Down`.
   - `standby.signal` 을 만들고 승격 마커를 지운다 — 매니저가 깨어나 컨테이너가 init 없이 다시 떠도 Follower 다(리스 경쟁 0).
   - `pg_ctl stop -m fast -t fenceFastWait` → 실패면 `-m immediate -t fenceImmediateWait` → 실패면 `comm = postgres` 인
     프로세스 전부 SIGKILL(얼어 있어도 받는다).
   - 확인: `comm = postgres` 이고 상태가 Z(좀비 — 얼어 있는 매니저가 거두지 않았다)가 아닌 프로세스 0. 결과 줄
     `FENCE_RESULT=Stopped|Killed|Down|Stuck`. exec 오류·시한 초과 → `Unreachable`.
   - fast shutdown 은 연결된 standby 에 종료 체크포인트까지 보낸 뒤 끝난다 — 펜스가 F2 보다 앞서는 이유다(ack 된 꼬리가
     생존자에 있다).
   - `Stopped·Killed·Down` → `PrimaryFenced`(Normal). `Unreachable·Stuck` → `PrimaryFenceUnproven`(Warning). 어느 쪽이든 F2 로
     간다 — 생존자 보호는 F2 가 결과 분류에 기대지 않고 한다.
2. **F2 수신 끊기** — 대상 = 샤드 멤버 중 옛 primary 를 뺀 Running ∧ in recovery 멤버(판정 후보 포함). `retargetCommand("")`
   (`ALTER SYSTEM SET primary_conninfo = ''` + `pg_reload_conf()`)로 walreceiver 를 내리고 `cutWait` 안에
   `pg_stat_wal_receiver` 0 행을 본다. 멤버마다 동시에 한다. 실패한 멤버는 승자 후보에서 빠지고 `StandbyCutFailed`(Warning).
   끊긴 멤버는 옛 primary 가 살아 있어도 더 받지 않는다 — 끝 위치가 고정된다.
3. **F3 승자** — 끊긴 멤버마다 `memberLSN`(수신·재생 중 큰 끝)을 exec 로 잰다. 최대 끝을 `need` 로
   `switchoverWinner(후보, "", need, received)`. 승자 ≠ 후보면 `FailoverWinnerChanged`(Normal) 를 내고 승자에 대해
   `shouldSkipFencedCandidate`(#220) · `promotionCandidateReadyForExec` 를 다시 본다. 잰 멤버 0 · 승자 fence 가드 참 ·
   승자 NotReady → 끊은 멤버를 CM primary 로 되돌리고(`retargetStandby`) 오류 — 창은 지우지 않아 다음 reconcile 이 곧바로
   다시 한다.
4. **F4** — 변경 없음. 승자 endpoint = `memberEndpoint(승자, shardRefFor(..).service)`.
5. **F5** — 승자를 뺀 끊긴 멤버를 `retargetStandby(승자 endpoint)` 하고 `recordRetarget` 한다. 실패는 그래프의
   `retargetDetached`(memberRetargetAfter)가 잇는다. 분기점 = 승자 끝 ≥ 각 생존자 끝이라 새 타임라인을 따른다 — 재시드 0.
6. 상수(산식 — 새 숫자 0): `fenceFastWait = fenceImmediateWait = statusPollInterval` ·
   `fenceExecBudget = fenceFastWait + fenceImmediateWait + statusPollInterval`(15 s) · `cutWait = replicationProbeTimeout`(5 s).
7. 지표 `postgres_operator_failover_fence_total{namespace,cluster,shard,outcome}`.

## 시간 경계 (바꾸지 않는다)

| 판정 | 경계 | 실측(판 3) | 이 결정의 더함(최악) |
|---|---|---|---|
| J8 PodReady False → 승격 | PromotionBound 70 s | S3 30 s · S9 11 s | F1 15 s + F2 10 s(동시) + F3 exec → S3 ≤ 60 s · S9 ≤ 41 s |
| J9 승격 → 생존자 연결 | RejoinBound 40 s | S3 붉음 | F5 가 같은 reconcile 에 재지정 — 수 초 |
| J10 가장 긴 공백 | FailoverGapBound 115 s · NodeDeathGapBound | S3 11 s | 공백 시작이 F1 로 당겨지고 끝은 같다 |
| J15 ack 유실 | 1 | S3 1(tx 1495) | fast 정지면 0 — 꼬리가 생존자에 있다 |

## Drill 계약 — S6b 재설계

판 3 S6b 는 승격이 안 일어나 J16 이 판정할 것이 없었다: (1) 깨뜨린 conninfo 를 OP4a 재지정이 ~37 s 에 고쳤다
(DetachLead 60 s 이전) (2) graceful 삭제한 primary 를 STS 가 ~14 s 에 되살렸다.

- **끊기** = 대상 standby 파드 넷ns 의 iptables(노드에서 `nsenter -t <PID 1> -n`) `OUTPUT -p tcp --dport 5432 -d <옛 primary 파드 IP>
  -j REJECT --reject-with tcp-reset`. conninfo 는 건드리지 않는다 — 재지정이 conninfo 를 다시 써도 같은 IP 라 못 고친다. 새
  primary IP 는 막히지 않아 failover 뒤 J9 가 판정할 수 있다. exec·API 경로는 파드 넷 밖이라 오퍼레이터 관측은 그대로다.
- **primary 손실** = 옛 primary 컨테이너 프로세스 트리 전부(매니저 + postmaster + 자식) SIGSTOP, `PrimaryFreezeHold =
  PromotionBound + PromotionExecBudget` 동안. 하트비트·readiness 가 끊겨 S3 와 같은 감지 경로로 failover 가 확실히 돈다. F1 은
  fast·immediate 가 안 먹어 SIGKILL 단까지 간다 — S3(fast)·S9(Unreachable)와 함께 세 단을 모두 덮는다.
- **유효성(fail-closed)**: DetachLead 끝에 대상의 walreceiver 가 streaming 이 아니어야 primary 를 얼린다 — 아니면 유발 오류로
  시나리오가 붉다("끊김 유지 실패"). S6b `Expect` 에 `PromotionsRequired = 1` — 승격 0 은 J3 이 붉힌다.
- **J16 의미 유지**: 옛 primary 는 얼었다가 죽어 끝 위치 모름 → Demoted data-bearing = true. 끊긴 standby 는 분기점 뒤라
  재지정으로 붙는다 — Stale 이 생기면 false 여야 한다. VAP 거부 확인은 그대로.

## Consequences

- S3: 생존 standby 가 분기점을 넘지 않는다 → 재시드 0, J9 는 F5 로 수 초. 옛 primary 는 기존대로 hold → Demoted.
- 노드 사망·kubelet 단절(F1 Unreachable)에서도 F2 가 생존자를 지킨다. 남는 위험은 오퍼레이터와 끊긴 쪽에 있는 클라이언트가
  옛 primary 에 쓰는 것 — 그 쓰기는 Demoted 스냅숏(data-bearing)에 남는다.
- F1 뒤 F4 전에 오퍼레이터가 죽으면 그 사이 primary 가 없다 — 다른 오퍼레이터 replica 가 리더를 받아 처음부터 한다.
  끊긴 생존자는 그 동안 그래프 재지정(memberRetargetAfter)이 CM primary 로 돌린다.
- 오판(옛 primary 가 실은 건강) 때도 F1 은 그 primary 를 standby 로 내린다 — 디바운스·`heartbeatOnly` 가 앞에서 거른다.

## Alternatives Considered

- 승격 전 생존자를 후보로 재지정(캐스케이드)만: 고른 뒤 재지정 전까지 생존자가 옛 primary 에서 더 받아 후보를 넘을 수 있다.
  끊고 → 재고 → 고르는 F2·F3 이 그 경합을 없앤다.
- 펜스(F1)만: exec 가 안 되는 경우(노드·kubelet 단절, postgres 생존)에 생존자가 계속 받는다. 결과 분류에 생존자 안전을 걸게 된다.
- read-only 펜스(`default_transaction_read_only`): 클라이언트가 끌 수 있고 WAL 은 계속 생긴다(ADR-0032 와 같은 기각).
- 파드 삭제로 멈추기: SIGTERM 이 곧 SIGKILL 이라 꼬리 스트리밍이 없고(J15), STS 가 ~14 s 에 되살려 경합한다.
- 매니저의 리스 자가 펜스: I26 위반이고, S3 처럼 얼어붙은 매니저는 아무것도 못 한다.
- 펜스가 성공하면 hold 없이 제자리 재합류(ADR-0032 식): 결과가 Killed·Unreachable 일 때 L0 증명이 없다 — 별도 결정으로 미룬다.
- S6b 를 NetworkPolicy 로 끊기: kind CNI 의 집행 여부가 판마다 다를 수 있고 라벨 셀렉터가 새 primary 까지 막을 수 있다.
- S6b 를 노드 `docker stop` 으로: S9 와 겹치고 F1 SIGKILL 단을 덮는 시나리오가 없어진다.

## Status

Accepted. 슬라이스: F1(오퍼레이터) · H1(S6b 하네스) · V1(kind 판정문). I26 가드 개정은 ADR-0037.
