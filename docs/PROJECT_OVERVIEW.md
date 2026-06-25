# postgres-operator 프로젝트 개요

> 버전: v0.4.0-beta.1 | 라이선스: MIT | 언어: Go 1.26 | 프레임워크: Kubebuilder / controller-runtime
>
> 🔖 진행 중 작업을 이어받거나 재검증하려면 먼저 [WORK_HANDOFF.ko.md](WORK_HANDOFF.ko.md)를 보라 — 브랜치 `chore/ha-pitr-e2e-consolidation`의 커밋 구성·검증 결과·남은 라이브 E2E·재현 방법 정리.

## 1. 프로젝트 목적

`postgres-operator`는 **Kubernetes 위에서 vanilla PostgreSQL 18+을 선언적으로 운영**하기 위한 Kubernetes Operator다. 외부 fork 없이 upstream PostgreSQL 바이너리를 그대로 사용하기 때문에 기존 libpq/JDBC/asyncpg 드라이버와 완전히 호환된다.

단기 목표는 단일 클러스터 PostgreSQL(Primary + Replica) 운영 자동화이며, 장기 목표는 **수평 샤딩 + 분산 SQL 레이어**를 vanilla PostgreSQL 위에 구축하는 것이다.

```
[현재 GA]
  단일 클러스터: Primary + Replica, HA Failover, 백업, 커넥션 풀, 선언적 DB/역할

[로드맵 — 설계 단계]
  ShardRange CRD → pg-router 쿼리 라우터 → 크로스 샤드 분산 트랜잭션
```

---

## 2. 기술 스택

| 항목 | 선택 | 이유 |
|---|---|---|
| 언어 | Go 1.26 | Kubernetes 생태계 표준 |
| 프레임워크 | Kubebuilder + controller-runtime | CRD 스캐폴딩 + reconcile 루프 |
| PostgreSQL | 17 / 18 (upstream 바이너리) | fork 없음 = 완전 호환 |
| 백업 | pgBackRest (기본), WAL-G, Barman (플러그인) | 플러그인 교체 가능 |
| 커넥션 풀 | PgBouncer 1.19+ | CNPG 호환 surface |
| 인증서 | cert-manager (선택) | TLS 자동 발급/갱신 |
| 모니터링 | Prometheus Operator + Grafana | ServiceMonitor / PrometheusRule |
| 이미지 배포 | `ghcr.io/keiailab/postgres-operator` | GitHub Container Registry |

---

## 3. 아키텍처 전체 구조

```
┌────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                        │
│                                                            │
│  ┌──────────────────────────────────┐                      │
│  │  postgres-operator (manager Pod) │                      │
│  │                                  │                      │
│  │  ┌──────────────┐  ┌──────────┐ │                      │
│  │  │ Reconcilers  │  │ Webhooks │ │                      │
│  │  │ PostgresCluster│  │ Admission│ │                      │
│  │  │ BackupJob    │  │ Validate │ │                      │
│  │  │ Pooler       │  └──────────┘ │                      │
│  │  │ PostgresDB   │               │                      │
│  │  │ PostgresUser │  ┌──────────┐ │                      │
│  │  │ ScheduledBkp │  │ Plugin   │ │                      │
│  │  │ ShardSplitJob│  │ Registry │ │                      │
│  │  └──────────────┘  └──────────┘ │                      │
│  │                                  │                      │
│  │  ┌────────────────────────────┐  │                      │
│  │  │  HA Failover Lease         │  │                      │
│  │  │  (별도 leader election)    │  │                      │
│  │  └────────────────────────────┘  │                      │
│  └──────────────────────────────────┘                      │
│                                                            │
│  ┌────────────────────────────────────────────────────┐    │
│  │  PostgresCluster "my-cluster"                      │    │
│  │                                                    │    │
│  │  StatefulSet (shard-0)    StatefulSet (shard-N)    │    │
│  │  ┌──────────┐             ┌──────────┐             │    │
│  │  │ primary  │             │ primary  │             │    │
│  │  │ replica0 │   ...       │ replica0 │             │    │
│  │  └──────────┘             └──────────┘             │    │
│  │                                                    │    │
│  │  Pooler (PgBouncer)  ────→ rw endpoint             │    │
│  └────────────────────────────────────────────────────┘    │
└────────────────────────────────────────────────────────────┘
```

---

## 4. CRD 목록 (8종)

