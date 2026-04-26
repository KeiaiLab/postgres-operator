# keiailab/postgres-operator

> **MongoDB Sharded Cluster 아키텍처를 차용한 Citus 분산 PostgreSQL Kubernetes Operator**

Apache 2.0 라이선스의 오픈소스 프로젝트입니다. PostgreSQL + Citus extension을 기반으로 하되, 토폴로지 모델은 MongoDB의 **3계층 sharded cluster**(`mongos / config server replica set / shard replica set`)를 차용해 책임 분리와 운영 단순성을 달성합니다.

상태: **alpha (개발 중, Phase 0)**. PR/Issue 환영합니다.

---

## 왜 또 다른 PostgreSQL Operator인가

기존 PG 오퍼레이터(CloudNativePG, Zalando, Crunchy PGO, StackGres, Percona)는 모두 단일/스트리밍 복제 모델에 초점이 맞춰져 있고, **Citus 분산 토폴로지를 1급 시민으로 다루는 Apache 2.0 + Go 오퍼레이터는 비어 있습니다**. 본 프로젝트는 그 공백을 채우되, 단순히 "Citus를 K8s에 배포"하는 수준이 아니라 **MongoDB가 production에서 검증한 3계층 책임분리 모델**을 가져옵니다.

| 비교 대상 | Citus 통합 수준 | 토폴로지 1급 표현 | 라이선스/스택 |
|---|---|---|---|
| CloudNativePG | 플러그인(여러 Cluster CR 묶음) | ✗ | Apache 2.0 / Go |
| Zalando postgres-operator | `citus.{group, cluster}` 필드 | ✗ | MIT / Go |
| StackGres `SGShardedCluster` | 1급 표현 | ○ | **AGPL-3.0** / Java |
| **keiailab/postgres-operator** | **1급 표현 + Mongo 토폴로지** | **○** | **Apache 2.0 / Go** |

---

## 아키텍처 — MongoDB Sharded Cluster on Citus

```
                    ┌──────────────────────────┐
                    │   App / Client           │
                    └───────────┬──────────────┘
                                │ libpq
                ┌───────────────▼───────────────┐
                │   Router (mongos analog)      │  무상태, HPA, PgBouncer 사이드카
                │   metadata_synced=true PG     │  pg_dist_* 캐시, 데이터 0
                └───────────────┬───────────────┘
                                │
       ┌────────────────────────┼────────────────────────┐
       │                        │                        │
┌──────▼──────┐         ┌───────▼───────┐         ┌──────▼──────┐
│ ConfigSvr   │         │  Shard Set A  │         │ Shard Set B │
│  Set (CSS)  │ ◀──────▶│  (PG RS×N)    │         │ (PG RS×N)   │
│  PG RS×3    │ metadata│  shouldhave-  │         │ shouldhave- │
│  pg_dist_*  │  sync   │  shards=true  │         │ shards=true │
│  권위        │         │  실제 shard   │         │ 실제 shard  │
└─────────────┘         └───────────────┘         └─────────────┘
```

| 계층 | MongoDB 대응 | 역할 |
|---|---|---|
| **Router** | `mongos` | 무상태 쿼리 라우터, HPA로 수평확장, PgBouncer 통합 |
| **Config Server Set (CSS)** | `config server replica set` | 메타데이터 권위 RS (3-member sync), `pg_dist_*` 보유, 데이터 shard 없음 |
| **Shard Set (SS)** | `shard replica set` | 데이터 RS (각자 election), 실제 shard 보유, zone-aware 가능 |

---

## 핵심 기능 (계획)

- **선언적 분산 토폴로지**: `PostgresCluster` CR 하나로 CSS + SS[] + Router 전체 표현
- **자동 메타데이터 동기화**: `pg_dist_node` ↔ K8s Endpoints drift 감지/복원
- **RS 단위 자동 failover**: K8s API as DCS, Patroni 미사용 (CNPG 모델)
- **PITR 정합성**: `citus_create_restore_point` 2PC로 분산 named restore point 강제
- **Shard rebalancer**: Mongo balancer 모델 (window 스케줄, online/blocking)
- **Zone-aware sharding**: tag→SS 매핑 (Mongo zone 차용)
- **선언적 분산 테이블**: `DistributedTable` / `ReferenceTable` CRD
- **Schema-based sharding**: Citus 12+ 자동 SaaS 멀티테넌시
- **백업 plugin 인터페이스**: pgBackRest / WAL-G / Barman 추상화
- **PG 16/17/18 지원**: Citus 호환 매트릭스 자동 추적

---

## 지원 매트릭스

| PostgreSQL | Citus | 상태 |
|---|---|---|
| 16 | 12.1+ / 13.0+ | **Stable Tier 1** (예정) |
| 17 | 13.0+ | **Stable Tier 1** (예정) |
| 18 | Citus PG18 호환 마이너 발표 시점 | **Beta** (`preview-pg18` 채널) |

PG18 활성화: `--feature-gates=PostgresEighteen=true`. Citus 호환 발표는 `.github/workflows/upstream-watch.yml`이 자동 추적합니다.

---

## Quickstart (계획, Phase 1 완료 시 활성화)

```bash
kubectl apply -f https://github.com/keiailab/postgres-operator/releases/latest/download/install.yaml
kubectl apply -f examples/dev-cluster.yaml
kubectl port-forward svc/orders-router 5432:5432
psql "host=localhost port=5432 dbname=app user=app sslmode=require"
```

---

## 로드맵

전체 14개 Phase, 약 10개월. 자세한 계획은 [`docs/roadmap.md`](docs/roadmap.md), 설계 결정은 [`docs/adr/`](docs/adr/) 참조.

---

## 기여하기

- 행동강령: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1)
- 기여 절차: [CONTRIBUTING.md](CONTRIBUTING.md) (DCO sign-off 필수)
- 거버넌스: [GOVERNANCE.md](GOVERNANCE.md) (RFC 절차 포함)
- 보안 신고: [SECURITY.md](SECURITY.md) (90일 비공개 윈도우)
- 메인테이너: [MAINTAINERS.md](MAINTAINERS.md)

`good first issue` 라벨이 붙은 이슈로 시작하시는 것을 권장합니다.

---

## 라이선스

[Apache License 2.0](LICENSE) © 2026 keiailab. 자세한 내용은 [NOTICE](NOTICE) 참조.

> 본 프로젝트는 MongoDB의 sharded cluster 아키텍처를 **모델**로 참조하지만, MongoDB의 코드나 독점 사양을 포함하지 않습니다.
