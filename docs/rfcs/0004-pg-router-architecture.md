# RFC-0004: pg-router architecture

- Status: **Partially implemented** (P2 single-shard routing live-validated 2026-06-27)
- Date: 2026-05-02 (impl status updated 2026-06-27)
- Authors: @phil
- Target: Phase P2 (~v0.5.0) ~ P6 (~v0.9.0)
- Supersedes: none (new)

> **구현 현황 (2026-06-27)** — 상세: [docs/sharding/ROUTER-GAP-ANALYSIS.ko.md](../sharding/ROUTER-GAP-ANALYSIS.ko.md):
> - ✅ **P2 single-shard 라우팅 라이브 검증** — `cmd/pg-router` query-mode(`PGROUTER_MODE=query`):
>   PG wire 프레이밍 + 라우팅키 추출(토크나이저) + vindex(hash/range/consistent-hash) → 샤드 backend.
>   **scram-sha-256 / cleartext 백엔드 인증 대행** 완료 → 실 프로덕션 PG 동작. 배포 가능(`Dockerfile.router`
>   + `config/router/`). 라이브: `id='alice'`→shard-0 / `'bob'`→shard-1.
> - 🟡 **P3 scatter-gather** — in-process 라이브러리 동작(`scatter.go` merge), 프록시 레벨 forwarding 후속.
> - ⬜ **parameterized(extended) 라우팅** — 단일라운드만; lib/pq describe-first 는 describe-round 대행 필요.
>   **P6 분산 트랜잭션 coordinator** 미착수.

## §1 Summary

`pg-router` is a **stateless PostgreSQL wire protocol proxy**. It accepts the application's PG connections, parses the SQL → evaluates the vindex → performs either single-shard fast-path forwarding or multi-shard scatter-gather, then merges responses. It watches all distributed metadata (ShardRange) via the K8s API and holds no state of its own (free HPA scaling). Gradual introduction by phase: **P2** = hash vindex + single-shard routing, **P3** = vindex extensions + scatter-gather, **P6** = distributed transaction coordinator. It abstracts the application behind a single endpoint, with a target single-shard fast-path latency overhead < 1ms.

## §2 Motivation

### §2.1 Problem

A core component of the self-built distributed SQL — provides a *PG-compatible single endpoint* to the application while abstracting sharding/replication. Requirements:

- **100% PG wire protocol compatibility**: all canonical drivers (libpq, JDBC, asyncpg, pq.Conn, etc.) work.
- **Stateless**: free HPA scaling from 0 to N pods.
- **Low latency**: single-shard queries add < 1ms vs. a direct PG call.
- **Fault tolerant**: a failure of one backend shard has zero impact on queries to other shards.
- **Observable**: per-query prometheus / OpenTelemetry traces.

### §2.2 User scenarios

**Scenario 1: application connection**
```python
conn = psycopg.connect("postgres://user:pass@foo-router.prod.svc:5432/foo")
cur = conn.execute("SELECT * FROM users WHERE tenant_id = %s", (42,))
# router: tenant_id=42 → murmur3 hash → 0x71... → shard-1 → direct forwarding
# latency: 0.3ms (router) + 1.2ms (shard) = 1.5ms
```

**Scenario 2: scatter-gather (P3+)**
```python
cur = conn.execute("SELECT count(*) FROM users")
# router: no WHERE clause → scatter to all shards
# Each shard: SELECT count(*) → router applies SUM aggregate
# latency: 5ms (slowest shard) + 0.5ms (router merge) = 5.5ms
```

### §2.3 Non-goals

- SQL rewriting / federation (heterogeneous DBs) — out of scope, permanently.
- Distributed materialized views — P7+.
- pg_stat_statements / EXPLAIN compatibility (distributed plan visibility) — separate work in P6.

## §3 Design / Specification

### §3.1 Component decomposition

