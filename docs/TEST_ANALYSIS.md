# 테스트 분석 보고서

> 실행 환경: Dev Container (golang:1.26, Debian 13 trixie) | 실행 명령: `make test`  
> 테스트 프레임워크: Go 표준 `testing` + Ginkgo v2 (컨트롤러 통합) | envtest: Kubernetes 1.36

---

## 1. 실행 환경 및 자원 사용

### 테스트 레이어 구조

```
make test
 ├─ [1] make manifests generate     # CRD/RBAC YAML + DeepCopy 코드 재생성
 ├─ [2] go fmt + go vet             # 정적 검사
 └─ [3] go test (KUBEBUILDER_ASSETS 주입)
         ├─ 단위 테스트 (in-process, 외부 의존 없음)
         └─ envtest 통합 테스트 (in-process K8s API + etcd 바이너리)
```

### 자원 사용 요약

| 자원 | 내용 |
|---|---|
| 프로세스 | goroutine 기반 — 실제 컨테이너/VM 없음 |
| 네트워크 | 없음 (fake clientset 또는 in-process API 서버) |
| 스토리지 | `t.TempDir()` 임시 디렉토리 + `bin/k8s/1.36.0-linux-amd64/` envtest 바이너리 |
| envtest 바이너리 | `etcd`, `kube-apiserver`, `kubectl` (로컬 다운로드, 실행 시 자동 기동) |
| 소요 시간 | 단위: 1~5s / envtest: 12~13s (API 서버 기동 포함) |

---

## 2. 패키지별 테스트 결과

| 패키지 | 커버리지 | 소요 시간 | 테스트 방식 |
|---|---|---|---|
| `api/v1alpha1` | 2.2% | 0.05s | 단위 (타입 검증) |
| `cmd/instance` | 6.9% | 0.19s | 단위 (진입점 smoke) |
| `cmd/pg-router` | 31.9% | 0.01s | 단위 |
| `internal/controller` | 72.6% | 11.9s | **envtest 통합** |
| `internal/controller/failover` | **98.8%** | 4.1s | 단위 (fake clientset) |
| `internal/instance/election` | **96.5%** | 12.9s | 단위 + 실시간 lease 경합 |
| `internal/instance/fencing` | **89.7%** | 0.3s | 단위 (fake clientset) |
| `internal/instance/supervise` | 78.2% | 3.4s | 실행 프로세스 기반 |
| `internal/plugin` | **91.7%** | 0.02s | 단위 |
| `internal/plugin/backup/*` | 76~86% | 0.02~0.05s | 단위 |
| `internal/postgres` | **95.4%** | 0.5s | 단위 (SQL 생성) |
| `internal/router` | 74.6% | 0.04s | 단위 |
| `internal/version` | 78.6% | 0.03s | 단위 |
| `internal/webhook/v1alpha1` | **94.7%** | 7.7s | envtest 통합 |
| `test/chart` | — | 0.02s | YAML 정적 검증 |

---

## 3. 패키지별 TC 상세

---

### 3.1 `internal/controller/failover` — HA Failover 핵심 (커버리지 98.8%)

**목적**: Primary Pod 장애 감지 → 승격 후보 선정 → Promotion Plan 실행까지 3단계 로직이 올바르게 작동하는지 검증.

#### Detection (장애 감지)

| TC | 시나리오 | 검증 내용 |
|---|---|---|
| `TestDetectPrimaryFailure_HealthyPrimary` | Primary가 Ready=true | `Failed=false`, `Reason=None`, `PromotionCandidate=nil` |
| `TestDetectPrimaryFailure_NoPrimary` | Primary pod 없음, Replica 2개 Ready | `Failed=true`, `Reason=NoPrimary`, 가장 낮은 Lag(20B)의 `pg-2` 선정 |
| `TestDetectPrimaryFailure_NotReady` | Primary Ready=false | `Failed=true`, `Reason=PrimaryNotReady`, 메시지에 pod 이름 포함 |
| `TestDetectPrimaryFailure_NoEligibleReplica` | Primary 없음 + Replica 전부 NotReady | `Reason=NoEligibleReplica`, `PromotionCandidate=nil`, 메시지에 "manual intervention" |
| `TestSelectPromotionCandidate_OrdersByLagThenName` | 5개 하위 케이스 (빈 목록 / 전부 NotReady / Lag 차이 / 동률-사전순 / NotReady 제외) | LagBytes 오름차순 → 동률 시 Pod 이름 사전순으로 후보 선정 |

