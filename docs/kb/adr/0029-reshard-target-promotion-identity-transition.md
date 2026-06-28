# ADR-0029: resharding target shard 영구 승격 — 정체성 전이 설계

- **Date**: 2026-06-28
- **Status**: Proposed
- **Authors**: @claude (세션 작업), review pending
- **Refs**: ADR-0027 (비-ordinal target 식별·격리), ADR-0001 (self-built distributed SQL), #220 (failover identity saga)

## P-A.2 selector audit / implementation note (2026-06-28)

### P-A.2.1 status endpoint precondition (2026-06-28)

instance manager 는 이제 `POSTGRES_SERVICE_NAME` 으로 전달된 실제 StatefulSet service name 으로
Pod status endpoint 를 보고한다. ordinal shard 는 기존과 동일하게 `<cluster>-shard-<ordinal>-headless`
를 받고, reshard target 은 `<cluster>-rsd-<target>-headless` 를 받는다. env var 가 없는 기존 Pod 는
기존 ordinal service naming 으로 fallback 한다.

이 변경은 Promote P-B 자체는 아니지만 P-B 의 전제 조건이다. target Pod 가 source ordinal service
endpoint 를 status 로 내보내면, router/status/failover 가 승격 대상 shard 를 잘못된 DNS 로 해석할 수
있기 때문이다. named target shard row 생성, source decommission, target HA 확대는 여전히 P-B/P-C 범위다.

### P-B.1 active named target status (2026-06-28)

`PostgresClusterReconciler` 는 이제 active `ShardRange.spec.ranges[].shard` 중 ordinal shard 가 아닌
이름을 `PostgresCluster.status.shards[]` 에 추가한다. 예를 들어 ShardRange 가 `t1` 을 가리키면
status row 는 `name=t1`, `ordinal=-1` 로 기록된다. target Pod 선택은
`postgres.keiailab.io/reshard-target=<id>` 와, 향후 adopt 후의
`postgres.keiailab.io/shard-id=<id>` 를 모두 허용한다.

이로써 routing flip 이후 `StatusBackendResolver` 가 target shard primary endpoint 를 해석할 수 있다.
단, 이것은 lifecycle 승격 전체가 아니다. 이 시점 기준으로 source ordinal resource decommission,
target replica scale-up/HA, spec shard model 의 named-list 전환, Promote phase idempotency 는 P-B/P-C
잔여 범위였다.

### P-C.1 active topology source decommission (2026-06-29)

native cluster 에 하나 이상의 `ShardRange` 가 존재하면 `ShardRange.spec.ranges[].shard` 의 union 을
active topology 로 본다. active topology 에서 빠진 ordinal shard 는 StatefulSet replicas 를 0 으로
낮추고 `PostgresCluster.status.shards[]` 에서 제외한다. StatefulSet/PVC 는 삭제하지 않는다.

이 변경으로 routing flip 후 target shard 가 Ready 인데도 기존 source `shard-0` fallback row 가
Ready=false 로 남아 cluster 를 Provisioning/Degraded 에 묶어두는 문제를 제거한다. 조건 메시지와
Ready event 의 shard count 도 active status row 수를 사용한다.

이 시점 기준 잔여 범위는 target replica scale-up/HA, 명시적 ShardSplitJob Promote phase,
source resource/PDB 정리 정책, CRD spec 의 named shard model 전환이었다.

### P-C.2 active target HA scale-up (2026-06-29)

active topology 에 포함된 non-ordinal shard 는 `PostgresClusterReconciler` 가 ConfigMap, headless Service,
StatefulSet 을 직접 유지한다. StatefulSet replicas 는 `1 + spec.shards.replicas` 로 조정되고,
target replica init container 는 target primary endpoint 를 받아 `pg_basebackup` 경로로 들어간다.
hibernation/restore 중에는 active target 도 replicas=0 으로 내려간다.

selector 와 lease 격리는 유지한다. active target 의 Service/StatefulSet selector 는 여전히
`postgres.keiailab.io/reshard-target=<id>` 이고, Pod env 에는 `POSTGRES_RESHARD_TARGET` 이 남아 target
전용 lease 를 사용한다. 즉 이 단계는 HA scale-up 이며, 아직 "target 을 ordinal shard 로 rename/adopt" 하지는
않는다.

이 단계 직후 남은 범위는 명시적 ShardSplitJob Promote phase 의 target adopt/idempotency,
source PDB/resource cleanup 정책, CRD spec 의 named shard model 전환, 그리고 live chaos/e2e 검증이었다.

