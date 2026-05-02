# RFC-0003: ShardSplitJob — 7-step online resharding workflow

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P4 (~v0.7.0)
- Supersedes: 없음 (신규)

## §1 Summary

`ShardSplitJob` CRD 를 도입하고, **PostgreSQL logical replication 기반 7-step online resharding workflow** 를 정의한다. 단계: **Provisioning → Streaming → CatchingUp → Diffing → Cutover → Cleanup → Done**. Cutover 단계에서 router 가 source shard 의 write 를 fence (수ms ~ 수백ms) 한 후 final lag drain → ShardRange atomic 갱신 → fence 해제 순서로 진행한다. 데이터 손실 0, cutover 동안 read 가용성 유지, P99 cutover 시간 < 500ms 가 목표. 실패 시 cutover 이전은 rollback, 이후는 forward-only 정책.

## §2 Motivation

### §2.1 문제

분산 DB 의 핵심 가치는 *온라인 무중단 재분할* 능력이다. 그러나 hidden complexity 가 매우 높다:

- **데이터 일관성**: source 와 target shard 가 *완전 일치* 한 시점에 cutover.
- **트랜잭션 경계**: 진행 중 transaction 의 atomicity 보장.
- **Sequence sync**: SERIAL / IDENTITY 컬럼 시퀀스가 target 에서 jump 없이 이어져야.
- **Materialized view**: source 에 의존하는 mview 무효화/재생성.
- **Prepared statement plan cache**: client 의 plan 이 stale shard 를 가리킬 수 있음.
- **Cutover SLA**: write 차단 시간이 application timeout (보통 5~10s) 을 절대 초과 X.

### §2.2 사용자 시나리오

**시나리오 1: 수동 split (P4 GA)**
운영자가 모니터링에서 shard-a (200GB, p99 300ms) 과부하 감지:
```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardSplitJob
metadata: { name: split-shard-a-20260502, namespace: prod }
spec:
  cluster: foo
  sourceShard: shard-a
  splitPoint: "0x20000000"          # hash range 중간점
  newShard: shard-a-1
```
operator 가 7 단계 자동 진행, 30 분 ~ 수 시간 후 `phase: Done`. application 은 무중단.

**시나리오 2: 자동 split (P5)**
KEDA 가 `autoSplit.triggers` 충족 감지 → operator 가 ShardSplitJob 자동 생성. `requireApproval: true` 시 `phase: Pending` 에서 멈춤, 사용자가 `kubectl annotate ssj/... approval=yes` 로 재개.

### §2.3 비목표

- shard merge (역방향) — P6+ 별도 RFC.
- non-hash vindex 의 split — P5 range/consistent-hash 확장 후 별도 작업.
- cross-cluster migration — P7+.

## §3 Design / Specification

### §3.1 spec / status

```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardSplitJob
metadata: { name: split-shard-a-20260502, namespace: prod }
spec:
  cluster: foo                       # required
  sourceShard: shard-a                # required
  splitPoint: "0x20000000"            # required, vindex 의존 표현
  newShard: shard-a-1                  # required, 신규 shard 이름
  # 선택
  parallelism: 4                       # logical decoding worker 수
  diffSampleRate: 0.01                 # diff 단계 sampling (1%)
  cutoverTimeoutSeconds: 30
  approval: pending | approved         # autoSplit 승인 게이트 (P5+)
status:
  phase: Provisioning | Streaming | CatchingUp | Diffing | Cutover | Cleanup | Done | Failed | Pending
  startedAt: "2026-05-02T10:00:00Z"
  completedAt: null
  progressPercent: 73
  metrics:
    bytesStreamed: 142000000000
    bytesTotal: 200000000000
    lagMs: 45
    rowsDiffed: 12450000
    rowsMismatch: 0
  cutover:
    fenceStartedAt: null
    fenceEndedAt: null
    durationMs: 0
  conditions:
    - type: Healthy
      status: "True"
    - type: Cutover
      status: "False"
      reason: NotYet
  failureReason: ""
  failurePhase: ""
```

### §3.2 7 단계 상세 알고리즘

#### Phase 1: Provisioning

```
1. operator 가 신규 shard StatefulSet (`<cluster>-<newShard>`) 생성
2. PVC bound, pod ready 대기 (timeout 10min)
3. instance manager 기동, primary election 완료 (RFC 0003 election 사용)
4. 초기 schema 복제: pg_dump --schema-only source | pg_restore target
5. status.phase: Provisioning → Streaming
```

