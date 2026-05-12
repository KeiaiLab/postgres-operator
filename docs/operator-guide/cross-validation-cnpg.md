# Cross-Validation Report: keiailab/postgres-operator vs CloudNativePG

> 일자: 2026-05-03
> 환경: kind v0.31.0 / k8s v1.35.0 / single-node arm64 / Docker 29.4.1
> 동일 노드에 두 operator + 각 1 instance 동시 배포

> **본 보고서는 marketing 자료가 아니며 alpha 단계의 객관적 차이만 기록한다.**
> 우리 operator 가 모든 차원에서 우월하다는 주장은 사실이 아니며,
> 본 측정 자체가 우리 측에서 **3 개 production bug** 를 드러냈다.

## TL;DR

| 차원 | CNPG 1.27.0 | keiailab 0.3.0-alpha | 차이 |
|---|---|---|---|
| **Time-to-Ready** (CR apply → Pod Ready) | **24s** | **62s** | +158% (우리 더 느림) |
| Pod RSS sum (single instance) | 188 MB | **144 MB** | −23% |
| Operator manager RSS | 67 MB | **50 MB** | −25% |
| Pod 안 PID 1 RSS | 60 MB (manager) | **28 MB** (instance) | −53% |
| Manager image (host) | 169 MB | **106 MB** | −37% |
| Manager image (kind 압축) | 36.8 MB | **30.3 MB** | −18% |
| PG runtime image (host) | 1.04 GB | **675 MB** | −35% |
| PG runtime image (kind 압축) | 264 MB | **163 MB** | −38% |
| Go LoC (실코드, test 제외) | 94,130 | **5,220** | −94% |
| 패키지 수 | 171 | **20** | −88% |
| go.mod direct deps | 45 | **8** | −82% |
| CRD YAML lines | 18,955 | **1,778** | −91% |
| 최소 CR YAML 줄 수 | **8** | 13 | +63% (우리 verbose) |

**해석**:

- **자원 footprint 는 우리가 작다** — alpha 단계의 자연스러운 결과 (기능 적음).
- **Time-to-Ready 는 우리가 약 2.6× 느림** — readinessProbe initialDelaySeconds=30 + waitSupReady 폴링이 보수적. 단축 가능 (cycle 7 후속).
- **CR 사용자 인지부하**: CNPG 8 lines vs ours 13 lines. CNPG 의 sharding/router 필드 부재로 minimum CR 이 더 짧음. Ours 는 shardingMode=none 명시 등 explicit 함.
- **코드/CRD 크기 차이는 GA 거리의 척도** — 우리가 작은 만큼 backup, monitoring, replication 자동화 등이 *부재*.

## 측정 방법

### 환경

```
kind create cluster --name pg-bench
# 4 image kind 노드에 로드:
# - ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0
# - ghcr.io/cloudnative-pg/postgresql:18-bookworm
# - local/postgres-operator:bench  (cmd/main.go bench build)
# - local/pg:18 → ghcr.io/keiailab/pg:18 retag
```

### CNPG 시나리오

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: {name: cnpg-bench}
spec:
  instances: 1
  imageName: ghcr.io/cloudnative-pg/postgresql:18-bookworm
  storage: {size: 1Gi}
```

`kubectl apply -f cnpg-cr.yaml` 시점부터 Pod conditions[Ready]=True 까지의 wall clock.

### Ours 시나리오

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata: {name: ours-bench, namespace: default}
spec:
  postgresVersion: "18"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 0
    storage: {size: 1Gi}
```

동일 시점/동일 노드에서 측정.

### Memory 측정

각 Pod 의 `/proc/[pid]/status` 의 `VmRSS` 합산:

```sh
kubectl exec <pod> -c postgres -- bash -c \
  'awk "/^VmRSS:/ {s+=\$2} END {print s\" KB\"}" /proc/[0-9]*/status'
```

(distroless container 안에는 ps 가 없어서 /proc 직접 파싱.)

### Operator manager memory

kind 노드 host 측 `crictl inspect` → PID 추출 → `/proc/<pid>/status` 의 VmRSS.

## 측정 중 발견된 우리 측 production bug 3 종