```
┌──────────────────────────────────────────────────────────┐
│ pg-router Pod                                            │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────┐ │
│  │ wire frontend│  │   planner    │  │ wire backend   │ │
│  │ (PG v3 srv)  │─▶│ - parse      │─▶│ (per-shard pool│ │
│  │              │  │ - vindex eval│  │  per-tenant    │ │
│  │ TLS, auth    │  │ - plan cache │  │  optional)     │ │
│  └──────────────┘  └──────┬───────┘  └────────────────┘ │
│                           │                              │
│                    ┌──────▼───────┐                      │
│                    │ routing table│ ◀── ShardRange watch │
│                    │ (in-memory)  │                      │
│                    └──────────────┘                      │
│                                                          │
│  ┌─────────────────────────────────────────────────────┐ │
│  │ dtxn coordinator (P6+): 2PC prepare / commit log    │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

Each router pod is **stateless** — free to restart/redeploy. The only *restart cost*: connection pool warmup + plan cache miss.

### §3.2 wire protocol parser

Library: **pg_query_go** (PostgreSQL License, compatible). Uses the PG core parser as-is → 100% syntax compatibility.

Supported messages:

| Message | P2 | P3 | P6 |
|---|---|---|---|
| StartupMessage / AuthRequest / SSLRequest | ✓ | ✓ | ✓ |
| Simple Query (`Q`) | ✓ | ✓ | ✓ |
| Extended Query (`P`/`B`/`E`/`D`/`S`) | ✓ | ✓ | ✓ |
| COPY FROM / COPY TO | partial | ✓ | ✓ |
| LISTEN / NOTIFY | single-shard | single-shard | — |
| FETCH (cursor) | — | — | — |
| SAVEPOINT (distributed) | — | — | partial |
| advisory lock (cluster-wide) | — | — | — |

For unsupported items, the router returns an **explicit error** (PG SQLSTATE 0A000 `feature_not_supported`). No silent skipping.

### §3.3 planner

```go
type Plan interface {
    Execute(ctx context.Context, conn *Connection) error
}

// single-shard fast path (P2+)
type SingleShardPlan struct {
    Shard   string
    Query   string  // original SQL as-is
    Bind    []byte
}

// scatter-gather (P3+)
type ScatterPlan struct {
    Shards     []string
    Query      string
    Aggregate  AggSpec   // count/sum/avg/min/max — post-processed by router
    OrderBy    []OrderClause
    Limit      int
}

// distributed JOIN (P6+, colocated only)
type ColocatedJoinPlan struct {
    Shards []string
    Query  string  // identical query on each shard, results UNION ALL
}

// reject (P2~P5: explicit cross-shard JOIN)
type RejectPlan struct{ Reason string }
```

**Plan decision algorithm** (simplified):
```
1. parse SQL → AST (pg_query_go)
2. extract the tables' keyspaces (distributed / reference / colocated)
3. extract = / IN on vindex columns from the WHERE clause
4. extraction success + matches a single shard → SingleShardPlan
5. extraction fails + read-only + aggregatable → ScatterPlan
6. distributed JOIN + all tables in the same colocated group → ColocatedJoinPlan
7. otherwise → RejectPlan ("cross-shard JOIN not supported")
```

### §3.4 plan cache

**LRU cache** (key = normalized SQL + bind-type signature, value = compiled Plan):

```go
type PlanCache struct {
    mu    sync.RWMutex
    lru   *lru.Cache[planKey, Plan]   // default size 1024
}

