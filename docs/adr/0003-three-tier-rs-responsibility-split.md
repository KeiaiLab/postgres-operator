# ADR 0003 — 3계층 RS 책임 분리 (CSS / SS / Router)

- **상태**: Accepted
- **날짜**: 2026-04-26
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001, ADR 0002

## 컨텍스트

ADR 0001에서 MongoDB 토폴로지 모델 채택을 결정했지만, "각 계층이 정확히 무엇을 하고 무엇을 하지 않는가"는 별도의 명시가 필요하다. 책임 경계가 모호하면 (1) reconciler 간 race condition, (2) split-brain 위험, (3) 운영자의 "어디로 가서 무엇을 해야 하는지" 혼란이 발생한다.

## 결정

세 계층의 책임을 다음과 같이 정확히 분리한다.

### Config Server Set (CSS) — 메타데이터 RS

**해야 할 일**:
- `pg_dist_node`, `pg_dist_shard`, `pg_dist_placement`, `pg_dist_partition`, `pg_dist_colocation`, `pg_dist_object` 권위 보관
- 분산 DDL 게이트웨이: `create_distributed_table`, `alter_distributed_table`, `create_reference_table` 등은 항상 CSS primary에서 실행
- `citus_add_node`/`citus_update_node`/`citus_remove_node` 호출 진입점
- 3-member sync replication, `synchronous_commit = remote_apply`
- Operator의 메타데이터 drift 감지 read 대상

**하지 말아야 할 일**:
- 데이터 shard 보유 (`citus_set_node_property(..., 'shouldhaveshards', false)` 강제)
- 응용 OLTP 쿼리 처리 (Router로 라우팅)
- 백그라운드 잡(rebalance 등) 실행 단일점 (background worker는 모든 노드 분산)

### Shard Set (SS) — 데이터 RS

**해야 할 일**:
- 분산 테이블의 실제 shard 보유 (`shouldhaveshards=true`)
- 자체 streaming replication primary election (RS 단위 격리)
- 새 primary 결정 시 instance manager가 CSS primary에 `citus_update_node` 호출
- 메타데이터 사본 보유 (Citus 11+ metadata sync, read-only로 취급)
- zone tag 기반 데이터 격리 가능

**하지 말아야 할 일**:
- DDL 직접 실행 (반드시 CSS primary 경유)
- 메타데이터 직접 수정 (read-only로 취급)
- Cross-shard 분산 쿼리의 진입점 (Router 경유)

### Router — 무상태 라우터 (mongos analog)

**해야 할 일**:
- `metadata_synced=true` PG 인스턴스로 분산 쿼리 플래닝
- PgBouncer 사이드카로 connection multiplex
- HPA 기반 수평확장
- 클라이언트 인증 종단 (SCRAM, mTLS)
- `pg_dist_*` 캐시 staleness 모니터 (`router_metadata_lag_seconds`)

**하지 말아야 할 일**:
- PVC 보유 (무상태)
- DDL 실행 (CSS로 라우팅)
- shard 데이터 보유 (`shouldhaveshards=false`)
- election 참여 (RS가 아님)

## 강제 메커니즘

1. **Validating Webhook**:
   - `configServers.members`가 짝수면 거절 (split-brain 방지)
   - `deployment=production`인데 `members<3`이면 거절
2. **Reconciler 검증**:
   - CSS Pod에 `shouldhaveshards=true`가 감지되면 자동 교정 + 경고 이벤트
   - Router Pod에 데이터 shard가 감지되면 자동 교정
3. **DDL 라우팅**:
   - `DistributedTableReconciler`가 직접 SS에 DDL을 보내지 않고 CSS primary로 라우팅
4. **Election 격리**:
   - K8s lease는 RS 단위로 별개 (`<cluster>-css-primary`, `<cluster>-shard-a-primary`, ...)
   - Router는 lease 미보유

## 근거

- **단일 책임 원칙**: 계층 간 책임이 겹치지 않으면 reconciler 로직이 단순.
- **failover 격리**: SS 한 개의 사고가 CSS/Router로 전파되지 않음.
- **운영자 직관**: "메타데이터 문제? CSS 본다. 라우팅 문제? Router 본다. 데이터 문제? SS 본다."
- **MongoDB 운영 패턴**과 동일 → 기존 Mongo 운영 경험을 그대로 가져올 수 있음.

## 트레이드오프

- **계층 간 통신 hop 증가**: Client → Router → SS (Mongo와 동일 모델, 일반적으로 µs 단위)
  - **완화**: PgBouncer transaction pooling으로 connection 비용 분산
- **CSS DDL 직렬화**: 모든 DDL이 CSS primary 단일 경로
  - **완화**: DDL은 본질적으로 드문 작업 + Citus가 워커로 비동기 전파

## 결과

- 위 책임 분리는 코드 레벨에서 강제 (webhook + reconciler 자동 교정)
- 각 reconciler는 자신의 계층 외 객체를 직접 수정 금지
- 본 ADR 변경은 RFC 필수
