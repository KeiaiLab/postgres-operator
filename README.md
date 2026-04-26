# keiailab/postgres-operator

> **Citus 분산 PostgreSQL을 K8s native로 만드는 오퍼레이터 — 핵심 차별화는 Stateless QueryRouter 계층**

Apache 2.0 라이선스의 오픈소스 프로젝트. PostgreSQL + Citus extension 기반 분산 클러스터를 단일 `PostgresCluster` CR로 선언적으로 운영합니다. Citus 표준 토폴로지(coordinator + workers)에 **stateless QueryRouter 계층** 하나를 추가해 라우팅을 무상태로 수평확장 가능하게 만든 것이 본 프로젝트의 본질적 기여입니다.

상태: **alpha (개발 중, Phase 0)**. PR/Issue 환영합니다.

---

## 정직한 포지셔닝

본 프로젝트는 두 가지 일을 한다:

1. **Citus를 K8s에서 1급 시민으로 운영** — `PostgresCluster` CR, 메타데이터 자동 sync, 선언적 분산 테이블, PITR 정합성, shard rebalance 등 전부 declarative.
2. **Stateless QueryRouter 계층 추가** — Citus 표준엔 없는, HPA로 수평확장 가능한 무상태 라우터 풀. 본 프로젝트의 진짜 새로움.

> 초기 설계에서 "MongoDB sharded cluster on Citus"라는 토폴로지 모델을 차용했으나, 자체 비판적 검토 결과 (a) "shard"라는 단어의 의미 충돌, (b) Config Server Set / Shard Set은 사실상 Citus 표준의 재명명에 불과한 점이 드러나 폐기했습니다. 자세한 사유는 [ADR 0001](docs/adr/0001-stateless-query-router-on-citus.md) 참조.

---

## 왜 또 다른 PostgreSQL Operator인가

| 비교 대상 | Citus 통합 | Stateless 라우터 분리 | 라이선스/스택 |
|---|---|---|---|
| CloudNativePG | 플러그인(여러 Cluster CR 묶음) | ✗ | Apache 2.0 / Go |
| Zalando postgres-operator | `citus.{group, cluster}` 필드 | ✗ | MIT / Go |
| Crunchy PGO / Percona | ✗ | ✗ | Apache 2.0 / Go |
| StackGres `SGShardedCluster` | 1급 표현 | ✗ | **AGPL-3.0** / Java |
| **keiailab/postgres-operator** | **1급 표현** | **○** | **Apache 2.0 / Go** |

차별화의 무게중심은 **Stateless QueryRouter** 한 곳입니다. 그 외 declarative 분산 테이블, PITR 정합성, 자동 메타데이터 sync 등은 Citus를 K8s native로 만들기 위한 필수 기능이지 차별화 자체는 아닙니다.

---

## 토폴로지 (Citus 표준 + QueryRouter 계층)

```
                      ┌──────────────────────────┐
                      │   App / Client           │
                      └───────────┬──────────────┘
                                  │ libpq (TLS)
                  ┌───────────────▼───────────────┐
                  │   QueryRouter (stateless)     │  무상태, HPA, PgBouncer 사이드카
                  │   metadata_synced=true PG     │  pg_dist_* 캐시, PVC 없음
                  │   본 프로젝트의 핵심 차별화   │  본 프로젝트가 추가한 신규 계층
                  └───────────────┬───────────────┘
                                  │
       ┌──────────────────────────┼─────────────────────────┐
       │                          │                         │
┌──────▼──────────┐       ┌───────▼────────┐        ┌───────▼────────┐
│  Coordinator    │       │  Worker pool A │        │  Worker pool B │
│  (HA RS)        │       │  (HA RS)       │        │  (HA RS)       │
│  pg_dist_* 권위 │       │  shard 보유    │        │  shard 보유    │
│  DDL 게이트웨이 │       │  자체 election │        │  자체 election │
└─────────────────┘       └────────────────┘        └────────────────┘
   sync replication        streaming replication       streaming replication
```

| 계층 | 역할 | HA | 출처 |
|---|---|---|---|
| **QueryRouter** | 분산 쿼리 라우팅, PgBouncer 통합, HPA 수평확장 | 무상태 (Pod 재기동 무손실) | **본 프로젝트 신규** |
| **Coordinator** | `pg_dist_*` 메타데이터 권위, DDL 게이트웨이 | streaming replication + lease election | Citus 표준 |
| **Worker** | 분산 테이블 shard 보유 | streaming replication + lease election | Citus 표준 |

자세한 책임 경계는 [ADR 0003](docs/adr/0003-queryrouter-stateless-design.md) 참조.

---

## 핵심 기능 (계획)

- **선언적 토폴로지**: `PostgresCluster` 단일 CR로 Coordinator + Worker pools + QueryRouter 표현
- **자동 메타데이터 동기화**: `pg_dist_node` ↔ K8s Endpoints drift 감지/복원
- **HA**: K8s API as DCS (Patroni 미사용, [ADR 0002](docs/adr/0002-no-patroni-instance-manager.md))
- **PITR 정합성**: `citus_create_restore_point` 2PC로 분산 named restore point 강제
- **`RebalanceJob`**: `citus_rebalance_start` 래퍼 + window 스케줄
- **`ShardPlacementPolicy`**: `citus_set_node_property` + tag-aware placement
- **선언적 분산 테이블**: `DistributedTable` / `ReferenceTable` CRD
- **Schema-based sharding**: Citus 12+ 자동 SaaS 멀티테넌시
- **백업 plugin**: pgBackRest / WAL-G / Barman 추상화
- **PG 16/17/18 지원**: Citus 호환 매트릭스 자동 추적 (`upstream-watch.yml`)

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
kubectl apply -f examples/dev-cluster.yaml      # Coordinator×1 + Worker×1 + QueryRouter×1 (5분 quickstart)
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
