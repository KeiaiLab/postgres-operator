# E2E 테스트 종합 분석 보고서

## 2026-06-24 추가 검증: PITR restore drill 보강 완료

이번 추가 작업에서는 `PITR restore + checksum drill (D.3.2)`를 `PContext` 상태에서 실제 실행 가능한 `Context`로 전환하고, live Kind 환경에서 실패 원인을 단계적으로 제거했다.

### 최종 결과

| 항목 | 결과 |
|------|------|
| 대상 | `go test -count=1 -tags=e2e ./test/e2e -ginkgo.label-filter=p1 -ginkgo.focus='PITR restore'` |
| 환경 | Kind `postgres-operator-e2e-pitr-codex`, PostgreSQL 18, CertManager skip |
| 최종 결과 | **7 PASS / 0 FAIL / 60 SKIP** |
| 핵심 검증 | full backup 성공, WAL archive 확인, restore Job 성공, `before` row 존재, `after` row 부재, checksum failure 0 |

### RCA와 수정 요약

1. filesystem pgBackRest repo가 `EmptyDir`에 있어 restore 시 사라질 수 있었다. repo 경로를 data PVC 내부 `/var/lib/postgresql/data/pgbackrest`로 고정했다.
2. pgBackRest spool 기본 경로 `/var/spool/pgbackrest`가 read-only/non-root 환경에서 쓰기 실패했다. dataplane Pod와 restore Job에 `ephemeral-pgbackrest-spool` EmptyDir를 추가했다.
3. restore 후 PostgreSQL이 repo env 없는 기본 `restore_command`로 WAL을 찾지 못했다. restore 완료 후 `postgresql.auto.conf`의 `restore_command`를 repo env와 `--pg1-path` 포함 형태로 재작성했다.
4. `--archive-check=n` 때문에 WAL archive 없이도 full backup이 성공으로 표시될 수 있었다. 이 우회를 제거해 복구 불가능한 백업을 실패로 드러나게 했다.
5. `archive_command`가 `archive-push`에 `--pg1-user`/`--pg1-database`를 넘겨 pgBackRest가 거부했다. `stanza-create` 옵션과 `archive-push` 옵션을 분리했다.
6. shell positional arg `$1` 전달은 PostgreSQL config parser/outer shell에서 깨지기 쉬웠다. `archive-push "%p"`로 바꿔 PostgreSQL의 WAL path placeholder를 직접 전달했다.
7. 저트래픽 E2E에서는 WAL segment가 자연스럽게 archive되지 않는다. `pg_switch_wal()` 후 해당 WAL 파일이 repo에 생길 때까지 기다리도록 했다.
8. targetTime을 초 단위로 잘라 restore하면 `before` 트랜잭션 직전으로 복구될 수 있었다. `clock_timestamp()` + `RFC3339Nano` + pgBackRest microsecond 포맷으로 시점 정밀도를 보존했다.
9. restore 직후 Pod 재생성 타이밍 때문에 checksum 쿼리가 단발 실패할 수 있었다. checksum 검증도 `Eventually`로 감쌌다.

### 검증 명령

```bash
make manifests generate
make test

go test -count=1 -tags=e2e ./test/e2e \
  -timeout 30m -v \
  -ginkgo.v \
  -ginkgo.label-filter=p1 \
  -ginkgo.focus='PITR restore'
```

### Insight

PITR은 “restore 명령이 성공했다”만으로는 품질 기준을 만족하지 않는다. 백업 시점의 필수 WAL, target 이후 WAL switch/archive, recovery target 정밀도, restore 후 PostgreSQL 재기동까지 모두 연결되어야 한다. 이번 수정은 실패를 숨기던 `--archive-check=n`을 제거하고, 실제 archive 실패를 먼저 드러낸 뒤 그 원인을 고쳤다는 점에서 오픈소스 operator 기준에 더 가깝다.

---

> 실행 일시: 2026-06-22 06:12 ~ 06:32 KST  
> 명령: `CERT_MANAGER_INSTALL_SKIP=true make test-e2e-failover`  
> 환경: Dev Container (golang:1.26, Docker-in-Docker) · Kind v1.36.1 · PostgreSQL 18  
> 소요 시간: 645초 (약 10분 45초)  
> 결과: 4 PASS · 7 FAIL · 9 PENDING · 47 SKIP (11/67 spec 실행)

---

## 1. 프로젝트 방향성 요약

postgres-operator는 **MIT 라이선스 기반 자체 구축 PostgreSQL Kubernetes Operator**다.  
외부 PostgreSQL operator를 fork·embed하지 않고, K8s 위의 vanilla PostgreSQL 18+에서  
`G0(Day-0 배포) → G1(단일 샤드 HA) → G2(운영 품질) → G3(샤딩 기반) → G4(온라인 리샤딩) → G5(Distributed SQL) → G6(1.0.0 GA)` 순으로 진화하는 로드맵을 따른다.

> 아래 완성도는 **2026-06-22 failover drill 실행 시점 스냅샷**이다. 이후 PITR restore drill 완료
> (상단 "2026-06-24 추가 검증" 섹션, 7 PASS)와 PostgresDatabase/User `status.applied` live 검증,
> E2E fixture 독립화가 반영되기 전 값이므로 G1·G2는 실제로 이보다 진척돼 있다. 정확한 재산정은 전체
> p1/p2 E2E 재실행이 선행돼야 한다.

| Gate | 목표 | 현재 완성도 |
|------|------|------------|
| G0 | Day-0 배포 | **100%** |
| G1 | Single-shard HA (failover + sync repl) | **81%** (live chaos/shard-identity 재검증 필요) |
| G2 | 운영 품질 (Pooler / Hibernation / RBAC / Observability) | **72%** (live drill 미완) |
| G3 | Sharding foundation | 37% |
| G4~G5 | Resharding · Distributed SQL | 0% |
| G6 | 1.0.0 GA | 12% |

**이번 e2e 테스트(`label=p2`)의 목적**: G1 HA failover 경로와 G2 운영 기능(Pooler, Hibernation, ImageCatalog, DB/User 선언적 관리)이 **실제 Kind 클러스터 + 실제 PostgreSQL Pod 위에서** 설계대로 동작하는지 검증.

---

## 2. 테스트 환경 구성 (BeforeSuite)

| 단계 | 내용 | 결과 |
|------|------|------|
| Kind 클러스터 생성 | `kindest/node:v1.36.1` 노드 1개 | ✅ 완료 |
| Operator 이미지 빌드 | `make docker-build IMG=ghcr.io/keiailab/postgres-operator:0.3.0-alpha` | ✅ 완료 (약 1분 22초, e2e 전용 임시 테스트 태그) |
| PG 런타임 이미지 빌드 | `Dockerfile.pg --build-arg PG_MAJOR=18` → `ghcr.io/keiailab/pg:18` | ✅ 완료 |
| Kind에 이미지 로드 | `kind load docker-image ...` | ✅ 완료 |
| CRD + RBAC 설치 | `kubectl apply --server-side -f dist/install.yaml` | ✅ 완료 |
| CertManager | `CERT_MANAGER_INSTALL_SKIP=true` → 설치 생략 (Webhook 없음) | ✅ 생략 정상 |

> **dist/install.yaml** : Webhook 설정 없이 8 CRD + RBAC + Deployment만 포함. CertManager 불필요하도록 설계된 것이 확인됨.

---

## 3. 통과한 테스트 케이스 (4개)

