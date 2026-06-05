# ADR-0027: G3 online-resharding 의 비-ordinal target shard 식별 모델

- **Date**: 2026-06-05
- **Status**: Proposed
- **Authors**: @phil

## Context

G3 online-resharding 의 `ShardSplitJob` reconciler 는 *phase 전이 골격* 이다 (2026-06-05 코드 검증). `internal/controller/shardsplitjob_controller.go` 의 7-phase state machine 은 `nextPhase` 전이 + `ValidateSplitPlan`(#213) gate + `applyRouting`(#217) 만 실 동작하고, **나머지 phase 는 side-effect 가 없다**:

- `Bootstrap`: target shard StatefulSet 미생성 (즉시 InitialCopy 로 전이).
- `InitialCopy`: `router.CopyTable`(#215, `internal/router/reshard_copy.go`) 미호출.
- `CDCCatchup` / `Cutover`: pass-through.

각 phase 의 side-effect 를 구현하려면 *target shard 를 실제 K8s 자원으로 생성* 해야 하는데, 여기서 **식별 모델 충돌** 이 발생한다:

- 라이브 cluster 의 shard 식별은 *ordinal 에 깊게 결합* 되어 있다:
  - `names.go:ShardStatefulSetName(cluster string, ordinal int32) = "<cluster>-shard-<ord>"`
  - `SelectorLabels(cluster, "shard", ordinal)` → pod/STS 에 `postgres.keiailab.io/shard=<ordinal>` label 부여.
  - `aggregate_status.go` / `metrics.go` 가 이 ordinal `shard` label 로 pod 를 select 해 ShardStatus / failover 결정을 합성.
- 반면 `ShardSplitJob.Spec.Targets[].ShardID` 는 *문자열* ("shard-0a", "shard-0b") — ordinal 모델에 맞지 않는다.

`buildPGStatefulSet`(builders.go:1005) coupling 분석: `name` / `serviceName` 은 *string 파라미터* 라 비-ordinal 이름 주입이 가능하다. 유일한 ordinal 결합점은 `shardOrdinal int32` → `SelectorLabels` → `postgres.keiailab.io/shard` label *하나* 다.

**가장 중요한 제약 — #220 교훈**: 직전 #220 failover saga (2026-06-05 merged `0cdbfc6`) 는 *shard-identity 버그가 미묘하고 위험* 함을 증명했다. 8+ surgical fix 가 각각 한 데이터 손실 경로를 닫으면 다음이 열렸고 (mechanism shift), 근본 원인은 3개 컴포넌트(bootstrap-init / election / operator promotion)가 standby-vs-primary 결정을 *stale/racy 식별 입력* 으로 수행한 것이었다. 결론: **shard-identity 변경은 incremental hack 이 아니라 신중한 ground-up 설계가 필요** (systematic-debugging Phase 4.5).

따라서 target shard 식별 모델은 *구현 전 설계 결정* 이 필요하다 (`standards/adr.md §2`: 데이터 모델 변경 = ADR MUST, `standards/principles.md §1` Think Before Coding).

## Decision

resharding target shard 는 라이브 cluster 의 ordinal shard 모델과 **격리된 식별 namespace** 를 사용한다 — #220-class identity 혼동을 *구조적으로* 차단:

1. **격리 label**: target shard 는 `postgres.keiailab.io/reshard-target=<shardID>` label 을 갖는다 (ordinal `postgres.keiailab.io/shard` label *재사용 금지*). `aggregateShardStatus` / `metrics` 는 ordinal `shard` label 로만 select 하므로 *transient target 에 blind* → resharding 중 failover/status 간섭 0.
2. **격리 naming**: `TargetShardStatefulSetName(cluster, shardID) = "<cluster>-rsd-<shardID>"` (rsd = resharding) — `<cluster>-shard-<ord>` 와 분리되어 collision 불가.
3. **phase 별 mergeable 증분** (#216/#217 phase-merge 패턴 정합 — 각 phase 가 독립 PR):
   - **P1**: 비-ordinal naming + 격리-label helper + unit test. (P2 에서 즉시 호출되므로 orphan 아님.)
   - **P2**: `Bootstrap` phase 가 target shard 의 StatefulSet + headless Service + ConfigMap 을 격리 식별로 생성. fake-client unit test (Bootstrap 후 N target STS 존재 단언). 가역(rollback=target STS delete). **선행 prerequisite (P1 실측 발견 2026-06-05)**: `ShardSplitJob.Spec.Targets[].ShardID` 에 *현재 CRD pattern 부재* + 형제 패턴 `^[a-z][a-z0-9_]{0,62}$` 는 언더스코어 허용 = DNS-1123 무효. P2 가 shardID 를 K8s 자원명(`<cluster>-rsd-<shardID>`)에 직접 박으므로, P2 진입 전 ShardID 에 DNS-safe pattern(`^[a-z][a-z0-9-]{0,N}$`, 하이픈) 추가 + `make manifests` regen 의무.
   - **P3**: `InitialCopy` phase 가 source shard primary endpoint + cluster postgres Secret → sourceDSN/targetDSN 구성 → table 별 `CopyTable`(#215) 호출. 가역(rollback=target drop).
   - **P4**: `CDCCatchup` phase 가 source→target logical replication(publication/subscription) + lag < `CDCMaxLag`(기본 16MB) 대기.
   - **P5**: `Cutover` phase 가 `CutoverWindow` 내 write-block + 최종 sync → `RoutingUpdate`(#217, 이미 merged). `AllowForwardOnly=false` 만 자동(역방향 replication rollback).
   - **P6**: `Cleanup` phase 가 target shard 를 1급 ordinal shard 로 승격 (`reshard-target` label → `shard` ordinal label 재부여). **두 namespace 가 만나는 유일한 identity-transition 지점** — operator-driven + fenced + single-authority 로 설계해 #220-class race 회피. §6 L3 snapshot 안전망 의무.

## Consequences

### 긍정
- target shard 격리 → resharding 중 라이브 failover/status 간섭 0. #220 risk class 가 *구조적으로* 회피됨 (transient target 이 ordinal shard 모델에 절대 진입하지 않음).
- 각 phase(P1~P6)가 독립 mergeable 증분 → #216/#217 의 검증된 incremental 패턴 재사용. multi-session 작업이 tractable.
- `buildPGStatefulSet` 의 string `name`/`serviceName` 파라미터 재사용 → STS builder 대규모 rewrite 불요.

### 부정
- shard-identity namespace 가 2개(ordinal + reshard-target)로 늘어 복잡도 증가.
- **P6 re-label transition 이 가장 신중을 요하는 지점** — 두 namespace 가 만나며, 잘못하면 #220-class identity 혼동 재현. operator-driven single-authority 설계 + 라이브 chaos 검증 의무.
- P1~P6 는 multi-session 에 걸침 — 본 ADR 이 spec 으로 각 phase 를 tractable 하게 만든다.

## Alternatives Considered

- **Synthetic 고-ordinal** (shard-0a → ordinal 1000): **Rejected** — ordinal `shard` label 을 재사용하므로 `aggregateShardStatus`/metrics/failover 가 transient target 을 *라이브 shard 로 오인* → 정확히 #220-class identity 혼동. 고위험.
- **`SelectorLabels` 를 전역에서 string shard ID 로 확장**: **Rejected (현재)** — names/builders/aggregate_status/metrics/controller + 테스트 6파일을 *한 변경* 으로 건드려 blast radius 가 크고 incremental 검증이 어렵다. 격리-label 접근이 더 surgical (§3).
- **target 을 별도 PostgresCluster 로 생성 후 병합**: **Rejected** — cluster 단위 lifecycle/router/SSO 중복 + 병합 시점 데이터 정합 복잡도가 격리-label 접근보다 큼.

## Refs

- #223 (G3 ShardSplitJob phase side-effects 추적 이슈 — 본 ADR 이 그 설계)
- #213 `ValidateSplitPlan` / #215 `CopyTable` / #216 phase machine / #217 `RoutingUpdate` (재사용 building block)
- #220 (failover saga — shard-identity 신중 설계 교훈, merged `0cdbfc6`)
- `standards/adr.md §2` (데이터 모델 변경 ADR MUST) + `standards/principles.md §1` (Think Before Coding)