cross-validation 의 진짜 가치는 자원 비교 자체보다 **alpha-deployable 이 vaporware 였음** 을 드러낸 점. 본 세션 이전의 모든 unit test / envtest / `make validate` 는 통과 상태였으나, 실 K8s 에서는 다음 3 개가 동시에 차단:

### Bug 1 — RBAC privilege escalation 차단

K8s 는 operator 가 *자기 자신이 안 가진 권한* 을 다른 SA 에 grant 하는 것을 거부한다 (privilege escalation prevention). 우리 operator ClusterRole 에 `persistentvolumeclaims/patch` 가 없는데 `buildInstanceRole` 에서 그 권한을 grant 하려 함:

```
roles.rbac.authorization.k8s.io "ours-bench-instance" is forbidden:
  user "system:serviceaccount:postgres-operator-system:..." is attempting
  to grant RBAC permissions not currently held:
  {APIGroups:[""], Resources:["persistentvolumeclaims"], Verbs:["get" "list" "watch" "patch"]}
```

**Fix**: `+kubebuilder:rbac` marker + helm chart RBAC 에 `persistentvolumeclaims [get,list,watch,patch]` 추가.

CNPG 는 같은 fencing 패턴을 별도 ClusterRole 로 grant 하지 않음 — webhook + finalizer 기반 split-brain 방지를 사용. 우리는 RFC 0003 의 PVC label 기반 fence 라 이 권한이 필요.

### Bug 2 — plugin auto-register 가 vanilla PG 부팅 차단

`cmd/main.go` 에서 6 종 extension (pgaudit, pgvector, pgcron, pgnodemx, postgis, setuser) 을 무조건 plugin Registry 에 등록 → `renderPostgresConf` 가 모든 cluster 의 `postgresql.conf` 에 `shared_preload_libraries=pgaudit,pgvector,...` 를 강제. 그러나 공식 `postgres:18-bookworm` image 에는 해당 .so 부재:

```
2026-05-03 12:21:28.728 GMT [18] FATAL: could not access file "pgaudit": No such file or directory
2026-05-03 12:21:28.728 GMT [18] LOG:  database system is shut down
```

**Fix**: 모든 `Register` 호출 dormant (`_ = X.Register`). per-cluster `spec.extensions` 기반 재배선은 별도 RFC. 현재는 vanilla PG 부팅만 보장.

CNPG 는 image 에 다양한 extension 을 *사전 빌드* + `spec.postgresql.shared_preload_libraries` 로 사용자 명시 opt-in 패턴.

### Bug 3 — Promote race + already-primary 처리 부재

두 sub-bug:

3a) `OnStartedLeading` 에서 election 이 leader 권한을 즉시 부여하면 instance manager 가 postgres 의 unix socket 이 listen 시작하기 전에 `sup.Promote()` 호출 →

```
dial unix /var/run/postgresql/.s.PGSQL.5432: connect: no such file or directory
```

3b) fresh initdb 직후의 DB 는 이미 primary (`recovery.signal` 부재). `pg_promote(true, 30)` 가 false 반환 → 우리는 error 처리 → fencingErrCh → main exit 2 → CrashLoopBackOff.

**Fix**:
- `waitSupReady` helper — `Promote` 호출 전 `IsReady` polling 30s.
- `Promote` 가 먼저 `pg_is_in_recovery()` 검사. !inRecovery 면 즉시 nil (멱등).

CNPG 는 manager 가 직접 PG bootstrap (initdb + pg_basebackup + standby/primary 결정) 까지 모두 책임 → 같은 race 가 구조적으로 부재.

## Feature matrix (alpha 단계 한계 명시)

> 2026-05-12: Pooler/Monitoring 행은 CNPG 1.29 공식 문서의 Pooler CRD,
> PgBouncer parameter map, Service template, exporter 표면을 추가 기준으로 재확인했다.

