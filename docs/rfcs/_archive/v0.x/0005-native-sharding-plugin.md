# RFC-0005: Native Sharding Plugin (Citus 메커니즘 분해 + Apache-2.0 호환 path)

- Date: 2026-05-01
- Status: Draft
- Authors: @keiailab
- Related: ADR-0010 (license + sharding strategy), ADR-0005 (plugin SDK interface model)

## 0. 요약 (TL;DR)

ADR-0010이 결정한 "Citus 격리 + vanilla PG default" 정책의 *장기 path*. Citus가 제공하는 분산 SQL capability subset을 Apache-2.0 호환 plugin으로 자체 구현하기 위해 Citus의 핵심 7개 메커니즘을 분해하고, 본 operator의 5-interface Plugin SDK를 확장한 `ShardingPlugin` 인터페이스를 제시한다. 단계별 마일스톤(Phase 2A~Phase 4)을 정의하고 각 phase의 trade-off, 위험, 이미 존재하는 솔루션과의 비교를 평가한다.

**현실 인지**: Citus는 Microsoft 전담팀이 10년 이상 누적한 ~500K LOC C 코드. 1:1 대체는 multi-year 작업이며 본 RFC가 약속하는 것은 *capability subset* 구현이지 parity가 아니다.

## 1. Context

### 1.1 결정 배경 (ADR-0010 요약)

- Citus = AGPL-3.0 → operator 사용자가 SaaS 운영 시 §13 의무 부담
- 본 operator = Apache-2.0 → 청정 default 유지
- 0.2.0-alpha 이후 default = vanilla PG18, Citus는 Beta opt-in

### 1.2 제거 시 공백

분산 SQL이 default에서 빠졌으므로 다음 사용자 시나리오가 *기능 공백*에 들어간다:
- 단일 PG가 감당하기 어려운 OLTP scale (>1TB working set, >10K QPS)
- 멀티-노드 sharded write workload
- 분산 join이 필요한 분석 쿼리

본 RFC는 본 공백을 단계적으로 메우는 path를 제시한다.

### 1.3 비교: 기존 분산 PostgreSQL 솔루션

| 솔루션 | 라이센스 | 접근법 | 본 RFC 관점 |
|---|---|---|---|
| Citus | AGPL-3.0 | PG extension, distributed planner C extension | 회피 대상 (license) |
| Vitess | Apache-2.0 | MySQL/PG sharding proxy | PG 지원 alpha. 외부 proxy 패턴 참고 |
| YugabyteDB | Apache-2.0 (구 PG fork) | PG-compatible distributed DB | 별도 stack, drop-in 아님 |
| CockroachDB | BUSL (Business Source) | PG wire compatible distributed | 본 operator 외부, 비교군 |
| pgcat / pgbouncer + sharding 룰 | MIT/PostgreSQL | connection-level sharding proxy | Phase 2A 후보 |
| postgres_fdw + pg_partman | PostgreSQL License | FDW + partition routing | Phase 2A 시작점 |

본 RFC는 *plugin 형태*의 자체 구현을 목표로 하며, 외부 솔루션은 사용자가 선택할 수 있는 alternatives로만 다룬다.

## 2. Citus 핵심 메커니즘 분해 (7 components)

본 절은 Citus의 공개 documentation, 코드 구조, 학술 논문(VLDB 2021 "Citus: Distributed PostgreSQL for Data-Intensive Applications") 분석에 기반한다.

### C1. Distributed Query Planner

**역할**: SQL을 받아 어느 shard로 routing/parallelize할지 결정. PG의 native planner 결과물(query tree)을 distributed query plan으로 변환.

**Citus 구현**: `multi_planner` (~150K LOC). Hook을 통해 PG planner_hook에 주입. Logical planner → physical planner → job tree 생성.

**난이도**: ★★★★★ (가장 어려움). Citus의 핵심 가치이자 5년 이상 누적 작업.

### C2. Distributed Executor