| CRD | 단축명 | 범위 | 역할 |
|---|---|---|---|
| `PostgresCluster` | `pgc` | Namespace | Primary + Replica 토폴로지 핵심 리소스 |
| `BackupJob` | `bj` | Namespace | 1회성 백업 / PITR 복원 |
| `ScheduledBackup` | — | Namespace | 크론 스케줄 백업 자동 생성 |
| `Pooler` | `pool` | Namespace | PgBouncer 커넥션 풀 레이어 |
| `PostgresDatabase` | `pgdb` | Namespace | DB / 스키마 / Extension / FDW 선언적 관리 |
| `PostgresUser` | `pguser` | Namespace | PostgreSQL 역할 / 패스워드 선언적 관리 |
| `ImageCatalog` | `pgic` | Namespace | 네임스페이스 범위 PostgreSQL 이미지 카탈로그 |
| `ClusterImageCatalog` | `pgcic` | Cluster | 클러스터 전체 공유 이미지 카탈로그 |

---

## 5. 컨트롤러 구조

모든 컨트롤러는 `cmd/main.go`에서 단일 `controller-runtime Manager`에 등록된다.

```
cmd/main.go
├── PostgresClusterReconciler   — 클러스터 라이프사이클 (StatefulSet/Service/ConfigMap 생성)
├── BackupJobReconciler         — 백업/복원 Job 실행
├── PostgresDatabaseReconciler  — SQL DDL 실행 (psql exec via pod)
├── PostgresUserReconciler      — SQL 역할 관리 (psql exec via pod)
├── ScheduledBackupReconciler   — 크론 기반 BackupJob 생성
├── ShardSplitJobReconciler     — 샤드 분할 (CRD만, 컨트롤러 구현 예정)
├── PoolerReconciler            — PgBouncer Deployment 관리
└── FailoverLease (Runnable)    — HA Failover 전용 Kubernetes Lease
```

---

## 6. Plugin 아키텍처

5종의 Plugin 인터페이스가 `internal/plugin/api.go`에 동결(freeze) 정의되어 있다.

```
BackupPlugin       — 백업 도구 추상화 (pgBackRest / WAL-G / Barman)
ExporterPlugin     — Prometheus exporter 사이드카
ExtensionPlugin    — PostgreSQL extension 라이프사이클
RouterPlugin       — QueryRouter 라우팅 정책
AuthPlugin         — 인증 메커니즘 (SCRAM / mTLS / OIDC)
```

현재 등록된 플러그인:

| 플러그인 | 타입 | shared_preload 순서 |
|---|---|---|
| pgaudit | Extension | 100 |
| pgvector | Extension | 100 |
| pgcron | Extension | 200 |
| pgnodemx | Extension | 300 |
| postgis | Extension | 300 |
| setuser | Extension | 300 |
| pgbackrest | Backup | — |

---

## 7. 바이너리 구성

| 바이너리 | 위치 | 역할 |
|---|---|---|
| `manager` | `cmd/main.go` | Operator 컨트롤 플레인 (본 문서 대상) |
| `instance` | `cmd/instance/` | PostgreSQL Pod 내부 PID1 데이터플레인 |
| `pg-router` | `cmd/pg-router/` | PoC 수준 PG wire-protocol 라우터 |

---

## 8. 현재 상태 및 로드맵

**현재 (v0.4.0-beta.8)**
- 단일 클러스터 운영: Primary + Replica, HA, 백업, 풀링, 모니터링 — beta 품질
- PITR restore drill **완료** (2026-06-24 live 7 PASS), 자동 failover reconcile 연결·fencing·promotion 코드 완료
- `ShardRange` / `ShardSplitJob` CRD 정의 완료, **컨트롤러 미구현**

**로드맵 (순서대로)**

1. HA 강화 — ~~PITR drill~~(완료), chaos/node-loss failover live drill 재검증 (ADR-0027 shard-identity)
2. `ShardRange` CRD 컨트롤러 + `pg-router` (수동 멀티 샤드 라우팅)
3. Scatter-gather 쿼리 + 읽기 레플리카 오토스케일
4. `ShardSplitJob` — 온라인 샤드 분할
5. 부하 기반 자동 분할/리밸런스
6. 크로스 샤드 분산 트랜잭션 및 JOIN

---

## 9. 디렉토리 구조

