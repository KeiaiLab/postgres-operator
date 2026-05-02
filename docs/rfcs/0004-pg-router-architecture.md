# RFC-0004: pg-router 아키텍처

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P2 (~v0.5.0) ~ P6 (~v0.9.0)
- Supersedes: 없음 (신규)

## §1 Summary

`pg-router` 는 **stateless PostgreSQL wire protocol proxy** 이다. application 의 PG 연결을 받아 SQL 을 parse → vindex 평가 → 단일 shard fast-path forwarding 또는 multi-shard scatter-gather 수행 후 응답을 merge 한다. 모든 분산 메타데이터 (ShardRange) 를 K8s API 로 watch 하며, 자체 상태는 보유하지 않는다 (HPA 자유 스케일). Phase 별 점진 도입: **P2** = hash vindex + 단일-shard 라우팅, **P3** = vindex 확장 + scatter-gather, **P6** = 분산 트랜잭션 coordinator. 단일 endpoint 로 application 추상화하고, single-shard fast-path latency 오버헤드 < 1ms 가 목표.

## §2 Motivation

### §2.1 문제

자체 분산 SQL 의 핵심 컴포넌트 — application 에 *PG 호환 단일 endpoint* 를 제공하면서 sharding/replication 을 추상화. 요구사항:

- **PG wire protocol 100% 호환**: libpq, JDBC, asyncpg, pq.Conn 등 모든 정식 driver 동작.
- **stateless**: HPA 로 0~N pod 자유 스케일.
- **low latency**: 단일-shard 쿼리는 직접 PG 호출 대비 < 1ms 추가.
- **fault tolerant**: backend shard 1 개 실패가 다른 shard 의 쿼리에 영향 0.
- **observable**: 쿼리 단위 prometheus / OpenTelemetry trace.

### §2.2 사용자 시나리오

**시나리오 1: application 연결**
```python
conn = psycopg.connect("postgres://user:pass@foo-router.prod.svc:5432/foo")
cur = conn.execute("SELECT * FROM users WHERE tenant_id = %s", (42,))
# router: tenant_id=42 → murmur3 hash → 0x71... → shard-1 → 직접 forwarding
# latency: 0.3ms (router) + 1.2ms (shard) = 1.5ms
```

**시나리오 2: scatter-gather (P3+)**
```python
cur = conn.execute("SELECT count(*) FROM users")
# router: WHERE 절 없음 → 모든 shard scatter
# 각 shard: SELECT count(*) → router 가 SUM aggregate
# latency: 5ms (가장 느린 shard) + 0.5ms (router merge) = 5.5ms
```

### §2.3 비목표

- SQL rewriting / federation (이종 DB) — 범위 외, 영구.
- materialized view 분산 — P7+.
- pg_stat_statements / EXPLAIN 호환 (분산 plan visibility) — P6 별도 작업.

## §3 Design / Specification

### §3.1 컴포넌트 분해

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

각 router pod 는 **stateless** — 재시작/재배포 자유. 유일한 *재시작 비용*: connection pool warmup + plan cache miss.

### §3.2 wire protocol parser

라이브러리: **pg_query_go** (PostgreSQL License, 호환). PG core parser 를 그대로 사용 → 100% 문법 호환.

지원 message:

| 메시지 | P2 | P3 | P6 |
|---|---|---|---|
| StartupMessage / AuthRequest / SSLRequest | ✓ | ✓ | ✓ |
| Simple Query (`Q`) | ✓ | ✓ | ✓ |
| Extended Query (`P`/`B`/`E`/`D`/`S`) | ✓ | ✓ | ✓ |
| COPY FROM / COPY TO | partial | ✓ | ✓ |
| LISTEN / NOTIFY | single-shard | single-shard | — |
| FETCH (cursor) | — | — | — |
| SAVEPOINT (분산) | — | — | partial |
| advisory lock (cluster-wide) | — | — | — |

미지원 항목은 router 가 **명시적 에러** (PG SQLSTATE 0A000 `feature_not_supported`) 반환. silent skip 금지.

### §3.3 planner

```go
type Plan interface {
    Execute(ctx context.Context, conn *Connection) error
}

// 단일-shard fast path (P2+)
type SingleShardPlan struct {
    Shard   string
    Query   string  // 원본 SQL 그대로
    Bind    []byte
}

// scatter-gather (P3+)
type ScatterPlan struct {
    Shards     []string
    Query      string
    Aggregate  AggSpec   // count/sum/avg/min/max — router 후처리
    OrderBy    []OrderClause
    Limit      int
}

// distributed JOIN (P6+, colocated 만)
type ColocatedJoinPlan struct {
    Shards []string
    Query  string  // 각 shard 에서 동일 쿼리, 결과 UNION ALL
}

// 거부 (P2~P5: 명시적 cross-shard JOIN)
type RejectPlan struct{ Reason string }
```

