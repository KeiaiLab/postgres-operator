# ADR-0039: loopdrill J10 은 두 계측기가 함께 멈춘 시간만 공백에서 뺀다 (호스트 정지 보정 · H1 관측 · 30 s 넘는 정지는 재실행)

- **Date**: 2026-11-08
- **Status**: Accepted
- **Authors**: @claude (설계 — V1 판 5 S6b J10)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.12 J10 · 부록 A(탐침 표본 하한) · 부록 B-3 ·
  `internal/loopdrill/budgets.go` · `internal/loopdrill/judge_write.go` · `test/e2e/loopdrill/testdata/write-probe.yaml` ·
  컨텍스트 `keiailab-postgres-operator-loopdrill-v1-run5-s6b-j10-host-stall-2026-11-08` ·
  증거 `~/.local/state/loopdrill-v1/s6b-evidence-run5/`

## Context

V1 판 5 S6b 의 J10 이 붉다 — "10s 넘는 공백 2(허용 1)". 첫 공백 82 s 는 계획된 failover 다(허용 1 = primary 단계 하나,
부록 B-3). 둘째 공백은 오퍼레이터와 무관하다.

```
14:54:28.37Z  ack 1849   (서버 clock_timestamp, 탐침 위상 .37)
              ── WSL2 VM 전체 정지 ~14 s ──  worker·worker3 kubelet 저널 침묵 28.4→42.8 · 오퍼레이터 이벤트 0 · load 109/32 CPU
14:54:42.69Z  ack 1850   (예정 :33.37 — 공급 bash 가 늦게 깼다)
14:54:42.70Z  ack 1851   (예정 :38.37 — 밀린 틱을 바로 보냄, 15 ms 간격)
14:54:43.70Z  ack 1852   위상 .37 → .70 로 고정 — 탐침 클라이언트 자체가 멈췄다는 표지
```

- 판정은 서버 시각(ack epoch)만 본다. 탐침이 멈춘 것인지 DB 가 멈춘 것인지 기록에 없다 → 드릴이 호스트 정지와
  진짜 쓰기 정지를 가르지 못한다.
- 위상 이동은 사람이 읽은 단서다. 공급기가 정수 초 `sleep` 이라 생긴 부수 효과이고, 서버만 멈춘 경우에도 밀린 문장이
  15 ms 간격으로 몰려 나오는 꼴은 같다 — 기계 판별 근거로 쓰기엔 약하다.
- 탐침 파드 CPU limit 200m — 호스트 부하 아래 CFS 스로틀만으로도 탐침만 멈출 수 있다(DB 는 돈다).
- 예산(ReplicaGapBudget 10 s · GapBound · LongGaps · 표본 하한)은 부록 B-3 산식이다. 이 ADR 은 어느 것도 바꾸지 않는다.

## Decision

**두 독립 계측기가 같은 시각에 함께 멈췄다는 기록이 있을 때만, 그 겹친 시간을 그 공백에서 뺀다.** 뺀 양은 H1 관측으로
판정문에 싣는다. 30 s(statusStaleThresh) 이상 정지는 보정하지 않고 판 전체를 재실행 대상으로 낸다.

```
 계측기 A  탐침 공급기(kind 노드 컨테이너 안)   ack <id> <exec> <due> <sent>
           due  = 이 연결 안에서 그 문장이 나갈 예정 시각(ns 격자)
           sent = 공급기가 실제로 문장을 낸 시각
           탐침 맹점 B_i = [due_i, sent_i]  (sent_i − due_i > HostStallFloor 일 때만)

 계측기 B  드라이버 정지 시계(호스트 프로세스, kind 밖)
           HostStallSample 마다 시각 표본 → 이웃 표본 차(벽시계·단조 중 큰 것) > HostStallFloor 이면
           정지 구간 S = [앞 표본 벽시계, 뒤 표본 벽시계] → 상태 디렉터리 host-stalls.jsonl 에 덧붙임

 J10 보정  ack i−1 → ack i 공백 g_i = exec_i − exec_{i−1}
           credit_i = | [exec_{i−1}, exec_i] ∩ B_i ∩ ∪S |
           보정 공백 = g_i − credit_i   → 가장 긴 공백 · ReplicaGapBudget 초과 수는 보정 공백으로 센다(상한 그대로)
```

1. **보정 조건** — 맹점(A)과 호스트 정지(B)가 **둘 다** 있어야 한다. 탐침만 멈춤(스로틀) · 드라이버만 멈춤 · DB 만 멈춤은
   보정 0. 두 프로세스는 다른 cgroup(kind 노드 컨테이너 대 호스트)이라 함께 멈춤은 VM·호스트 전역 정지의 기계 증거다.
   그 시간에는 DB·오퍼레이터도 돌지 못했다.