```
postgres-operator/
├── api/v1alpha1/            # CRD 타입 정의 (Go types)
│   ├── postgrescluster_types.go
│   ├── backupjob_types.go
│   ├── pooler_types.go
│   ├── postgresdatabase_types.go
│   ├── postgresuser_types.go
│   ├── scheduledbackup_types.go
│   ├── imagecatalog_types.go
│   ├── shardrange_types.go      # 로드맵 전용 (컨트롤러 없음)
│   └── shardsplitjob_types.go   # 로드맵 전용 (컨트롤러 없음)
│
├── internal/
│   ├── controller/          # Reconciler 구현체
│   │   ├── postgrescluster_controller.go
│   │   ├── backupjob_controller.go
│   │   ├── pooler_controller.go
│   │   ├── postgresdatabase_controller.go
│   │   ├── postgresuser_controller.go
│   │   ├── scheduledbackup_controller.go
│   │   ├── shardsplitjob_controller.go
│   │   └── failover/        # HA 자동 failover (detection/lease/promotion)
│   ├── instance/            # PostgreSQL Pod PID1 데이터플레인
│   │   ├── election/        # Primary lease election
│   │   ├── fencing/         # PVC fencing
│   │   ├── statusapi/       # HTTP status endpoint
│   │   └── supervise/       # Process supervisor
│   ├── plugin/              # Plugin SDK 인터페이스 + 구현체
│   │   ├── api.go           # 5종 인터페이스 동결 정의
│   │   ├── registry.go      # 플러그인 레지스트리
│   │   ├── backup/          # pgBackRest / WAL-G / Barman
│   │   └── extension/       # pgaudit / pgvector / pgcron / ...
│   ├── postgres/            # SQL DSL (grants 등)
│   ├── router/              # vindex shard 해석
│   └── webhook/v1alpha1/    # Admission webhook
│
├── cmd/
│   ├── main.go              # Manager 진입점
│   ├── instance/            # PID1 instance manager
│   └── pg-router/           # PoC 라우터
│
├── config/
│   ├── crd/bases/           # 자동 생성 CRD YAML (make manifests)
│   └── rbac/                # 자동 생성 RBAC (make manifests)
│
├── charts/postgres-operator/ # Helm 차트 (배포 대상)
├── docs/                    # 각종 문서 (ADR, RFC, runbook 등)
└── Makefile                 # gate = lint + test + audit + validate
```

---

## 10. 설치 및 빠른 시작

```bash
# Helm으로 Operator 설치
helm install postgres-operator ./charts/postgres-operator

# 단일 노드 클러스터 생성
kubectl apply -f - <<EOF
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quickstart
  namespace: default
spec:
  postgresVersion: "18"
  shards:
    initialCount: 1
    replicas: 1       # 1 primary + 1 replica
    storage:
      size: 10Gi
EOF

# Ready 대기
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m
```

---

## 11. 관련 문서

| 문서 | 위치 | 설명 |
|---|---|---|
| **문서 지도** | `docs/DOCS_MAP.ko.md` | 분석/작업/환경 문서군의 입구·역할·SSOT·관계 (여기부터) |
| 기능 심층 분석 | `docs/FEATURE_DEEP_DIVE.md` | 각 기능 상세 동작 분석 |
| 단위·통합 테스트 분석 | `docs/TEST_ANALYSIS.md` | 패키지별 커버리지·TC 상세 (테스트 수치 SSOT) |
| E2E 테스트 보고서 | `docs/E2E_TEST_REPORT.ko.md` | E2E 라이브 드릴 RCA 누적 로그 |
| 작업 인수인계 | `docs/WORK_HANDOFF.ko.md` | 현재 브랜치 커밋 구성·검증·남은 일·재현법 |
| Dev Container 설정 | `docs/dev-setup-devcontainer.md` | Windows Dev Container 개발 환경 (권장) |
| WSL2 설정 | `docs/dev-setup-wsl.md` | Windows WSL2 개발 환경 (대안) |
| 아키텍처 | `docs/ARCHITECTURE.md` | 설계 결정 및 트레이드오프 |
| 로드맵 | `docs/ROADMAP.md` | 단계별 개발 계획 |
| ADR 인덱스 | `docs/kb/adr/INDEX.md` | Architecture Decision Records |
| RFC 인덱스 | `docs/rfcs/INDEX.md` | Request for Comments |
| Operator 가이드 | `docs/operator-guide/` | 배포 / HA / 모니터링 가이드 |
| Runbook | `docs/runbooks/` | 백업 / 복구 / HA / 보안 절차 |
