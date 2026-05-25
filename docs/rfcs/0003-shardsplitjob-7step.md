# RFC-0003: ShardSplitJob — 7-step online resharding workflow

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P4 (~v0.7.0)
- Supersedes: none (new)

## §1 Summary

Introduce the `ShardSplitJob` CRD and define a **7-step online resharding workflow based on PostgreSQL logical replication**. Stages: **Provisioning → Streaming → CatchingUp → Diffing → Cutover → Cleanup → Done**. In the Cutover stage, the router fences writes for the source shard (a few ms to a few hundred ms), then drains final lag → atomically updates the ShardRange → unfences in that order. Goals: zero data loss, read availability maintained during cutover, P99 cutover time < 500ms. On failure: rollback before cutover, forward-only afterward.

## §2 Motivation

### §2.1 Problem

The core value of a distributed DB is the ability to *re-partition online without downtime*. The hidden complexity is very high:

- **Data consistency**: cutover at the moment when source and target shard are *fully aligned*.
- **Transaction boundary**: atomicity of in-flight transactions.
- **Sequence sync**: SERIAL / IDENTITY column sequences must continue without a jump on the target.
- **Materialized views**: invalidate / regenerate mviews that depend on the source.
- **Prepared statement plan cache**: client plans may point at a stale shard.
- **Cutover SLA**: the write-blocked window must never exceed the application timeout (typically 5~10s).

### §2.2 User scenarios

**Scenario 1: manual split (P4 GA)**
An operator notices from monitoring that shard-a is overloaded (200GB, p99 300ms):
```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardSplitJob
metadata: { name: split-shard-a-20260502, namespace: prod }
spec:
  cluster: foo
  sourceShard: shard-a
  splitPoint: "0x20000000"          # midpoint of the hash range
  newShard: shard-a-1
```
The operator runs the 7 stages automatically; after 30 minutes to several hours, `phase: Done`. The application has zero downtime.

**Scenario 2: auto split (P5)**
KEDA observes that `autoSplit.triggers` are satisfied → the operator auto-creates a ShardSplitJob. When `requireApproval: true`, it stops at `phase: Pending`, and the user resumes it with `kubectl annotate ssj/... approval=yes`.

### §2.3 Non-goals

- Shard merge (reverse direction) — separate RFC in P6+.
- Splitting a non-hash vindex — separate work after the P5 range/consistent-hash extension.
- Cross-cluster migration — P7+.

## §3 Design / Specification

### §3.1 spec / status

```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardSplitJob
metadata: { name: split-shard-a-20260502, namespace: prod }
spec:
  cluster: foo                       # required
  sourceShard: shard-a                # required
  splitPoint: "0x20000000"            # required, vindex-dependent representation
  newShard: shard-a-1                  # required, new shard name
  # optional
  parallelism: 4                       # number of logical decoding workers
  diffSampleRate: 0.01                 # sampling rate at the diff stage (1%)
  cutoverTimeoutSeconds: 30
  approval: pending | approved         # approval gate for autoSplit (P5+)
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

### §3.2 7-stage detailed algorithm

#### Phase 1: Provisioning

```
1. The operator creates the new shard's StatefulSet (`<cluster>-<newShard>`)
2. Wait for PVC bound, pod ready (timeout 10min)
3. instance manager starts, primary election completes (use RFC 0003 election)
4. Initial schema replication: pg_dump --schema-only source | pg_restore target
5. status.phase: Provisioning → Streaming
```

Idempotent: on re-entry, reuse the existing StatefulSet. On failure, rollback (delete the target shard) is possible.

#### Phase 2: Streaming

Use PG **native logical decoding** (PG 16+) or the `pglogical` extension. This RFC adopts native logical decoding (zero extension dependency):

```sql
-- source shard
SELECT pg_create_logical_replication_slot('split_<newShard>', 'pgoutput');
CREATE PUBLICATION split_<newShard>_pub FOR TABLE <distributed tables>
  WHERE (vindex_hash(distribution_column) >= 0x20000000
         AND vindex_hash(distribution_column) < 0x40000000);

-- target shard
CREATE SUBSCRIPTION split_<newShard>_sub
  CONNECTION 'host=<source> dbname=foo user=replicator'
  PUBLICATION split_<newShard>_pub
  WITH (copy_data = true, create_slot = false, slot_name = 'split_<newShard>');