2. **보정 상한** — credit 은 공백 안이고 맹점 안이다. 탐침이 예정대로 보냈을 시각(due) 앞의 공백은 정상 주기라 보정하지 않는다.
   S6b: g = 14.32 s, B = [33.37, 42.68], S ≈ [28.4, 42.6] → credit ≈ 9.2 s → 보정 공백 ≈ 5.1 s ≤ 10 s.
   DB 가 20 s 멈춘 사이 탐침이 9.3 s 멈추면 보정 공백 10.7 s 로 여전히 붉다.
3. **보정 0 인 자리(fail-closed)** — 창 가장자리 공백 · 3-필드 ack(옛 탐침 · 옛 판정문) · 정지 기록 없음(드라이버 재기동 중 ·
   운영 L1) · 정지 파일을 못 읽음 · HostStallFloor 이하 지연.
4. **H1 관측** — 판정이 아니라 관측이다(`Verdict.Observations`, ID `H1`). 창 안 정지 구간 수 · 보정에 쓴 수 · 보정 합 ·
   가장 긴 정지. `Green()` 은 관측을 보지 않는다. 드라이버 요약줄과 rejudge 출력에 싣는다.
5. **판정문 결과** — 순서대로 첫 해당:
   `incomplete` → **`host-stalled`**(어느 판이든 창 안 정지 ≥ StatusStaleThresh. 오퍼레이터 시한 전부가 30 s 이상이라 이보다 긴
   정지는 오퍼레이터를 비정상 조건에 몰아넣어 다른 판정도 오퍼레이터를 재지 못한다 — RED 모드 재현도 무효, 재실행)
   → 기존 RED · `not-green` → **`green-host-stalls`**(전부 초록 ∧ 어느 판이든 H1 보정 > 0) → `green`.
   `green-host-stalls` 는 V1 통과다 — 예산은 그대로이고 뺀 것은 두 계측기가 함께 멈춘 시간뿐이다. `== green` 으로 비교하는
   소비자는 고칠 때까지 붉게 읽는다(fail-closed 쪽 오류).
6. **rejudge** — 기록만 본다. 판정문의 `after.hostStalls` 와 ack `due`·`sent` 로 같은 보정을 한다. 저널·이벤트 같은 바깥
   증거로 정지 구간을 새로 넣는 입력은 두지 않는다(사후 큐레이션 = 사람 게이트이자 정직성 구멍). 옛 판정문은 보정 0 이라
   결과가 바뀌지 않는다 — 판 5 S6b 는 rejudge 로 초록이 되지 않고 **재실행**한다. `observations` 필드는 저장 판정문 계약에
   더한다(모르는 필드 거부는 유지).
7. **멱등·재개** — 정지 파일은 덧붙이기 전용 JSONL, 읽을 때 정렬·합집합이라 재기동·중복 기록이 같은 결과다. 드라이버가 없던
   시간은 기록이 없어 보정 0.

### 상수 (budgets.go 에 더한다 — 기존 값 무변)

| 이름 | 값 | 근거 |
|---|---|---|
| HostStallSample | 100 ms | 정지 시각 해상도 — 보정 오차 ≤ 2 표본 |
| HostStallFloor | 1 s | 스케줄러 지터(ms)보다 크고 ReplicaGapBudget − WriteProbeInterval(5 s 여유)보다 작다. 이하면 보정 0 |

판정 문턱(ReplicaGapBudget · FailoverGapBound · NodeDeathGapBound · EmergencyGapBound · WriteGapBudget · LongGaps · 표본 하한)은
바꾸지 않는다. I26(인스턴스 매니저·pg 이미지) 무관 — 탐침은 공개 postgres 이미지의 bash 다.

## Consequences

- 호스트 전역 정지가 J10 을 붉히지 않는다. 진짜 쓰기 정지는 계측기가 멈춘 시간만큼만 덜 보이고, 그 양은 H1 로 드러난다.
- 탐침 로그 형식이 5 필드가 된다. `ParseAcks` 는 3·5 필드를 다 읽는다 — 운영 L1 이 옛 파드를 돌려도 깨지지 않는다(보정 0).
- 살아 있는 탐침 파드는 args 가 바뀌어 `kubectl apply` 가 거부된다 → 설치가 args 불일치를 보고 파드를 다시 만든다(워밍업 게이트 그대로).
- 정지가 잦은 호스트에서는 `green-host-stalls` 가 늘어난다. 30 s 이상이면 `host-stalled` 로 재실행 — 벽시계 비용은 그 판만.
- 공급기는 ns 격자로 바뀌어 위상이 정지 뒤에도 유지된다(밀린 틱은 지금처럼 곧바로 보낸다).

