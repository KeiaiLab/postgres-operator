# ADR 0001 — Citus 위에 MongoDB Sharded Cluster 토폴로지 채택

- **상태**: Accepted
- **날짜**: 2026-04-26
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0002, ADR 0003

## 컨텍스트

PostgreSQL Kubernetes Operator 생태계는 성숙했지만, Citus(분산 PG extension)를 1급 시민으로 다루는 Apache 2.0 + Go 오퍼레이터는 비어 있다. 기존 시도들의 한계:

- **단일 coordinator 모델** (Citus 권장 표준): coordinator가 메타데이터 권위 + 쿼리 라우팅 + 일부 데이터까지 겸직 → (1) 라우팅 단일점, (2) failover 폭발 반경이 클러스터 전체로 확산, (3) 쿼리 라우터의 수평확장 불가.
- **CloudNativePG의 Citus 플러그인**: 여러 `Cluster` CR을 외부에서 묶음. 토폴로지가 1급 표현 아님.
- **StackGres `SGShardedCluster`**: 1급 표현은 있으나 AGPL-3.0 + Java 스택으로 임베딩/기여 진입장벽 높음.

운영 경험상 **MongoDB sharded cluster의 3계층 모델**(`mongos / config server replica set / shard replica set`)은 다음을 production에서 검증했다:

- 라우터(`mongos`)는 무상태 → 자유로운 수평확장
- config server는 메타데이터 부하만 받음 → 작은 RS로도 충분
- 각 shard RS는 자체 election → failover 도메인이 RS 단위로 격리

## 결정

본 오퍼레이터는 MongoDB의 3계층 책임분리를 Citus에 다음과 같이 매핑한다:

| MongoDB | 본 오퍼레이터 |
|---|---|
| `mongos` | **Router** — 무상태 PG (`metadata_synced=true`) + PgBouncer 사이드카, HPA 지원 |
| `config server replica set` | **Config Server Set (CSS)** — PG sync RS 3-member, `pg_dist_*` 권위, 데이터 shard 없음 |
| `shard replica set` | **Shard Set (SS)** — PG streaming RS, 자체 election, 실제 shard 보유 |

CRD 표현: `PostgresCluster.spec.{configServers, shards[], routers}`.

## 근거

1. **failover 폭발 반경 격리**: SS 한 개의 election 사고는 그 SS의 shard만 영향. CSS는 별개 RS이므로 메타데이터는 멀쩡.
2. **무상태 라우터**: HPA로 자유 확장, Pod 재기동 무손실. `mongos`가 검증한 모델.
3. **Citus 11+ metadata syncing**과의 자연스러운 정합: 모든 워커가 메타데이터 사본을 가지므로 router 안의 PG도 동일 메커니즘으로 caching.
4. **시장 차별화**: Apache 2.0 + Go + Mongo 토폴로지 조합은 비어 있다. CNPG가 Citus 통합을 강화해도 토폴로지 1급 표현·`ZoneSharding`·`ShardBalancer`로 차별화 유지.

## 트레이드오프

- **최소 클러스터 크기 증가**: production에서 CSS×3 + SS×3 + Router×2 = 최소 11 Pod. 학습곡선 우려.
  - **완화**: `spec.deployment: development | production` 필드. development는 CSS×1 + SS×1 + Router×1 (최소 3 Pod). 5분 quickstart 가능.
- **단일 coordinator보다 운영 표면적 크다**: 모니터링/알람 대상이 3계층으로 늘어남.
  - **완화**: Phase 10 관측성에서 토폴로지 뷰 대시보드 + 5종 알람 표준 제공.
- **Citus의 비표준 구성**: 공식 Citus 가이드는 단일 coordinator 모델. CSS/Router 분리는 비공식.
  - **완화**: 본질적으로 metadata-synced 워커를 두 그룹(메타데이터-only, router-only)으로 운영하는 것이며, Citus 11+의 정상 동작 범위 내.

## 대안 (검토 후 기각)

- **단일 coordinator + HA standby 1**: CNPG/Zalando 스타일. 라우터 수평확장 불가, failover 시 라우팅 중단.
- **coordinator 자체를 N개 stateful로 운영**: Citus는 단일 메타데이터 권위 가정 → 분산 합의 구현 비용 과다.
- **모든 노드를 동일 역할로**: metadata-synced 모드로 모두 동일 권한이지만, DDL 게이트웨이/책임 분리가 불명확해져 운영 혼란.

## 결과

- `PostgresCluster` 루트 CRD에 `configServers / shards[] / routers` 3계층 명시
- Phase 1~3에서 3계층 부트스트랩, election, metadata 동기화 구현
- 본 ADR의 결정은 `GOVERNANCE.md` 절차로만 변경 가능 (RFC 필수)