### P-B.2 explicit Promote adopt phase (2026-06-29)

ShardSplitJob state machine 에 `Promote` phase 를 추가했다. 전이는 `Cleanup -> Promote -> Completed` 이며,
CRD status phase enum 에도 `Promote` 를 반영했다.

이번 조각의 역할은 target shard 를 운영 관측 identity 에 편입하는 것이다. `Promote` 는 각 target
StatefulSet object label, Pod template label, live target Pod label 에
`postgres.keiailab.io/shard-id=<target>` 을 붙인다. 선택자는 계속
`postgres.keiailab.io/reshard-target=<target>` 에 고정한다. StatefulSet selector 를 바꾸지 않기 때문에
Kubernetes selector 불변성 문제를 피하고, `POSTGRES_RESHARD_TARGET` 기반 target 전용 lease 격리도 유지한다.

이 구현은 `MergeFrom` patch 기반이라 같은 phase 가 반복 reconcile 되어도 같은 label 상태로 수렴한다. 다만
source StatefulSet/Service/PVC/PDB 삭제, source data retention 정책, promote 중 target/source pod kill 을 포함한
live chaos 검증은 아직 별도 잔여 범위다. 즉 P-B 전체 완료가 아니라 P-B 의 adopt/idempotency slice 완료다.

### P-B.3 Promote source-active gate (2026-06-29)

`Promote` phase 가 target adopt 를 수행하기 전에 matching `ShardRange` 의 active shard set 을 확인한다.
모든 `spec.sources[]` 가 active set 에서 빠져 있고 모든 target shard ID 가 active set 에 있어야 한다.
source 가 아직 active 하거나 target 이 아직 active topology 로 들어오지 않았으면 `Promote` phase 를 유지한 채
requeue 하며 target StatefulSet/template/live Pod 에 `shard-id` 를 붙이지 않는다.

이 게이트는 source/target 이 같은 운영 identity 로 동시에 관측되는 #220-class 중간 상태를 줄이는 1차 fence 다.
아직 target Pod readiness gate, source PDB/PVC/Service 삭제 정책, live chaos 검증은 별도 잔여 범위다.

이번 hardening batch 에서 selector 사용처를 다음처럼 분리했다.

- **그대로 둔 것**: `ShardStatefulSetName`, `ShardServiceName`, PDB/TLS/PVC resize, source shard DNS,
  bootstrap loop 는 여전히 ordinal resource naming 이다. 기존 StatefulSet/Service selector 불변성과
  업그레이드 호환성 때문에 Promote P-B 전에는 바꾸지 않는다.
- **변경한 것**: `aggregateShardStatus` 는 더 이상 `postgres.keiailab.io/shard=<ord>` selector 만으로
  pod 를 고르지 않는다. cluster 공통 label 로 넓게 list 한 뒤 코드에서
  `postgres.keiailab.io/shard=<ord>` 또는 `postgres.keiailab.io/shard-id=shard-<ord>` 를 OR 필터링한다.
- **metrics/failover**: 직접 pod selector 를 갖지 않고 `PostgresCluster.status.shards` 를 소비한다.
  따라서 aggregation 이 `shard-id` 를 이해하면 metrics/failover 는 status 경유로 따라온다.
- **완료한 것**: active `ShardRange.spec.ranges[].shard` 에 나타난 named target 은 `status.shards` 에
  `name=<id>`, `ordinal=-1` row 로 생성한다. target Pod 집계는 `reshard-target=<id>` 또는 adopt 후
  `shard-id=<id>` label 을 허용한다.
- **P-B 주의점**: target 에 `shard-id` 를 붙이기 전에 source ordinal shard 를 fence/관측 제외해야 한다.
  source 와 target 이 같은 `shard-id` 로 동시에 보이면 aggregation 은 split-brain 으로 보고 primary 2개
  상황을 노출한다. 이는 의도적인 안전 신호이며, Promote phase 는 이 중간 상태를 만들지 않아야 한다.

## Context

2026-06-28 기준 online resharding 의 데이터 경로가 *전부* 결선·라이브 검증되었다 (offline + online
무중단 CDC, 스키마/인덱스/PK/제약 복제, write-block, full e2e — `WORK_HANDOFF.ko.md §6.6`).
당시 ShardSplitJob state machine 중 Bootstrap→InitialCopy/CDCCatchup→Cutover→RoutingUpdate→Cleanup→Completed
가 실 K8s+PG 에서 동작했다.

**그러나 당시 ADR-0027 의 P6(승격)는 미구현이었다.** 당시 resharding 완료 후 상태:

