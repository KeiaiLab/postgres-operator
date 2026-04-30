# keiailab/postgres-operator

> **PGO 수준의 단일 PG HA 운영 품질 + Citus 분산 토폴로지 1급 + 플러그인 SDK 기반 확장성을 한 번에 제공하는 Apache-2.0 Go 오퍼레이터**

상용 제품 수준의 오픈소스 PostgreSQL 쿠버네티스 오퍼레이터를 목표로 합니다. 단일 PG HA 운영(HA, 백업/PITR, 풀러, 모니터링, 보안, 업그레이드)은 Crunchy PGO 수준 품질을 자체 코드로 제공하며, 그 위에 **Citus 분산 토폴로지 1급 지원**과 **플러그인 SDK 기반 확장성**을 차별화로 둡니다.

상태: **alpha (개발 중, Phase 0)**. 외부 PR/Issue/Pillar 오너 컨트리뷰터 환영.

> **단일 PG HA만 필요한 경우** Crunchy PGO 또는 CloudNativePG 사용을 권장합니다. **Citus 분산 PG가 1급으로 필요하거나, 백업·exporter·extension·라우터를 플러그인으로 확장하고 싶은 팀**이 본 프로젝트의 청중입니다.

---

## 미션 — 3축

본 프로젝트는 다음 세 가지 일을 한다 ([ADR 0001 v2](docs/adr/0001-stateless-query-router-on-citus.md), [ADR 0004](docs/adr/0004-build-not-fork-or-layer.md) 참조):

1. **PGO-class 풀스택 (기본 품질)** — HA, pgBackRest 백업/PITR, PgBouncer 풀러, pgMonitor 호환 관측, TLS/mTLS, in-place + blue/green 업그레이드, 멀티 K8s standby. **모두 자체 코드** (Pillar P1~P10, P14).
2. **Citus 1급 (차별화 1)** — `coordinator + workers[]` 단일 CR, `pg_dist_node` 자동 sync, 선언적 `DistributedTable`/`ReferenceTable`/`RebalanceJob`/`ShardPlacementPolicy`, **분산 PITR 2PC 조정자**, **Stateless QueryRouter** 계층 (Pillar P11~P12).
3. **Plugin SDK (차별화 2, 메타)** — `BackupPlugin`/`ExporterPlugin`/`ExtensionPlugin`/`RouterPlugin`/`AuthPlugin` 5종 Go 인터페이스. 새 백업 도구 추가 = 인터페이스 구현 1주. in-process + gRPC over UDS 두 모델 (Pillar P13).

---

## 왜 또 다른 PostgreSQL Operator인가

| 비교 대상 | 단일 PG HA | Citus 1급 | Stateless 라우터 | Plugin SDK | 라이선스/스택 |
|---|---|---|---|---|---|
| **Crunchy PGO** | ✅ Patroni, pgBackRest, pgMonitor — **단일 PG HA의 사실상 표준** | ✗ (README에 Citus 언급 0) | ✗ | ✗ | Apache 2.0 / Go |
| CloudNativePG | ✅ K8s API as DCS | 플러그인(여러 Cluster CR 묶음) | ✗ | 부분(외부 plugin) | Apache 2.0 / Go |
| Zalando postgres-operator | ✅ Patroni | `citus.{group, cluster}` 필드 | ✗ | ✗ | MIT / Go |
| Percona | ✅ | ✗ | ✗ | ✗ | Apache 2.0 / Go |
| StackGres `SGShardedCluster` | ✅ | 1급 표현 | ✗ | ✗ | **AGPL-3.0** / Java |
| **keiailab/postgres-operator** | **✅ PGO-class 자체 구현** | **✅ 1급 표현** | **✅** | **✅ 5종 인터페이스** | **Apache 2.0 / Go** |

본 프로젝트의 차별화 무게중심은 **Citus 1급 + Plugin SDK** 두 곳입니다. 단일 PG HA는 PGO 수준 품질을 약속하는 "기본 품질"이지 차별화 자체는 아닙니다. 자세한 결정 근거는 [ADR 0004](docs/adr/0004-build-not-fork-or-layer.md) 참조 (PGO fork·soft layer 옵션을 모두 거부한 사유 기록).

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

## Development (로컬 게이트)

본 프로젝트는 [ADR 0009](docs/adr/0009-no-github-actions-rfc-0002.md)에 따라 **GitHub Actions를 사용하지 않습니다** (글로벌 RFC 0002, 2026-04-29 사고 트리거). 모든 게이트(lint·test·audit·secrets)는 *로컬 4 계층*으로 일원화됐습니다.

### 1회 셋업

```bash
# 보안 도구 설치 (macOS)
brew install gitleaks trivy
# Linux는 apt/공식 binary 참조: https://aquasecurity.github.io/trivy/

# pre-commit hook 활성화 (1회 실행)
pip install pre-commit  # 또는 brew install pre-commit
pre-commit install --hook-type pre-commit --hook-type pre-push
```

### 4 계층 게이트

| 계층 | 시점 | 명령 | 차단 기준 |
|---|---|---|---|
| **L1 pre-commit** | `git commit` | `make lint` | lint error 1건 이상 |
| **L2 pre-push** | `git push` | `make test`, `make audit`, gitleaks, go.mod drift | error 1건 이상 |
| **L3 Makefile** | 개발자 수시 | `make test-e2e` (kind 7-9분), `make build` | 로컬 명시 검증 |
| **L4 PR review** | merge 전 | PR body의 "로컬 게이트 PASS" 증거 블록 | 증거 부재 시 머지 차단 |

### PR 머지 증거 블록 (필수)

PR 본문 또는 첫 commit 메시지에 다음 형식 포함 (`standards/ci.md §2`):

```
로컬 게이트 PASS:
- pre-commit run --all-files: PASS
- pre-push hooks: PASS
- make test: PASS
- make audit: PASS  (HIGH+CRITICAL = 0)
```

부재 시 리뷰어가 머지를 차단합니다. 우회(`--no-verify`)는 사고 보고 의무 (`incident-kb.md`).

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