### TC-PASS-01: Failover — ord-0 초기 primary 선출
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G1 · `internal/controller/failover/` · ROADMAP `[x] Primary-delete e2e baseline` |
| **테스트 파일** | `test/e2e/failover_e2e_test.go:161` (It #1) |
| **테스트 니즈** | PostgresCluster 생성 시 StatefulSet ord-0이 primary로 자동 선출되고, `status.shards[0].primary.pod`가 `failover-shard-0-0`으로 기록되는지 확인 |
| **기대 결과** | `status.shards[0].primary.pod=failover-shard-0-0`, `ready=true` (2분 이내) |
| **실제 결과** | **통과** |
| **성공 근거** | `PostgresClusterReconciler`가 StatefulSet 생성 후 instance manager의 `instance-status` annotation을 읽어 ShardStatus를 집계. ord-0이 `pg_ctl start`로 primary 기동 후 annotation 게시 → reconciler가 status에 반영 |

---

### TC-PASS-02: Failover — ord-1 standby 부팅 + role=replica annotation
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G1 · `[x] Replica rejoin (pg_basebackup)` |
| **테스트 파일** | `test/e2e/failover_e2e_test.go:175` (It #2) |
| **테스트 니즈** | ord-1이 ord-0에서 `pg_basebackup`으로 초기 데이터를 받아와 streaming standby로 부팅되는지, `standby.signal` 파일이 PGDATA에 존재하는지 확인 |
| **기대 결과** | `instance-status` annotation의 `role=replica` + `PGDATA/standby.signal` 존재 (3분 이내) |
| **실제 결과** | **통과** |
| **성공 근거** | instance manager init container가 `pg_basebackup --checkpoint=fast` 실행 → `standby.signal` 생성 → PostgreSQL이 streaming replication 모드로 기동. `A.1 basebackup drill PASS (T31, 2026-05-17)` 로드맵 항목과 정합 |

---

### TC-PASS-03: Hibernation — annotation on → STS scale-down to 0
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] 선언적 hibernation` (ROADMAP: "live kind 검증 pending") |
| **테스트 파일** | `test/e2e/hibernation_e2e_test.go:75` |
| **테스트 니즈** | `cnpg.io/hibernation=on` annotation 부착 시 operator가 모든 shard StatefulSet의 `spec.replicas=0`으로 축소하는지 확인 |
| **기대 결과** | STS spec.replicas = 0 (2분 이내) |
| **실제 결과** | **통과** (30.6초) |
| **성공 근거** | `internal/controller/postgrescluster_controller.go`의 reconcile loop가 annotation을 감지해 STS `replicas=0` 패치. 환경변수 `SMOKE_HIBERNATION=1` smoke 테스트에서 사전 검증된 경로 |

---

### TC-PASS-04: Hibernation — status.phase=Hibernated 전이
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] 선언적 hibernation` |
| **테스트 파일** | `test/e2e/hibernation_e2e_test.go:90` |
| **테스트 니즈** | Pod 스케일다운 후 `PostgresCluster.status.phase`가 `Hibernated`로 전이되는지 확인 |
| **기대 결과** | `status.phase = Hibernated` |
| **실제 결과** | **통과** (0.06초, 앞 TC 직후 즉시 확인) |
| **성공 근거** | `internal/controller/status.go`의 `aggregateStatus`가 "모든 STS replicas=0" 조건을 감지해 phase를 Hibernated로 설정 |

---

## 4. 실패한 테스트 케이스 (7개)

### TC-FAIL-01: Failover — primary kill 후 RTO 30초 이내 신규 primary 선출
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G1 · `[ ] 자동 failover 로직` (ROADMAP 미완성 항목) |
| **테스트 파일** | `test/e2e/failover_e2e_test.go:206` (It #3) |
| **테스트 니즈** | `kubectl delete pod --force --grace-period=0`으로 primary를 강제 종료한 뒤, operator가 alive replica를 탐지해 30초 안에 새 primary를 선출하는 자동 failover 경로 검증 |
| **기대 결과** | 45초 window 안에 `status.shards[0].primary.pod`가 신규 Pod로 변경 + `ready=true`. 실측 RTO < 30초 |
| **실제 결과** | **실패** — 45초 timeout. `failover-shard-0-0=true` 유지 (primary 미변경) |
| **실패 메시지** | `primary still failover-shard-0-0 (no failover): failover-shard-0-0=true` |

**근본 원인 분석**:

```
[현상] primary pod force-delete → K8s StatefulSet controller가 ~30초 안에 동일 PVC로 
       동일 ordinal pod를 재생성 → operator가 "self-heal"로 판단, failover 미발동.

[원인 ①] force-delete 기반 e2e 시나리오가 live failover 조건을 만들지 못함
  - 현재 Reconcile()에는 clusterFailoverDecision() → shouldPromoteAfterDebounce() →
    executeClusterPromotion() 경로가 존재함
  - 즉 "reconcile 루프와 failover 감지 미연결"은 현재 소스 기준 RCA가 아님
  - 실패 재현에서는 StatefulSet self-heal이 동일 ordinal을 빠르게 복구해
    promotion 조건이 지속 관측되지 못함

[원인 ②] StatefulSet self-heal이 failover 감지 window를 선점
  - force-delete 후 STS가 동일 PVC+ordinal로 pod를 ~30초 안에 재생성
  - failover debounce/detect window(설계: 노드 실패 감지 후 수 초) 이전에 
    pod가 복구되므로 operator 입장에서 "transient pod restart"로 처리됨
  - 진짜 failover(노드 상실 / PVC 상실)는 pod가 재생성되지 않는 상황이어야 발동

[원인 ③] ADR-0027: shard-identity 재설계 필요
  - chaos 코드 주석(failover_chaos_test.go:91~100): "True failover (node loss / PVC loss) 
    needs the ground-up shard-identity redesign tracked by ADR-0027"
  - 현 구조는 ordinal 기반 식별로 인해 promotion 후 "어떤 pod가 새 primary인지"를 
    외부에서 안정적으로 지정하기 어려움
```

**ROADMAP 상태**: `[ ] 자동 failover 로직` — 현재 소스에는 failover trigger path가 있으나, StatefulSet self-heal을 넘어서는 live chaos 시나리오와 shard-identity transition 검증이 남아 있음.

---

### TC-FAIL-02: Hibernation — PVC 보존 (delete 금지)
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] 선언적 hibernation` (envtest 완료, live kind 검증 미완) |
| **테스트 파일** | `test/e2e/hibernation_e2e_test.go:99` |
| **테스트 니즈** | STS scale-down 후 PostgreSQL 데이터 PVC가 보존되어 재기동 시 데이터 연속성을 보장하는지 확인 |
| **기대 결과** | `kubectl get pvc -l postgres.keiailab.io/cluster=pg-hibernation-test` → 1개 이상 |
| **실제 결과** | **실패** — PVC 목록 빈 배열 `[]` |
| **실패 메시지** | `hibernation 후 PVC 보존되어야 함: Expected <[]string\|len:0> not to be empty` |

**근본 원인 분석**:

```
[원인] PVC에 postgres.keiailab.io/cluster=<name> 레이블 미부착
  - StatefulSet의 volumeClaimTemplate으로 생성된 PVC에는 기본적으로 
    STS controller가 설정하는 레이블만 존재
  - operator가 PVC 생성 시 postgres.keiailab.io/cluster 레이블을 명시적으로 
    부착하지 않아 label selector로 조회 시 0건 반환
  - PVC 자체는 존재하지만 조회 방법(label selector)과 실제 레이블이 불일치

[확인 필요] internal/controller/builders.go에서 PVC volumeClaimTemplate 생성 시
  레이블 주입 여부 → 미구현 또는 다른 레이블 키를 사용하는 것으로 추정
```

---

### TC-FAIL-03: Replica Cluster — streaming standby 도달
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] Replica cluster / externalClusters` (live cross-cluster drill 미완) |
| **테스트 파일** | `test/e2e/external_clusters_drill_e2e_test.go:82` |
| **테스트 니즈** | Source cluster A에서 `pg_basebackup`으로 데이터를 받아온 Replica cluster B가 streaming standby 모드(`pg_is_in_recovery()=true`)에 도달하는지 검증 |
| **기대 결과** | `SELECT pg_is_in_recovery()::text` → `true` (5분 이내) |
| **실제 결과** | **초기 실패** — DNS 해석 실패 및 standalone replica의 failover 경로 진입으로 streaming standby 유지 불가 |

**근본 원인 분석**:

```
[원인 ①] Source cluster 사전 부트스트랩 누락
  테스트 코드(external_clusters_drill_e2e_test.go:38) 주석:
  "Source cluster + 인증 정보 Secret 가정 (smoke.sh 가 사전 실행)"
  → 이번 테스트 실행에서는 "pg-source" 클러스터가 존재하지 않음
  → Secret "src-replicator-pwd"도 없음

[원인 ②] DNS 해석 실패
  PostgreSQL 로그: "could not translate host name 
  'failover-shard-0-0.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local' 
  to address: Name or service not known"
  → Replica cluster가 pg-failover-e2e 네임스페이스의 cluster를 참조하려 했으나
     해당 네임스페이스는 별개의 테스트 클러스터(failover)이며 replicator 계정 없음

[원인 ③] 크로스-클러스터 네트워크 전제 조건 미충족
  - ROADMAP에 "ordinal-0 외부 pg_basebackup + password passfile + TLS client cert" 
    완료로 표기되어 있으나, 실제 e2e에서는 source cluster 준비 + replicator Secret 
    사전 생성이 필요 (테스트 BeforeAll에서 처리하지 않음)
```

---

### TC-FAIL-04: PostgresDatabase — status.applied=true 미도달
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] CRD PostgresDatabase` (Live smoke 검증 pending) |
| **테스트 파일** | `test/e2e/postgresdatabase_e2e_test.go:73` |
| **테스트 니즈** | `PostgresDatabase` CR 적용 시 operator가 실제 PostgreSQL에 database + schema + privilege를 생성하고 `status.applied=true`를 설정하는지 확인 |
| **기대 결과** | `status.applied = true` (2분 이내) |
| **실제 결과** | **실패** — timeout |

**근본 원인 분석**:

```
[원인 ①] 전제 클러스터 "quickstart" 없음
  - 테스트 코드 주석: "전제: PostgresCluster 'quickstart'가 동일 ns에 Ready=True. 
    별 BeforeSuite가 quickstart를 부트스트랩하거나, smoke.sh가 선 실행"
  - p2 label 테스트만 실행 시 quickstart 클러스터가 없어 PostgresDatabase 
    controller가 ready primary를 찾지 못함

