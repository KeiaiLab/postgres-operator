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
단, 이것은 lifecycle 승격 전체가 아니다. source ordinal resource decommission, target replica scale-up/HA,
spec shard model 의 named-list 전환, Promote phase idempotency 는 아직 P-B/P-C 잔여 범위다.

이번 hardening batch 에서 selector 사용처를 다음처럼 분리했다.

- **그대로 둔 것**: `ShardStatefulSetName`, `ShardServiceName`, PDB/TLS/PVC resize, source shard DNS,
  bootstrap loop 는 여전히 ordinal resource naming 이다. 기존 StatefulSet/Service selector 불변성과
  업그레이드 호환성 때문에 Promote P-B 전에는 바꾸지 않는다.
- **변경한 것**: `aggregateShardStatus` 는 더 이상 `postgres.keiailab.io/shard=<ord>` selector 만으로
  pod 를 고르지 않는다. cluster 공통 label 로 넓게 list 한 뒤 코드에서
  `postgres.keiailab.io/shard=<ord>` 또는 `postgres.keiailab.io/shard-id=shard-<ord>` 를 OR 필터링한다.
- **metrics/failover**: 직접 pod selector 를 갖지 않고 `PostgresCluster.status.shards` 를 소비한다.
  따라서 aggregation 이 `shard-id` 를 이해하면 metrics/failover 는 status 경유로 따라온다.
- **아직 하지 않은 것**: `shard-id=t1` 같은 named target 을 status.shards 의 새 row 로 생성하지는 않았다.
  현재 `PostgresClusterReconciler` 는 `spec.shards.initialCount` ordinal loop 를 유지한다. named shard
  row 생성은 P-B/P-C 의 ShardRange/spec 모델 전환 범위다.
- **P-B 주의점**: target 에 `shard-id` 를 붙이기 전에 source ordinal shard 를 fence/관측 제외해야 한다.
  source 와 target 이 같은 `shard-id` 로 동시에 보이면 aggregation 은 split-brain 으로 보고 primary 2개
  상황을 노출한다. 이는 의도적인 안전 신호이며, Promote phase 는 이 중간 상태를 만들지 않아야 한다.

## Context

2026-06-28 기준 online resharding 의 데이터 경로가 *전부* 결선·라이브 검증되었다 (offline + online
무중단 CDC, 스키마/인덱스/PK/제약 복제, write-block, full e2e — `WORK_HANDOFF.ko.md §6.6`).
ShardSplitJob 7-phase 중 Bootstrap→InitialCopy/CDCCatchup→Cutover→RoutingUpdate→Cleanup→Completed
가 실 K8s+PG 에서 동작한다.

**그러나 ADR-0027 의 P6(승격)는 미구현이다.** 현재 resharding 완료 후 상태:

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
- envtest: Promote phase 가 target 에 `shard-id` 부여 + source label 제거 + status.shards 재계산 +
  source STS 회수 순서·멱등 단언.
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
2. **P-B**: ShardSplitJob Promote phase — fence + adopt + status 편입 + source decommission. envtest.
3. **P-C**: spec shard 모델 확장(명명 목록) + 마이그레이션. 라이브 chaos drill.

> 본 ADR 은 *설계 결정* 이며 구현은 위 P-A~P-C 로 분할한다. ADR-0027 P6 의 "신중한 ground-up
> 설계" 요구를 충족한다(standards/principles.md §1 Think Before Coding).
