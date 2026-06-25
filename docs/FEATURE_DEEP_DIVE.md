# postgres-operator 기능 심층 분석

> 각 CRD와 컨트롤러의 내부 동작을 코드 레벨에서 분석한 문서.
> 대상 버전: v0.4.0-beta.1 | 소스: `api/v1alpha1/`, `internal/controller/`

---

## 목차

1. [PostgresCluster — 클러스터 라이프사이클](#1-postgrescluster--클러스터-라이프사이클)
2. [HA Failover — 자동 장애 복구](#2-ha-failover--자동-장애-복구)
3. [BackupJob / ScheduledBackup — 백업 시스템](#3-backupjob--scheduledbackup--백업-시스템)
4. [Pooler — PgBouncer 커넥션 풀](#4-pooler--pgbouncer-커넥션-풀)
5. [PostgresDatabase — 선언적 데이터베이스 관리](#5-postgresdatabase--선언적-데이터베이스-관리)
6. [PostgresUser — 선언적 역할 관리](#6-postgresuser--선언적-역할-관리)
7. [ImageCatalog / ClusterImageCatalog — 이미지 카탈로그](#7-imagecatalog--clusterimagecatalog--이미지-카탈로그)
8. [Plugin SDK — 확장 아키텍처](#8-plugin-sdk--확장-아키텍처)
9. [ShardSplitJob / ShardRange — 미래 샤딩 설계](#9-shardsplitjob--shardrange--미래-샤딩-설계)

---

## 1. PostgresCluster — 클러스터 라이프사이클

**소스**: `api/v1alpha1/postgrescluster_types.go`, `internal/controller/postgrescluster_controller.go`

### 1.1 Reconcile 흐름

reconcile은 5초(`statusPollInterval`) 주기 requeue와 Owns(StatefulSet/Deployment) Watch 이벤트로 트리거된다.

```
[Reconcile 진입]
  │
  ├─ 1. CR fetch + PostgresVersion 매트릭스 검증
  │       ↓ 실패 시 → Degraded + VersionRejected condition
  │
  ├─ 2. ImageCatalog 해석 (imageCatalogRef 사용 시)
  │       ↓ 실패 시 → Degraded + ImageCatalogRejected condition
  │
  ├─ 3. Replica cluster 설정 검증 (externalClusters 참조 확인)
  │
  ├─ 4. RBAC upsert (SA + Role + RoleBinding)
  │       — Pod 기동 전 선행 필수 (fail-fast 회피)
  │
  ├─ 5. TLS Certificate CR upsert (cert-manager, Phase 2)
  │
  ├─ 6. [shard 수만큼 반복]
  │       ├─ ConfigMap upsert (postgresql.conf + pg_hba.conf)
  │       ├─ Headless Service upsert (DNS 핵심)
  │       ├─ StatefulSet upsert (primary + replica Pods)
  │       ├─ PDB upsert (replicas >= 2일 때 자동 생성)
  │       └─ shard status 수집 (Pod annotation 기반 → STS readyReplicas fallback)
  │
  ├─ 7. Router 자원 upsert (shardingMode=native일 때만)
  │       └─ ConfigMap + ClusterIP Service + Deployment
  │
  ├─ 8. PVC online expansion (Spec.Storage.Size 증가 감지 시 PVC patch)
  │
  ├─ 9. Switchover 처리 (annotation 트리거 계획 전환)
  │
  ├─ 10. Failover 결정 + debounce gate
  │
  ├─ 11. 스테일 replica 재시드 + 이상 primary 재시드
  │
  ├─ 12. Condition + Phase 갱신
  │
  ├─ 13. Config hot-reload (pg_reload_conf 실행)
  │
  └─ 14. Status.Update() → 5초 후 requeue
```

### 1.2 생성되는 Kubernetes 자원

| 자원 | 이름 패턴 | 역할 |
|---|---|---|
| `ServiceAccount` | `<cluster>-instance` | instance manager 전용 SA |
| `Role` | `<cluster>-instance` | Pod/Lease/Secret 접근 |
| `RoleBinding` | `<cluster>-instance` | SA ↔ Role 바인딩 |
| `ConfigMap` | `<cluster>-shard-<ord>-config` | postgresql.conf, pg_hba.conf |
| `Service` (Headless) | `<cluster>-shard-<ord>` | StatefulSet DNS |
| `StatefulSet` | `<cluster>-shard-<ord>` | primary + replica Pods |
| `PodDisruptionBudget` | `<cluster>-shard-<ord>` | minAvailable=1 (HA 보호) |
| `Certificate` (cert-manager) | `<cluster>-tls` | TLS 인증서 (Phase 2) |

### 1.3 StatefulSet 구조

```
StatefulSet: <cluster>-shard-0
  replicas: 1 (primary) + spec.shards.replicas (async)
  
  Pod containers:
    - postgres       : upstream PostgreSQL 바이너리
    - instance-mgr   : PID1 instance manager (선출/fencing/상태 API)
    - pgbackrest-sidecar : 백업 사이드카 (pgBackRest 사용 시)
  
  Init containers:
    - bootstrap: ordinal > 0이고 primaryEndpoint가 있으면 pg_basebackup
                 없으면 initdb (ordinal-0 또는 첫 부팅)
  
  VolumeClaimTemplates:
    - data: spec.shards.storage.size (PVC, immutable selector)
```

### 1.4 클러스터 Phase 전환 규칙

```
Provisioning  ─→  Ready
    ↑                │
    └── Degraded ←──┘   (primary 실패 감지 시)
         │
    Hibernated (cnpg.io/hibernation=on 어노테이션)
```

| Phase | 조건 |
|---|---|
| `Provisioning` | 모든 shard primary가 아직 Ready 아님 |
| `Ready` | 모든 shard primary Ready + router Ready (native 모드일 때) |
| `Degraded` | Ready였다가 primary 실패 감지됨 |
| `Hibernated` | `cnpg.io/hibernation=on` 어노테이션 설정됨 |
| `Reconfiguring` | spec 변경 처리 중 (향후 사용) |

### 1.5 Hibernation (하이버네이션)

`cnpg.io/hibernation=on` 어노테이션을 붙이면 StatefulSet replicas=0으로 축소하고 PVC는 보존한다.
어노테이션을 제거하면 복구된다. CloudNativePG 패턴과 동일하다.

```bash
# 하이버네이션 진입
kubectl annotate postgrescluster my-cluster cnpg.io/hibernation=on

# 복구
kubectl annotate postgrescluster my-cluster cnpg.io/hibernation-
```

### 1.6 Replica Cluster (외부 클러스터 복제)

`spec.replica.enabled=true` + `externalClusters` 설정으로 외부 PostgreSQL 클러스터를
스트리밍 복제로 따라가는 Standby 클러스터를 구성할 수 있다.

```yaml
spec:
  externalClusters:
    - name: primary-dc
      connectionParameters:
        host: pg-primary.prod.svc
        port: "5432"
        user: streaming_replica
      password:
        name: replication-secret
        key: password
  replica:
    enabled: true
    source: primary-dc
```

Bootstrap 방식은 `spec.bootstrap.pg_basebackup`으로 최초 full copy를 수행한다.

### 1.7 동기 복제 설정

```yaml
spec:
  postgresql:
    synchronous:
      method: any         # any (quorum) 또는 first (priority)
      number: 1           # 커밋 확인에 필요한 동기 standby 수
      dataDurability: required  # required=블록 | preferred=quorum 자동 낮춤
```

CEL 검증: `postgresql.synchronous.number <= shards.replicas`

### 1.8 Config Hot-Reload

SIGHUP 레벨 파라미터 변경 시(max_connections 제외) Pod 재시작 없이 반영된다.

```
ConfigMap 변경 → reconcile → pg_reload_conf() 실행 (psql exec)
```

`postgresql.conf` 해시가 변경된 경우에만 reload를 시도하며, 에러는 best-effort로
로그에만 기록하고 reconcile을 중단하지 않는다.

---

## 2. HA Failover — 자동 장애 복구

**소스**: `internal/controller/failover/`, `internal/controller/postgrescluster_controller.go`

### 2.1 이중 Lease 구조

operator는 두 개의 독립된 Kubernetes Lease를 사용한다.

```
Lease 1: controller-runtime manager lease
  — 일반 reconciler 실행 권한 (리소스 생성/수정)
  — leader인 Pod만 reconcile 실행

Lease 2: postgres-operator-failover-leader (FailoverLeaseName)
  — failover 결정/실행 전용
  — NeedLeaderElection()=false → 모든 manager replica가 경쟁
  — manager lease와 별도: reconciler 중단 없이 failover 핫패스 격리
```

이 분리 덕분에 manager Pod이 재시작되어도 failover lease는 즉시 다른 Pod이 인계한다.

### 2.2 장애 감지 (Detection)

`failover.DetectPrimaryFailure(shard)` — **순수 함수, 네트워크 호출 없음**

```
입력: ShardStatus (Pod annotation 기반 live aggregation)

판정 규칙:
  1. shard.Primary == nil
     → ReasonNoPrimary (instance manager heartbeat 없음)
  
  2. shard.Primary.Ready == false
     → ReasonPrimaryNotReady (PostgreSQL 응답 불가)
  
  3. 위 두 경우 + Ready replica 없음
     → ReasonNoEligibleReplica (수동 개입 필요)

승격 후보 선택: SelectPromotionCandidate()
  → 가장 WAL lag(bytes)가 작은 Ready replica
  → 동률 lag: Pod 이름 사전순 (결정적)
```

### 2.3 Debounce Gate (오탐 방지)

단일 reconcile 관측으로 즉시 promotion하면 일시적 상태 flicker(standby join 중 잠깐 Primary=nil 등)가 건강한 멤버를 fencing할 수 있다.

```
failoverDebounceThreshold = 8초 (statusPollInterval 5초의 ~1.5배)

로직:
  실패 첫 관측 → failoverPending[key] = now
  후속 reconcile → now - first >= 8s 일 때만 promotion 실행
  실패 해소 → pending 삭제
```

canStart 조건: 실패 최초 관측 시점에 클러스터가 `Ready` 상태였어야 함
→ Provisioning 중인 클러스터에서 false promotion 방지

### 2.4 Promotion 실행

`failover.BuildPromotionPlan()` + `Promoter.Execute()`

```
결정적 4단계 (순서 변경 불가):

  1. RemoveStandbySignal
     — $PGDATA/standby.signal 파일 삭제
     — PostgreSQL standby 모드 해제의 전제조건

  2. PgCtlPromote
     — `pg_ctl promote -D $PGDATA` 실행
     — recovery → primary 전환 트리거

  3. WaitNotInRecovery
     — `SELECT pg_is_in_recovery()` 폴링
     — promote 완료 확인

  4. UpdateInstanceRole
     — Pod annotation role=primary 갱신
     — operator status 합성 입력
```

실행 방법: `pods/exec` API (KubernetesBackupSidecarExecutor 재사용)

### 2.5 Fenced Candidate Guard (재승격 방지)

failback 시 이미 fence된 이전 primary를 다시 승격하는 것을 방지한다.

```
조건: fenced PVC를 가진 Pod이 승격 후보인데 unfenced 멤버가 이미 서비스 중
→ 승격 건너뜀 (로그 기록만)

이유: 이전 primary가 돌아왔을 때 unfenceTargetPVC로 fence를 해제하면
     stale timeline으로 재가동하여 post-failover 쓰기가 소실될 수 있음
```

### 2.6 Stale Replica Re-seed

primary가 Ready인데 replica가 장시간 NotReady 상태로 stuck된 경우 pg_basebackup으로 재시드한다.

```
조건: primary Ready + replica NotReady 지속 (임계 시간 초과)
동작: pg_basebackup으로 해당 replica 재초기화
```

### 2.7 Rogue Primary Re-seed

failback 후 이전 primary가 stale 환경(PRIMARY_ENDPOINT 환경변수)으로 부팅하여 initdb로
자신을 초기화하고 새로운 primary로 행동하는 "rogue" 상태 처리.

```
감지: 두 개의 Pod이 동시에 primary로 보고하는 경우
동작: 정상 promoted primary가 아닌 쪽을 standby로 재시드
```

### 2.8 Switchover (계획된 Primary 전환)

annotation으로 트리거하는 계획적 primary 전환 (데이터 손실 없음).

```bash
kubectl annotate postgrescluster my-cluster \
  postgres.keiailab.io/switchover-target=my-cluster-shard-0-1
```

---

## 3. BackupJob / ScheduledBackup — 백업 시스템

**소스**: `api/v1alpha1/backupjob_types.go`, `internal/controller/backupjob_controller.go`

### 3.1 BackupJob — 단일 백업 실행

BackupJob은 **원자적(atomic)** 단위다. 생성 후 변경 불가 — 변경이 필요하면 새 CR을 생성한다.

```
불변 필드:
  - spec.cluster.name
  - spec.tool
  - spec.type
  - spec.executionMode
```

**Phase 전환:**

```
Pending → Running → Succeeded
                 ↘ Failed
```

### 3.2 Execution Mode

| 모드 | 동작 | 적합 도구 |
|---|---|---|
| `sidecar` | PostgreSQL Pod 내 long-running 사이드카에 명령 전달 (pod exec) | pgBackRest |
| `job` | 독립 Kubernetes Job 생성 후 바이너리 호출 | WAL-G |
| `""` | 플러그인 기본값 | — |

**sidecar 모드 (pgBackRest):**

```
BackupJobReconciler
  → BackupPlugin.BackupCommand() 로 argv 생성
  → KubernetesBackupSidecarExecutor.Exec() 로 pod exec
  → 결과 파싱 → status.backupID + status.bytes 갱신
```

**job 모드 (WAL-G):**

```
BackupJobReconciler
  → spec.jobTemplate 기반 batch/v1 Job 생성
  → Job 완료 감시 → status 갱신
```

### 3.3 PITR 복원 (Point-In-Time Recovery)

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
spec:
  cluster:
    name: my-cluster
  tool: pgbackrest
  repo: repo1
  type: restore
  restore:
    targetTime: "2026-06-20T10:00:00Z"   # 특정 시각으로 복원
    # 또는
    backupID: "20260620-100000F"          # 특정 백업 ID로 복원
```

### 3.4 백업 보존 정책

```yaml
spec:
  retention:
    keepFull: 7          # 풀 백업 7개 유지
    keepIncremental: 28  # 증분 백업 28개 유지
```

### 3.5 ScheduledBackup — 크론 기반 자동 백업

`robfig/cron/v3`를 사용하는 6-field 크론 (초 단위 지원).

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: ScheduledBackup
spec:
  schedule: "0 0 2 * * *"    # 매일 새벽 2시 (초/분/시/일/월/요일)
  backupTemplate:
    spec:
      cluster:
        name: my-cluster
      tool: pgbackrest
      repo: repo1
      type: full
```

ScheduledBackupReconciler가 크론 시각 도달 시 `BackupJob` CR을 자동 생성한다.

### 3.6 현재 지원 백업 도구

| 도구 | 상태 | 실행 모드 | 특징 |
|---|---|---|---|
| pgBackRest | 완전 구현 | sidecar | PG Pod와 동거, WAL 아카이브 |
| WAL-G | 구현됨 | job | S3/GCS/Azure 직접 스트리밍 |
| Barman | 구현됨 | — | Barman Cloud 통합 |

---

## 4. Pooler — PgBouncer 커넥션 풀

**소스**: `api/v1alpha1/pooler_types.go`, `internal/controller/pooler_controller.go`

### 4.1 동작 개요

```
애플리케이션 → Pooler Service → PgBouncer Pod(s) → PostgresCluster
```

Pooler는 PostgresCluster 앞에 위치하는 **PgBouncer Deployment**를 관리한다.

### 4.2 연결 타입

| 타입 | 라우팅 대상 | 사용 시나리오 |
|---|---|---|
| `rw` | Primary (쓰기 엔드포인트) | 일반 OLTP 쓰기/읽기 |
| `ro` | Replica (읽기 엔드포인트) | 읽기 전용 쿼리 분리 |

### 4.3 Pool Mode

| 모드 | 특징 | 권장 대상 |
|---|---|---|
| `session` | 클라이언트 세션 동안 PG 연결 유지 | SET 명령 / prepared statement 사용 앱 |
| `transaction` | 트랜잭션 완료 후 연결 반납 | 일반 OLTP (권장, 연결 재사용 최대) |
| `statement` | 각 쿼리 후 연결 반납 | 제약: 트랜잭션/prepared statement 불가 |

### 4.4 Built-in Auth (자동 인증 설정)

`spec.pgbouncer.authSecretRef`를 생략하면 operator가 자동으로 처리:

```
1. PostgreSQL primary Pod에 psql exec
2. `keiailab_pooler_pgbouncer` LOGIN 역할 생성 (랜덤 패스워드)
3. `<pooler-name>-builtin-auth` Secret 생성 (userlist.txt 포함)
4. PgBouncer auth_file로 마운트
```

```bash
# 패스워드 강제 회전
kubectl annotate pooler my-pool \
  postgres.keiailab.io/rotate-pooler-password=true
```

### 4.5 TLS 설정

**방법 1: cert-manager 자동 발급**

```yaml
spec:
  pgbouncer:
    autoTLS:
      issuerRef:
        name: my-issuer
        kind: ClusterIssuer
      clientEnabled: true   # 클라이언트 → PgBouncer TLS
      serverEnabled: true   # PgBouncer → PostgreSQL mTLS
```

**방법 2: Self-Signed (cert-manager 없는 환경)**

```yaml
spec:
  pgbouncer:
    autoTLS:
      selfSigned: true
      clientEnabled: true
```

RSA-2048, 유효기간 1년, 만료 30일 전 자동 회전.

**방법 3: 직접 Secret 지정**

```yaml
spec:
  pgbouncer:
    serverTLSSecret:
      name: my-server-tls
    clientTLSSecret:
      name: my-client-tls
```

### 4.6 PAUSE / RESUME

```yaml
spec:
  paused: true   # 모든 PgBouncer Pod에 PAUSE 명령 실행
```

운영 중 점검 시 애플리케이션 연결을 끊지 않고 쿼리 대기 상태로 유지할 수 있다.

### 4.7 Prometheus 익스포터

```yaml
spec:
  pgbouncer:
    exporter:
      image: prometheuscommunity/pgbouncer-exporter:latest
      port: 9127
```

PgBouncer 내부 통계를 Prometheus 메트릭으로 노출한다.

### 4.8 Config Hash 기반 Hot-Reload

pgbouncer.ini 변경 시 Pod 재시작 없이 `RELOAD` 명령으로 적용.
`status.configHash`로 현재 적용된 설정 버전을 추적한다.

---

## 5. PostgresDatabase — 선언적 데이터베이스 관리

**소스**: `api/v1alpha1/postgresdatabase_types.go`, `internal/controller/postgresdatabase_controller.go`

### 5.1 동작 원리

`PostgresDatabaseReconciler`는 대상 PostgresCluster의 **ready primary Pod**에 `psql exec`를 실행하여 SQL DDL을 적용한다. 클러스터 외부에서 직접 연결하지 않으므로 네트워크 방화벽에 무관하다.

```
PostgresDatabase CR 변경
  → Reconciler
  → primary Pod exec (psql -c "CREATE DATABASE ...")
  → status.applied = true / false
```

### 5.2 관리 가능한 객체

**데이터베이스 자체**
```yaml
spec:
  name: appdb
  owner: app_role
  ensure: present   # 또는 absent
  databaseReclaimPolicy: retain  # 또는 delete (CR 삭제 시 DROP DATABASE)
```

**익스텐션**
```yaml
spec:
  extensions:
    - name: pgcrypto
      ensure: present
      version: "1.3"     # 특정 버전 고정
      schema: public
    - name: postgis
      ensure: present
```

**스키마**
```yaml
spec:
  schemas:
    - name: app
      owner: app_role
      privileges:
        - role: readonly_role
          privileges: [USAGE]
        - role: app_role
          privileges: [USAGE, CREATE]
```

**Foreign Data Wrapper (FDW)**
```yaml
spec:
  fdws:
    - name: postgres_fdw
      handler: postgres_fdw_handler
      validator: postgres_fdw_validator
      usage:
        - name: analyst_role
          type: grant
```

**Foreign Server**
```yaml
spec:
  servers:
    - name: remote_db
      fdw: postgres_fdw
      options:
        - name: host
          value: remote.db.example.com
        - name: port
          value: "5432"
```

### 5.3 보호된 이름 (CEL 검증)

다음 DB 이름은 admission webhook에서 거부된다:
- `postgres`
- `template0`
- `template1`

### 5.4 Reclaim Policy

| 정책 | CR 삭제 시 | 권장 환경 |
|---|---|---|
| `retain` (기본) | 데이터베이스 유지 | 운영 환경 (데이터 보호) |
| `delete` | `DROP DATABASE` 실행 | 개발/테스트 환경 |

---

## 6. PostgresUser — 선언적 역할 관리

**소스**: `api/v1alpha1/postgresuser_types.go`, `internal/controller/postgresuser_controller.go`

### 6.1 지원 역할 속성

```yaml
spec:
  name: app_user
  login: true           # LOGIN 속성
  superuser: false      # SUPERUSER
  createdb: false       # CREATEDB
  createrole: false     # CREATEROLE
  replication: false    # REPLICATION
  bypassrls: false      # BYPASSRLS
  inherit: true         # INHERIT (null=PG 기본값)
  connectionLimit: 25   # -1=무제한
  validUntil: "infinity" # 패스워드 만료 없음
```

### 6.2 패스워드 관리

**Secret 참조 방식 (권장):**
```yaml
spec:
  passwordSecretRef:
    name: app-user-password
# Secret 내용:
#   data:
#     username: app_user
#     password: <base64>
```

Secret의 `resourceVersion`이 변경될 때만 패스워드를 재적용한다.
`status.passwordSecretResourceVersion`으로 마지막 적용 버전을 추적한다.

**패스워드 비활성화:**
```yaml
spec:
  disablePassword: true  # ALTER ROLE ... PASSWORD NULL
```

CEL 검증: `passwordSecretRef`와 `disablePassword`는 동시 설정 불가.

### 6.3 역할 멤버십

```yaml
spec:
  inRoles:
    - readonly_role
    - monitoring_role
```

`GRANT readonly_role TO app_user` SQL이 실행된다.

### 6.4 보호된 이름 (CEL 검증)

다음은 거부된다:
- `postgres`
- `pg_`로 시작하는 모든 이름 (PostgreSQL 내부 예약)

### 6.5 PostgresCluster Status 연동

`PostgresCluster.status.managedRolesStatus`에 모든 PostgresUser 상태가 집계된다.

```yaml
status:
  managedRolesStatus:
    byStatus:
      reserved: [postgres, streaming_replica]
      reconciled: [app_user]
      pending-reconciliation: [new_user]
    cannotReconcile:
      broken_user: ["role does not exist"]
    passwordStatus:
      app_user:
        secretResourceVersion: "12345"
        observedGeneration: 3
```

---

## 7. ImageCatalog / ClusterImageCatalog — 이미지 카탈로그

**소스**: `api/v1alpha1/imagecatalog_types.go`

### 7.1 목적

PostgreSQL 이미지를 major version으로 추상화하여 클러스터별 핀닝을 가능하게 한다.

```yaml
# 네임스페이스 범위
apiVersion: postgres.keiailab.io/v1alpha1
kind: ImageCatalog
metadata:
  name: pg-images
spec:
  images:
    - major: 18
      image: ghcr.io/keiailab/postgres:18.1@sha256:abc123...
    - major: 17
      image: ghcr.io/keiailab/postgres:17.5@sha256:def456...
```

```yaml
# 클러스터 전체 공유
apiVersion: postgres.keiailab.io/v1alpha1
kind: ClusterImageCatalog
```

### 7.2 PostgresCluster에서 참조

```yaml
spec:
  imageCatalogRef:
    kind: ClusterImageCatalog
    name: company-pg-images
    major: 18  # major 번호가 postgresVersion보다 우선
```

`imageCatalogRef`가 설정되면 `spec.postgresVersion`은 무시되고 `major`가 단일 진실이 된다.

### 7.3 CloudNativePG 호환

`APIGroup`으로 `postgresql.cnpg.io`를 수용하여 CNPG 매니페스트 포팅을 용이하게 한다.
단, 실제 lookup은 keiailab CRD 기준이다.

### 7.4 운영 권장사항

```yaml
# 운영 환경: digest 핀
image: ghcr.io/keiailab/postgres:18.1@sha256:abc123...

# 개발 환경: tag만
image: ghcr.io/keiailab/postgres:18.1
```

Digest 핀으로 이미지 변조/변경을 방지한다.

---

## 8. Plugin SDK — 확장 아키텍처

**소스**: `internal/plugin/api.go`, `internal/plugin/registry.go`

### 8.1 설계 원칙

```
핵심 reconciler → Plugin 인터페이스만 호출
플러그인 구현체 → 인터페이스 구현 + Registry 등록 1줄
```

새 플러그인 추가 = **인터페이스 구현 1개 + cmd/main.go에 Register() 1줄**.
핵심 reconciler 코드 변경 없음.

### 8.2 5종 인터페이스

#### BackupPlugin

```go
type BackupPlugin interface {
    Name() string
    PerformBackup(ctx, target, opts) (BackupResult, error)
    RestorePIT(ctx, target, ts) error
    Validate(spec *BackupSpec) error
}
```

추가로 `BackupCommandPlugin`을 구현하면 pod exec 경로를 사용할 수 있다:

```go
type BackupCommandPlugin interface {
    BackupCommand(target, opts) ([]string, error)
    RestoreCommand(target, ts) ([]string, error)
    ParseBackupResult(output []byte, opts) BackupResult
}
```

#### ExtensionPlugin

```go
type ExtensionPlugin interface {
    Name() string
    SharedPreloadOrder() int  // 낮은 값이 먼저 로드
    PreInstall(ctx, conn *sql.DB) error   // CREATE EXTENSION 전
    PostInstall(ctx, conn *sql.DB) error  // CREATE EXTENSION 후
    Validate(version string) error
}
```

`shared_preload_libraries` 순서:

```
pgaudit=100, pgvector=100 → pgcron=200 → pgnodemx=300, postgis=300, setuser=300
```

동률 order는 Name() 사전순으로 결정적 정렬.

#### ExporterPlugin

```go
type ExporterPlugin interface {
    Name() string
    SidecarSpec() corev1.Container  // exporter 사이드카 컨테이너 스펙
    DashboardJSON() ([]byte, error) // Grafana 대시보드 JSON
    AlertRulesYAML() ([]byte, error) // PrometheusRule YAML
}
```

#### RouterPlugin

```go
type RouterPlugin interface {
    Name() string
    BuildRouterPodSpec(target) (corev1.PodSpec, error)
    HealthProbe(ctx, podName, podNamespace string) error
}
```

ADR 0003 강제: 라우터 PodSpec은 PVC 마운트, streaming replication, K8s Lease를 포함하면 안 된다.

#### AuthPlugin

```go
type AuthPlugin interface {
    Name() string
    Configure(ctx, target) error
    SecretSchemaJSON() ([]byte, error)
    RotateSecret(ctx, target, oldRef) (newRef, error)
}
```

### 8.3 현재 등록된 Extension 플러그인

| 플러그인 | 파일 | 역할 | SharedPreloadOrder |
|---|---|---|---|
| `pgaudit` | `internal/plugin/extension/pgaudit/` | PostgreSQL 감사 로그 | 100 |
| `pgvector` | `internal/plugin/extension/pgvector/` | AI 벡터 검색/임베딩 | 100 |
| `pgcron` | `internal/plugin/extension/pgcron/` | DB 내부 크론 작업 스케줄러 | 200 |
| `pgnodemx` | `internal/plugin/extension/pgnodemx/` | 노드 수준 OS 메트릭 노출 | 300 |
| `postgis` | `internal/plugin/extension/postgis/` | 지리 공간 데이터 처리 | 300 |
| `setuser` | `internal/plugin/extension/setuser/` | 사용자 권한 위임 모델 | 300 |

### 8.4 Extension Opt-in

Extension은 cluster spec에 명시할 때만 활성화된다 (RFC 0006 R1).
등록만 되어 있어도 cluster가 opt-in하지 않으면 vanilla PostgreSQL로 부팅된다.

```yaml
spec:
  extensions:
    - pgvector
    - pgaudit
```

**주의**: 이미지에 해당 extension의 `.so` 파일이 없으면 PostgreSQL이 FATAL로 시작 실패한다.
이미지 선택은 사용자 책임이다.

---

## 9. ShardSplitJob / ShardRange — 미래 샤딩 설계

**소스**: `api/v1alpha1/shardrange_types.go`, `api/v1alpha1/shardsplitjob_types.go`

### 9.1 현재 상태

CRD 타입만 정의되어 있으며 **컨트롤러가 구현되지 않았다**.
`ShardSplitJobReconciler`는 빈 scaffolding이다.

### 9.2 ShardRange — 샤드 범위 정의

특정 테이블의 데이터를 여러 shard에 분산하는 범위를 정의한다.

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: ShardRange
spec:
  cluster:
    name: my-cluster
  table: orders
  shardKey: customer_id
  ranges:
    - shard: 0
      min: "0"
      max: "1000000"
    - shard: 1
      min: "1000000"
      max: ""  # 무한대
```

### 9.3 ShardSplitJob — 온라인 샤드 분할

실행 중인 shard를 두 개로 분할하는 7단계 프로세스 (RFC 0003).

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: ShardSplitJob
spec:
  cluster:
    name: my-cluster
  sourceShard: 0
  # 분할 후 두 shard의 범위 정의
```

7단계 분할 알고리즘 (RFC 0003):
1. 새 shard 프로비저닝
2. 초기 full 복제
3. Catch-up 복제 (변경분)
4. 트래픽 잠금 (write 차단)
5. 최종 동기화
6. 라우팅 테이블 전환
7. 트래픽 잠금 해제

### 9.4 AutoSplit — 자동 분할 트리거

`shardingMode=native`에서만 의미를 가지며, 현재는 설계 단계다.

```yaml
spec:
  autoSplit:
    enabled: true
    requireApproval: true  # true: ShardSplitJob 생성 후 수동 승인 필요
    triggers:
      sizeThresholdGB: 500     # 500GB 초과 시
      p99LatencyMs: 100        # P99 레이턴시 100ms 초과 시
      cpuPercent: 80           # CPU 80% 초과 시
      durationMinutes: 10      # 위 조건이 10분 지속 시
```

---

## 부록: 주요 상수 및 설정값

| 상수 | 값 | 위치 | 설명 |
|---|---|---|---|
| `statusPollInterval` | 5초 | `postgrescluster_controller.go` | reconcile requeue 주기 |
| `failoverDebounceThreshold` | 8초 | `postgrescluster_controller.go` | failover 오탐 방지 debounce |
| `FailoverLeaseName` | `postgres-operator-failover-leader` | `failover/lease.go` | HA failover lease 이름 |
| LeaderElectionID | `bdce7c33.keiailab.io` | `cmd/main.go` | manager leader election lease 이름 |
| pgPort | `5432` | `internal/controller/` | PostgreSQL 기본 포트 |
| 기본 pool_mode | `session` | `pooler_types.go` | PgBouncer 기본 풀 모드 |
| PDB minAvailable | `1` | `pdb.go` | 가용 Pod 최소 수 (HA 보호) |
| TLS 자동 갱신 | 만료 30일 전 | `pooler_controller.go` | Self-signed 인증서 갱신 트리거 |
| Failover lease 기간 | 15s / 10s / 2s | `internal/instance/election/` | LeaseDuration / RenewDeadline / RetryPeriod |

---

## 용어집

> 정의는 [GLOSSARY.ko.md](GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| Failover (장애 조치) | Primary 장애 감지 후 Replica 하나를 새 Primary로 자동 승격해 서비스를 잇는 동작. |
| Promotion (승격) | Replica를 Primary로 올리는 행위. 본 operator는 `pg_promote()`(SQL)로 수행. |
| Switchover (계획 전환) | 장애가 아닌 의도된 상황에서 Primary를 다른 인스턴스로 무중단 전환하는 동작. |
| Fencing (PVC Fencing) | 옛/이상 Primary가 데이터에 쓰지 못하도록 PVC 접근을 차단해 split-brain을 막는 격리. |
| Debounce (디바운스) | 일시적 신호로 인한 오탐 failover를 막기 위해 장애를 일정 시간(기본 8초) 유지될 때만 인정하는 대기. |
| Lease (임대) | Kubernetes Lease 오브젝트. 외부 HA 에이전트 없이 이를 DCS로 써서 Primary 선출을 한다. |
| Rogue Primary | 정상 승격 절차 밖에서 자신이 Primary라 여기는 이상 인스턴스. 감지 시 re-seed로 정리. |
| Re-seed (재시드) | 뒤처지거나 이상한 인스턴스의 데이터를 새 Primary 기준으로 다시 복제해 정상화. |
| PITR (Point-In-Time Recovery) | WAL을 재생해 데이터베이스를 특정 과거 시점으로 복원하는 기법. |
| WAL (Write-Ahead Log) | 변경을 먼저 기록하는 PostgreSQL의 로그. 복제·PITR의 기반. |
| pgBackRest | 본 operator의 기본 백업 도구(플러그인). WAL-G·Barman은 대체 플러그인. |
| Hibernation | 클러스터를 STS scale-0으로 내려 PVC는 보존한 채 휴면시키는 기능. |
| Replica Cluster | 외부 클러스터를 streaming standby로 복제하는 구성. |
| CEL validation | CRD 스키마에서 표현식으로 값 제약을 거는 검증(예: 보호된 이름 차단). |
| ShardRange / ShardSplitJob / AutoSplit | 샤드 범위 정의 CRD / 온라인 샤드 분할 작업 / 임계치 도달 시 자동 분할 트리거. |