| 기능 | CNPG 1.29 | Ours 0.3.0-alpha | 비고 |
|---|---|---|---|
| Single-shard PG (primary only) | ✅ | ✅ | both deployable |
| Image Catalog | ✅ `ImageCatalog` + `ClusterImageCatalog` | ⚠️ CRD + rollout path | `ImageCatalog`/`ClusterImageCatalog` CRD, CNPG-compatible `spec.imageCatalogRef.{apiGroup,kind,name,major}`, namespaced/cluster-scoped lookup, catalog image → shard StatefulSet init/main container image 반영, image hash annotation 기반 rollout drift 추적, catalog 변경 watch/envtest 검증 완료. extension image volume mount 와 official digest catalog 공급, live rollout 실측 잔여 |
| Streaming replication (replicas≥1) | ✅ | ⚠️ partial | first-boot standby `pg_basebackup`, `primary_conninfo application_name=<pod>` wiring, status 기반 replica 관찰 구현. live long-run/partition 검증 잔여 |
| Synchronous replication | ✅ `.spec.postgresql.synchronous` | ⚠️ partial | `spec.postgresql.synchronous.{method,number,dataDurability}` CRD + CEL `number<=shards.replicas`, PostgreSQL `ANY/FIRST N (...)` 렌더링, `required` 는 desired/observed Pod 이름 전체, `preferred` 는 Ready replica 만 사용하고 quorum 을 낮춤. ConfigMap hash → StatefulSet rolling reconcile 적용. live commit/RPO=0 drill 잔여 |
| Declarative hibernation | ✅ `cnpg.io/hibernation=on/off` | ⚠️ envtest + smoke path | CNPG-compatible annotation 지원. shard StatefulSet/PVC template 소유권 유지 + database Pod `replicas=0`, native router `replicas=0`, `status.phase=Hibernated`, condition `cnpg.io/hibernation=True`, `off`/annotation 제거 시 desired replicas 복구. `SMOKE_HIBERNATION=1` 이 PVC marker row 생성 → hibernate → PVC 보존 확인 → rehydrate → marker `SELECT` 를 검증하도록 추가됐다. live kind 실측 잔여 |
| Automated failover | ✅ (Patroni-less, native) | ⚠️ partial | instance manager election/promote smoke PASS 이력 + controller 순수 failover decision/helper + Ready 이후 primary failure 를 `status.phase=Degraded`, `FailoverReady=False` 로 노출. controller-layer promotion 은 replica Pod `postgres` container 에 `standby.signal` 제거 → `pg_ctl promote` → `pg_is_in_recovery()` polling → `instance-status` primary annotation patch 경로까지 구현. old-primary standby marker 일반화 + current `PRIMARY_ENDPOINT` main-container 주입 + `pg_rewind --target-pgdata ... --source-server ...` command-runner + rewind failure 시 fresh `pg_basebackup` fallback 구현. rejoin 준비 실패는 `status.shards[].replicas[].reason/message` 로 표면화한다. network partition/STONITH + live pg_rewind 검증 잔여 |
| pg_basebackup → standby join / pg_rewind rejoin | ✅ | ⚠️ partial | first-boot standby `pg_basebackup` + existing PGDATA old-primary marker + `pg_rewind` 기반 former-primary rejoin 경로 구현. 신규 cluster 는 `wal_log_hints=on` 으로 rewind 가능성 확보. `pg_rewind --source-server` normal connection auth 를 위해 alpha HBA 에 postgres normal connection trust line 을 scram host rule 앞에 배치했다. rewind 실패 시 dataDir 교체 + fresh `pg_basebackup` fallback 이 원본 dataDir restore-on-failure 로 동작한다. live e2e 재검증 잔여 |
| Backup (Barman / pgBackRest) | ✅ Barman built-in + scheduled/on-demand | ⚠️ CRD/controller partial | `BackupJob` phase 전이 + `ScheduledBackup` cron→BackupJob 생성 + `RestorePIT` 호출 경로 + `executionMode=job` runner Job 관찰 + pgBackRest command-runner/sidecar exec 경로. Barman/restore drill 잔여 |
| PITR | ✅ | ⚠️ controller/plugin path only | pgBackRest `restore --type=time --target=...` 호출 경로와 sidecar exec 경로는 있으나 실제 restore + checksum drill 미완료 |
| Connection pooler (PgBouncer) | ✅ Pooler CRD + PgBouncer Deployment | ⚠️ Pooler CRD/controller partial | `instances`, `type=rw/ro`, `pgbouncer.poolMode`, `parameters`, `pg_hba`, Pod template, Service template, `deploymentStrategy`, `serviceAccountName`, ConfigMap/Deployment/Service 생성, auth Secret fail-closed 검증, PgBouncer HBA file 렌더링 + `auth_type=hba`/`auth_hba_file` operator-owned 검증, 사용자 제공 server/client TLS Secret 렌더링 + 필수 키 fail-closed 검증, PgBouncer TCP readiness/liveness/startup probe, CNPG-compatible parameter allowlist + operator-owned key fail-closed 검증, read-only root filesystem 호환 `unix_socket_dir = ` 기본값, `instances>1` topology spread/PDB 자동 생성, `maxUnavailable=0` rolling update 기본값, `type=ro` ready replica host list + `server_round_robin=1` + `server_login_retry=2`, status `instances/readyReplicas/backendTargets/configHash`, explicit PgBouncer exporter sidecar + metrics ServicePort + `/metrics` probe + PodMonitor selector label/sample 계약, CNPG `cnpg_pgbouncer_*` metric prefix 기반 PrometheusRule alert 렌더 검증, `spec.paused` PAUSE/RESUME reconcile + `status.paused`, `pgbouncer.parameters` 패치 시 ConfigMap `config.sha256` projection 대기 + ready Pod `SIGHUP` in-place reload + Pod hash annotation audit + Pooler Service 재접속 실측 완료. built-in auth user/TLS 자동 생성, live Prometheus scrape/Grafana 검증 잔여 |
| Monitoring (ServiceMonitor/Rules) | ✅ | ⚠️ partial chart support | Helm Metrics Service/ServiceMonitor/PrometheusRule + BackupJob phase metric + Pooler phase metric/alert + PostgresCluster status 기반 replication lag bytes metric/alert + Pooler PodMonitor sample + PgBouncer exporter collection/client waiting/maxwait alert 렌더 검증 + Grafana dashboard ConfigMap 렌더 검증 존재. live Prometheus scrape / Grafana import 검증 잔여 |
| Declarative database management | ✅ Database CRD | ⚠️ CRD/controller partial | `PostgresDatabase` CRD + `spec.cluster/name/owner/ensure/tablespace/extensions/schemas/fdws/servers/privileges` + ready primary Pod `psql` reconcile + status `applied/observedGeneration/conditions` + `databaseReclaimPolicy=delete` finalizer 구현. CNPG 범위를 넘는 database/schema privilege grant/revoke 검증 포함. live smoke, retain 정책 실측 잔여 |
| Declarative role management | ✅ `.spec.managed.roles` + `status.managedRolesStatus` | ⚠️ CRD/controller partial | `PostgresUser` CRD + `spec.cluster/name/ensure/login/superuser/createdb/createrole/replication/bypassrls/inherit/connectionLimit/inRoles/passwordSecretRef/disablePassword/validUntil` + ready primary Pod `psql` reconcile + status `applied/observedGeneration/conditions/passwordSecretResourceVersion` 구현. spec 밖 membership REVOKE, password Secret `username` match, `passwordSecretRef`+`disablePassword` fail-closed, referenced Secret update watch, `PostgresCluster.status.managedRolesStatus.byStatus/cannotReconcile/passwordStatus` 집계 검증 완료. live smoke, actual password rotation SQL round-trip 잔여 |
| Replica clusters / externalClusters | ✅ standalone + distributed topology | ⚠️ streaming standalone path | `externalClusters[].connectionParameters`, `password`, `sslKey`, `sslCert`, `sslRootCert`, `bootstrap.pg_basebackup.source`, `replica.enabled/source` 표면 추가. standalone replica 는 ordinal 0 도 external source 에서 `pg_basebackup` 후 `standby.signal`/`primary_conninfo` 를 기록하고, instance manager 는 영구 follower election 으로 local promotion 을 차단한다. password Secret 은 `passfile`, TLS Secret 은 `sslkey/sslcert/sslrootcert` 로 연결한다. source mismatch/missing host/incomplete SecretKeySelector 는 `ReplicaClusterRejected` fail-closed. WAL archive/object-store hybrid, distributed topology demotion/promotion token, live cross-cluster drill 잔여 |
| Multi-region | ✅ replica clusters | ⚠️ partial via replica cluster API | streaming standalone replica cluster 경로가 multi-region read-only/DR 기반을 제공하지만, distributed topology controlled switchover 와 symmetric backup 은 미완료 |
| Multi-shard sharding | ❌ | ⚠️ schema + plugin SDK | RFC 0005 진행 |
| In-place major upgrade | ✅ | ❌ | 미정 |
| Webhook validation | ✅ | ✅ CEL XValidation | both production-grade |
| Native fencing (PVC label) | ❌ (CR finalizer) | ✅ RFC 0003 | both 다른 모델 |
| OLM bundle (operatorhub.io) | ✅ community-operators 채널 등록 (Artifact Hub OLM) | ⚠️ bundle 0.3.0-alpha.18 준비 완료, PR 대기 | `bundle/manifests/` 에 8 owned CRD + alm-examples + CSV description 정합 (T26), `operator-sdk bundle validate --select-optional suite=operatorframework` 통과. T28 에서 k8s-operatorhub/community-operators PR 진행 예정. |
| Helm chart (Artifact Hub helm) | ✅ artifacthub.io/packages/helm/cloudnative-pg | ⚠️ artifacthub.io/packages/helm/keiailab-postgres-operator 등록, alternativeName 정합 (Chart annotation) | T26 에서 ArtifactHub URL/alternativeName 정합 완료. live Artifact Hub `Verified Publisher` 처리 잔여 (repositoryID 부합성). |
| Local supply chain gates | ✅ pre-commit/test/lint | ✅ ADR-0009 4 계층 게이트 (L1 pre-commit, L2 pre-push test/audit/govulncheck, L3 Makefile validate/audit/gate, L4 PR 본문 evidence) | both GitHub Actions 미사용 정책 (RFC-0002 §2). |
| Security: vulnerability scan | ✅ trivy + govulncheck (CI) | ✅ make audit (govulncheck + trivy fs HIGH/CRITICAL + gosec) | T26: `moby/spdystream` v0.5.0→v0.5.1 (CVE-2026-35469) fix 후 trivy fs 0 vulnerabilities. |
| DCO sign-off enforcement | ✅ GitHub bot | ✅ lefthook commit-msg + DCO_STRICT=1 | T26: `make hooks-install` wrapper + CONTRIBUTING 정합. force-push 없이 retroactive 정정 불가 — 다음 cycle 부터 모든 commit DCO compliant. |