[원인 ②] live e2e와 unit 검증 범위가 분리되어 있음
  - 현재 unit 테스트에서는 ready primary 대상 reconcile 후 `status.applied=true` 경로가 확인됨
  - 따라서 이 실패의 1차 RCA는 quickstart 클러스터/owner user 등 live fixture 전제조건 미충족
  - 남은 작업은 독립 fixture 기반으로 e2e를 재실행해 실제 PostgreSQL Pod 위의 SQL 적용까지 확인하는 것
```

---

### TC-FAIL-05: PostgresUser — status.applied=true 미도달
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] CRD PostgresUser` (Live smoke 검증 pending) |
| **테스트 파일** | `test/e2e/postgresuser_e2e_test.go:64` |
| **테스트 니즈** | `PostgresUser` CR 적용 시 PostgreSQL에 role이 생성되고 `status.applied=true` 설정 확인 |
| **기대 결과** | `status.applied = true` |
| **실제 결과** | **실패** — TC-FAIL-04와 동일 원인 |

**근본 원인**: PostgresDatabase와 동일 — `quickstart` 클러스터 부재가 1차 원인. `status.applied` unit 경로는 확인됐으므로 독립 fixture 기반 live e2e 재검증이 필요.

---

### TC-FAIL-06: Pooler — PgBouncer Deployment 2/2 Ready
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] Connection pooler (PgBouncer)` (ROADMAP: "Pooler Service psql smoke 2026-05-12 kind에서 통과") |
| **테스트 파일** | `test/e2e/pooler_e2e_test.go:54` |
| **테스트 니즈** | `Pooler` CR 생성 시 2개의 PgBouncer Pod가 Ready 상태에 도달하는지 확인 |
| **기대 결과** | Deployment readyReplicas = 2 (3분 이내) |
| **실제 결과** | **실패** — BeforeAll 단계에서 timeout |

**근본 원인 분석**:

```
[원인] Pooler가 참조하는 PostgresCluster "quickstart"가 해당 namespace에 없음
  - 테스트에서 Pooler.spec.cluster = "quickstart"를 참조
  - "pg-pooler-e2e" namespace에는 "quickstart" 클러스터가 없음
  - PgBouncer의 userlist.txt 생성을 위해 primary cluster의 Secret이 필요한데
    참조 cluster 부재로 Secret 조회 실패 → Deployment Pod가 ContainerCreating/Error

[참고] ROADMAP에 "Pooler Service psql smoke PASS (2026-05-12)"가 기록되어 있으나,
  이는 hack/smoke.sh로 quickstart 클러스터를 먼저 준비한 뒤 실행한 시나리오.
  독립 실행(fresh kind, p2 label only)에서는 사전 준비 없이는 재현 불가.
```

---

### TC-FAIL-07: ImageCatalog — STS 이미지 교체 (pg:17 → pg:18 rollout)
| 항목 | 내용 |
|------|------|
| **로드맵 위치** | G2 · `[~] ImageCatalog / ClusterImageCatalog` (live rollout 측정 pending) |
| **테스트 파일** | `test/e2e/imagecatalog_e2e_test.go:52` |
| **테스트 니즈** | ImageCatalog에서 major=17 이미지를 참조하는 클러스터를 생성 후 kind에서 실제로 해당 이미지로 STS가 기동하는지 확인 |
| **기대 결과** | StatefulSet 이미지 = `ghcr.io/keiailab/pg:17` |
| **실제 결과** | **실패** |

**근본 원인**:

```
[원인] pg:17 이미지 Kind 클러스터 미적재
  - Dockerfile.pg는 PG_MAJOR 인수로 빌드
  - 이번 테스트에서는 PG_MAJOR=18만 빌드하여 Kind에 로드
  - pg:17 이미지가 없으면 ImagePullBackOff → Pod 미기동 → STS 이미지 확인 불가
  - 추가로 ImageCatalog controller의 catalog → STS 이미지 매핑 live 검증도 pending 상태
```

---

## 5. PENDING 항목 — 미구현 사유와 해결 경로

> 2026-06-22 기준 PENDING 9개. 이후 **그룹 B(PITR) 4개는 2026-06-24 전부 해소**(상단 섹션 참조) → 잔존 5개.
> 잔존 PENDING의 핵심은 그룹 A(자동 failover live drill)이며, reconcile 연결·promotion·fencing 코드는
> 완료 상태이고 node/PVC loss 계열 live 재검증만 남았다.

### PENDING 그룹 A: 자동 Failover 프로모션 (GA #248, ADR-0027)

관련 파일: `test/e2e/failover_chaos_test.go:101` (`PContext`)

PENDING 항목 (4개):
- 초기 primary 식별 (chaos)
- Primary force delete (chaos)
- replica가 새 primary로 promotion (RTO < 60s)
- 이전 primary가 standby로 rejoin
- Cluster Ready=True 복귀

**구현/검증 상태**:

| 계층 | 구현 상태 |
|------|-----------|
| `DetectPrimaryFailure()` (detection.go) | **완료** — 순수 함수, 9 unit test 통과 |
| `SelectPromotionCandidate()` (detection.go) | **완료** — LagBytes 오름차순 정렬 |
| `BuildPromotionPlan()` (promotion.go) | **완료** — 4단계 플랜 생성 |
| `PromoteFromDecision()` (promotion.go) | **완료** — Promoter interface 호출 |
| reconcile 루프 → failover 트리거 연결 | **코드상 완료** — `clusterFailoverDecision()` → `shouldPromoteAfterDebounce()` → `executeClusterPromotion()` 경로 존재 |
| shard-identity 재설계 (ADR-0027) | **설계 완료, 구현 진행 중** |

**라이브 RCA (2026-06-16 chaos 코드 주석)**: StatefulSet이 force-delete된 primary Pod를 동일 PVC·ordinal로 ~30초 내에 재생성하여 failover detect/debounce window를 선점. 진짜 failover는 노드 상실·PVC 상실 시나리오에서만 발동 가능.

**해결 경로** (ADR-0027 P1~P6 단계별):

```
Step 1 (P1, 즉시 착수 가능):
  - 격리 label 헬퍼 추가: postgres.keiailab.io/reshard-target=<shardID>
  - TargetShardStatefulSetName() 함수 구현
  - unit test로 검증

