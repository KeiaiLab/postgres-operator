# Sharding Architecture (G3-G5)

> postgres-operator 의 sharding foundation 설계 — `ShardRange` CRD source of truth + `pg-router` 단일 endpoint + `ShardSplitJob` 7-step online resharding. Citus / Vitess 의 *디자인 패턴* 참조, *embedding 금지* (ADR-0001 license-clean boundary).

## G3 Foundation — 현재 상태

### ShardingMode field (G3 partial, [~])

`api/v1alpha1/postgrescluster_types.go`:

```go
type ShardingMode string

const (
    ShardingNone   ShardingMode = "none"   // single shard (current)
    ShardingNative ShardingMode = "native" // self-built (G3+)
)

type PostgresClusterSpec struct {
    Sharding ShardsSpec `json:"sharding,omitempty"`
}

type ShardsSpec struct {
    Mode    ShardingMode `json:"mode"`
    Initial int          `json:"initial"`  // 초기 shard 수
    Replicas int         `json:"replicas"` // shard 당 replica
    Storage  StorageSpec `json:"storage"`
}
```

### Plugin interface (G3 partial, [~])

`internal/plugin/sharding/api.go`:

```go
type ShardingPlugin interface {
    PlaceShard(ctx context.Context, range_ ShardRange) (PlacementHints, error)
    LookupShard(ctx context.Context, key string) (ShardRef, error)
    RebalanceHints(ctx context.Context, current []ShardPlacement) ([]Migration, error)
}
```

## G3 Pending — ShardRange CRD (D.8.1, not started)

### CRD 신설 — `api/v1alpha1/shardrange_types.go`

```go
type ShardRangeSpec struct {
    // ClusterRef — PostgresCluster 이름
    ClusterRef LocalObjectReference `json:"clusterRef"`
    // RangePolicy — hash / list / range / consistent-hash
    RangePolicy RangePolicyType `json:"rangePolicy"`
    // RangeBounds — policy 별 boundary 표현
    RangeBounds RangeBounds `json:"rangeBounds"`
    // PlacementHints — manual placement (optional, G3.7)
    PlacementHints *PlacementHints `json:"placementHints,omitempty"`
}

type ShardRangeStatus struct {
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    AssignedShard string `json:"assignedShard,omitempty"`
    MetadataVersion int64 `json:"metadataVersion,omitempty"`
}
```

### Metadata store

Option A: PostgresCluster catalog table `_pgo_shard_metadata`
Option B: Sidecar (sharding-metadata-store) with own PVC
**Decision**: Option A (G3.3) — leverage existing PG catalog, minimize moving parts.

### Hash-range policy (D.8.2)

```sql
-- Vindex 평가: hash(key) → shard_id mod N
CREATE FUNCTION _pgo_hash_vindex(key text, shards int) RETURNS int
LANGUAGE plpgsql AS $$
BEGIN
    RETURN abs(hashtext(key)) % shards;
END;
$$;
```

### List policy

key → shard_id 정적 mapping (`_pgo_shard_metadata` table).

### Consistent-hash

Future (G4+) — ring-based mapping for minimal data movement on resize.

## G4 Pending — pg-router (D.8.4-7)

`cmd/pg-router/main.go` — separate binary:

- libpq wire protocol v3 listener (TCP :5432)
- SQL parser via [libpg_query](https://github.com/pganalyze/libpg_query) Go bindings
- Routing decisions:
  - **Single-shard fast path**: SELECT/UPDATE/DELETE with WHERE shard_key = ... → 1 shard
  - **Multi-shard scatter-gather**: aggregation queries → parallel + merge
  - **Cross-shard transactions**: 2PC coordinator (G5 D.10.2)

### Sharding 7-step online resharding (G4 D.9.x)

1. **Snapshot + WAL capture** — source shard pg_basebackup + WAL position record
2. **Target shard bootstrap** — new empty StatefulSet
3. **Initial copy** — pg_basebackup from snapshot
4. **CDC catch-up** — logical replication from source primary
5. **Cutover** — pg-router 라우팅 갱신 + 짧은 write-block (target <500ms p99)
6. **Routing update** — `_pgo_shard_metadata` 갱신 + propagation
7. **Source cleanup** — old shard data + StatefulSet GC

## G5 Pending — Distributed SQL (D.10.x)

- **Scatter-gather**: parallel exec on all shards + result merge in pg-router
- **2PC / saga**: distributed transaction coordinator (Saga preferred for OLTP, 2PC for strong consistency batch)
- **Isolation matrix**: 어떤 isolation level 이 어떤 조건 (single-shard / cross-shard) 에서 보장되는지 문서화
- **Benchmarks**: sysbench-tpcc + pgbench --select-only on 4 shards

## Non-goals

- ❌ Citus extension embedding (license-clean Apache-2.0 only)
- ❌ Vitess fork
- ❌ PostgresQL < 18

## Refs

- ROADMAP.md G3-G5 (P-D.7.x + D.8.x + D.9.x + D.10.x)
- ADR-0001 (self-built distributed SQL)
- libpg_query: https://github.com/pganalyze/libpg_query