**자원**: 실제 K8s API 불필요 — `ShardStatus` struct 직접 생성.

#### Lease (리더 선출 경합)

| TC | 시나리오 | 검증 내용 |
|---|---|---|
| `TestLeaseElection` | A, B 두 Operator Pod가 동일 Lease 경합 | ① A가 먼저 Leader 획득, ② B는 A가 active인 동안 Leader 안 됨(단일성), ③ A cancel 후 B가 Lease 승계, ④ OnStartedLeading 순서·OnStoppedLeading 콜백 검증 |
| `TestNewLeaseValidation` | nil client / 빈 Namespace / 빈 Identity | 각각 error 반환, LeaseName 미지정 시 기본값 자동 적용 |

**자원**: `k8s.io/client-go/kubernetes/fake` — 실 etcd 없이 in-memory API.  
**타이밍**: LeaseDuration=1.5s, RenewDeadline=1s, RetryPeriod=200ms (실제 값의 ~1/10 축소).

#### Promotion (승격 실행)

| TC | 시나리오 | 검증 내용 |
|---|---|---|
| `TestBuildPromotionPlan_Steps` | 정상 후보(`pg-1`) 로 Plan 생성 | 4단계 순서 고정: `RemoveStandbySignal → PgCtlPromote → WaitNotInRecovery → UpdateInstanceRole` |
| `TestBuildPromotionPlan_NilCandidate` | 후보 nil | `ErrNoPromotionTarget` |
| `TestPromoteFromDecision_HealthyNoOp` | `Decision.Failed=false` | Promoter 미호출 (no-op) |
| `TestPromoteFromDecision_ExecutesPlan` | `Failed=true`, 후보 있음 | fakePromoter 1회 호출, ShardName 검증 |
| `TestPromoteFromDecision_NilPromoter` | Promoter=nil | "nil Promoter" 포함 에러 |
| `TestPromoteFromDecision_NoCandidate` | `NoEligibleReplica` | `ErrNoPromotionTarget` wrap |
| `TestPromoteFromDecision_PromoterErrorWrapped` | Promoter가 에러 반환 | 원본 에러 chain 유지, shard/pod 이름이 wrap 메시지에 포함 |

---

### 3.2 `internal/instance/election` — Primary Lease 선출 (커버리지 96.5%)

**목적**: 각 Shard Pod가 Kubernetes Lease를 통해 Primary를 선출하는 메커니즘 검증. RFC 0003 §1~§9 코드 강제.

| TC 그룹 | 검증 내용 |
|---|---|
| **Lease 파라미터 유효성** | `LeaseDuration > RenewDeadline > RetryPeriod` 제약 위반 시 에러; 기본값이 제약을 만족함 |
| **Lease 이름 규약** | `PrimaryLeaseName("orders","shard",0)` → `"orders-shard-0-primary"`, ordinal -1 에러, role=router 에러 |
| **Reshard Target Lease 이름** | `ReshardTargetLeaseName("orders","shard-0a")` → `"orders-rsd-shard-0a-primary"`, 빈 shardID 에러 |
| **충돌 격리 불변식** | ordinal 0~999와 reshard target lease 이름이 절대 겹치지 않음 (`-rsd-` segment로 분리) |
| **Real 입력 검증** | nil client / 빈 LeaseName / Namespace / Identity → 에러 |
| **Null Election** | 단독 노드 — Run() 즉시 Leader, OnStartedLeading·OnNewLeader·OnStoppedLeading 콜백 순서 검증 |
| **Follower Election** | 영구 Follower — ctx cancel 후 OnStartedLeading 미호출 확인 |
| **Mock Election** | SetStatus(Leader) → OnStartedLeading 발화, SetExternalLeader("p2") → 현 Leader demote + OnNewLeader("p2") |
| **인터페이스 일관성** | Real / Null / Follower / Mock 모두 `Election` 인터페이스를 만족하는지 컴파일 가드 |