**역할**: Plan tree를 받아 worker 노드에 fragment query를 보내고 결과 집계. Adaptive executor는 connection pool 재사용, transaction recovery 처리.

**Citus 구현**: `multi_executor` + `adaptive_executor`. Real-time + Task-tracker 두 모드.

**난이도**: ★★★★ — connection pool, transaction state, error recovery가 복잡.

### C3. Shard Placement & Metadata Catalog

**역할**: 어느 shard가 어느 worker 노드에 위치하는지 추적 (`pg_dist_placement`, `pg_dist_shard`, `pg_dist_node`). Reference table 복제, distribution column hash range.

**Citus 구현**: PG catalog tables. 메타데이터는 coordinator + 모든 worker에 동기화 (Citus 11+).

**난이도**: ★★★ — 데이터 모델은 직관적이나 동기화 일관성 보장이 까다로움.

### C4. Shard Rebalancer

**역할**: 노드 추가/제거 시 shard를 자동 재배치. CPU/disk balance, locality 고려.

**Citus 구현**: `rebalance_table_shards()` SQL 함수. 백그라운드 worker가 shard move를 수행 (non-blocking).

**난이도**: ★★★★ — non-blocking shard move + transaction safety + abort recovery.

### C5. Distributed Transactions (2PC + heartbeat)

**역할**: 여러 shard에 걸친 ACID transaction 보장. Two-phase commit + cohort heartbeat + recovery.

**Citus 구현**: `pg_dist_transaction` + custom 2PC coordinator. Long-running prepared transaction 자동 정리 (timer-based).

**난이도**: ★★★★★ — 분산 system 정합성 + node failure 시 recovery는 corner case가 무한.

### C6. Reference Tables

**역할**: 모든 worker에 동기 복제되는 작은 테이블 (룩업/dim 테이블용). Distributed table과 collocate join 가능.

**Citus 구현**: 단일 placement → 모든 노드 복제. INSERT/UPDATE는 모든 노드에 동기 적용.

**난이도**: ★★★ — 동기 복제 일관성 + write throughput 제한.

### C7. Columnar Storage

**역할**: append-only columnar table access method. 분석 워크로드용 대용량 압축.

**Citus 구현**: `cstore_fdw` → `citus_columnar` (PG access method). zstd 압축, predicate push-down.

**난이도**: ★★★★ — PG access method API + 압축 + chunk metadata.

### 정리

C1 + C2 + C5는 *분산 시스템 핵심 어려움*에 위치하며 Citus의 가치 80%를 차지한다. C3 + C4는 운영 자동화. C6 + C7은 *부가* capability — drop in replacement 의무 없음.

## 3. 본 operator의 plugin 모델 매핑

현재 5-interface plugin SDK (`internal/plugin/api.go`):

1. `BackupPlugin` — pgBackRest, WAL-G 등
2. `ExporterPlugin` — postgres_exporter, custom exporters
3. `ExtensionPlugin` — pgaudit, pgcron, pgvector, **citus** (현재 Beta)
4. `RouterPlugin` — pgbouncer, pgcat
5. `AuthPlugin` — Vault-issued credentials, IAM

본 5개는 **단일 PG 인스턴스 또는 클러스터 외부**에 작용한다. 분산 SQL은 *클러스터 내부 다중 노드 조정*이 필요하므로 새 인터페이스가 필요하다.

### 신규 인터페이스: `ShardingPlugin`