- target shard 는 *격리 식별* 로 존재한다: K8s 자원 `<cluster>-rsd-<shardID>` + label
  `postgres.keiailab.io/reshard-target=<shardID>` (ordinal `postgres.keiailab.io/shard` label 부재).
- `ShardRange.spec.ranges` 는 target *이름*(예: t0/t1)으로 flip 됨 → 라우터는 정상 라우팅.
- 그러나 `aggregateShardStatus` / `metrics` / failover 는 ordinal `shard=<N>` label 로만 select
  하므로 **승격된 target 에 blind** — status.shards 에 안 잡히고, primary 죽어도 failover 안 됨.
- source 의 ordinal shard(예: shard-0)는 데이터가 비워졌으나 K8s 자원·ordinal 식별은 살아 있음.

즉 **resharding 으로 만든 새 shard 가 cluster 의 1급 시민이 아니다** — 운영(HA/관측)에서 누락된다.
ADR-0027 은 이 전이를 "두 namespace 가 만나는 유일한 identity-transition 지점, operator-driven +
fenced + single-authority 로 설계, #220-class race 회피, 라이브 chaos 검증 의무"로만 명시하고
*상세 설계를 미뤘다*. 본 ADR 이 그 상세 설계다.

**왜 incremental hack 이 위험한가 (#220 교훈 재확인)**: shard-identity 는 bootstrap-init /
leader-election / operator promotion 3 컴포넌트가 *동일 식별 입력* 으로 standby-vs-primary 를
판정한다. 전이 중 일부만 ordinal label 을 갖고 일부는 reshard-target label 을 갖는 *중간 상태* 가
관측되면, aggregateShardStatus 가 "primary 0개" 또는 "primary 2개"로 오판 → failover 오동작 →
데이터 손실. 따라서 전이는 **단일 권한(operator) + fenced(중간 상태 비관측) + 멱등** 이어야 한다.

## Decision

### 식별 모델: ordinal → *명명(named) shard* 일반화 (장기 정답)

근본 원인은 cluster 가 shard 를 *ordinal(0,1,2…)* 로만 식별하는 것이다. 그러나 ShardRange(라우팅
SSOT)는 *이름* 으로 shard 를 가리키며(Vitess/Citus 도 keyrange/named shard 모델), resharding 은
ordinal 이 아닌 이름(t0/t1)을 만든다. 두 모델의 충돌이 승격을 어렵게 한다.

**결정**: shard 식별을 *명명 shard* 로 일반화하고, ordinal 은 명명 shard 의 한 특수 형태(이름이
`shard-<N>`)로 흡수한다. 구체적으로:

1. **통합 식별 label `postgres.keiailab.io/shard-id=<name>`** 도입. 기존 ordinal shard 는
   `shard-id=shard-<N>` (+ 하위호환 위해 `postgres.keiailab.io/shard=<N>` 병행 한시 유지),
   resharding target 은 승격 시 `reshard-target=<id>` → `shard-id=<id>` 로 *재부여*.
   `aggregateShardStatus`/`metrics`/failover 의 selector 를 `shard-id` 기반으로 일반화한다.
2. **승격 = label 재부여 + cluster status 편입 + source 폐기**, 단일 operator reconcile 트랜잭션
   경계 안에서 fenced 수행(아래 §메커니즘).
3. **ordinal 재명명 안 함**: target 은 `rsd-<id>` 이름(K8s 자원)·`<id>`(논리 shard)을 *영구 유지*
   한다. ShardRange→backend resolver 가 이미 이름 기반이므로 라우팅 무변경. ordinal 로 rename
   하면 ShardRange 이름과 자원명이 어긋나 라우팅이 깨진다 → rename 금지.

### 승격 메커니즘 (fenced, single-authority, 멱등)

ShardSplitJob 에 **Promote phase**(또는 Cleanup 후 별 phase)를 추가, operator 가 다음을 *순서대로*
수행하며 각 단계는 멱등(재진입 안전):

1. **precondition gate**: RoutingUpdate 완료(ShardRange flip 확정) + 각 target pod Ready +
   CDC/복사 Job 완료 확인. 하나라도 미충족 → requeue(전이 보류). 중간 상태에서 승격 시작 금지.
2. **fence**: 승격 대상 target 들에 `shard-id` label 을 *원자적으로* 부여하기 전, source ordinal
   shard 를 먼저 *관측에서 제외*(source STS 를 scale 0 또는 `shard-id` label 제거)해 "ordinal
   shard-0 이 primary"라는 stale 관측을 끊는다. 이 시점 source 는 이미 비어 있음(Cleanup 완료).
3. **adopt**: 각 target STS/pod 에 `shard-id=<id>` label 부여(`reshard-target` label 은 보존 또는
   제거). 이 한 번의 label 전이가 "두 namespace 가 만나는 유일 지점"(ADR-0027) — operator 만
   수행, 외부 컨트롤러/사용자 개입 없음(single-authority).
4. **status 편입**: PostgresCluster.status.shards 를 새 명명 shard 집합으로 재계산(aggregate 가
   `shard-id` 로 select 하므로 자동) + spec 의 shard 토폴로지를 ShardRange 와 정합화(spec.shards
   가 ordinal count 모델이면, 명명 shard 목록 모델로 확장 필요 — 별 변경).
5. **decommission source**: 비워진 source ordinal shard 의 STS/Svc/PVC 회수(가역성 종료 지점 —
   AllowForwardOnly 의미와 정합).
6. **Completed**.

전이가 reconcile 한 번에 안 끝나면(pod 재시작 등) 각 단계 멱등 재진입으로 수렴. aggregateShardStatus
는 전이 *완료 후* 의 label 만 관측(fence 덕에 중간 상태 비관측) → primary 0/2개 오판 회피.

## Consequences

### 긍정
- resharding 산출 shard 가 1급 시민(HA·관측·failover 대상)이 됨 — 운영 누락 해소.
- 명명 shard 일반화는 Vitess/Citus 정합 + 향후 임의 토폴로지(merge, 다단 split)의 토대.
- ordinal rename 회피 → 라우팅(ShardRange↔resolver) 무변경, blast radius 축소.

### 부정 / 위험
- **selector 일반화(`shard-id`)가 aggregate_status/metrics/failover/names + 테스트 다수 파일을
  건드림** — ADR-0027 이 격리 결정에서 Rejected 했던 그 blast radius. 승격에선 불가피하나, 하위호환
  병행 label + phase 별 증분 + 라이브 chaos 검증으로 위험 관리.
- **fence 단계가 정합 핵심** — source 관측 제외와 target adopt 사이 race 가 있으면 #220 재현. 단일
  reconcile authority + 멱등 + 라이브 chaos drill(승격 중 pod kill) 의무.
- spec 의 shard 모델(ordinal count → 명명 목록) 확장은 CRD 변경(마이그레이션 고려).

### 검증 의무
- envtest: Promote phase 가 target StatefulSet/template/live Pod 에 `shard-id` 를 부여하고 selector 는
  `reshard-target` 으로 유지하는지 단언.
- 잔여 envtest: source label 제거 또는 source STS/PDB/PVC 회수 정책이 결정되면 순서·멱등 단언 추가.
- 라이브 chaos: 승격 진행 중 operator/target pod kill → 재진입 수렴 + primary 단일성 유지 확인.

## Alternatives Considered

- **ordinal 재명명(rsd-t0 → shard-1)**: **Rejected** — ShardRange 가 이름으로 라우팅하므로 rename
  시 ShardRange 이름과 자원명 불일치 → 라우팅 붕괴. ShardRange 도 동시 rename 하면 atomic 보장
  난해 + 라우터 hot-reload 윈도우에 라우팅 공백.
- **승격 안 함(target 을 영구 격리 유지)**: **Rejected** — resharding 산출 shard 가 HA/관측에서
  영구 누락 = 운영 불가. resharding 의 목적(영구 토폴로지 변경) 미달.
- **별도 PostgresCluster 로 승격**: **Rejected** (ADR-0027 과 동일) — cluster lifecycle/router 중복.

## 구현 순서 (별 PR, 각 mergeable)

1. **P-A**: `shard-id` 통합 label 도입 + aggregate_status/metrics/failover selector 일반화(ordinal
   `shard` label 하위호환 병행). envtest 회귀(기존 ordinal cluster 무영향).
2. **P-B**: ShardSplitJob Promote phase — fence + adopt + status 편입 + source decommission. P-B.2 target
   adopt slice 는 구현됨. source decommission 과 live chaos 는 잔여.
3. **P-C**: spec shard 모델 확장(명명 목록) + 마이그레이션. 라이브 chaos drill.

> 본 ADR 은 *설계 결정* 이며 구현은 위 P-A~P-C 로 분할한다. ADR-0027 P6 의 "신중한 ground-up
> 설계" 요구를 충족한다(standards/principles.md §1 Think Before Coding).