**자원**: fake clientset, 실제 타이머 (LeaseDuration ~1.5s), goroutine.

---

### 3.3 `internal/instance/fencing` — PVC Fencing (커버리지 89.7%)

**목적**: 장애 발생 시 Split-brain 방지를 위한 PVC 라벨 기반 Fencing 동작 검증.

| TC | 검증 내용 |
|---|---|
| `TestPVCName_AppendsDataPrefix` | `"orders-coordinator-0"` → `"data-orders-coordinator-0"` 명명 규약 |
| `TestNewReal_RejectsEmptyFields` | nil client / 빈 namespace / 빈 PVCName → 에러 |
| `TestReal_MarkFenced_AddsLabel` | fake PVC에 `MarkFenced()` → `fencing=true` 라벨 추가 확인 |
| `TestReal_MarkFenced_Idempotent` | 이미 Fenced PVC에 2회 호출해도 에러 없음 |
| `TestReal_Unfence_RemovesLabel` | Fence 라벨만 제거, 다른 라벨(`app=postgres`) 보존 |
| `TestReal_IsFenced_Reflects` | 4개 케이스: 라벨 없음 / 무관 라벨 / fenced=true / fenced=false-string |
| `TestReal_IsFenced_NotFound` | PVC 미존재 → NotFound 에러 |
| `TestReal_VerifyNotFenced_*` | Fenced → `ErrFenced`, 클린 → nil |
| `TestMock_LifecycleAndCounters` | MarkFenced/Unfence/IsFenced 호출 카운터 + SetFenced 직접 전이 |

**자원**: fake clientset — 실 K8s API 없음, in-memory PVC 객체.

---

### 3.4 `internal/instance/supervise` — PostgreSQL 프로세스 관리 (커버리지 78.2%)

**목적**: PostgreSQL 프로세스(PID1 역할)의 Start/Stop/Reload/SIGKILL 동작을 실제 OS 프로세스로 검증.

**픽스처**: `testdata/fake-postgres.sh` — 실제 postgres 없이 signal trap을 흉내내는 bash 스크립트.

| TC | 검증 내용 |
|---|---|
| `TestNewReal_RejectsEmptyFields` | BinPath/DataDir/ConfigFile/HbaFile/LocalDSN 중 하나라도 비면 에러 |
| `TestNewReal_DefaultPort` | Port=0 → 5432 기본값 적용 |
| `TestReal_PIDZeroBeforeStart` | Start 전 PID=0 |
| `TestReal_StartStop` | Start → PID>0, Stop(5s timeout) → 정상 종료 |
| `TestReal_StartTwiceErrors` | 이미 실행 중에 Start → 에러 |
| `TestReal_StopBeforeStartErrors` | Start 전 Stop → 에러 |
| `TestReal_StopFastImmediate` | `INT_DELAY=0` 환경변수 → 즉시 종료 |
| `TestReal_StopTimeoutSendsSIGKILL` | `TERM_DELAY=2` 설정, 1초 ctx → timeout 에러 반환 후 SIGKILL로 프로세스 종료, ExitCh 신호 확인 |
| `TestReal_Reload_DoesNotError` | SIGHUP 전송 → fake-postgres가 "RELOADED" 출력 확인 |
| `TestReal_StartFailsWithBadBinary` | 존재하지 않는 바이너리 경로 → Start 에러 |
| `TestReal_ExitCh_BroadcastsExit` | `EXIT_AFTER=0.2` → 0.2초 후 ExitCh 신호 수신 |
| `TestReal_StartFailsImmediately` | `FAIL_ON_START=1` → fork 성공 후 즉시 비정상 종료, ExitCh에 non-nil 에러 |
| `TestMock_*` | Mock 구현의 Start/Stop 카운터, 에러 주입, SlotCallTracking, SimulateExit, IsReady 상태 전이 |