## 적합 use case

**CNPG 권장**:
- 즉시 production 배포 필요
- backup/PITR/HA 가 1 일 안에 작동해야 함
- 자체 운영자가 없거나 operator-internals 책임 회피
- multi-shard 미사용 (single PG primary + replicas)

**Ours 적합 (현 alpha)**:
- single-shard 만 사용 + HA 는 미요구 (dev/staging)
- footprint 절감 (50 MB operator + 144 MB PG Pod)
- *codebase 를 직접 읽고 변경* 하려는 small team — CNPG 의 94k LoC vs Ours 5k LoC.
- 자체 분산 SQL (multi-shard) 까지 가는 장기 로드맵 — RFC 0005 native sharding plugin 이 P2+ 에서 의미.

## Reproducibility

본 측정은 `hack/smoke.sh` 의 패턴을 두 operator 에 동시 적용. 재실행:

```fish
# 1. cleanup
kind delete cluster --name pg-bench

# 2. setup
kind create cluster --name pg-bench
docker pull --platform linux/arm64 ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0
docker pull --platform linux/arm64 ghcr.io/cloudnative-pg/postgresql:18-bookworm
docker buildx build --provenance=false --sbom=false --load -t local/postgres-operator:bench .
docker buildx build --provenance=false --sbom=false --load \
    -f Dockerfile.pg --build-arg PG_MAJOR=18 -t local/pg:18 .
docker tag local/pg:18 ghcr.io/keiailab/pg:18

# 3. load images (kind 의 ctr import 가 multi-arch manifest list 거부 — docker save 우회)
for img in local/postgres-operator:bench local/pg:18 \
           ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0 \
           ghcr.io/cloudnative-pg/postgresql:18-bookworm \
           ghcr.io/keiailab/pg:18; do
    docker save "$img" -o /tmp/img.tar
    docker exec -i pg-bench-control-plane ctr -n k8s.io images import /dev/stdin < /tmp/img.tar
end

# 4. CNPG
kubectl apply -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-1.27.0.yaml

# 5. ours
make build-installer IMG=local/postgres-operator:bench
sed -i.bak 's/imagePullPolicy: Always/imagePullPolicy: IfNotPresent/g' dist/install.yaml
kubectl apply --server-side -f dist/install.yaml

# 6. CR apply + measure (반복)
```

