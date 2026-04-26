# ADR 0003 — QueryRouter 계층의 Stateless 설계

- **상태**: Accepted
- **날짜**: 2026-04-26
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001 (Citus 표준 + QueryRouter), ADR 0002 (Patroni 미사용)

## 컨텍스트

ADR 0001에서 본 오퍼레이터의 핵심 차별화로 **stateless QueryRouter 계층**을 채택했다. 본 ADR은 이 계층의 책임 경계와 구현 제약을 정의한다.

QueryRouter의 책임 경계가 모호하면 (1) reconciler 간 race condition, (2) 메타데이터 stale 라우팅, (3) "어디서 무엇을 해야 하는지" 운영자 혼란이 발생한다.

## 결정

### QueryRouter는 무상태(stateless)다

**해야 할 일**:
- Citus 11+ `metadata_synced=true` PG 인스턴스로 분산 쿼리 플래닝 수행
- PgBouncer 사이드카로 connection multiplex (transaction pooling 디폴트)
- HPA 기반 수평확장 (CPU/메모리 또는 custom metric)
- 클라이언트 인증 종단 (SCRAM-SHA-256, mTLS)
- `pg_dist_*` 캐시 staleness 모니터링: `router_metadata_lag_seconds` 메트릭 노출

**하지 말아야 할 일**:
- PVC 보유 금지 (Pod 재기동 무손실 보장)
- DDL 직접 실행 금지 → Coordinator로 라우팅
- Shard 데이터 보유 금지 (`shouldhaveshards=false` 강제)
- streaming replication 참여 금지 (RS 멤버가 아님)
- K8s lease 보유 금지 (election 미참여)

### Coordinator는 메타데이터 권위 + DDL 게이트웨이

- `pg_dist_*` 시스템 카탈로그의 **유일한 쓰기 경로**
- `create_distributed_table`, `alter_distributed_table` 등 모든 분산 DDL의 진입점
- HA: 1+ sync standby (CNPG 스타일 instance manager + K8s lease)
- 데이터 shard 보유 여부는 Citus 표준 그대로 (디폴트 `shouldhaveshards=false` 권장하나 강제 아님)

### Worker는 데이터 책임

- 분산 테이블의 실제 shard 보유 (`shouldhaveshards=true`)
- pool별 streaming replication 자체 election
- 새 primary 결정 시 instance manager가 Coordinator에 `citus_update_node` 호출
- 메타데이터 사본 보유(Citus metadata sync, read-only로 취급)

## 강제 메커니즘

1. **Validating Webhook**:
   - QueryRouter spec에 `storage` 필드가 있으면 거절(무상태)
   - `coordinator.members`가 짝수면 거절(split-brain 방지)
2. **Reconciler 자동 교정**:
   - QueryRouter Pod에 `shouldhaveshards=true`가 감지되면 자동 교정 + 경고 이벤트
   - QueryRouter Pod에 PVC가 마운트되면 거절
3. **DDL 라우팅**:
   - `DistributedTableReconciler`는 직접 Worker에 DDL을 보내지 않고 Coordinator primary로 라우팅
4. **Election 격리**:
   - K8s lease는 RS 단위(`<cluster>-coordinator-primary`, `<cluster>-worker-<pool>-primary`)
   - QueryRouter는 lease 미보유

## 근거

- **단일 책임 원칙**: 라우팅(QueryRouter) / 메타데이터+DDL(Coordinator) / 데이터(Worker) 분리.
- **수평확장 자유도**: 라우팅 부하만 늘어나는 워크로드(앱 서버 fan-out)에서 QueryRouter만 HPA로 늘리면 됨. Coordinator/Worker 영향 없음.
- **장애 격리**: QueryRouter Pod 재기동/장애는 데이터 손실 0, Coordinator/Worker 영향 0.
- **운영자 직관**: "라우팅 문제? QueryRouter 본다. 메타데이터 문제? Coordinator 본다. 데이터 문제? Worker 본다."

## 트레이드오프

- **연결 hop 증가**: Client → QueryRouter → Worker (Coordinator를 거치지 않으므로 1 hop. PgBouncer transaction pooling으로 connection 비용 분산)
- **router metadata stale 위험**: Citus metadata sync lag으로 잘못된 shard 라우팅 가능
  - **완화**: `router_metadata_lag_seconds` 임계 초과 시 readiness 실패 + 알람
- **계층 추가에 따른 운영 표면적 증가**:
  - **완화**: `spec.deployment: development` 모드는 routers.replicas=1 + coordinator 단일 + worker 1 pool로 quickstart 5분 보장

## 결과

- `cmd/router/main.go` 별도 바이너리, distroless 이미지로 패키징
- Service 노출: `<cluster>-router` (ClusterIP/LoadBalancer)
- 본 ADR 변경은 RFC 필수
