# ADR-0025: Repmgr/PgBouncer/Barman 통합 — bitnami parity

- **Date**: 2026-05-14
- **Status**: Proposed
- **Authors**: @phil

## Context

bitnami/postgresql-ha chart 는 다음 3 component 를 *single chart* 로 통합한다:
1. **Repmgr** — 자동 failover + replication topology 관리 (PostgreSQL extension)
2. **PgBouncer** — connection pooling (transaction-level + session-level mode)
3. **Barman** — backup/restore (full + incremental + PITR)

keiailab/postgres-operator (v0.3.0-alpha) 는 *self-built distributed SQL* (ADR-0001) keystone 기반으로 *coordinator + worker pool + stateless router* 토폴로지를 자체 구현 중. 다만 *operational quality* 측면에서 bitnami parity 도달 위해 위 3 component 동등 또는 초과 기능 필요.

본 ADR 는 v0.3.0-beta 격상 시 통합 계획 결정.

## Decision

**Phase A** (v0.3.0-beta 진입): Repmgr + PgBouncer + Barman 등가 기능을 *PostgresCluster CR + 자체 controller* 으로 구현.

### Repmgr 등가 (Phase A.1)
- `internal/controller/failover/` (T29 진행) 의 *Promoter interface* 확장
- *3-node sentinel* 패턴: primary + sync replica + async replica
- Lease-based leader election + WAL position 기반 promotion (PostgreSQL 18 의 logical replication slot 활용)
- Bitnami parity: 자동 failover ≤ 30s

### PgBouncer 등가 (Phase A.2)
- `PostgresCluster.spec.pooler.enabled = true` toggle
- *sidecar pgbouncer container* 또는 *별 Pooler CR* (선택)
- transaction-level pooling default + session-level toggle
- Bitnami parity: 1000+ concurrent connection 처리

### Barman 등가 (Phase A.3)
- `BackupJob CR` 확장: full + incremental + WAL archive
- PITR target_time / target_xid / target_name 지원
- S3 + GCS + Azure Blob backup target (별 `BackupTarget CR`)
- Bitnami parity: cross-region replication + retention policy

**Phase B** (v1.0.0 GA 격상): 위 3 기능의 e2e test + production smoke + 라이브 ArgoCD verify

## Consequences

### 긍정
- bitnami/postgresql-ha 동등 features → migration 진입 장벽 0
- chart users 가 *익숙한 toggle* 만으로 enterprise PG 운영
- 자체 controller 라 *plugin-based extension* (ADR-0001 의 Pillar P13)

### 부정
- Phase A.1~A.3 = ~3 cycle 작업 (cycle 26~28 estimate)
- *Bitnami 와 정확히 동일 behavior* 보장 어려움 — *interface compatibility* 만 보장

### Trade-off
- 자체 구현 vs Repmgr/PgBouncer fork: ADR-0003 가 *외부 operator/backend fork 금지* 명시. 자체 구현 path 선택.

## Alternatives Considered

- **A1. Repmgr fork**: AGPL/BUSL 의존성 영구 제거 (ADR-0003) — Rejected.
- **A2. CrunchyData PGO 통합**: 외부 operator 의존 — Rejected.
- **A3. 본 ADR**: 자체 구현 + bitnami parity — **Accepted**.

## References

- ADR-0001 self-built distributed SQL keystone
- ADR-0003 외부 operator/backend fork 금지
- bitnami/postgresql-ha chart values (parity reference)
- keiailab/postgres-operator T29 (AutoTLS) + T30 (PG18/PG17 HA smoke)