```

⚠ PG row-filter only supports `WHERE` expressions. To express the hash of the distribution column as an expression, the recommended pattern is to **add a stored generated column** `_vindex_hash` on the source shard ahead of time (auto-introduced during the P3 schema-migration stage).

```
- copy_data: initial snapshot + subsequent streaming
- The operator polls `pg_stat_subscription` to update progress
- Reflects the bytesStreamed / bytesTotal ratio in status.progressPercent
```

#### Phase 3: CatchingUp

```
- After initial copy, streaming-only mode
- Poll every 1s: lag = source.LSN - subscription.received_lsn
- Transition phase when lag < 100ms is sustained for 30s
- Failed on timeout (default 1h)
```

#### Phase 4: Diffing

```
- Per distributed table, compare row counts (the split range on source vs. all of target)
- Sample rows at spec.diffSampleRate (default 1%) and compare SHA256
- On mismatch, possibly due to logical-replication apply lag → wait briefly and retry (3 times)
- Final mismatch 0 → enter Cutover; otherwise Failed
```

```sql
-- diff query (run on source / target each)
SELECT count(*), md5(string_agg(md5(t.*::text), ',' ORDER BY id))
FROM <table> t
WHERE vindex_hash(distribution_column) >= 0x20000000
  AND vindex_hash(distribution_column) < 0x40000000
  AND id % 100 = 0;   -- 1% sampling
```

#### Phase 5: Cutover (most critical)

Target: write-blocked window < 500ms (P99).

```
1. cutover.fenceStartedAt = now()
2. operator → all router pods send gRPC fence signal:
     FenceShardWrites(cluster=foo, shard=shard-a, range=[0x20000000, 0x40000000))
   The router returns 50000 (custom error code) immediately for writes in that range,
   while forwarding reads to the source as-is.
3. final lag drain: wait until lag == 0 (max 5s)
   - confirm source's last commit LSN → wait for target to apply up to that LSN
4. atomic update of ShardRange CRD:
   ranges:
     - { lo: 0x00000000, hi: 0x1FFFFFFF, shard: shard-a }
     - { lo: 0x20000000, hi: 0x3FFFFFFF, shard: shard-a-1 }   # new
     - ...
   server-side apply + optimistic lock
5. routers receive the watch event and refresh their routing tables (~50ms)
6. operator → all routers send unfence signal
7. cutover.fenceEndedAt = now()
   cutover.durationMs = fenceEndedAt - fenceStartedAt
```

**Fence mechanism**:
- The router manages fence state in an in-memory map (`map[range]fenced`).
- The gRPC call is broadcast to all router replicas (the operator enumerates them via EndpointSlice).
- If one router fails to respond, abort the cutover (full rollback).

**Hidden complexity handling**:

| Issue | Handling |
|---|---|
| **Sequence sync** | apply the result of `setval(seq, last_value)` from source directly on target (right after cutover) |
| **Materialized view** | replicate the mview definitions from source to target + REFRESH (during the Diffing stage) |
| **Prepared statement plan cache** | when the router reloads its routing table, invalidate per-client plan caches (re-emit Close + Parse per PG ProtocolVersion 3.0) |
| **In-flight transactions** | at fence time, ABORT in-flight tx on the source shard (`pg_terminate_backend` for the affected ranges). Clients retry and connect to the new shard |

#### Phase 6: Cleanup

```
1. Delete rows in the split range from the source shard:
   DELETE FROM <table>
   WHERE vindex_hash(distribution_column) >= 0x20000000
     AND vindex_hash(distribution_column) < 0x40000000;
   (partition by parallelism, throttled DELETE — avoid wal pressure)