본 보고서의 측정 시점은 docker BuildKit `--provenance=false --sbom=false` 가 단일 arch single-platform 이미지를 생성할 때만 kind import 가 성공함 — multi-arch manifest list 는 `--all-platforms` 가 강제되어 다른 arch 의 digest 부재로 fail. 본 사실은 우리 측 `Dockerfile.pg` 자체가 multi-arch 를 *의도하지 않음* 을 전제 (kind/dev 환경 한정).

## 결론

- **alpha-deployable 의 정의가 정직했는지 본 cross-validation 이 검증** — 답은 *부분적으로 No*. 3 bug 가 fix-forward 후에야 배포 가능.
- **자원 footprint 차이 (≈25% 작음) 는 GA 거리의 표상** — 기능이 적기에 가벼울 뿐.
- **Time-to-Ready 차이 (2.6×) 는 fixable 한 보수적 probe 설정** — cycle 7 후속.
- **production 도입 결정은 feature matrix 의 ❌/⚠️ 행을 사용자가 인지** 후만 권장.

*본 보고서의 모든 수치는 단일 측정 — 통계적 신뢰구간 부재. 정식 SLA 측정은 GA 단계에서.*

---

## 2026-05-04 추가 측정 — RFC 0006 R3 회귀 + smoke 재검증