Step 2 (현 코드 기준 완료, live 재검증 필요):
  - PostgresClusterReconciler.Reconcile()에서 failover decision 산출 후
    debounce window가 충족되면 executeClusterPromotion() 실행
  - 남은 과제는 force-delete가 아닌 node loss/PVC loss 계열 live drill에서
    promotion 조건과 RTO를 검증하는 것

Step 3 (ADR-0027 P6): shard-identity transition
  - 승격된 pod를 ordinal primary로 공식화
  - reshard-target label → shard ordinal label 전환
  - operator-driven single-authority 패턴으로 race 회피

Step 4 (e2e 검증):
  - PContext → Context로 전환 (이 한 줄이 PENDING 해제 기준)
  - KIND_CLUSTER 환경에서 노드 drain 또는 taint를 사용한 
    "진짜 노드 불가 접근" 시나리오로 테스트
```

---

### PENDING 그룹 B: PITR Restore + Checksum (GA #248) — ✅ 2026-06-24 전부 해소

관련 파일: `test/e2e/pitr_restore_e2e_test.go`

> **상태 갱신 (2026-06-24)**: 본 그룹의 4개 항목은 모두 `PContext` → `Context` 로 전환되어
> live Kind 환경에서 **7 PASS / 0 FAIL** 로 통과했다. 상세 RCA·수정 내역은 본 문서 상단
> "2026-06-24 추가 검증: PITR restore drill 보강 완료" 섹션 참조. 아래 항목/표는 이력 보존용이다.

해소된 항목 (구 PENDING):
- `BackupJob type=restore + targetTime` 적용 후 phase=Succeeded
- restore 후 marker row 'before' 존재 확인
- restore 후 'after' row 부재 (PITR 시점 정확성)
- `pg_checksums --check` 데이터 무결성 검증

**구현/검증 상태 (✅ 해소)**:

| 계층 | 구현 상태 |
|------|-----------|
| `BackupJob.spec.type=restore` → `RestorePIT()` call path | **완료** |
| pgBackRest command-runner plugin | **완료** |
| K8s sidecar exec path | **완료** |
| 실제 pgBackRest restore 오케스트레이션 | **완료** — `backupjob_controller.go reconcileSidecarRestore` (STS scale-0 → Pod 정지 대기 → data PVC 마운트 restore Job → restore-in-progress 락) |
| WAL 아카이빙 + 복원 검증 drill | **완료** — 2026-06-24 live drill 7 PASS (full backup → WAL archive → restore → before 존재/after 부재 → checksum 0) |

**해결 경로**:

```
Step 1: WAL 아카이빙 설정 검증
  - pgBackRest stanza 생성 + WAL archive-push 정상 동작 확인
  - BackupJob type=full 실행 → phase=Succeeded 확인

Step 2: PITR 복원 오케스트레이션
  - RestorePIT(targetTime)이 pgBackRest restore --type=time --target=... 를 
    실제로 exec하는 코드 완성
  - restore 완료 후 BackupJob phase를 Succeeded로 전환하는 status 업데이트

Step 3: e2e 시나리오
  1) "before" 레코드 삽입 → 타임스탬프 기록 (t1)
  2) "after" 레코드 삽입
  3) BackupJob(type=restore, targetTime=t1) 생성
  4) restore 완료 후 "before" 존재, "after" 부재 확인
  5) pg_checksums --check로 데이터 무결성 검증
```

---

## 6. 추가로 필요한 작업 (소스코드 방향성 기반)

### 6-1. 즉시 수정 가능한 항목 (테스트 환경 전제 조건 오류)

| 항목 | 문제 | 해결 방법 |
|------|------|-----------|
| Pooler, PostgresDatabase, PostgresUser 테스트 | `quickstart` 클러스터 전제 조건 미충족 | `BeforeAll`에 quickstart 클러스터 부트스트랩 추가 또는 독립적인 전용 클러스터 생성 |
| ImageCatalog 테스트 | pg:17 이미지 미적재 | `BeforeAll`에서 `docker build -f Dockerfile.pg --build-arg PG_MAJOR=17` + kind load 추가 |
| Replica Cluster 테스트 | source cluster + replicator Secret 미생성 | `BeforeAll`에 source cluster 생성 + `src-replicator-pwd` Secret 생성 코드 추가 |
| Hibernation PVC label | PVC에 cluster 레이블 미부착 | `builders.go`의 `VolumeClaimTemplate`에 `postgres.keiailab.io/cluster=<name>` 레이블 추가 |

### 6-2. 구현/재검증 항목 (ROADMAP 기준)

| 우선순위 | 항목 | ROADMAP 참조 | 예상 난이도 |
|----------|------|-------------|------------|
| **HIGH** | Failover live chaos/shard-identity 검증 | G1 `[ ] 자동 failover` | 높음 (ADR-0027 연동 필요) |
| **HIGH** | PVC retention label live 재검증 | G2 `[~] 선언적 hibernation` | 낮음 (unit 수정 완료) |
| **MEDIUM** | PostgresDatabase/User live e2e fixture 재검증 | G2 `[~] CRD` | 낮음~중간 (unit 경로 확인됨) |
| **MEDIUM** | PITR restore 오케스트레이션 | G1 `[~] PITR restore` | 높음 |
| **MEDIUM** | ImageCatalog pg:17/18 이미지 live 재검증 | G2 `[~] ImageCatalog` | 낮음 (helper 추가됨) |
| **LOW** | Failover chaos e2e PContext 해제 조건 정리 | G1 `[ ] 자동 failover` | 중간 |

### 6-3. 테스트 아키텍처 개선 방향과 후속 상태

```
기존 문제: 여러 p2 테스트가 "quickstart" 클러스터에 암묵적으로 의존하지만,
           p2 단독 실행 시 해당 클러스터가 없어 BeforeAll에서 실패.

권장 개선:
  Option A) 각 테스트 파일의 BeforeAll에 전용 PostgresCluster 생성 코드 추가
            (현재 Failover, Hibernation 테스트가 이미 이 패턴 사용 → 일관성)
  
  Option B) e2e_suite_test.go의 BeforeSuite에 공통 "base" 클러스터 생성 추가
            (quickstart를 BeforeSuite에서 생성하면 모든 p2 테스트에서 재사용 가능)
  
  권장: Option A — 각 테스트가 독립적으로 실행 가능해야 한다는 원칙에 부합.
        테스트 간 상태 공유는 flaky test의 원인이 됨.