idempotent: 재진입 시 기존 StatefulSet 재사용. 실패 시 rollback (target shard 삭제) 가능.

#### Phase 2: Streaming

PG **native logical decoding** (PG 16+) 또는 `pglogical` extension 활용. 본 RFC 는 native logical decoding 채택 (extension 의존 0):

```sql
-- source shard
SELECT pg_create_logical_replication_slot('split_<newShard>', 'pgoutput');
CREATE PUBLICATION split_<newShard>_pub FOR TABLE <분산테이블들>
  WHERE (vindex_hash(distribution_column) >= 0x20000000
         AND vindex_hash(distribution_column) < 0x40000000);

-- target shard
CREATE SUBSCRIPTION split_<newShard>_sub
  CONNECTION 'host=<source> dbname=foo user=replicator'
  PUBLICATION split_<newShard>_pub
  WITH (copy_data = true, create_slot = false, slot_name = 'split_<newShard>');
```

⚠ PG row-filter 는 `WHERE` expression 만 지원. 분산 column 의 hash 값을 expression 으로 표현하기 위해 **source shard 에 stored generated column** `_vindex_hash` 를 미리 추가하는 것이 권장 패턴 (스키마 마이그레이션 P3 단계에서 자동 도입).

```
- copy_data: 초기 스냅샷 + 이후 streaming
- operator 가 `pg_stat_subscription` polling 으로 진행률 갱신
- bytesStreamed / bytesTotal 비율을 status.progressPercent 에 반영
```

#### Phase 3: CatchingUp

```
- 초기 copy 완료 후 streaming-only 모드
- 매 1s polling: lag = source.LSN - subscription.received_lsn
- lag < 100ms 가 30s 연속 유지되면 phase 전환
- timeout (default 1h) 초과 시 Failed
```

#### Phase 4: Diffing

```
- 분산 테이블 별로 row count 비교 (source 의 split 범위 vs target 전체)
- spec.diffSampleRate (default 1%) 만큼 row sample 해 SHA256 비교
- mismatch 발생 시 logical replication apply lag 증가 가능성 → 잠시 대기 후 재시도 (3회)
- 최종 mismatch 0 → Cutover 진입, 아니면 Failed
```

```sql
-- diff 쿼리 (source / target 각각 실행)
SELECT count(*), md5(string_agg(md5(t.*::text), ',' ORDER BY id))
FROM <table> t
WHERE vindex_hash(distribution_column) >= 0x20000000
  AND vindex_hash(distribution_column) < 0x40000000
  AND id % 100 = 0;   -- 1% sampling
```

#### Phase 5: Cutover (가장 critical)

목표: write 차단 시간 < 500ms (P99).

```
1. cutover.fenceStartedAt = now()
2. operator → 모든 router pod 에 gRPC fence 신호:
     FenceShardWrites(cluster=foo, shard=shard-a, range=[0x20000000, 0x40000000))
   router 는 해당 범위 write 를 즉시 50000 (custom error code) 반환,
   read 는 그대로 source 에 forwarding.
3. final lag drain: lag == 0 도달 대기 (max 5s)
   - source 의 마지막 commit LSN 확인 → target 이 그 LSN 까지 apply 대기
4. ShardRange CRD atomic update:
   ranges:
     - { lo: 0x00000000, hi: 0x1FFFFFFF, shard: shard-a }
     - { lo: 0x20000000, hi: 0x3FFFFFFF, shard: shard-a-1 }   # 신규
     - ...
   server-side apply + optimistic lock
5. router 들이 watch event 받아 routing table 갱신 (~50ms)
6. operator → 모든 router 에 unfence 신호
7. cutover.fenceEndedAt = now()
   cutover.durationMs = fenceEndedAt - fenceStartedAt
```

**fence 메커니즘**:
- router 는 fence 상태를 in-memory map (`map[range]fenced`) 으로 관리.
- gRPC 호출은 모든 router replica 에 broadcast (operator 가 EndpointSlice 로 enumerate).
- router 한 대 응답 실패 시 cutover abort (전체 rollback).

**Hidden complexity 처리**:

| 이슈 | 처리 |
|---|---|
| **Sequence sync** | source 의 `setval(seq, last_value)` 결과를 target 에 직접 적용 (cutover 직후) |
| **Materialized view** | source 의 mview 정의를 target 에 복제 + REFRESH (Diffing 단계) |
| **Prepared statement plan cache** | router 가 routing table reload 시 client 별 plan cache 무효화 (PG ProtocolVersion 3.0 의 Close + Parse 재발행) |
| **활성 transaction** | fence 시점 source shard 의 in-flight tx 는 ABORT (source 에 `pg_terminate_backend` for affected ranges). client 는 retry 로 새 shard 에 연결 |

#### Phase 6: Cleanup

```
1. source shard 의 split 범위 row 삭제:
   DELETE FROM <table>
   WHERE vindex_hash(distribution_column) >= 0x20000000
     AND vindex_hash(distribution_column) < 0x40000000;
   (parallelism 분할, throttled DELETE — wal pressure 회피)
2. logical replication slot + publication + subscription 제거
3. ShardRange status.generation++ 확인 (모든 router 가 동기화됨)
4. operator 가 배포한 임시 리소스 (gen column 등) 정리
```

#### Phase 7: Done

```
- status.phase = Done
- completedAt 기록
- PostgresCluster.status.shards[] 갱신 (신규 shard 등재)
- audit log: split 결과를 incident-kb (성공도 기록 권장)
```

### §3.3 실패 / Rollback 정책

| 단계 | 실패 시 | 회복 |
|---|---|---|
| Provisioning | target shard pod 미생성 | StatefulSet 삭제 → ShardSplitJob 삭제 후 재시도 |
| Streaming | replication 단절 | slot 보존 시 자동 재개. slot 손상 시 Provisioning 부터 재시작 |
| CatchingUp | lag 무한 증가 | source write 부하 줄이거나 target 자원 증설 후 재시도 |
| Diffing | mismatch 발생 | 3회 재시도 → 실패 시 Failed (운영자 수동 조사) |
| **Cutover** | router fence 응답 실패 | 즉시 abort, fence 해제, source 정상화 (~수백ms 영향) |
| Cleanup | DELETE 실패 | source 에 잔존 row 는 *논리적으로 unreachable* (router 가 새 shard 로 보냄) → forward-only, 백그라운드 재시도 |
| Done | — | — |

**Cutover 이후는 forward-only**: ShardRange 가 갱신되어 router 가 새 routing 사용 중. rollback 하려면 *역방향 split* (P6+ merge 기능) 필요.

### §3.4 CRD validation

```go
type ShardSplitJobSpec struct {
    // +kubebuilder:validation:Required
    Cluster string `json:"cluster"`
    // +kubebuilder:validation:Required
    SourceShard string `json:"sourceShard"`
    // +kubebuilder:validation:Required
    SplitPoint string `json:"splitPoint"`
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Pattern=`^[a-z0-9-]{1,63}$`
    NewShard string `json:"newShard"`

    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=16
    // +kubebuilder:default=4
    Parallelism int32 `json:"parallelism,omitempty"`

    // +kubebuilder:validation:Minimum=0.001
    // +kubebuilder:validation:Maximum=1.0
    // +kubebuilder:default=0.01
    DiffSampleRate float64 `json:"diffSampleRate,omitempty"`

    // +kubebuilder:validation:Minimum=5
    // +kubebuilder:validation:Maximum=300
    // +kubebuilder:default=30
    CutoverTimeoutSeconds int32 `json:"cutoverTimeoutSeconds,omitempty"`

    // +kubebuilder:validation:Enum=pending;approved
    Approval string `json:"approval,omitempty"`
}
```

### §3.5 Cutover SLA 측정

router 의 prometheus metric:
```
postgresql_router_shard_fence_duration_seconds{cluster, shard} histogram
postgresql_router_shard_fence_writes_rejected_total{cluster, shard} counter
```

operator 의 e2e 검증:
```bash
make test-e2e PILLAR=p4 -- --focus="cutover SLA"
# 100 회 split 시뮬레이션 후 P99 fence_duration < 500ms 확인
```

## §4 Drawbacks / Trade-offs