**plan 결정 알고리즘** (단순화):
```
1. parse SQL → AST (pg_query_go)
2. table 들의 keyspace 추출 (분산 / reference / colocated)
3. WHERE 절에서 vindex column 의 = / IN 추출
4. 추출 성공 + 단일 shard 매칭 → SingleShardPlan
5. 추출 실패 + read-only + aggregate 가능 → ScatterPlan
6. 분산 JOIN + 모든 table 이 colocated 그룹 → ColocatedJoinPlan
7. 그 외 → RejectPlan ("cross-shard JOIN not supported")
```

### §3.4 plan cache

**LRU cache** (key = SQL 정규화 + bind type signature, value = compiled Plan):

```go
type PlanCache struct {
    mu    sync.RWMutex
    lru   *lru.Cache[planKey, Plan]   // size 1024 default
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

**무효화**: ShardRange `status.generation` 변경 시 *전체* plan cache flush. 빈도가 낮아 (split 시 1 회) 비용 무시.

### §3.5 connection pool (per-shard)

각 shard 별 pool. PG `MaxConnections` (default 100) 한계 회피 위해 router 가 application 측 connection 을 *내부적으로 multiplex*.

```go
type ShardPool struct {
    primary    *PGXPool   // master 쓰기
    replicas   []*PGXPool // read 분산 (latest-aware)
    config     PoolConfig // size, idleTimeout, ...
}
```

**옵션 (P3+)**: per-tenant 격리.
```yaml
spec:
  router:
    perTenantIsolation:
      enabled: true
      tenantColumn: tenant_id
      maxConnsPerTenant: 10
```
"noisy neighbor" 효과 제거. 단, connection 갯수 증가 → shard 부하.

**transaction-aware**: BEGIN ~ COMMIT 사이에는 같은 backend connection 고정 (transaction sticky).

### §3.6 scatter-gather concurrency (P3+)

기본 default `concurrency: 8` (8 개 shard 까지 병렬). 더 많으면 batch 단위 처리.

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

merge 단계는 router 측 stream 처리 (memory pressure 회피):
- `count(*)` → 단순 합산.
- `ORDER BY ... LIMIT N` → top-N heap.
- `GROUP BY` → router 측 hash aggregate.

### §3.7 distributed transaction coordinator (P6+)

자세한 spec 은 RFC 0005 참조. 본 RFC 는 router 의 책임 영역만:

```go
type DTxCoordinator struct {
    txID      uuid.UUID
    shards    []*ShardConn  // 참여 shard 들
    state     TxState        // Active | Preparing | Prepared | Committing | Aborting
    log       *TxLog          // operator leader 가 etcd lease 로 관리
}

