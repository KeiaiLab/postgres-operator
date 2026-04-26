# ADR 0001 — Citus 표준 토폴로지 + Stateless QueryRouter 계층 도입

- **상태**: Accepted
- **날짜**: 2026-04-26
- **결정자**: @keiailab/maintainers
- **대체**: 기존 "MongoDB sharded topology on Citus" ADR을 폐기하고 본 ADR로 대체. 사유는 "재고 결과" 절 참조.
- **관련**: ADR 0002 (Patroni 미사용), ADR 0003 (QueryRouter 책임 분리)

## 컨텍스트

PostgreSQL Kubernetes Operator 생태계에서 Citus 분산 토폴로지를 1급 시민으로 다루는 Apache 2.0 + Go 오퍼레이터는 비어 있다. 본 프로젝트의 차별화 포인트를 명확히 정의해야 한다.

초기 설계 시 "MongoDB sharded cluster 토폴로지(`mongos / config server RS / shard RS`)를 Citus 위에 매핑한다"고 선언했으나, 자체 비판적 검토 결과 다음 문제가 드러났다:

1. **명명 충돌**: "shard"라는 단어가 Citus의 hash range(논리적 단위, 보통 32개)와 MongoDB의 shard replica set(물리적 노드 그룹)이라는 다른 계층의 의미로 동시에 사용되어 혼란 유발.
2. **중복 표현**: "Config Server Set"과 "Shard Set"은 본질적으로 Citus의 coordinator + worker 표준 토폴로지에 MongoDB식 명명을 덧씌운 것에 가까움. 메타데이터 권위 분리는 Citus coordinator가 이미 수행.
3. **진짜 차별화는 한 군데에 있음**: stateless query router 계층은 Citus가 기본 제공하지 않으며, MongoDB 모델에서 빌린 가치 중 본질적으로 새로운 것은 이 부분이 거의 전부.

## 결정

본 오퍼레이터는 다음을 채택한다.

### 토폴로지 명명 — Citus 표준 유지

- **Coordinator** (HA replica set): Citus 표준 coordinator 역할. `pg_dist_*` 메타데이터 권위 보관, 분산 DDL 게이트웨이. 1+ standby로 HA.
- **Worker** (HA replica set per pool): Citus 표준 worker 역할. 분산 테이블의 실제 shard 보유. 각 pool은 자체 streaming replication primary election.
- **QueryRouter** (신규, stateless): Citus 11+ `metadata_synced=true` PG 인스턴스 + PgBouncer 사이드카. 무상태, HPA 지원. **본 오퍼레이터의 핵심 차별화**.

### CRD 루트 표현

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
spec:
  coordinator: { members, storage, ... }       # 단수, HA RS
  workers: [ { name, members, ... }, ... ]     # 다수 pool
  routers: { replicas, pgbouncer, ... }        # 신규 stateless 계층
```

### 부가 CRD 명명 — Citus 용어로 일관

- `RebalanceJob` (이전: `ShardBalancer`) — `citus_rebalance_start` 래퍼 + window 스케줄러
- `ShardPlacementPolicy` (이전: `ZoneSharding`) — `citus_set_node_property` + tag-aware placement
- `DistributedTable`, `ReferenceTable` — 그대로 (이미 Citus 용어)

## 근거

### Citus 표준 명명을 유지하는 이유
- **PG/Citus 운영자에게 친숙**: 학습곡선 최소화. CR 필드명이 그대로 Citus 함수명/시스템 카탈로그명에 매핑.
- **명명 충돌 제거**: "shard"는 본 오퍼레이터에서 오직 Citus의 hash range만을 의미. "Shard Set" 같은 다른 계층 의미 사용 없음.
- **정직성**: MongoDB 명명을 빌리는 것은 마케팅 가치가 있을 수 있으나, 실제 동작은 Citus 표준이라는 점을 흐림. 정직한 포지셔닝이 장기 신뢰에 유리.

### QueryRouter 계층 도입의 정당성 (본 프로젝트의 핵심 차별화)
- Citus는 기본적으로 coordinator가 쿼리 라우터 + 메타데이터 권위 + (옵션) 데이터를 겸직.
- 라우터 부하가 크거나 connection 폭주가 있을 때 coordinator가 단일점이 됨.
- Citus 11+ metadata syncing은 모든 워커가 메타데이터 사본을 갖게 만들었지만, 이를 **stateless 라우터 풀**로 운영하는 표준화된 방법은 없음.
- 본 오퍼레이터는 `QueryRouter` 계층을 1급 CR로 두어:
  - HPA 기반 자유 수평확장
  - PgBouncer 사이드카로 connection multiplex
  - coordinator round-trip 제거 (router 안에서 분산 쿼리 플래닝)
  - Pod 재기동 무손실 (PVC 없음)

### 기존 PG 오퍼레이터가 못 하는 것
- CloudNativePG / Zalando: Citus 통합은 있지만 stateless router 계층 부재.
- StackGres `SGShardedCluster`: 토폴로지 1급 표현은 있으나 AGPL + Java + 라우터 분리 부재.
- 결과: **"Citus + stateless QueryRouter"** 조합이 Apache 2.0 Go 오퍼레이터로서 차별화 핵심.

## 재고 결과 (왜 MongoDB 모델을 폐기했나)

초기 plan에서 MongoDB 토폴로지 차용을 선언했으나 다음 비판이 정당함을 인정:

| 매핑 | 본질 | 평가 |
|---|---|---|
| CSS (Config Server Set) | Citus coordinator + sync standby의 다른 이름 | 명명만 다름, 중복 |
| SS (Shard Set) | Citus worker pool + HA의 다른 이름 | 명명만 다름, 중복 |
| Router (mongos analog) | Citus가 기본 제공 안 하는 stateless 라우터 | **진짜 가치** |
| ShardBalancer | `citus_rebalance_start` + window | window 강제는 가치, 명칭은 Mongo 차용 |
| ZoneSharding | `citus_set_node_property` + tag | 동작은 Citus, 명칭은 Mongo 차용 |

핵심: MongoDB 모델에서 빌린 가치의 70%는 마케팅이고 30%(Router 분리)만 본질적 기여. 따라서 **Router 분리는 유지**, **나머지 명명은 Citus 표준으로 정렬**한다.

## 트레이드오프

- **마케팅 임팩트 감소**: "MongoDB sharded cluster on Citus"라는 슬로건은 매력적이지만 정확도가 떨어짐. "Citus를 K8s native로 만드는 오퍼레이터, 핵심은 stateless QueryRouter"가 정직.
- **Mongo 운영자 친화성 상실**: 그러나 PG/Citus 운영자가 주 청중이므로 영향 미미.
- **위에서 약속한 ZoneSharding 등의 이름이 바뀜**: alpha 단계라 외부 영향 없음.

## 강제 메커니즘

- README, docs, CRD 필드명, 컨트롤러 명명 모두 Citus 용어로 통일
- `Coordinator` / `Worker` / `QueryRouter` 외의 계층 분리는 도입하지 않음
- 명명 변경 시 RFC 필수

## 결과

- Phase 1부터 `PostgresCluster.spec.{coordinator, workers[], routers}` CRD로 구현
- 핵심 차별화 메시지: **"Citus + Stateless QueryRouter, Apache 2.0 Go operator"**
- 본 ADR 변경은 RFC 필수