- **logical replication 의존성**: PG 16+ 필수 (PG 18 에서 row-filter + binary protocol 안정). PG 15 이하 환경은 미지원 → CRD validation 으로 차단.
- **stored generated column 추가 부담**: 분산 테이블에 `_vindex_hash` column 추가 → 약간의 storage / write overhead. 운영 영향 < 5% (벤치 검증 필요).
- **diff 단계 false positive**: 1% sampling 으로 3회 재시도 했음에도 mismatch 발생 가능 (간헐적 replication lag). 완화: sampling rate 동적 증가 + admin escalation alert.
- **cutover 동안 write 거부**: `cutoverTimeoutSeconds` 초과 시 client retry 폭주 가능. 완화: client 가이드 (exponential backoff + jitter).

## §5 Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **pg_dump + pg_restore (offline)** | 다운타임 발생, 대규모 shard 에서 시간 소요 큼 |
| **trigger-based replication (Slony, Bucardo)** | 외부 의존 (BSD/Apache 호환이지만 운영 복잡), PG native 가 우수 |
| **physical replication + range filter** | physical replication 은 row filter 불가 (전체 복제 후 DELETE 필요) |
| **shadow write (dual-write from app)** | application 변경 필요, 본 operator 의 추상화 위반 |
| **Citus `citus_split_shard_by_split_points`** | Citus 의존 (AGPL, 폐기 결정) |

## §6 Open Questions

1. PG 16 의 logical replication 의 DDL replication 부재 → split 진행 중 ALTER TABLE 발생 시 깨짐. 운영 권고: split 진행 중 schema migration 차단 (admission webhook). 자동화 가능한가?
2. `cutover.durationMs` P99 < 500ms 가 모든 cluster 크기에서 달성 가능한가? 1024-shard 클러스터의 router fence broadcast 가 bottleneck 될 가능성 → 벤치 후 P5 단계에서 evaluation.
3. ShardSplitJob 의 history 보관 정책 (Done 후 N 일) — TTL controller 이용? `ttlSecondsAfterFinished: 604800` (7 일) 권장.

## §7 Implementation Plan

### P3 (~v0.6.0) 사전 작업

- [ ] 분산 테이블에 `_vindex_hash` stored generated column 자동 추가하는 schema migration (별도 RFC/ADR).
- [ ] router 의 fence API gRPC interface 정의 (`internal/router/fence.proto`).

### P4 (~v0.7.0) 본 RFC 구현

- [ ] `api/v1alpha1/shardsplitjob_types.go` (kubebuilder marker).
- [ ] `internal/controller/resharder/controller.go` 7-phase state machine.
- [ ] `internal/controller/resharder/phases/` 각 단계 1 파일 (provisioning.go, streaming.go, ...).
- [ ] router 의 fence 처리 (`internal/router/fence.go`).
- [ ] e2e: 4-shard → split 1 → 5-shard, 데이터 정합 + cutover SLA P99 < 500ms.
- [ ] chaos test: streaming 도중 source kill / target kill / network partition.

### P5 (~v0.8.0) 자동화

- [ ] KEDA → ShardSplitJob 자동 생성 (`approval: pending` 상태로).
- [ ] approval annotation gate (`kubectl annotate ssj/... approval=approved`).

### 검증 명령

```bash
go test ./internal/controller/resharder/...        # 단위 (state machine)
go test ./internal/router/fence/...                # fence 단위
make test-e2e PILLAR=p4 -- --focus="ShardSplitJob"
make test-chaos PILLAR=p4                          # chaos-mesh 시나리오
make bench PILLAR=p4                               # cutover SLA 측정
```

성공 기준:
- 데이터 손실 0 (1M row insert during split → diff 후 모두 일치).
- Cutover P99 < 500ms (100 회 측정).
- 실패 시 idempotent retry 로 동일 결과 도달.

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §3.3, §7.2 P4
- PostgreSQL Logical Replication: https://www.postgresql.org/docs/18/logical-replication.html
- pg_create_logical_replication_slot: https://www.postgresql.org/docs/18/functions-replication.html
- Vitess VReplication (참조 only): https://vitess.io/docs/reference/vreplication/
- Citus split_shard (참조 only, 코드 차용 0): https://docs.citusdata.com/en/stable/develop/api_udf.html
- RFC 0001: PostgresCluster CRD v2
- RFC 0002: ShardRange CRD
- RFC 0004: pg-router architecture
- ADR 0003: License policy (AGPL/BUSL 금지)