**자원**: 실제 OS 프로세스 fork/exec (bash), `t.TempDir()`, POSIX signal (SIGTERM, SIGHUP, SIGKILL), stderr pipe.

---

### 3.5 `internal/postgres` — SQL DDL 빌더 (커버리지 95.4%)

**목적**: `GRANT`, `REVOKE`, `ALTER DEFAULT PRIVILEGES` SQL 문이 결정적으로, 안전하게 생성되는지 검증.

| TC | 검증 내용 |
|---|---|
| TABLE SELECT/INSERT | 정렬(알파벳) 후 결정적 SQL 출력 |
| ALL → ALL PRIVILEGES | `ALL` 키워드 확장 |
| WITH GRANT OPTION | SQL 접미사 추가 |
| REVOKE 일반 / GRANT OPTION FOR | `REVOKE`/`REVOKE GRANT OPTION FOR` 구분 |
| DEFAULT PRIVILEGES TABLE | `ALTER DEFAULT PRIVILEGES IN SCHEMA ... GRANT ... ON TABLES` |
| DEFAULT PRIVILEGES DATABASE/SCHEMA 거부 | `ErrInvalidGrant` — 지원하지 않는 object class 방어 |
| 입력 검증 | 빈 grantee/names/privileges, 잘못된 class/privilege → `ErrInvalidGrant` |
| quoting | `schema.table` → `"schema"."table"` 분리, `"` → `""` 이스케이프 |
| 중복 privilege 제거 | `SELECT,select,SELECT` → `SELECT` 단일화 |
| 결정성 | 동일 입력 → 동일 출력 (순서 고정 보장) |
| FUNCTION / SEQUENCE | `ON FUNCTION`, `ON SEQUENCE` 문법 지원 |

**자원**: 순수 Go 함수 — OS/네트워크 의존 없음.

---

### 3.6 `internal/plugin` — Plugin Registry (커버리지 91.7%)

**목적**: 5종 플러그인 인터페이스(BackupPlugin, ExporterPlugin, ExtensionPlugin, RouterPlugin, AuthPlugin)가 올바르게 등록·조회되는지 검증.

| TC | 검증 내용 |
|---|---|
| `TestRegistry_RegisterAndLookup` | 5종 플러그인 등록 후 이름으로 조회 |
| `TestRegistry_DuplicatePanics` | 동일 이름 중복 등록 → panic |
| `TestBackupOptions_ExecutionModeField` | `sidecar` / `job` / 빈 문자열 3가지 mode 컴파일 가드 |
| `TestEnabledExtensions_FilterAndSort` | ① 부분 opt-in + SharedPreloadOrder 정렬, ② missing 이름 보고, ③ nil → 빈 슬라이스 |

---

### 3.7 `internal/router` — Shard 라우팅 (커버리지 74.6%)

**목적**: ShardRange 기반 Vindex(hash/range/consistent-hash/lookup) 라우팅이 결정적으로 동작하는지 검증.

| TC | 검증 내용 |
|---|---|
| hash murmur3 결정성 | 동일 key → 동일 shard (2회 반복) |
| hash fnv / crc32 / murmur3 | 3개 hash function 모두 유효한 shard 반환 |
| range 사전식 비교 | `AA`→shard-west, `MN`→shard-east 등 4개 케이스 |
| range gap → ErrVindexNoMatch | 매핑 없는 key에 명시적 에러 |
| consistent-hash / lookup → ErrVindexUnsupported | 미구현 type에 명시적 에러 (미래 변경 방지) |
| ValidateNoOverlap 정상 | 비겹침 range 검증 통과 |
| ValidateNoOverlap overlap 검출 | `0x70000000` 겹침 구간 감지 |
| ValidateNoOverlap non-hash skip | range type은 hex 파싱 없이 skip |
| murmur3 known vector | `murmur3('', seed=0) == 0` (Apache Vitess 기준) |