// router 가 BEGIN 받으면:
// - 단일 shard 인 경우: 직접 PG BEGIN forwarding (오버헤드 0)
// - 다중 shard 가 추후 발견: lazy 2PC 시작
```

**recovery**: router crash 시 in-flight prepared txn 은 operator leader 가 가시화 (etcd 의 tx log 조회). 새 router 가 takeover 후 PREPARED 상태 txn 들에 대해 COMMIT 또는 ROLLBACK 결정.

### §3.8 보안

- **TLS 강제**: client → router (mTLS optional), router → shard (mTLS 필수, cert-manager 발급).
- **인증 위임**: router 는 SCRAM-SHA-256 만 지원. md5 미지원 (PG 15+ deprecated).
- **role 매핑**: PG role 은 모든 shard 에 동일 정의 (operator 가 sync). per-router 가상 role 미지원 (P7+).

### §3.9 관찰성

prometheus metrics (HPA 입력 포함):
```
postgresql_router_query_duration_seconds{plan_type, status}  histogram
postgresql_router_active_connections{role=client|backend}    gauge
postgresql_router_plan_cache_hits_total                      counter
postgresql_router_plan_cache_size                            gauge
postgresql_router_routing_table_generation                   gauge
postgresql_router_scatter_concurrency                        gauge
postgresql_router_shard_fence_writes_rejected_total          counter
```

OpenTelemetry trace: 1 query = 1 root span + 1 span per shard.

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

- **추가 hop latency**: 단일-shard 라도 +0.5~1ms 오버헤드 (parse + lookup + forwarding). 성능 critical app 에는 단점. 완화: shard 의 primary Service 를 직접 노출 (router 우회) 하는 *bypass mode* 옵션 (P5+ 검토).
- **unsupported feature 다수**: cursor / advisory lock / SAVEPOINT 분산 등 영구 미지원 항목이 application portability 제약. 완화: 명확한 문서 + e2e 테스트로 지원 범위 lock-in.
- **plan cache invalidation overhead**: split 시 모든 router 의 cache flush → 쿼리 latency 일시 spike. 완화: 점진 무효화 (ShardRange 의 keyspace 단위만 flush).
- **stateful 한 client 행위 (`SET search_path`)**: per-connection state 는 router 가 sticky 처리. shard 간 동기화 필요한 SET 은 모두 거부 (예: `SET LOCAL`).

## §5 Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **PgBouncer + 자체 sharding logic 추가** | PgBouncer 는 connection pool 만, SQL parse 미지원. 포크 부담 |
| **HAProxy (PG 모드)** | wire protocol 이해 부족, vindex 기반 라우팅 불가 |
| **Vitess vtgate 차용 (Apache 2.0)** | MySQL 전용. PG 포팅 = 사실상 신규 프로젝트 |
| **Citus router** | AGPL, 폐기 결정 |
| **client-side sharding (libpq 확장)** | application 변경 부담, language 별 SDK 다수 필요 |

## §6 Open Questions

1. `LISTEN/NOTIFY` 의 cluster-wide 전파 — 별도 pub-sub bus 도입? P5+ RFC 후보.
2. `EXPLAIN` 출력 — router 가 distributed plan 을 어떻게 표현? PG 호환 format vs 자체 format.
3. read replica 라우팅의 staleness tolerance — annotation 으로 명시 (`/*+ stale_ok=5s */`)? application 전환 부담 vs router default.

## §7 Implementation Plan

### P2 (~v0.5.0)

- [ ] `cmd/router/main.go` + `internal/router/` 패키지 구조.
- [ ] PG wire protocol frontend (`internal/router/wire/frontend.go`):
  - StartupMessage, SSL/TLS, SCRAM auth.
  - Simple + Extended query.
- [ ] hash vindex 평가 (`internal/vindex/hash.go`).
- [ ] 단일-shard plan + 직접 forwarding.
- [ ] ShardRange watch + routing table reload.
- [ ] connection pool (pgx 의 pgxpool 활용, MIT).
- [ ] HPA + ServiceMonitor + NetworkPolicy chart 추가.
- [ ] e2e: 4-shard 쿼리 → 정확한 shard 라우팅, latency P99 < 5ms.

### P3 (~v0.6.0)

- [ ] vindex 확장 (range, consistent-hash, lookup).
- [ ] scatter-gather plan + merge (count/sum/avg/min/max/order-by-limit).
- [ ] plan cache LRU.
- [ ] read replica 라우팅 (latest-aware).

### P6 (~v0.9.0)

- [ ] dtx coordinator (2PC 기반, RFC 0005 와 통합).
- [ ] colocated JOIN.
- [ ] saga 명시 declaration 처리.

### 검증 명령

```bash
go test ./internal/router/...                       # 단위
go test ./internal/router/wire/...                  # PG wire 호환 fuzz
make test-e2e PILLAR=p2 -- --focus="router single-shard"
make bench PILLAR=p2 -- --target=router-latency     # P99 < 1ms 추가 오버헤드 확인
make test-e2e PILLAR=p3 -- --focus="scatter-gather"
make test-driver-compat                              # libpq, JDBC, asyncpg, pq.Conn smoke
```

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §2.3 신설 컴포넌트, §3.3 알고리즘
- pg_query_go: https://github.com/pganalyze/pg_query_go (PostgreSQL License)
- pgx: https://github.com/jackc/pgx (MIT)
- PostgreSQL Frontend/Backend Protocol: https://www.postgresql.org/docs/18/protocol.html
- Vitess vtgate (참조 only, 코드 차용 0): https://vitess.io/docs/concepts/vtgate/
- RFC 0001: PostgresCluster CRD v2
- RFC 0002: ShardRange CRD
- RFC 0003: ShardSplitJob 7-step
- RFC 0005: Distributed transactions (2PC + saga)
- ADR 0001: Self-built distributed SQL
- ADR 0003: License policy (no AGPL/BUSL)