```go
// ShardingPlugin은 분산 sharding 백엔드를 추상화한다.
// 구현: Citus(AGPL, opt-in), Native(Apache-2.0, RFC 0005 Phase 2+), Vitess gateway 등.
//
// 본 인터페이스는 RFC 0005 Phase 2A 시점에 alpha freeze된다.
// alpha 단계에서는 메서드 추가만 허용 (non-breaking).
type ShardingPlugin interface {
    // Name은 본 플러그인의 고유 식별자.
    // PostgresClusterSpec.Sharding.Backend 와 일치해야 한다.
    Name() string

    // Capabilities는 본 백엔드가 지원하는 기능 집합을 보고한다.
    // 사용자가 ShardingSpec에 unsupported 기능을 지정하면 webhook이 거절한다.
    Capabilities() ShardingCapabilities

    // PreparePlacement는 PostgresCluster topology가 변경됐을 때
    // shard placement 갱신을 수행한다 (노드 추가/제거).
    // 멱등이며 controller reconcile loop에서 매번 호출된다.
    PreparePlacement(ctx context.Context, target ClusterTarget, topo Topology) error

    // CreateDistributedTable은 사용자 SQL DDL("DISTRIBUTED TABLE ... BY (col)")을
    // 해석하여 shard 생성 + metadata 등록을 수행한다.
    CreateDistributedTable(ctx context.Context, conn *sql.DB, spec DistributedTableSpec) error

    // CreateReferenceTable은 모든 노드에 동기 복제되는 작은 테이블 생성.
    // 백엔드가 reference table을 지원하지 않으면 Capabilities()에서 false 반환.
    CreateReferenceTable(ctx context.Context, conn *sql.DB, table string) error

    // RebalanceShards는 shard 재배치를 트리거한다 (백그라운드 비동기).
    // 진행 상황은 백엔드별 status table 쿼리로 확인한다.
    RebalanceShards(ctx context.Context, conn *sql.DB) (RebalanceJob, error)

    // RouteQuery는 SQL을 받아 어느 shard/worker에 보낼지 결정한다.
    // 본 메서드는 RouterPlugin과 협력하여 connection-level routing을 수행할 수도 있고,
    // 백엔드 자체 distributed planner에 위임할 수도 있다 (Capabilities로 신호).
    RouteQuery(ctx context.Context, query string, params []any) ([]ShardTarget, error)

    // Validate는 ShardingSpec 사용자 입력을 본 백엔드 관점에서 검사한다.
    // webhook 단계에서 호출.
    Validate(spec *ShardingSpec) error
}

// ShardingCapabilities는 백엔드 기능 광고.
type ShardingCapabilities struct {
    DistributedTables    bool   // C3 hash/range distribution
    ReferenceTables      bool   // C6 broadcast tables
    DistributedJoin      bool   // C1+C2 multi-shard join
    Distributed2PC       bool   // C5 cross-shard ACID
    OnlineRebalance      bool   // C4 non-blocking shard move
    ColumnarStorage      bool   // C7 columnar tables
    NativeQueryPlanner   bool   // 백엔드 자체 planner (Citus). false면 routing-only.
}

// DistributedTableSpec은 distributed table 정의.
type DistributedTableSpec struct {
    Name             string  // 스키마 포함 (e.g. "public.events")
    DistributionCol  string  // shard key column
    ShardCount       int32   // 기본값 32. range 또는 hash 분배
    ColocateWith     string  // 같은 distribution을 갖는 다른 테이블과 collocate
    Strategy         string  // "hash" | "range"
}

// ShardTarget은 query를 보낼 단일 shard 위치.
type ShardTarget struct {
    Worker      string  // hostname (Pod DNS)
    Port        int32
    ShardID     int64
}

// Topology는 PostgresCluster의 현재 노드 토폴로지 snapshot.
// internal/citus/topology.go의 Node 구조와 별개로 일반화된 형태.
type Topology struct {
    Coordinator *NodeInfo
    Workers     []NodeInfo
}

type NodeInfo struct {
    Pool     string
    Host     string
    Port     int32
    GroupID  int32
}

// RebalanceJob은 진행 중 rebalance 추적용.
type RebalanceJob struct {
    ID       string
    Started  time.Time
    Status   string  // "running" | "complete" | "failed"
}

// ShardingSpec은 PostgresCluster CRD의 spec.sharding 서브필드 (RFC 0005 Phase 2A 도입).
type ShardingSpec struct {
    Backend         string  // "citus" | "native-fdw" | ...
    DistributedTables []DistributedTableSpec
    ReferenceTables []string
    DefaultShardCount int32
    // 백엔드별 추가 옵션은 별도 BackendOptions struct (omitempty)
}
```