---

### 3.8 `internal/version` — 이미지 버전 매트릭스 (커버리지 78.6%)

**목적**: 지원 PG 버전 목록과 이미지 digest pin 정책이 회귀되지 않도록 봉인.

| TC | 검증 내용 |
|---|---|
| PG18 stable | `ChannelStable`, 이미지에 `@sha256:` 포함 (digest pin 강제) |
| PG17 stable | `ChannelStable` |
| PG16 stable | `ChannelStable` (legacy) |
| PG15/99 미지원 | `IsSupported` → `ok=false` |
| `Stable()` 결과 | 최소 1개 이상, 전부 `ChannelStable` |

**배경**: PG18 이미지는 mutable tag 대신 digest pin — 캐시된 stale 바이너리가 부팅되면 Fence Deadlock이 발생하는 버그(issue #218)에 대한 영구 방어.

---

### 3.9 `internal/controller` — Reconciler 통합 테스트 (커버리지 72.6%)

**목적**: 실제 Kubernetes API Server(envtest)에 CRD를 등록하고, Reconciler가 StatefulSet/Service/ConfigMap 등의 desired state를 올바르게 생성·갱신하는지 검증.

**환경**: in-process `etcd` + `kube-apiserver` 바이너리 기동, Ginkgo BDD 스타일.

주요 검증 항목 (test 파일 목록 기준):
- `postgrescluster_controller_test.go`: PostgresCluster 생성 → StatefulSet/Service 생성 확인
- `backupjob_controller_test.go`: BackupJob Reconcile 흐름
- `pooler_controller_test.go`: PgBouncer Deployment 생성
- `postgresdatabase_controller_test.go`: PostgresDatabase DDL 실행
- `postgresuser_controller_test.go`: PostgresUser 역할 관리
- `scheduledbackup_controller_test.go`: 크론 기반 BackupJob 자동 생성
- `failover_debounce_test.go`, `failover_promoter_test.go`: 8s debounce + promotion 통합 흐름
- `aggregate_status_test.go`: 다수 shard의 Condition 집계
- `cascade_delete_test.go`: Owner Reference 기반 연쇄 삭제
- `pdb_test.go`: PodDisruptionBudget 생성/갱신

---

### 3.10 `internal/webhook/v1alpha1` — Admission Webhook (커버리지 94.7%)

**목적**: PostgresCluster CRD 생성/수정 요청의 Admission 검증 로직이 올바르게 거부·허용하는지 검증.

**환경**: envtest (webhook 인증서 자동 발급 포함).

검증 항목:
- `admission_roundtrip_test.go`: Defaulting → Validation 왕복 테스트
- `postgrescluster_webhook_test.go`: spec 유효성 (지원 PG 버전, shard 수, 스토리지 크기 등) 위반 케이스 거부

---

## 4. 테스트 계층 정리

```
┌──────────────────────────────────────────────────────────────┐
│  e2e (test/e2e/)                                             │
│  Kind 클러스터 + 실제 PostgreSQL Pod — make test-e2e          │
│  (현재 실행 제외; Kind 클러스터 생성 필요)                       │
├──────────────────────────────────────────────────────────────┤
│  통합 테스트 (envtest)                                         │
│  in-process etcd + kube-apiserver, 실제 CRD/Reconciler        │
│  → internal/controller, internal/webhook/v1alpha1            │
├──────────────────────────────────────────────────────────────┤
│  단위 테스트 (fake clientset / OS process / pure function)    │
│  → failover, election, fencing, supervise, postgres, plugin   │
│     router, version, backup plugins                          │
└──────────────────────────────────────────────────────────────┘
```

---

## 5. 현재 커버리지 미비 영역

| 패키지 | 커버리지 | 비고 |
|---|---|---|
| `api/v1alpha1` | 2.2% | CRD 타입 정의 — 대부분 구조체, 동작 코드 적음 |
| `cmd/instance` | 6.9% | PID1 main() — OS 의존으로 통합 테스트 한계 |
| `internal/plugin/extension/*` | 0% | pgaudit/pgvector 등 — SQL 실행 필요, 실 PG 없이 불가 |
| `internal/instance/statusapi` | 0% | HTTP 상태 API — 단위 테스트 미작성 |
| `cmd/reshard-copy-poc` | 0% | PoC 코드 — 테스트 미작성 |

---

## 6. CRLF 문제와 .gitattributes 조치

### 문제 원인

Windows git의 기본 설정(`core.autocrlf=true`)은 checkout 시 LF→CRLF 변환을 수행한다.  
`#!/usr/bin/env bash\r` 형태의 shebang은 Linux에서 `bash\r` 라는 존재하지 않는 인터프리터로 처리되어 실행이 실패한다.

### 영향 범위

| 파일 | 증상 |
|---|---|
| `.devcontainer/post-install.sh` | `set: pipefail: invalid option name` — 컨테이너 기동 실패 |
| `testdata/fake-postgres.sh` | `env: 'bash\r': No such file or directory` — supervise 테스트 2건 실패 |

### 조치 1: `.gitattributes` 생성 (영구 해결)

`.gitattributes` 파일 추가로 git이 `.sh`를 포함한 모든 텍스트 파일을 LF로 관리하도록 강제한다.  
이후 Windows/Mac/Linux 어떤 환경에서 클론해도 스크립트는 LF로 체크아웃된다.

```
# 핵심 규칙
* text=auto eol=lf   # 기본: 모든 텍스트 LF
*.sh text eol=lf     # 셸 스크립트 명시적 LF 강제
*.go text eol=lf
```

### 조치 2: 기존 파일 정상화 (기존 클론 환경)

`.gitattributes` 추가 후 기존에 이미 클론된 환경에서는 `git` 명령으로 파일을 재정상화해야 한다:

```bash
# 인덱스 전체를 재노멀라이즈
git add --renormalize .
git commit -m "chore: normalize line endings via .gitattributes"
```

또는 작업 트리의 파일만 즉시 수정:

```bash
# Linux/macOS/WSL 또는 Dev Container 내부에서
find . -name "*.sh" -exec sed -i 's/\r//' {} +
```

---

## 7. 용어집

> 정의는 [GLOSSARY.ko.md](GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| envtest | 실제 클러스터 없이 API 서버/etcd만 띄워 컨트롤러를 통합 테스트하는 도구. |
| kind | Docker 컨테이너 안에 K8s 클러스터를 띄우는 도구. e2e 테스트에 사용. |
| 게이트(make gate) | lint + test + audit + validate를 묶은 릴리스 품질 게이트. |
| Reconcile / Reconciler | 실제 상태를 원하는 상태(spec)로 수렴시키는 controller-runtime 루프. |
| Lease (임대) | Kubernetes Lease 오브젝트. 외부 HA 에이전트 없이 이를 DCS로 써서 Primary 선출을 한다. |
| Fencing (PVC Fencing) | 옛/이상 Primary가 데이터에 쓰지 못하도록 PVC 접근을 차단해 split-brain을 막는 격리. |
| StatefulSet (STS) | 안정적 식별자·스토리지를 갖는 Pod 집합. PostgreSQL 인스턴스를 띄우는 워크로드. |
| DinD / DooD | Docker-in-Docker(컨테이너 안에서 또 데몬) / Docker-out-of-Docker(호스트 데몬 공유). |