```

후속 상태: Pooler, PostgresDatabase, PostgresUser, ImageCatalog, External Cluster는 전용 helper 기반 부트스트랩으로 정리됨. 현재 검증 수준은 e2e 패키지 컴파일까지이며, Kind live 재실행으로 fixture 독립성과 실제 SQL 적용을 확인해야 한다.

---

## 7. 전체 결과 매트릭스

| 테스트 ID | 테스트명 | Gate | 결과 | 사유 분류 |
|-----------|----------|------|------|-----------|
| TC-PASS-01 | Failover: ord-0 primary 선출 | G1 | ✅ PASS | - |
| TC-PASS-02 | Failover: ord-1 standby + basebackup | G1 | ✅ PASS | - |
| TC-PASS-03 | Hibernation: STS scale-down | G2 | ✅ PASS | - |
| TC-PASS-04 | Hibernation: phase=Hibernated | G2 | ✅ PASS | - |
| TC-FAIL-01 | Failover: RTO 30s 자동 promote | G1 | ❌ FAIL | live chaos/shard-identity 재검증 필요 |
| TC-FAIL-02 | Hibernation: PVC 보존 | G2 | ❌ FAIL | 레이블 미부착 (builders.go) |
| TC-FAIL-03 | Replica Cluster: streaming standby | G2 | ❌ FAIL | 전제조건 미충족 + DNS 실패 |
| TC-FAIL-04 | PostgresDatabase: status.applied | G2 | ❌ FAIL | 전제조건 미충족 (unit 경로 확인) |
| TC-FAIL-05 | PostgresUser: status.applied | G2 | ❌ FAIL | 전제조건 미충족 (unit 경로 확인) |
| TC-FAIL-06 | Pooler: PgBouncer 2/2 Ready | G2 | ❌ FAIL | 전제조건 미충족 |
| TC-FAIL-07 | ImageCatalog: pg:17 STS 이미지 | G2 | ❌ FAIL | pg:17 이미지 미적재 |
| TC-PEND-01~04 | Failover chaos drill | G1 | ⏳ PENDING | 구현 중 (ADR-0027) |
| TC-PEND-05~09 | PITR restore + checksum | G1 | ⏳ PENDING | 구현 중 (GA #248) |

### 실패 원인 분류 요약

| 분류 | 건수 | 항목 |
|------|------|------|
| **구현/검증 미완성** (ROADMAP 명시) | 2 | TC-FAIL-01, TC-PEND-01~09 |
| **테스트 전제조건 미충족** | 4 | TC-FAIL-03~06 |
| **코드 결함** (간단 수정) | 1 | TC-FAIL-02 (PVC 레이블) |
| **이미지 미적재** | 1 | TC-FAIL-07 |

---

## 8. 결론 및 다음 단계

### 현재 상태 평가

- **G0(Day-0 배포)**: 완전 동작 확인 — Kind 클러스터에 operator + CRD 설치, PG Pod 기동, primary/standby 구성 모두 정상
- **G1(HA)**: 탐지·승격 로직과 reconcile trigger path는 코드상 존재. 남은 핵심은 StatefulSet self-heal을 넘어서는 shard-identity/live chaos 검증
- **G2(운영 품질)**: 기반 기능(Hibernation scale-down, phase 전이)은 동작. Pooler·DB·User·ImageCatalog의 live e2e는 테스트 전제조건 정비 후 재검증 필요

### 작업 스트림 분리 기준

오픈소스 operator 기준에서는 분석, 테스트 픽스, 제품 코드, 릴리스 산출물을 한 PR에 섞지 않는다. 장애가 발생했을 때 원인 추적이 가능해야 하므로 다음 역할 단위로 분리한다.

| 스트림 | 역할 | 포함 항목 | 검증 기준 |
|--------|------|-----------|-----------|
| A. 분석/문서 | 현재 상태와 RCA 기록 | 프로젝트 개요, 기능 분석, 테스트/E2E 리포트 | 소스·ROADMAP·실측 로그 간 모순 없음 |
| B. 개발환경 | 재현 가능한 로컬/컨테이너 환경 | `.gitattributes`, Dev Container, WSL2 문서 | 새 clone에서 스크립트 LF 유지, Go 버전 일치 |
| C. 테스트 아키텍처 | e2e 독립 실행성 보장 | `BeforeAll` 독립 클러스터, pg:17 이미지 로드, source cluster fixture | `label=p2` 단독 실행 재현 가능 |
| D. 제품 코드 | 실제 operator 동작 수정 | PVC label, DB/User status, failover trigger, PITR restore | unit/envtest/e2e 중 해당 레이어 통과 |
| E. 릴리스/공급망 | 배포 아티팩트와 보안 신뢰성 | `dist/install.yaml`, Helm/OLM, SBOM, cosign | tag·image·chart 버전 정합, generated drift 없음 |

정리 순서는 A/B → C → D → E 로 둔다. 단, 릴리스 아티팩트의 의도치 않은 이미지 태그 변경처럼 배포 위험이 큰 변경은 즉시 분리하거나 제거한다.

### 우선순위별 다음 단계

```
[단기 — 테스트 픽스, 수일 이내]
1. builders.go: PVC volumeClaimTemplate에 postgres.keiailab.io/cluster 레이블 추가
2. 각 e2e 테스트 BeforeAll에 독립적 PostgresCluster 생성 코드 추가
   (Pooler, PostgresDatabase, PostgresUser, ImageCatalog, External Cluster)
3. ImageCatalog BeforeAll에 pg:17 이미지 빌드·로드 추가

[중기 — 구현, 수 주]
4. PostgresDatabase / PostgresUser status.applied live e2e 재검증 (unit-level 경로는 통과)
5. ADR-0027 P1/P3~P6: 격리 label 헬퍼, shard-identity transition, 자동 failover live chaos 검증
6. PITR restore 오케스트레이션 완성