> 환경: 동일 (kind v0.31 / Docker 29.4.1 / arm64), 본 cycle 의 commit chain (R1+R2+R3 task-a/b/c) 머지 후.

### A. smoke 회귀 (`hack/smoke.sh`) — F02 90→100% 게이트

| 지표 | 측정값 | 게이트 (RFC 0006 §7 alpha) |
|---|---|---|
| operator manager Available | ~12s | < 60s ✅ |
| **CR apply → cluster Ready** | **18s** (22:32:52 → 22:33:10) | **< 60s ✅** |
| psql round-trip (`SELECT 1`) | PASS | PASS ✅ |
| status.shards[0].primary.ready | true | true ✅ |
| status.conditions[Ready] | True ("all subsystems ready") | True ✅ |

이전 cross-validation 의 62s (single-shard Time-to-Ready) 대비 *3.4배 개선* — readinessProbe 30→5s 단축 (`78c93db`) + R1/R2 wiring 정착의 누적 효과.

### B. RFC 0006 R3 회귀 (`make test-e2e-failover`) — beta 게이트

| It | spec | 결과 | 측정값 |
|---|---|---|---|
| #1 | elects ord-0 as initial primary | ✅ PASS | — |
| #2 | spawns ord-1 as standby with role=replica annotation | ✅ PASS | — |
| **#3** | **promotes new primary within RTO 30s after primary kill** | **✅ PASS** | **RTO = 7.45s** |
| #4 | old primary rejoins as standby after pod restart | ⚠️ code path implemented | generic existing-PGDATA marker implemented; live e2e 재검증 잔여 |

**핵심**: RFC 0006 §7 beta 기준 (`replicas=2 cluster 의 primary kill → new primary 까지 RTO < 30s`) **4배 여유로 통과** (7.45s vs 30s 목표). beta phase 의 측정 게이트 충족.