func (c *PlanCache) GetOrCompile(sql string, bindTypes []OID) Plan {
    key := normalize(sql, bindTypes)
    if p, ok := c.lru.Get(key); ok {
        metrics.PlanCacheHit.Inc()
        return p
    }
    p := compile(sql, bindTypes)
    c.lru.Add(key, p)
    return p
}
```

**Invalidation**: when ShardRange `status.generation` changes, flush the *entire* plan cache. Low frequency (once per split), so cost is negligible.

### §3.5 connection pool (per-shard)

A pool per shard. To avoid PG's `MaxConnections` (default 100) limit, the router *internally multiplexes* application connections.

```go
type ShardPool struct {
    primary    *PGXPool   // master writes
    replicas   []*PGXPool // read distribution (latest-aware)
    config     PoolConfig // size, idleTimeout, ...
}
```

**Option (P3+)**: per-tenant isolation.
```yaml
spec:
  router:
    perTenantIsolation:
      enabled: true
      tenantColumn: tenant_id
      maxConnsPerTenant: 10
```
Removes "noisy neighbor" effects. Note: the number of connections grows → shard load increases.

**Transaction-aware**: between BEGIN ~ COMMIT, the same backend connection is pinned (transaction sticky).

### §3.6 scatter-gather concurrency (P3+)

Default `concurrency: 8` (parallel across up to 8 shards). Larger fan-outs are processed in batches.

```go
func (p *ScatterPlan) Execute(ctx context.Context, conn *Connection) error {
    sem := make(chan struct{}, p.Concurrency)
    results := make([]Result, len(p.Shards))
    g, ctx := errgroup.WithContext(ctx)
    for i, shard := range p.Shards {
        i, shard := i, shard
        sem <- struct{}{}
        g.Go(func() error {
            defer func() { <-sem }()
            r, err := executeOnShard(ctx, shard, p.Query)
            if err != nil { return err }
            results[i] = r
            return nil
        })
    }
    if err := g.Wait(); err != nil { return err }
    return p.merge(results, conn)
}
```

The merge stage is stream-processed at the router (to avoid memory pressure):
- `count(*)` → simple summation.
- `ORDER BY ... LIMIT N` → top-N heap.
- `GROUP BY` → hash aggregate on the router side.

### §3.7 distributed transaction coordinator (P6+)

See RFC 0005 for the detailed spec. This RFC only covers the router's responsibilities:

```go
type DTxCoordinator struct {
    txID      uuid.UUID
    shards    []*ShardConn  // participating shards
    state     TxState        // Active | Preparing | Prepared | Committing | Aborting
    log       *TxLog          // managed by the operator leader via etcd lease
}

// When the router receives BEGIN:
// - For a single shard: forward PG BEGIN directly (zero overhead)
// - When a second shard appears later: start lazy 2PC
```

**Recovery**: on router crash, in-flight prepared txns are made visible by the operator leader (lookup tx log in etcd). After a new router takes over, it decides COMMIT or ROLLBACK for txns in the PREPARED state.

### §3.8 Security

- **TLS enforced**: client → router (mTLS optional), router → shard (mTLS mandatory, certs issued by cert-manager).
- **Authentication delegation**: the router supports only SCRAM-SHA-256. md5 is not supported (PG 15+ deprecated).
- **Role mapping**: PG roles are defined identically on all shards (the operator syncs them). Per-router virtual roles are not supported (P7+).

### §3.9 Observability

prometheus metrics (including HPA inputs):
```
postgresql_router_query_duration_seconds{plan_type, status}  histogram
postgresql_router_active_connections{role=client|backend}    gauge
postgresql_router_plan_cache_hits_total                      counter
postgresql_router_plan_cache_size                            gauge
postgresql_router_routing_table_generation                   gauge
postgresql_router_scatter_concurrency                        gauge
postgresql_router_shard_fence_writes_rejected_total          counter
```

OpenTelemetry traces: 1 query = 1 root span + 1 span per shard.

### §3.10 HPA / Autoscale

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: foo-router
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Resource
      resource: { name: cpu, target: { type: Utilization, averageUtilization: 70 } }
    - type: Pods
      pods:
        metric: { name: postgresql_router_active_connections }
        target: { type: AverageValue, averageValue: "1000" }
```

## §4 Drawbacks / Trade-offs