[장기 — GA 준비]
7. ADR-0027 P3~P6 (shard-identity transition, 자동 failover e2e 검증)
8. 7일 soak + chaos engineering
9. SBOM + cosign 서명
```

### 후속 조치 상태

| 항목 | 상태 | 남은 검증 |
|------|------|-----------|
| TC-FAIL-02 PVC cluster label | unit-level 수정 완료 (`buildPGStatefulSet` VolumeClaimTemplate label) | `make test-e2e-failover` 재실행으로 live PVC selector 확인 |
| E2E 독립 부트스트랩 | Pooler/DB/User/ImageCatalog/External Cluster에 전용 cluster/image/source helper 추가 | `make test-e2e-failover` 재실행으로 live 독립 실행성 확인 |
| ImageCatalog pg:17/18 runtime image | helper에서 build/load 경로 추가 | live e2e 재실행으로 STS 이미지 pull 확인 |
| DB/User status.applied | unit-level status.applied 경로 확인 | 독립 fixture 기반 e2e 재실행 |
| Failover trigger wiring | 현재 코드상 `clusterFailoverDecision()` → `shouldPromoteAfterDebounce()` → `executeClusterPromotion()` 경로 존재 | node loss/PVC loss 계열 live drill |
| 5-shard failover 순수 로직 테스트 | `internal/controller/failover` 패키지 통과 | live multi-shard 장애복구 e2e는 별도 과제 |
| p2 live e2e 재실행 시도 | 컨테이너 기반 nested Kind에서는 kubeconfig host/TLS 문제로 BeforeSuite 중단, spec 0개 실행 | 실제 DevContainer/WSL2처럼 Kind API server 인증서 SAN과 접근 주소가 일치하는 환경에서 재실행 |

---

## 9. 2026-06-23 후속 검증 업데이트

이번 후속 작업은 "커밋"이 아니라 분석 기반 테스트/제품 표면 정리 트랙이다. 2026-06-22 리포트의 원본 결과는 그대로 보존하고, 아래 항목을 추가 실측으로 갱신한다.

### 9-1. 해결 또는 전진한 항목

| 항목 | 변경 내용 | 검증 결과 |
|------|-----------|-----------|
| TC-FAIL-02 PVC label | `buildPGStatefulSet`의 `volumeClaimTemplates`에 `postgres.keiailab.io/cluster=<cluster>` 레이블 추가 | `go test -count=1 ./internal/controller ./internal/controller/failover` 통과 |
| PostgresUser e2e | 독립 `quickstart` 부트스트랩, `spec.cluster.name`, Pod IP 기반 TCP 접속, `-c postgres` exec 보정 | targeted live e2e 통과: applied, role 생성, password rotate, old password reject, delete drop role |
| PostgresDatabase e2e | 독립 `quickstart` 부트스트랩, owner/reader user 생성, schema privilege manifest 보정 | targeted live e2e 통과 |
| ImageCatalog e2e | PG 17/18 런타임 이미지 build/load, `major` int, hash annotation path 보정 | targeted live e2e 통과 |
| Pooler e2e | 실제 리소스명 `<pooler>-pooler` 반영, PgBouncer/exporter 이미지 load, `stats_users` allowlist 추가, builtin role 기반 exporter/psql smoke 보정 | targeted live e2e 통과: 4 Passed, 0 Failed, 9 Pending, 54 Skipped |
| E2E helper | PG runtime image build/load, multi-platform image import fallback, 독립 cluster bootstrap helper 추가 | e2e package compile 통과 |

### 9-2. Pooler RCA 정정

초기 RCA는 `quickstart` 전제조건 미충족이었다. 후속 재실행에서 전제조건을 해소하자 추가 문제가 순차적으로 드러났다.

1. 테스트가 `pg-pooler-test-pgbouncer` Deployment를 조회했지만 컨트롤러 계약은 `PoolerDeploymentName(name) = <name>-pooler`였다.
2. exporter는 `postgres`로 PgBouncer admin DB에 접속했지만 builtin auth Secret에는 `keiailab_pooler_pgbouncer`만 존재했다.
3. exporter가 metrics를 읽으려면 PgBouncer `stats_users`가 필요했지만 controller allowlist가 이를 막고 있었다.
4. smoke용 `kubectl run`은 PG 이미지의 기본 entrypoint를 실행해 `POD_NAME` env 오류가 났다. `--command -- psql ...`로 command를 명시해 해결했다.

`admin_users`는 운영 명령 권한까지 열 수 있으므로 추가하지 않았다. exporter 목적에는 read-only 성격의 `stats_users`가 더 타이트한 표면이다.

### 9-3. 아직 제품 이슈로 남은 항목

| 항목 | 현재 상태 | 다음 방향 |
|------|-----------|-----------|
| Failover live drill | 초기 부트/standby 경로는 일부 전진했지만, force-delete 기반 자동 failover는 StatefulSet self-heal과 shard-identity 문제가 남음 | ADR-0027 shard-identity transition + node/PVC loss 계열 live drill |
| External replica cluster | source fixture/DNS/assertion 및 standalone replica failover 제외 처리 후 targeted live e2e PASS | regression guard 유지, full e2e suite에서 재확인 |
| PITR restore | 여전히 PENDING | restore orchestration 완성 후 PContext 해제 |

### 9-4. 이번 후속 검증 로그

| 실행 | 결과 |
|------|------|
| `go test -count=1 ./internal/controller ./internal/controller/failover` | PASS |
| `go test -count=1 -tags=e2e ./test/e2e -run TestDoesNotExist` | PASS |
| Pooler targeted live e2e | PASS: 4 Passed, 0 Failed |
| PostgresUser targeted live e2e | PASS |
| PostgresDatabase targeted live e2e | PASS |
| ImageCatalog targeted live e2e | PASS |
| External replica targeted live e2e | PASS: 3 Passed, 0 Failed, 9 Pending, 55 Skipped |
| Failover targeted live e2e | FAIL: live failover/shard-identity 계열 문제 |

*본 보고서는 `CERT_MANAGER_INSTALL_SKIP=true make test-e2e-failover` 실행 결과, 소스코드(`internal/controller/failover/`, `test/e2e/`), ROADMAP.ko.md, ARCHITECTURE.ko.md, ADR-0027을 교차 분석하여 작성하였습니다.*

---

## 10. 2026-06-23 추가 RCA: External replica live e2e 복구

### 결론

External replica cluster 실패는 단일 원인이 아니었다.

1. 테스트 fixture의 source DNS가 실제 headless Service 이름과 달랐다.
   - 기존: `pg-source-shard-0-0.pg-source-headless...`
   - 실제: `pg-source-shard-0-0.pg-source-shard-0-headless...`
2. standalone replica cluster가 일반 HA failover 루프에 들어가 operator-driven promotion 대상이 됐다.
   - 결과: `standby.signal`이 제거되고 `.keiailab-promoted-primary`가 생기며 replica가 독립 primary로 전환됐다.
   - `pg_is_in_recovery()`가 `false`를 반환했다.
3. E2E assertion이 `SELECT pg_is_in_recovery()::text` 결과를 `t`로 기대했다.
   - `::text` 출력은 `true/false`이므로 기대값은 `true`가 맞다.

### 적용한 분리 원칙

`spec.replica.enabled=true` cluster는 local primary를 선출하는 HA cluster가 아니라 external source를 추종하는 standalone replica cluster다. 따라서 다음 경로에서 제외했다.

- switchover
- automatic failover decision/promotion
- stale standby reseed
- rogue primary reseed
- fallback synthetic primary 생성

대신 Ready 조건은 "primary ready"가 아니라 "replica shard ready" 의미로 처리한다.

### 검증 결과

| 검증 | 결과 |
|------|------|
| `go test -count=1 -timeout=5m ./internal/controller` | PASS |
| `go test -count=1 -timeout=5m ./internal/controller/failover` | PASS |
| `go test -count=1 -tags=e2e ./test/e2e -run TestDoesNotExist` | PASS |
| External replica targeted live e2e | PASS: 3 Passed, 0 Failed, 9 Pending, 55 Skipped |

라이브 확인값:

- `pg_is_in_recovery()::text` = `true`
- `standby.signal` present
- `.keiailab-promoted-primary` absent
- replica cluster primary lease absent
- PostgreSQL log: `entering standby mode`, `ready to accept read-only connections`, `started streaming WAL from primary`

---

## 11. 2026-06-23 추가 RCA: Failover 초기 부트스트랩과 자동 승격 분리

### 결론

Failover e2e는 두 문제가 섞여 있었다.

1. **초기 HA 부트스트랩 문제**: 최초 StatefulSet 생성 시 `PRIMARY_ENDPOINT`가 비어 있었고, ord-0 primary가 관측된 뒤 Pod template이 바뀌면서 StatefulSet rolling update가 primary ord-0을 재시작했다. 이 재시작이 false failover window를 만들었다.
2. **자동 승격 제품 과제**: primary force-delete 이후 StatefulSet이 같은 PVC/ordinal의 ord-0을 빠르게 self-heal하면서, ord-1 promotion exec와 기존 primary 복구가 경쟁한다.

이번 작업에서는 1번을 수정했고, 2번은 ADR-0027 shard-identity/fencing 계열 구현 과제로 남겼다.

### 적용한 수정

초기 HA cluster에서는 아직 status primary가 없어도 deterministic primary endpoint를 ord-0 DNS로 계산한다.

- standalone replica cluster: external `replica.endpoint` 사용
- hibernating cluster: endpoint 미설정
- status에 관측된 primary endpoint가 있으면 해당 값을 우선
- status에 primary pod만 있으면 pod DNS를 endpoint로 렌더링
- HA 초기 부트스트랩에서는 `<sts>-0.<headless>.<namespace>.svc.cluster.local:5432` 사용

이렇게 하면 최초 StatefulSet revision부터 standby가 ord-0을 향해 basebackup/streaming을 시작하므로, primary 관측 직후 Pod template이 바뀌어 ord-0이 불필요하게 재시작되는 경로를 차단한다.

### 검증 결과

| 검증 | 결과 |
|------|------|
| `go test -count=1 -timeout=5m ./internal/controller -run TestPrimaryEndpointForShard -v` | PASS |
| `go test -count=1 -timeout=5m ./internal/controller -run TestController --ginkgo.focus='sets deterministic ordinal-zero PRIMARY_ENDPOINT'` | PASS |
| `go test -count=1 -timeout=8m ./internal/controller` | PASS |
| `go test -count=1 -timeout=5m ./internal/controller/failover` | PASS |
| `go test -count=1 -tags=e2e ./test/e2e -run TestDoesNotExist` | PASS |
| Failover targeted live e2e: `spawns ord-1 as standby with role=replica annotation` | PASS: 1 Passed, 0 Failed, 9 Pending, 57 Skipped |
| Failover file live e2e: `failover_e2e_test.go` | 2 Passed, 1 Failed, 9 Pending, 55 Skipped |

남은 실패는 `promotes new primary within RTO 30s after primary kill`이다. 실패 시점의 이벤트는 `FailoverPromotionFailed`이며, exec 대상 container가 아직 준비되지 않았거나 `exit code 137`로 종료되는 promotion race가 관측됐다. 동시에 StatefulSet은 기존 ord-0을 같은 PVC/ordinal로 재생성하므로 status가 다시 `failover-shard-0-0=true`로 돌아간다.

### Insight

이번 수정은 테스트 기대값을 낮춘 것이 아니라, operator가 HA cluster를 처음 만들 때 불필요한 PodTemplate 변경을 만들지 않도록 부트스트랩 계약을 명확히 한 것이다. 반면 primary kill 후 자동 승격 실패는 단순 timeout 조정으로 해결할 문제가 아니다. StatefulSet ordinal identity와 database primary identity가 강하게 결합되어 있어, 기존 primary가 self-heal되는 순간 새 primary 승격과 충돌한다.

오픈소스 operator 기준에서는 이 테스트를 완화하지 않는 편이 맞다. 이 실패는 실제 운영 장애 시 split-brain, stale primary, promotion target 불안정성으로 이어질 수 있는 설계 신호이기 때문이다.

### 다음 구현 방향

1. E2E runner 안정화: 동일 image tag 재사용 시 manager Deployment가 자동 rollout되지 않으므로, 반복 실행에서는 fresh Kind cluster 또는 명시적 rollout restart/unique tag 전략을 적용한다.
2. ADR-0027 P1-P2: shard identity label/helper와 failover decision의 격리 경계를 먼저 구현한다.
3. promotion orchestration: candidate pod가 standby ready 상태인지 확인한 뒤 promote를 실행하고, 기존 primary ordinal이 self-heal로 다시 primary lease를 잡지 못하도록 fencing/partition/scale 전략을 검증한다.
4. live chaos 확장: force-delete뿐 아니라 node loss/PVC loss 계열 시나리오를 분리해 자동 failover의 실제 failure domain을 검증한다.

한 줄 결론: 초기 standby 부트스트랩은 수정·검증됐고, 남은 자동 failover 실패는 ADR-0027 수준의 shard identity/fencing 구현으로 풀어야 한다.

---

## 12. 2026-06-24 추가 보강: E2E manager rollout 안정화

### 문제

E2E suite는 manager 이미지를 매번 `ghcr.io/keiailab/postgres-operator:0.3.0-alpha`로 빌드하고 kind node에 로드한다. 하지만 같은 tag를 재사용하면 `kubectl apply -f dist/install.yaml`만으로는 Deployment PodTemplate이 바뀌지 않을 수 있다.

결과적으로 반복 실행에서는 새로 빌드한 controller image가 kind node에 있어도, 기존 `postgres-operator-controller-manager` Pod가 계속 살아 있어 이전 코드로 테스트가 돌 수 있다. 이 상태에서는 failover 수정 여부를 라이브 E2E로 판단하기 어렵다.

### 적용한 수정

`BeforeSuite`의 operator 설치 직후 다음 순서를 강제했다.

1. `kubectl -n postgres-operator-system rollout restart deployment/postgres-operator-controller-manager`
2. `kubectl -n postgres-operator-system rollout status deployment/postgres-operator-controller-manager --timeout=180s`
3. 기존 `kubectl wait --for=condition=Available ...` 유지

이 방식은 image tag를 바꾸지 않아도 Deployment PodTemplate에 restart annotation을 갱신해 새 ReplicaSet을 만들고, 이후 테스트가 최신 manager Pod 위에서 실행되도록 보장한다.

### 검증 결과

| 검증 | 결과 |
|------|------|
| `go test -count=1 -tags=e2e ./test/e2e -run TestManagerDeploymentRefreshCommandsRestartAndWaitForRollout -v` | PASS |
| `go test -count=1 -tags=e2e ./test/e2e -run TestDoesNotExist` | PASS |
| focused live e2e: `-ginkgo.label-filter=p2 -ginkgo.focus="elects ord-0 as initial primary"` | PASS: 1 Passed, 0 Failed, 9 Pending, 57 Skipped |

### Insight

오픈소스 operator에서 E2E는 “테스트 대상이 최신 코드인가”부터 보장해야 한다. 같은 tag 재사용은 로컬 kind E2E에서는 흔한 패턴이지만, Deployment는 image byte가 바뀌었는지 알지 못한다. 따라서 image load와 Deployment rollout은 별개의 계약으로 다뤄야 한다.

한 줄 결론: 반복 E2E 실행 시에도 최신 manager Pod가 떠야 하므로, apply 이후 rollout restart/status를 명시적으로 수행한다.

---

## 13. 2026-06-24 추가 RCA: 자동 failover RTO 30s 통과

### 문제

focused live e2e `promotes new primary within RTO 30s after primary kill`는 초기 수정 뒤에도 RTO가 45초대였다.

- 1차 재실측: 기능적으로는 `failover-shard-0-1`이 primary가 됐지만 RTO 45.8초로 실패.
- 2차 재실측: candidate readiness guard 뒤에도 RTO 45.2초로 실패.
- operator 로그에는 `FailoverPromotionFailed`, `exit code 137`, `container not found ("postgres")`가 반복됐다.
- instance 로그에는 실패한 operator exec 이후 standby가 재시작되고, `standby.signal`이 사라진 상태로 Real elector branch에 들어가 lease 경합을 다시 시작하는 흐름이 보였다.

### RCA

핵심 원인은 단순 timeout이 아니라 promotion 경로의 안전 순서 문제였다.

1. promotion 전에 failed old primary PVC를 먼저 fence하지 않으면 StatefulSet self-heal이 기존 ordinal을 되살릴 수 있다.
2. fenced PVC나 Kubernetes Pod/container NotReady 상태가 promotion candidate로 남으면 잘못된 exec가 발생한다.
3. 기존 operator exec 스크립트는 `pg_ctl promote` 전에 `standby.signal` 제거와 `.keiailab-promoted-primary` 생성을 먼저 수행했다. promote가 `exit 137`로 실패하면 다음 부팅에서 standby가 아닌 elector로 동작할 수 있어 RTO가 lease duration만큼 늘어났다.

### 적용한 수정

- `executeClusterPromotion()`에서 promotion exec 전에 failed old primary PVC를 pre-fence.
- `aggregateShardStatus()`에서 fenced member와 Kubernetes Pod/container NotReady member를 promotion-ready replica에서 제외.
- `promotionCandidateReadyForExec()`로 exec 직전 PodReady + `postgres` container Ready를 재확인.
- `postgresPromotionCommand()`를 instance-manager와 같은 SQL `pg_promote(true, 30)` 기반으로 변경.
- `standby.signal` 제거와 promoted marker 생성은 promotion 성공 이후에만 수행하도록 순서 변경.

### 검증 결과

| 검증 | 결과 |
|------|------|
| `go test -count=1 -timeout=8m ./internal/controller -run 'TestPostgresPromotionCommandMutatesPGDATAOnlyAfterSQLPromote|TestPostgresClusterPromotion|TestShouldSkipFencedCandidate' -v` | PASS |
| focused live e2e: `-ginkgo.label-filter=p2 -ginkgo.focus="promotes new primary within RTO 30s after primary kill"` | PASS |

focused live e2e 실측값:

- RTO: **14.148820221s**
- 결과: **1 Passed, 0 Failed, 9 Pending, 57 Skipped**

### Insight

자동 failover에서 중요한 것은 "빨리 promote한다"가 아니라 "실패한 promote가 데이터 디렉터리의 의미를 바꾸지 않는다"이다. `standby.signal`과 promoted marker는 다음 부팅의 역할 결정을 바꾸는 강한 신호이므로, promotion 성공 전 mutation은 split-brain과 RTO 지연을 동시에 만들 수 있다.

`pg_ctl promote` 대신 SQL `pg_promote(true, 30)`을 쓴 이유는 instance-manager의 production promotion 경로와 동일한 API를 사용해 동작 차이를 줄이기 위해서다. 운영에서는 이런 차이가 장애 시점에만 드러나므로, promotion API는 하나로 통일하는 편이 로그 해석, 재현성, 유지보수성 모두에 유리하다.

### 남은 과제

이번 결과는 focused RTO spec 통과다. 아직 full p2 suite, old-primary standby rejoin spec, node/PVC loss 계열 chaos, ADR-0027 shard-identity transition 검증은 별도 단계로 남아 있다.

한 줄 결론: 자동 failover RTO 30초 focused live gate는 SQL promotion + 성공 후 PGDATA mutation 순서로 통과했다.

---

## 14. 2026-06-24 추가 검증: full p2 live suite PASS

### 목적

focused failover spec만으로는 운영 품질 게이트를 통과했다고 보기 어렵다. 같은 manager image tag를 재사용하는 Kind E2E 환경에서 최신 controller Pod가 실제로 재기동되는지, 그리고 p2 레이블 전체가 서로 간섭 없이 통과하는지 확인했다.

### 추가 수정

- E2E `managerImage`를 `dist/install.yaml` / `config/manager/kustomization.yaml`의 controller image와 같은 `ghcr.io/keiailab/postgres-operator:0.4.0-beta.8`로 정렬.
- `TestManagerImageMatchesInstallManifests`를 추가해 E2E suite 상수와 install manifest image drift를 단위 수준에서 차단.
- `BeforeSuite`의 manager rollout restart/status 경로와 함께, 같은 tag 재사용 시에도 최신 manager Pod가 테스트 대상이 되도록 보장.

### 검증 결과

full p2 live suite:

```bash
KIND_CLUSTER=postgres-operator-e2e-full-p2-codex \
CERT_MANAGER_INSTALL_SKIP=true \
go test -tags=e2e ./test/e2e -timeout 45m -v -ginkgo.v -ginkgo.label-filter=p2
```

실측 결과:

- 결과: **31 Passed, 0 Failed, 9 Pending, 27 Skipped**
- 실행 시간: **1710.457s**
- failover RTO: **14.565566414s**
- manager image 정합성 테스트: **PASS**
- manager rollout command 테스트: **PASS**

이번 full p2에서 확인된 통과 범위:

- hibernation: scale down/up, PVC 보존, marker row 보존.
- Pooler: Deployment / Service / Secret / AutoTLS / metrics ServiceMonitor / delete cleanup.
- PostgresDatabase: `status.applied=true`, DB/schema/extension 생성, finalizer 기반 drop.
- PostgresUser: `status.applied=true`, role 생성, 초기 비밀번호 Secret, password rotation, old password reject, finalizer 기반 drop.
- failover: ord-0 initial primary, ord-1 standby, RTO 30초 이내 promotion, old primary standby rejoin.
- external replica drill: 외부 primary 기반 replica bootstrap.
- ImageCatalog: catalog 기반 operator rollout 반영.

### 아직 끝나지 않은 범위

full p2가 통과했지만 전체 개발이 끝났다는 의미는 아니다. 현재 남은 9개 Pending과 GA 축은 별도로 구현/전환해야 한다.

- PITR restore orchestration: BackupJob restore path, recovery target, bootstrap/검증 자동화.
- failover chaos PContext 전환: node/PVC loss 계열 live drill을 pending에서 자동 실행 대상으로 승격.
- ADR-0027 shard-identity transition: StatefulSet ordinal/PVC 정체성에 묶인 failover 한계를 줄이는 구조 개선.
- G3 sharding controller: CRD/PoC 수준을 넘어 reconcile loop와 상태 조건을 구현.
- G4~G5 reshard / DSQL: 현재는 GA 준비 단계가 아니라 장기 구현 축.
- 릴리스 품질: 7일 soak, chaos 반복, SBOM, cosign 서명, chart/bundle release gate.

### Insight

이번 단계의 의미는 "모든 개발 완료"가 아니라 "운영 품질 p2 live gate의 주요 실패를 닫았다"이다. 특히 failover는 탐지/선정/승격 함수만 맞다고 충분하지 않고, 최신 manager Pod가 실제로 실행되는지, StatefulSet self-heal과 promotion mutation 순서가 충돌하지 않는지까지 라이브로 검증해야 한다.

open-source operator 기준에서는 Pending을 그대로 둔 상태에서 GA를 선언하면 안 된다. Pending은 의도된 backlog일 수 있지만, 각 항목은 왜 Pending인지, 어떤 release gate에서 반드시 해제할지, 실패 시 데이터 손상 가능성이 있는지까지 추적돼야 한다.

한 줄 결론: p2 live suite는 31 Passed / 0 Failed로 통과했지만, PITR·chaos·shard identity·sharding/reshard 계열 구현은 아직 남아 있다.

---

## 15. 2026-06-24 추가 검증: failover chaos PENDING 해제

### 목적

full p2에서 자동 failover와 old-primary standby rejoin은 통과했지만, `test/e2e/failover_chaos_test.go`의 p1 chaos drill은 여전히 `PContext`로 남아 있었다. 이번 단계에서는 해당 Pending이 현재 제품 코드 기준으로 실제 통과 가능한지 확인했다.

### 적용한 변경

- `Failover chaos drill (D.1.2)`의 `Primary kill chaos → 자동 failover`를 `PContext`에서 `Context`로 전환.
- 오래된 주석을 제거하고, 현재 promotion 경로의 회귀 방지 목적을 명시.

이 변경은 제품 코드 추가가 아니라 검증 게이트 전환이다. 이전 단계에서 적용한 promotion pre-fence, candidate readiness guard, SQL `pg_promote`, 성공 후 PGDATA mutation 순서가 이 drill의 전제다.

### 검증 결과

targeted p1 live suite:

```bash
KIND_CLUSTER=postgres-operator-e2e-failover-chaos-codex \
CERT_MANAGER_INSTALL_SKIP=true \
go test -tags=e2e ./test/e2e -timeout 25m -v \
  -ginkgo.v -ginkgo.label-filter=p1 -ginkgo.focus="Failover chaos drill"