### 매핑 표

| Citus mechanism | ShardingPlugin 메서드 | 우선순위 |
|---|---|---|
| C3 Placement | `PreparePlacement`, `CreateDistributedTable` | Phase 2A |
| C2 Executor | `RouteQuery` (단순 case), 백엔드 자체 planner (복잡 case) | Phase 2C |
| C6 Reference | `CreateReferenceTable` | Phase 2D |
| C4 Rebalance | `RebalanceShards` | Phase 3 |
| C5 2PC | (별도 인터페이스 `DistributedTxnPlugin`로 분리 검토) | Phase 3 |
| C1 Planner | `RouteQuery` 또는 백엔드 위임 | Phase 4 |
| C7 Columnar | (별도 ExtensionPlugin으로 격리) | Phase 4+ |

## 4. Phased Roadmap

### Phase 2A — Sharding Plugin Interface Freeze + FDW Skeleton

**목표**: 인터페이스를 동결하고 가장 단순한 백엔드 1개를 구현해 *동작*을 증명.

**산출물**:
- `internal/plugin/sharding/api.go` — 위 §3의 ShardingPlugin 인터페이스 + 보조 타입.
- `PostgresClusterSpec.Sharding` CRD 필드 추가 (optional, omitempty).
- `internal/plugin/sharding/fdw/` — postgres_fdw 기반 hash sharding plugin (PostgreSQL License — 라이센스 청정).
  - DistributedTable: parent table + worker별 partition foreign table.
  - ReferenceTable: postgres_fdw broadcast (UPDATE 시 모든 노드).
  - RouteQuery: hash(key) % shardCount.
- 하나의 distributed table에서 hash 기반 INSERT/SELECT가 동작.
- e2e 테스트: 3-worker cluster + distributed events 테이블 + 분산 INSERT/SELECT.

**비포함**: distributed JOIN, 2PC, online rebalance, columnar.

**예상 기간**: 2~3개월. 단, postgres_fdw의 push-down 한계로 분산 JOIN 미지원.

### Phase 2B — Reference Tables + Collocate Join

**산출물**: ReferenceTable 동기 복제 (trigger-based), collocated join (worker 내부에서 처리).

**예상 기간**: 1~2개월.

### Phase 2C — Smart Routing (read-only)

**산출물**: SELECT 쿼리의 distribution column 추출 + 적절 worker로 routing. SQL parser 도입 필요 (pg_query_go 사용).

**예상 기간**: 2~3개월.

### Phase 2D — Online Add/Remove Worker

**산출물**: PostgresClusterSpec.Workers 변경 시 shard 자동 재배치. 단, blocking move (read-only window).

**예상 기간**: 2~3개월.

### Phase 3 — Distributed 2PC + Online Rebalance

**산출물**: cross-shard ACID + non-blocking shard move. 분산 시스템 *진짜 어려움*에 진입.

**예상 기간**: 6~12개월. 본 RFC의 단일 phase 중 가장 위험.

### Phase 4 — Distributed Query Planner (선택)

**산출물**: 일반 JOIN/aggregation의 분산 실행. PG planner_hook 도입 또는 외부 proxy(pgcat fork) 검토.

**예상 기간**: 12~24개월. 또는 *영구 보류* 결정 후 Citus opt-in 을 분산 JOIN 사용자에게 권장.

## 5. 위험 분석

| 위험 | 가능성 | 영향 | 완화 |
|---|---|---|---|
| Phase 2A 후 사용자 기대 vs 구현 격차 | 높음 | 중 | RFC + chart NOTES + README에 *capability subset* 명시 |
| Phase 3 (2PC) corner case 누적 | 매우 높음 | 매우 큼 | Jepsen-style 테스트 필수, alpha 표기 6+ months |
| Native sharding 운영 어려움이 Citus opt-in 보다 큰 케이스 | 높음 | 중 | Citus opt-in path를 영구 보존 (license만 분명히) |
| upstream PG 18+ 의 logical replication 변경이 reference table 깨뜨림 | 중 | 중 | upstream-watch (단, RFC 0002 GH Actions 폐기 — 수동 모니터) |
| postgres_fdw push-down 한계 (Phase 2A) | 확정 | 중 | Phase 2A 자체를 "분산 INSERT만 1차 목표"로 좁힘 |