## Alternatives Considered

- **J10 허용(LongGaps)·ReplicaGapBudget 올리기** — 기각. 게이트 완화이고, replica 단계 진짜 정지를 숨긴다.
- **탐침을 클러스터 밖(호스트 프로세스)으로** — 기각. J10 은 -rw Service 경유 클러스터 안 클라이언트의 쓰기 공백을 잰다
  (운영 L1 과 같은 파일). 밖으로 옮기면 Service·kube-proxy 경로가 빠져 다른 것을 잰다. 호스트 정지도 그대로 맞는다.
- **탐침 자체 지연만으로 보정(a 의 clock_gettime 안)** — 기각. 탐침만 스로틀되는 동안 DB 가 멈춰 있었는지 알 수 없다 —
  DB 15 s 정지 안에 탐침 9 s 멈춤이면 6 s 로 초록이 된다(가짜 초록). 둘째 계측기를 요구한다.
- **드라이버 정지 시계만으로 보정** — 기각. 드라이버만 굶은 경우를 가르지 못한다.
- **노드 kubelet `/metrics` · 노드 안 바쁜 루프 타임스탬프 파일** — 기각. kubelet 지표는 정지 구간을 직접 주지 않고(긁기 간격
  공백뿐), 노드 안 루프는 kind 노드에 새 프로세스·권한을 들인다. 드라이버 시계가 같은 정보를 kind 밖에서 준다.
- **위상 이동 판별(정수 초 sleep 부수 효과)** — 기각. 우연한 구현 성질이고 서버 정지와 구분이 약하다.
- **(b) 부하 게이트(1분 load > 2 × nproc 이면 다음 유발 전 대기)** — 기각. 판 5 정지는 유발 8분 뒤 관측창에서 났다 — 유발 전
  대기는 그 창을 지키지 않는다. load 는 VM 정지를 예측하지 못하고, 2 × nproc 은 산식 없는 수다. 시한이 없으면 무기한 대기,
  시한 뒤 진행하면 게이트가 아니다.
- **(c) 둘 다** — (b) 기각으로 해당 없음.
- **정지가 보이면 언제나 깨끗한 재실행만** — 기각(기본값으로서). 공유 호스트(load 109)에서 정지가 반복되면 끝나지 않는다.
  30 s 이상 정지에만 재실행을 요구한다.
- **정지 공백을 통째로 판정에서 빼기(hostStall=true 표식)** — 기각. 공백 안 정지 아닌 시간까지 지운다. 겹친 시간만 뺀다.

## 슬라이스

| id | 브랜치 | 소유 파일 | 앞 |
|---|---|---|---|
| HS1 | feat/loopdrill-stall-detector | internal/loopdrill/hoststall.go(+_test) · budgets.go | — |
| HS2 | feat/loopdrill-j10-stall-credit | internal/loopdrill/judge_write.go · judge.go · judge_write_test.go · fixtures_test.go | HS1 |
| HS3 | feat/loopdrill-outcome-host-stalls | internal/loopdrill/report.go(+_test) · test/e2e/loopdrill/cmd/rejudge/main.go | HS2 |
| HS4 | feat/loopdrill-probe-due-sent | test/e2e/loopdrill/testdata/write-probe.yaml · internal/loopdrill/probe_script_test.go · test/e2e/loopdrill_install_e2e_test.go | HS2 |
| HS5 | feat/loopdrill-driver-stall-clock | test/e2e/loopdrill_hoststall_e2e_test.go(신규) · loopdrill_e2e_test.go · loopdrill_scenarios_e2e_test.go | HS1 · HS2 |

착지 뒤 확인: HS5·HS4 착지 뒤 V1 S6b 재실행 — 결과 `green` 또는 `green-host-stalls`, 판 5 꼴 정지가 다시 나면 H1 보정 > 0.

## Status

Accepted. 판단 근거: 보정 양은 두 독립 계측기의 교집합으로 기계가 정하고, 기존 예산·허용은 한 글자도 바뀌지 않으며,
증거가 없는 모든 자리는 보정 0(옛 판정과 같음)이다. 남는 위험(공유 커널 아래 두 cgroup 이 함께 굶는데 DB 만 도는 경우)은
H1 로 드러나고 30 s 이상은 재실행이다.