```

실측 결과:

- 결과: **6 Passed, 0 Failed, 4 Pending, 57 Skipped**
- failover chaos 실행 spec: **4 Passed**
- 새 primary promotion 관측 시간: **10.374s**
- 이전 primary standby rejoin 관측 시간: **16.657s**
- 남은 Pending: **PITR restore/checksum 4개**

### Insight

이번 결과로 "자동 failover가 라이브 chaos에서 여전히 미구현"이라는 과거 RCA는 더 이상 현재 코드 기준 사실이 아니다. 다만 이 말은 StatefulSet ordinal/PVC 기반 구조가 완전히 해결됐다는 뜻은 아니다. force-delete primary chaos는 통과했지만, node loss, PVC loss, shard identity transition은 다른 failure class이며 별도 drill과 설계 검증이 필요하다.

운영 기준에서는 Pending을 해제하는 순간 해당 테스트는 release gate가 된다. 따라서 failover chaos는 앞으로 flaky하면 다시 Pending 처리하는 것이 아니라, promotion 경로 또는 테스트 전제 조건 중 어느 쪽이 깨졌는지 RCA를 수행해야 한다.

한 줄 결론: failover chaos p1 Pending은 해제했고, 현재 라이브 검증 기준 남은 명시적 E2E Pending은 PITR restore/checksum이다.

---

## 16. 용어집

> 정의는 [GLOSSARY.ko.md](GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| Failover (장애 조치) | Primary 장애 감지 후 Replica 하나를 새 Primary로 자동 승격해 서비스를 잇는 동작. |
| Promotion (승격) | Replica를 Primary로 올리는 행위. 본 operator는 `pg_promote()`(SQL)로 수행. |
| RTO (Recovery Time Objective) | 장애에서 서비스 복구까지 허용되는 목표 시간. 본 프로젝트 failover 드릴 기준 30초. |
| Fencing (PVC Fencing) | 옛/이상 Primary가 데이터에 쓰지 못하도록 PVC 접근을 차단해 split-brain을 막는 격리. |
| Pod readiness | Pod/컨테이너가 트래픽을 받을 준비가 됐는지의 K8s 상태. 승격 후보 검증에 사용. |
| PITR (Point-In-Time Recovery) | WAL을 재생해 데이터베이스를 특정 과거 시점으로 복원하는 기법. |
| Replica Cluster | 외부 클러스터를 streaming standby로 복제하는 구성. |
| Hibernation | 클러스터를 STS scale-0으로 내려 PVC는 보존한 채 휴면시키는 기능. |
| kind | Docker 컨테이너 안에 K8s 클러스터를 띄우는 도구. e2e 테스트에 사용. |
| RCA (Root Cause Analysis) | 장애·실패의 근본 원인 분석. |