## 6. 결정 기준 (Phase 진입 게이트)

각 phase 진입 전 다음을 확인:
1. **시장 신호**: 직전 phase 산출물 사용자 수 (alpha → beta 전환 metric).
2. **운영 fitness**: 직전 phase의 corner case 보고 빈도 (Jepsen + customer reports).
3. **인력 가용성**: 본 RFC의 phase는 모두 multi-month. 단일 contributor로 부족.
4. **대안 비교**: 진입 시점에 Vitess for PG, pgcat-shard, pg_dirtread 등 신규 솔루션 등장 시 본 path 재평가.

## 7. Alternatives Considered

### A. Phase 4 (Native query planner) 영구 포기 + Citus opt-in 권장

분산 JOIN/aggregation이 필수인 사용자는 Citus opt-in 으로 유도. 우리는 Phase 2A~3 (placement, routing, 2PC) 까지만 자체 구현.

- 장점: 본 RFC 범위 70% 축소, 현실적.
- 단점: 분산 SQL "plug-and-play" 메시징 약화. SaaS 사용자는 여전히 AGPL 부담.

### B. 외부 proxy 통합 (Vitess for PG)

ShardingPlugin을 Vitess 게이트웨이로 구현. PG 18 호환은 Vitess upstream에 의존.

- 장점: 우리는 routing layer만 구현. Distributed planner는 Vitess 의존.
- 단점: 외부 stack 의존. operator 자체 가치 약화.

### C. 사용자에게 sharding을 *위임*

operator는 단일 PG HA 만 보장. Sharding은 사용자가 application 레벨에서 구현 (예: pgbouncer + 라우팅 룰 + middleware).

- 장점: 가장 단순. operator 책임 명확.
- 단점: 본 차별화(distributed SQL operator)가 사라짐.

## 8. 본 RFC 채택 시 후속 작업

1. **ADR-0010 AI-007 진입**: ShardingPlugin 인터페이스 PR (`internal/plugin/sharding/api.go` 신설).
2. **CRD 확장**: `PostgresClusterSpec.Sharding` optional 필드 추가. webhook 검증.
3. **README + roadmap.md 갱신**: Phase 2A~Phase 4 명시. Citus opt-in path 영구 보존.
4. **examples/sharding/** 신규: postgres_fdw 사용 distributed table sample.
5. **e2e 테스트 확장**: 3-worker cluster + distributed events 시나리오.

## 9. 미해결 질문

1. ShardingPlugin과 RouterPlugin (pgbouncer/pgcat) 의 책임 경계는? RouteQuery 메서드가 양쪽에 있을 수 있음 — RFC 0005 v2에서 결정.
2. 2PC 의 *cohort heartbeat* 를 별도 sidecar 컨테이너로 둘지, manager 내부 goroutine으로 둘지? Phase 3 진입 시 결정.
3. SQL parser 도입 시 의존성: pg_query_go (PostgreSQL License) vs 자체 lexer? Phase 2C 진입 시 결정.
4. Reference table 의 *동기* 복제 vs *준동기*? trigger-based 동기는 write throughput 제한. Phase 2B 결정.

## 10. 시간선

본 RFC는 **roadmap**이 아닌 **path**다. 시간 추정은 minimum이며, 단일 contributor 진행 시 2~3배 늘어날 수 있다. 0.2.0-alpha 시점에 시간 약속을 하지 않는다.

진행 상황은 `docs/roadmap.md` 의 Pillar P11 (분산 SQL) 섹션에서 추적한다.