**It #4 의 현재 상태**:
- 옛 primary 가 *kill* 됐을 때 `OnStoppedLeading` callback 이 호출되지 않는 문제를 보완하기 위해 bootstrap container 가 existing PGDATA + HA + current `PRIMARY_ENDPOINT` 가 자기 자신이 아닌 경우 `.keiailab-restart-primary-as-standby` marker 를 생성한다.
- instance manager 는 marker 발견 시 main container 의 current `PRIMARY_ENDPOINT` 를 사용해 `pg_rewind --target-pgdata <PGDATA> --source-server "host=<current-primary> ..."` 를 실행한 뒤 `standby.signal` 과 `primary_conninfo` 를 구성하고 election 진입을 지연한다.
- `pg_rewind` 실패 시 fresh `pg_basebackup` fallback 을 시도한다. fallback 이 실패하면 원본 dataDir 을 복구하고 marker 를 남겨 다음 restart 에서 재시도하며, `standby.signal` 은 만들지 않는다.
- rejoin 준비 실패는 종료 전 자기 Pod `postgres.keiailab.io/instance-status` annotation 에 `reason=RejoinPreparationFailed` 와 상세 message 를 남기고, controller aggregate 가 이를 `PostgresCluster.status.shards[].replicas[].reason/message` 로 전달한다.
- 남은 범위: Kind/chaos 기반 live e2e 재검증, 임의 network partition/STONITH, 실제 divergent WAL rewind drill.

### C. 본 측정에서 추가로 발견된 *test-infra* 회귀 5건 (모두 fix-forward)

본 cycle 이 *RFC 0006 R3 commit chain 의 첫 실 kind 실행* — task-c 가 compile-only 검증만 했기에 다음 환경 정합성 회귀가 한 번에 드러남:

| # | 위치 | 증상 | 수정 |
|---|---|---|---|
| 1 | `hack/smoke.sh:72` | operator namespace 표기 불일치 → `kubectl wait` NotFound | `postgres-operator-system` |
| 2 | `hack/smoke.sh:36` | OPERATOR_IMG `:smoke` ↔ install.yaml `:0.3.0-alpha` drift → ImagePullBackOff | OPERATOR_TAG 를 `Chart.yaml` `appVersion` 에서 도출 |
| 3 | `hack/smoke.sh:32` | NS env override 가 sample CR 의 hardcoded `metadata.namespace=default` 와 어긋남 | NS hardcode `default` |
| 4 | `test/e2e/e2e_suite_test.go:36,~64` | managerImage `example.com/...` ↔ install.yaml `:0.3.0-alpha` drift + operator install step 자체 누락 | managerImage 정렬 + `make build-installer + kubectl apply -f dist/install.yaml + wait Available` 추가 |
| 5 | `test/e2e/{failover,postgrescluster}_e2e_test.go` | label selector `postgres.keiailab.io/cluster=` 가 controller 의 실제 라벨 (`app.kubernetes.io/instance=`) 과 불일치 → Pod selector 영원히 zero match → 5분 timeout | 6 occurrence 일괄 수정 |

**클래스 분석**: 5 건 모두 *unit + envtest 가 catch 못 하는 환경 정합성*. RFC 0006 §1 의 "검증되지 않은 기능이 vaporware" 원칙이 *테스트 코드 자체에* 적용된 사례 — 테스트도 실행되지 않으면 vaporware.

### D. Phase 게이트 갱신 (RFC 0006 §4)

| Phase | 코드 게이트 | 측정 게이트 | 상태 |
|---|---|---|---|
| **alpha** (R1+R2) | ✅ implemented | ✅ smoke Pod Ready 18s < 60s | **통과** |
| **beta** (R3) | ✅ implemented (R3 task-a/b/c) | ✅ RTO 7.45s < 30s (It #3) / ⚠️ It #4 후속 fix 필요 | **부분 통과** — R3 rejoin gap 후속 |
| GA-single (R4) | ❌ pending | — | 미진입 |
| GA-distributed (R5) | schema only | — | 미진입 |

### E. 재현 절차

```fish
# 1. smoke (F02 단일 cluster 검증)
./hack/smoke.sh

# 2. R3 회귀 (replicas=1 + primary kill)
make test-e2e-failover

# 3. cleanup
kind delete cluster --name postgres-operator-test-e2e
kind delete cluster --name postgres-operator-smoke
```

---

*본 측정은 단일 환경 (M1 arm64 / Docker 29.4.1). 다른 arch / kernel / runtime 에서의 차이는 별도 측정 필요.*