- **Extra hop latency**: even single-shard adds +0.5~1ms overhead (parse + lookup + forwarding). A downside for performance-critical apps. Mitigation: consider a *bypass mode* option (P5+) that exposes the shard's primary Service directly (bypassing the router).
- **Many unsupported features**: cursor / advisory lock / distributed SAVEPOINT etc. are permanently unsupported, constraining application portability. Mitigation: clear documentation + lock the supported range with e2e tests.
- **plan cache invalidation overhead**: every split flushes the cache on every router → temporary spike in query latency. Mitigation: incremental invalidation (flush only the keyspace of the affected ShardRange).
- **Stateful client behavior (`SET search_path`)**: per-connection state is handled stickily by the router. SET statements that require cross-shard sync are all rejected (e.g., `SET LOCAL`).

## §5 Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| **PgBouncer + add own sharding logic** | PgBouncer is only a connection pool, no SQL parsing. Fork burden |
| **HAProxy (PG mode)** | Insufficient understanding of the wire protocol, cannot do vindex-based routing |
| **Reuse a foreign MySQL-only router** | wire-protocol incompatible. PG porting = essentially a new project |
| **External PG sharding extension's router** | license incompatible, decided to abandon |
| **client-side sharding (libpq extension)** | Application change burden, many language-specific SDKs needed |

## §6 Open Questions

1. Cluster-wide propagation for `LISTEN/NOTIFY` — introduce a separate pub-sub bus? Candidate for P5+ RFC.
2. `EXPLAIN` output — how should the router represent a distributed plan? PG-compatible format vs. our own format.
3. Staleness tolerance for read-replica routing — express via annotation (`/*+ stale_ok=5s */`)? Application transition burden vs. router default.

## §7 Implementation Plan

### P2 (~v0.5.0)

- [ ] `cmd/router/main.go` + `internal/router/` package structure.
- [ ] PG wire protocol frontend (`internal/router/wire/frontend.go`):
  - StartupMessage, SSL/TLS, SCRAM auth.
  - Simple + Extended query.
- [ ] Hash vindex evaluation (`internal/vindex/hash.go`).
- [ ] Single-shard plan + direct forwarding.
- [ ] ShardRange watch + routing-table reload.
- [ ] Connection pool (use pgxpool from pgx, MIT).
- [ ] HPA + ServiceMonitor + NetworkPolicy chart additions.
- [ ] e2e: 4-shard queries → exact shard routing, latency P99 < 5ms.

### P3 (~v0.6.0)

- [ ] vindex extensions (range, consistent-hash, lookup).
- [ ] scatter-gather plan + merge (count/sum/avg/min/max/order-by-limit).
- [ ] LRU plan cache.
- [ ] Read-replica routing (latest-aware).

### P6 (~v0.9.0)

- [ ] dtx coordinator (2PC-based, integrated with RFC 0005).
- [ ] Colocated JOIN.
- [ ] Handle explicit saga declarations.

### Verification commands

```bash
go test ./internal/router/...                       # unit
go test ./internal/router/wire/...                  # PG wire compatibility fuzz
make test-e2e PILLAR=p2 -- --focus="router single-shard"
make bench PILLAR=p2 -- --target=router-latency     # verify P99 < 1ms added overhead
make test-e2e PILLAR=p3 -- --focus="scatter-gather"
make test-driver-compat                              # libpq, JDBC, asyncpg, pq.Conn smoke
```

## §8 References

- pg_query_go: https://github.com/pganalyze/pg_query_go (PostgreSQL License)
- pgx: https://github.com/jackc/pgx (MIT)
- PostgreSQL Frontend/Backend Protocol: https://www.postgresql.org/docs/18/protocol.html
- RFC 0001: PostgresCluster CRD v2
- RFC 0002: ShardRange CRD
- RFC 0003: ShardSplitJob 7-step
- RFC 0005: Distributed transactions (2PC + saga)
- ADR 0001: Self-built distributed SQL
- ADR 0003: License policy (no AGPL/BUSL)