2. Remove the logical replication slot + publication + subscription
3. Verify ShardRange status.generation++ (all routers synchronized)
4. Clean up temporary resources deployed by the operator (gen column, etc.)
```

#### Phase 7: Done

```
- status.phase = Done
- Record completedAt
- Update PostgresCluster.status.shards[] (register the new shard)
- audit log: record the split result in incident-kb (recommended even on success)
```

### §3.3 Failure / rollback policy

| Stage | On failure | Recovery |
|---|---|---|
| Provisioning | target shard pod not created | Delete StatefulSet → delete ShardSplitJob and retry |
| Streaming | replication broke | If slot is preserved, resume automatically. If slot is damaged, restart from Provisioning |
| CatchingUp | lag grows unboundedly | Reduce write load on source or expand target resources, then retry |
| Diffing | mismatch occurs | Retry 3 times → on failure, Failed (manual operator investigation) |
| **Cutover** | router fence response fails | Immediate abort, unfence, restore source (~hundreds of ms impact) |
| Cleanup | DELETE fails | Remaining rows on source are *logically unreachable* (router routes to the new shard) → forward-only, background retry |
| Done | — | — |

**After cutover, forward-only**: ShardRange has been updated and routers are using the new routing. To roll back, you need a *reverse split* (the merge feature, P6+).

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

### §3.5 Cutover SLA measurement

Router prometheus metrics:
```
postgresql_router_shard_fence_duration_seconds{cluster, shard} histogram
postgresql_router_shard_fence_writes_rejected_total{cluster, shard} counter
```

Operator e2e verification:
```bash
make test-e2e PILLAR=p4 -- --focus="cutover SLA"
# Simulate 100 splits, verify P99 fence_duration < 500ms
```

## §4 Drawbacks / Trade-offs

- **Logical-replication dependency**: PG 16+ required (PG 18 stabilizes row-filter + binary protocol). PG 15 and below are unsupported → blocked at CRD validation.
- **Stored generated column overhead**: adding `_vindex_hash` columns to distributed tables → some storage / write overhead. Operational impact < 5% (benchmark verification required).
- **False positives in the diff stage**: even with 3 retries and 1% sampling, mismatches can occur (intermittent replication lag). Mitigation: dynamically increase sampling rate + admin escalation alert.
- **Writes rejected during cutover**: if `cutoverTimeoutSeconds` is exceeded, client retry storms may occur. Mitigation: client guidance (exponential backoff + jitter).

## §5 Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| **pg_dump + pg_restore (offline)** | causes downtime; takes too long for large shards |
| **trigger-based replication (Slony, Bucardo)** | external dependency (BSD/Apache compatible, but operationally complex); PG native is superior |
| **physical replication + range filter** | physical replication cannot row-filter (need to replicate everything and DELETE afterward) |
| **shadow write (dual-write from app)** | requires application changes; violates this operator's abstraction |
| **External sharding extension's split UDF** | external-extension dependency (license incompatibility, decided to abandon) |

## §6 Open Questions

1. PG 16 logical replication does not replicate DDL → an ALTER TABLE during a split breaks it. Operational guidance: block schema migration during a split (admission webhook). Can this be automated?
2. Is P99 `cutover.durationMs` < 500ms achievable at every cluster size? In a 1024-shard cluster, the router fence broadcast could become a bottleneck → evaluate at the P5 stage after benchmarking.
3. ShardSplitJob history retention policy (N days after Done) — use the TTL controller? `ttlSecondsAfterFinished: 604800` (7 days) recommended.

## §7 Implementation Plan

### P3 (~v0.6.0) preliminary work

- [ ] Schema migration that automatically adds the `_vindex_hash` stored generated column to distributed tables (separate RFC/ADR).
- [ ] Define the router's fence API gRPC interface (`internal/router/fence.proto`).

### P4 (~v0.7.0) implementation of this RFC

- [ ] `api/v1alpha1/shardsplitjob_types.go` (kubebuilder markers).
- [ ] `internal/controller/resharder/controller.go` 7-phase state machine.
- [ ] `internal/controller/resharder/phases/` one file per phase (provisioning.go, streaming.go, ...).
- [ ] Router fence handling (`internal/router/fence.go`).
- [ ] e2e: 4-shard → split 1 → 5-shard, data consistency + cutover SLA P99 < 500ms.
- [ ] chaos test: source kill / target kill / network partition during streaming.

### P5 (~v0.8.0) automation

- [ ] KEDA → auto-create ShardSplitJob (with `approval: pending`).
- [ ] Approval annotation gate (`kubectl annotate ssj/... approval=approved`).

### Verification commands

```bash
go test ./internal/controller/resharder/...        # unit (state machine)
go test ./internal/router/fence/...                # fence unit
make test-e2e PILLAR=p4 -- --focus="ShardSplitJob"
make test-chaos PILLAR=p4                          # chaos-mesh scenarios
make bench PILLAR=p4                               # measure cutover SLA
```

Success criteria:
- Zero data loss (insert 1M rows during split → all match after diff).
- Cutover P99 < 500ms (100 measurements).
- Idempotent retry on failure reaches the same outcome.

## §8 References

- PostgreSQL Logical Replication: https://www.postgresql.org/docs/current/logical-replication.html
- pg_create_logical_replication_slot: https://www.postgresql.org/docs/current/functions-replication.html
- RFC 0001: PostgresCluster CRD v2
- RFC 0002: ShardRange CRD
- RFC 0004: pg-router architecture
- ADR 0003: License policy (no AGPL/BUSL)
