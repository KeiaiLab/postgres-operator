<p align="center">
  <a href="CHANGELOG.md">English</a> |
  <b>한국어</b> |
  <a href="CHANGELOG.ja.md">日本語</a> |
  <a href="CHANGELOG.zh.md">中文</a>
</p>

# Changelog (한국어)

> 영문 원본: [CHANGELOG.md](CHANGELOG.md) — canonical / 정본

본 프로젝트는 SemVer 를 따른다.

## [Unreleased]

### Added (추가됨)

- *(olm,docs)* `docs/operator-guide/community-operators-onboarding.md` —
  k8s-operatorhub/community-operators 채널 온보딩 절차 (사전 요구 체크리스트,
  bundle 이미지 build / push, `gh pr create`, upgrade-graph 운영,
  Artifact Hub OLM 검증). 첫 T28 산출물.
- *(ci)* `make hooks-install` / `make hooks-check` 타깃 — lefthook DCO /
  Conventional Commits 게이트를 활성화하는 wrapper. CONTRIBUTING 도 정합.
- *(ci)* `make validate` 에 4 개 추가 게이트 — bundle CRD 수 ≥ 8,
  `operator-sdk bundle validate` 기본 + `suite=operatorframework`,
  Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag 드리프트 검사,
  그리고 `.github/workflows/` 부재 (ADR-0009 강제).
- *(olm)* CSV 의 `customresourcedefinitions.owned[]` 에 ImageCatalog /
  ClusterImageCatalog / PostgresDatabase / PostgresUser 의 `description` +
  `displayName` 추가 — operatorframework suite 의 4 개 경고 제거.
- *(oss)* 신규 `SUPPORT.md` (GitHub Discussions / Issues / PR 경로,
  보안 라우팅, 응답 시간 기대치). 0.3.0-alpha.17 / 0.3.0-alpha.18 의
  CHANGELOG 항목도 정합.
- *(docs)* README — 0.3.0-alpha.18 의 8-CRD 표면 표 + 6-step quickstart.
- *(docs)* TASKS — T26 (cross-cut OSS/OLM 정합, 완료), T27 (신규 CRD 의
  live smoke 자동화, ①–④ 모두 완료), T28 (community-operators 등록 절차)
  등록.
- *(smoke)* `hack/smoke.sh` 에 4 개의 신규 CRD 시나리오 추가:
  `SMOKE_DATABASE=1` (PostgresDatabase → `status.applied` +
  `pg_database` + reclaim=delete DROP), `SMOKE_USER=1` (PostgresUser →
  `status.applied` + `pg_roles` + DROP ROLE), `SMOKE_SCHEDULEDBACKUP=1`
  (cron `immediate` → BackupJob 생성), `SMOKE_IMAGECATALOG=1`
  (ImageCatalog / ClusterImageCatalog schema + lookup). 스텝 번호는
  N/15 로 정렬.
- *(api)* owned CRD 8 개 모두에 `kubebuilder:resource:categories` 마커 추가
  — `kubectl get postgres` / `database` / `backup` / `pooler` / `image` /
  `role` / `all` 별칭과 OperatorHub UI 그룹화.
- *(ci)* `make lint-k8s` 타깃을 `make validate` 에 통합 — `dist/install.yaml`
  과 helm-template 출력에 대한 kube-linter 정적 분석 (liveness-port /
  readiness-port / non-root / readOnlyRoot / resource-limit, 30+ checks).
- *(ci,olm)* `config/scorecard/` + `bundle/tests/scorecard/config.yaml` +
  `make scorecard` 타깃 — 6 개의 operator-sdk scorecard 테스트
  (basic-check-spec, olm-bundle-validation, olm-crds-have-validation,
  olm-crds-have-resources, olm-spec-descriptors, olm-status-descriptors)
  자동화. 실 실행은 kind 클러스터 필요 (`make smoke` 후 `make scorecard`).
- *(controller)* **Pooler built-in auth** (T27 ⑤) —
  `spec.pgbouncer.authSecretRef` 가 비어 있으면 operator 가 PostgresCluster
  ready primary Pod 에 `keiailab_pooler_pgbouncer` LOGIN 역할 (24-byte
  crypto/rand base64 비밀번호) 과 userlist.txt Secret
  `<pooler-name>-builtin-auth` (Pooler 소유) 를 생성한다. 생태계 호환성을
  위해 PgBouncer userlist 형식 `cnpg_pooler_pgbouncer` 는 유지. primary 를
  기다리는 동안에는 Phase=Pending + Reason=BuiltinAuthWaitingForPrimary
  로 idempotent ensure. `PoolerReconciler.PodExecutor` 배선 회귀도 함께
  수정 (PAUSE/RESUME + SIGHUP-reload 경로 복원).
- *(controller)* **Pooler AutoTLS — cert-manager 연동** (T29) —
  `spec.pgbouncer.autoTLS` 가 설정되면 operator 가 cert-manager `Certificate`
  CR (역할별: server / client) 을 `unstructured` 로 emit 하고, cert-manager
  가 Secret (`tls.crt` + `tls.key` + `ca.crt`) 을 발급한다. PgBouncer
  Deployment 가 자동 생성된 Secret 을 투명하게 마운트. 명시적인
  `Server/ClientTLSSecret` 은 여전히 우선. 신규 헬퍼
  (`poolerEffectiveServerTLSSecretName`,
  `poolerEffectiveClientTLSSecretName`, `poolerAutoTLS{Server,Client}Active`),
  `cert-manager.io/certificates` 에 대한 신규 RBAC 마커, 2 개의 신규 unit
  test (`TestPoolerAutoTLS_CreatesCertificate`,
  `TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence`), 신규 샘플
  `config/samples/postgres_v1alpha1_pooler_autotls.yaml`.
- *(controller)* **Pooler built-in auth 비밀번호 회전** (T27 ⑥) —
  `postgres.keiailab.io/rotate-pooler-password=true` 어노테이션이 설정되면
  새 비밀번호로 `ALTER ROLE`, in-place userlist.txt Secret 갱신, 어노테이션
  제거, `Pooler.Status.BuiltinAuthLastRotation` 기록을 트리거. 신규 unit
  test `TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation`.
- *(docs)* ROADMAP "current state" 스냅샷 — alpha.18 + OLM bundle +
  declarative DB 표면 + 로컬 4-layer 게이트에 대한 4 개 행 추가.
- *(docs)* Cross-validation 매트릭스 — 5 개 차원 추가: OLM bundle /
  Helm chart / 로컬 supply-chain 게이트 / 보안 취약점 스캔 / DCO
  사인오프 강제.

### Added (추가됨)

- *(api,controller)* T29 stage 4 — `Pooler.spec.pgbouncer.autoTLS.selfSigned`
  필드. true 로 설정 (그리고 `issuerRef` 가 비어 있을 때) 시 operator 가
  in-process RSA-2048 자가-서명 CA + leaf 인증서 (1 년 유효, 30 일 갱신
  skew) 를 생성하고 `<pooler>-client-tls` / `<pooler>-server-tls` Secret
  (`tls.crt`/`tls.key`/`ca.crt` 키) 에 저장 — cert-manager Certificate
  발급 Secret 과 동일한 레이아웃. cert-manager 가 없는 환경의 갭을 메움.
  CEL XValidation 규칙으로 admission 시점에 "{issuerRef, selfSigned} 중
  정확히 하나" 강제. 회귀 테스트
  `TestPoolerAutoTLS_SelfSignedCreatesSecretAndMirrorsNotAfter` + 샘플 CR.
- *(api,controller)* T29 stage 5 — `Pooler.Status.AutoTLSClientCertNotAfter`
  + `AutoTLSServerCertNotAfter` 가 cert-manager `Certificate.status.notAfter`
  를 미러링하여 운영자가 fleet 전반의 만료 인증서를 나열할 수 있다
  (`kubectl get poolers -A -o wide` 가 두 신규 컬럼을 `priority=1` 에
  노출). reconciler 는 `unstructured` 로 cert-manager Certificate CR 을
  읽음 (SDK 의존 없음); 조회 중 에러는 V(1) 로 로깅되고 "unknown" 처리되어
  일시적 cert-manager 중단이 Pooler reconcile 의 나머지를 막지 않는다.
  회귀 테스트 `TestPoolerAutoTLS_MirrorsNotAfterToStatus`.
- *(api,controller)* `PostgresUser.spec.userReclaimPolicy` (`retain` 기본,
  `delete`) 가 `PostgresDatabase.spec.databaseReclaimPolicy` 를 미러링.
  `delete` 로 설정 시 reconciler 가 `postgres.keiailab.io/postgresuser-finalizer`
  를 부착하고, garbage-collection 이전에 기존 `ensure=absent` reconcile
  스크립트로 `DROP ROLE` 실행. PG18 kind smoke iter#7 의 "kubectl delete
  postgresuser 가 PostgreSQL role 을 남긴다" 관찰을 종결.

### Fixed (수정됨)

- *(instance)* HA bootstrap fence race — 최종 수정. 원래 규칙
  "memberCount>1 클러스터에서 모든 leader-stop 시 fence" 가 모든 일시적
  lease-renewal 누락에서 bootstrap Pod 의 PVC 를 fence 하고
  standby.signal 을 seed 하여, 다음 부팅이 영원히 Follower 분기를 타는
  문제를 일으켰다. 3-layer 수정: (i) `supervise.IsStandby(dataDir)`
  short-circuit; (ii) `promotedAtLeastOnce atomic.Bool` 플래그로 실제
  promotion 상태에만 fencing 게이트; (iii) **standby-pod election
  downgrade** — 디스크에 standby.signal 이 있는 상태로 부팅하는 pod 는
  Follower election 을 탐 (절대 lease 경쟁 안 함). 그 위에
  `handleStoppedLeading` 은 이제 side-effect-free —
  failover 는 `executeClusterPromotion` 을 통해서만 operator-driven.
  PG18 / PG17 SHARD_REPLICAS=1 HA smoke 5/5 PASS + WAL 리플리케이션 검증;
  SHARD_REPLICAS=0 5/5 회귀 확인. 신규 회귀 테스트
  `TestHandleStoppedLeading_NeverFencesOrDemotes` 가 no-op 계약을 고정.
- *(controller)* PostgresDatabase / PostgresUser 의 psql 호출이 OS 사용자
  `pg-keiailab` (Dockerfile.pg USER 디렉티브) 으로 기본 사용. iter#5 의
  `eval` 버그 제거 후 `FATAL: role "pg-keiailab" does not exist` 로
  표면화 (PG18 kind smoke iter#6). 렌더링된 reconcile 스크립트의
  모든 psql 호출에 명시적 `-U postgres` 추가 (`psql_base` 상수 + 모든
  per-database 호출). 회귀 테스트
  `TestPostgresDatabaseReconcileScriptDoesNotUseEval` 가 렌더링된
  명령에 `-U postgres` 를 요구하도록 갱신.
- *(smoke)* `hack/smoke.sh` 가 `kubectl apply -f dist/install.yaml` 이후
  operator Pod 를 재시작하지 않아 kind 가 캐시된 이미지를 재사용
  (`imagePullPolicy=IfNotPresent` + 동일 태그) 했다. Pod 가 디스크의
  소스보다 오래된 operator 바이너리를 실행하여 신규 수정을 가려버림.
  smoke.sh 는 이제 apply 후 controller-manager deployment 를
  `kubectl rollout restart` 하고 `rollout status` 로 신규 ReplicaSet 을
  대기.
- *(controller)* PostgresDatabase / PostgresUser reconcile 스크립트가
  `eval "$psql_base" -c '<SQL>'` 로 psql 을 호출했다. 외부 shell 이
  `eval` 에 인자를 전달하기 전에 `<SQL>` 주변의 single quote 를 벗겨버려,
  `eval` 이 모든 인자를 공백으로 concat 후 전체 문자열을 재파싱. SQL 이
  공백 기준 word-split 되어 psql 이 `-c CREATE`, `DATABASE`, `smoke_db_x`,
  … 를 별개 인자로 인식 — `FATAL: role "1" does not exist` 와
  `FATAL: role "DATABASE" does not exist` 유발 (PG18 kind smoke iter#5
  관찰). 모든 `eval "$psql_base" …` call site 를 inline 의 완전한
  `psql -v ON_ERROR_STOP=1 -X -q -d postgres -c '<SQL>'` 호출로 교체하여
  SQL 이 단일 shell-quoted 인자에 머무르고 psql 에 atomic 하게 전달되도록
  함. 2 개의 신규 회귀 테스트
  (`TestPostgresDatabaseReconcileScriptDoesNotUseEval`,
  `TestPostgresUserReconcileScriptDoesNotUseEval`) 가 렌더링된 스크립트에
  `eval` 이 절대 포함되지 않음을 단언.
- *(controller)* PostgresDatabase / PostgresUser `status.applied` 가
  finalizer 가 이미 부착된 상태에서도 미설정 (no condition, empty
  `status: {}`) 으로 남을 수 있었다. 두 근본 원인 — *(a)* finalizer-add
  경로가 `Requeue:true` 를 반환하여 SQL apply 를 두 번째 pass 로 연기했고,
  informer-cache 전파 지연 하에서 오래된 snapshot 으로 루프하기 쉬웠다;
  *(b)* `statusUpdate` 가 `apierrors.IsConflict` 를 조용히 삼켜, 동일
  generation 에서 finalizer Update 와 status Update 가 경합할 때 status
  payload 가 통째로 떨어졌다. reconciler 는 이제 (i) finalizer 를 추가하고
  *같은* reconcile pass 를 이어감 (single-pass apply + status), (ii)
  conflict 시 한 번 re-fetch 후 retry 후 포기. PG18 kind smoke iter#3
  에서 관찰; 갱신된 테스트
  `TestPostgresDatabaseReconcileDeletePolicyAddsFinalizerBeforeApply` 가
  이제 single-pass `status.applied=true` 를 단언하며 커버. 일관성을 위해
  동일한 conflict-retry 패턴을 BackupJob, ScheduledBackup, Pooler 의
  `statusUpdate` 헬퍼에도 retrofit.
- *(controller)* Pooler — upstream PostgresCluster 의
  `status.shards[0].primary.ready` 가 Pooler 의 첫 reconcile *이후* true 로
  flip 했을 때, PoolerReconciler 가 PostgresCluster 에 `Watches` 가 없어서
  Pooler 가 `phase=Failed, reason=TargetNotFound` 에 영원히 갇혔다 (PG18
  kind smoke iter#4 관찰: Pooler 가 14:29:38Z 에 reconcile, cluster 가
  14:29:42Z 에 Ready=True, Deployment 미생성). PoolerReconciler 는 이제
  `Watches(&PostgresCluster{}, EnqueueRequestsFromMapFunc(...))` 로 status
  변경에 매칭되는 `spec.cluster.name` 의 namespace 내 모든 Pooler 를
  re-enqueue 하며, missing-target 분기는 `Failed` 대신 `phase=Pending` +
  `RequeueAfter` 로 표기. 회귀 테스트
  `TestPoolerReconcileTargetNotFoundIsPendingWithRequeue` 추가.
- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; SPDY streaming 경유 Kubelet / CRI-O /
  kube-apiserver DoS). `trivy fs --severity HIGH,CRITICAL --exit-code 1`
  이 다시 green.
- *(ci,kustomize)* manager Deployment 가 `containerPorts` 에 8081 health
  포트를 나열하지 않던 드리프트 종결. `config/manager/manager.yaml` 이
  `ports: []` 에서 `ports: [{name: health, containerPort: 8081, protocol:
  TCP}]` 로 전환, helm chart 와 `dist/install.yaml` 의 manager Deployment
  를 정합 (kube-linter liveness-port / readiness-port 검사).
- *(docs,license)* NOTICE 에서 stale legacy AGPL-3.0 third-party
  sharding-extension 항목 제거 — ADR-0003 (AGPLv3 영구 금지 라이선스
  정책) 및 ADR-0001 (self-built distributed SQL). NOTICE 는 이제
  `go.mod` 의 직접 의존만 나열 (Prometheus, Ginkgo, robfig/cron,
  moby/spdystream, …).

## [0.3.0-alpha.18] - 2026-05-12

### Added (추가됨)

- *(api,controller)* `ImageCatalog` + `ClusterImageCatalog` CRD 추가
  (TASKS T24). `spec.imageCatalogRef.{apiGroup,kind,name,major}`
  (생태계 호환성을 위해 `postgresql.cnpg.io` apiGroup 수용), namespaced
  / cluster-scoped 조회, catalog → StatefulSet 이미지 전파, image-hash
  어노테이션 기반 rollout 드리프트.
- *(api,controller)* `PostgresDatabase` + `PostgresUser` CRD (TASKS T22).
  Ready-primary `psql` reconcile 이 database / tablespace / schema /
  extension / FDW / foreign server, 그리고 role flags / membership /
  `connectionLimit` / `passwordSecretRef` / `disablePassword` /
  `validUntil` 적용. `databaseReclaimPolicy=delete` finalizer +
  `status.applied/observedGeneration/conditions` + `managedRolesStatus`
  집계.
- *(controller,instance)* Standalone replica cluster + externalClusters
  스트리밍 경로 (TASKS T25). `spec.externalClusters[]`,
  `bootstrap.pg_basebackup.source`, `replica.enabled/source`.
  `POSTGRES_REPLICA_CLUSTER=standalone` persistent-follower election,
  password Secret passfile + TLS Secret projected mount, source-mismatch
  fail-closed.
- *(api,controller)* `Pooler` CRD + PgBouncer 연결-풀 레이어 (F05).
  `instances`, `type=rw/ro`, `pgbouncer.{poolMode,parameters,pg_hba}`,
  auth / TLS Secret, exporter 사이드카, `spec.paused` PAUSE/RESUME,
  `pgbouncer.parameters` SIGHUP reload, HA topology / PDB.
- *(observability)* metrics + Grafana 대시보드 + PrometheusRule +
  ServiceMonitor (F05). BackupJob / Pooler phase 메트릭, 리플리케이션-지연
  바이트, PgBouncer exporter 알람, cluster-overview + Pooler 대시보드
  ConfigMap, kube-prometheus-stack 사이드카와 호환.
- *(controller,instance)* Failover promoter 실행 + follower election
  (F03 후속, PR #38/#39 랜딩). Replica-Pod `postgres` 컨테이너 exec →
  `pg_ctl promote` → `pg_is_in_recovery()` 폴링 → primary 어노테이션
  패치.
- *(backup)* `ScheduledBackup` CRD + 사이드카 exec runner + pgBackRest
  command-runner 플러그인 (F04). 6-field cron + `concurrencyPolicy`
  Allow/Forbid + retention + JobTemplate.
- *(release,ci)* Artifact Hub 자동 등록 / smoke `hack/artifacthub_*.sh`
  + Makefile `artifacthub-{register,smoke}` 타깃. kind smoke 에
  `SMOKE_HIBERNATION=1` (생태계-도구 호환성을 위해 hibernation
  어노테이션 `cnpg.io/hibernation` 유지 + PVC 마커 보존) 와
  `SMOKE_POOLER=1` (PgBouncer Service psql / PAUSE / RESUME / config
  reload) 시나리오 추가. `make validate` 의 CRD 개수 단언이 2 → 8 로
  상향되고 18 개 monitoring-render grep 검사 추가.
- *(olm)* `bundle/manifests/` 가 0.3.0-alpha.18 에 정합 — 8 CRD +
  alm-examples 일관 (`operator-sdk bundle validate` 0 warnings). owned-CRD
  의 7 개 `config/samples/` 파일 모두 활성화.

### Fixed (수정됨)

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1
  (CVE-2026-35469 HIGH; SPDY streaming 경유 Kubelet / CRI-O /
  kube-apiserver DoS). k8s.io/client-go 의 간접 표면도 refresh.

### Changed (변경됨)

- *(chart)* `version` 0.3.0-alpha.16 → 0.3.0-alpha.18, `appVersion`
  0.3.0-alpha.17 → 0.3.0-alpha.18, manager-image `newTag`
  0.3.0-alpha.18. 이전 alpha.17 bump 가 `version: 0.3.0-alpha.16` 을
  남겨둔 상태 — 이번 사이클이 셋 모두 정렬.

## [0.3.0-alpha.17] - 2026-05-12

### Fixed (수정됨)

- *(bootstrap)* 비어 있지 않은 stale `postmaster.pid` 의 PID-alive 검사
  (INC-0046 P19 ⑲, 프로덕션 클러스터 스코프). 남아 있던 좀비 파일이
  새 PG 시작을 막던 회귀를 종결.

## [0.3.0-alpha.16] - 2026-05-10

### Bug fixes (버그 수정)

- *(lint)* SA1019 + gocyclo nolint 지시문 추가.
- *(bundle)* generate-kustomize-manifests 단계 제거 (PR-B9.4) (#25).

### Chores (정리)

- *(oss)* `CITATION.cff` 추가 (#23).

### Features (기능)

- *(bundle)* OperatorHub.io bundle scaffold + ADR-0013 (PR-B9 cross-cut)
  (#24).

## [0.3.0-alpha.12] - 2026-05-08

### Fixed (수정됨)

- `copySpec` panic — `*unstructured.Unstructured` (cert-manager
  `Certificate` CR) 이 미지원이었다. switch case 추가 (NestedMap spec +
  Labels).

## [0.3.0-alpha.11] - 2026-05-08

### Fixed (수정됨)

- Helm 차트의 `rbac.yaml` 에서 `cert-manager.io/certificates` 규칙 누락
  (alpha.10 controller-gen 갱신이 `config/rbac/role.yaml` 만 sync; Helm
  차트의 `rbac.yaml` 은 수동 유지). 라이브 클러스터의 `ClusterRole` 이
  out-of-sync 가 되어 `Certificate` 요청이 Forbidden 처리됨.

## [0.3.0-alpha.10] - 2026-05-08

### Fixed (수정됨)

- ClusterRole 에 `cert-manager.io/certificates` RBAC 누락 → Phase-2 의
  `Certificate` CR upsert 가 Forbidden. `kubebuilder:rbac` 마커 추가.

## [0.3.0-alpha.9] - 2026-05-08

### Fixed (수정됨)

- `buildCertificate` panic — `unstructured.SetNestedField` 의 `dnsNames`
  가 deep-copy 호환을 위해 `[]string` → `[]any` 로 변환. alpha.8 이후의
  첫 실제 적용에서 포착.

## [0.3.0-alpha.8] - 2026-05-08

### Added (Pillar P7 §7 — TLS 통합 3-phase 마무리)

- **Phase 1 (alpha.5)**: `spec.tls` 필드 facade —
  `TLSSpec{Enabled, IssuerRef, CertSecretName}`. webhook 은 `enabled=true`
  시 `NotImplemented` 로 거부.
- **Phase 2 (alpha.6)**: 자동 cert-manager `Certificate` CR emit
  (unstructured, cert-manager Go SDK 의존 0). `IssuerRef` 설정 +
  `Enabled=true` 시 reconciler 가 `<cluster>-tls` Secret 의 발급을 위임.
  SAN = cluster name + per-shard headless service 의 DNS 형태 4×. ECDSA
  P-256 + `rotationPolicy=Always`.
- **Phase 3a (alpha.7)**: 서버 인증서 마운트를 위한 STS `Volumes` +
  `VolumeMounts` (`/etc/ssl/postgres`, PG key-file 권한 검사를 위해
  `defaultMode=0o400`).
- **Phase 3b (alpha.8)**: `postgresql.conf` 에 `ssl=on` +
  `ssl_cert_file` / `ssl_key_file` / `ssl_ca_file` +
  `ssl_min_protocol_version=TLSv1.2`. `pg_hba.conf` 가 `host` → `hostssl`
  로 전환 (외부 client 의 plaintext 연결 금지; pod-to-pod 가 신뢰
  경계이므로 replication 은 `host` 유지).

### Refactored (리팩토링)

- `Reconcile` 의 cyclomatic-complexity 감축 — `reconcileInstanceRBAC` (3
  upsert 통합) 와 `reconcileTLS` 헬퍼로 추출. gocyclo < 30 baseline 복원.

## [0.3.0-alpha.4] - 2026-05-08

### Fixed (수정됨)

- `dist/install.yaml` / Helm 차트 / 라이브 GitOps dry-run 검증 흐름 복원
  — `PostgresCluster` 설치 번들이 다시 server-side dry-run 을 통과.
- Go 1.25.10 builder 이미지에 release-gate baseline 정합 — stdlib 보안
  baseline 정렬.

## [0.3.0-alpha.3] - 2026-05-07

### Fixed (수정됨)

- 기존 PGDATA 를 가진 Postgres Pod 가 재시작될 때, bootstrap init 컨테이너
  가 kubelet 이 `fsGroup` 을 적용한 이후에도 `chmod 0700 "$PGDATA"` 를
  재실행한다. `data/postgres-shard-0-0` 재생성 중 PostgreSQL 이
  `invalid permissions` 로 종료되는 회귀를 라이브에서 관찰.

## [0.3.0-alpha.2] - 2026-05-07

### Added (추가됨)

- `hack/smoke.sh` 의 PG17/PG18 매트릭스 오버라이드 (`PG_MAJOR`,
  `POSTGRES_VERSION`, `SHARD_REPLICAS`) 와 HA WAL-streaming 게이트.
- PG18 failover smoke 게이트: primary Pod 삭제 후 standby-promotion RTO
  측정, CR-status primary 수렴 확인, 재시작된 이전 primary 가 standby
  로 재진입함을 검증.
- `deploy/overlays/prod/` GitOps 진입점 — kubebuilder
  `config/{crd,rbac,manager}` 를 prod 네임스페이스로 정렬하고 자동 생성된
  Namespace 리소스를 제거. ArgoCD 단방향 sync 전제.
- `deploy/postgres-cluster.yaml` — 프로덕션 `PostgresCluster` CR 샘플
  (db 네임스페이스, `shardingMode=none`, `replicas=2`, ceph-block,
  monitoring on).
- `deploy/README.md` — 운영 runbook (사전 요구, 적용, 롤백).
- ADR-0006 — GitOps deploy-overlay 채택 결정.

### Fixed (수정됨)

- election identity 를 `podName/podUID` 로 전환하여 동일 이름의 ordinal
  재생성이 이전 primary 의 lease 를 즉시 회수하지 못하도록 함.
- 재시작된 ordinal-0 primary 가 이제 `standby.signal` / `primary_conninfo`
  를 재구성; `ReleaseOnCancel=false` 와 status 폴링 추가 — PG18 failover
  smoke 에서 RTO 21 s (< 30 s) 관찰.

## [0.3.0-alpha.1] - 2026-05-06

### Changed (변경됨)

- Chart.yaml `version` + `appVersion` 0.3.0-alpha → 0.3.0-alpha.1
  (반복적 pre-release 표기).
- `config/manager/kustomization.yaml` `newTag` 동기화.
- `dist/install.yaml` 재생성 (`make build-installer`) — 이미지 태그
  0.3.0-alpha.1.

### Fixed (수정됨)

- `release` 타깃이 이제 `docker buildx build --platform linux/amd64 --push`
  로 이미지를 빌드+푸시 (조직 §2 에 따라 기본 빌더 명시). 단일 호출에서
  Build + push 가 atomic ($(CONTAINER_TOOL) build 분리 단계 제거).

### Changed (BREAKING)

- **`PostgresCluster` CRD 스키마 재정의 (RFC 0001 v2 — F01a)**:
  `spec.coordinator` / `spec.workers[]` / `spec.routers` /
  `spec.extensions` / `spec.sharding.backend` / `spec.deployment` 제거.
  새로운 6-필드 구조 (`postgresVersion` / `shardingMode` / `shards` /
  `router` / `autoSplit` / `backup` / `monitoring`) 로 교체. `status` 도
  `topology` / `channel` 을 떨어뜨리고 `phase` / `shards[]` / `router` 를
  도입. v0.x 매니페스트는 비호환 (alpha-channel 정책).
- CRD 가 이제 RFC 0001 §3.3 의 3 개 CEL XValidation 을 임베드 —
  `shardingMode↔shards`, `router↔native`, `autoSplit↔native` — API 서버가
  직접 거부.
- Webhook 검증이 PostgresVersion 매트릭스 조회 + autoSplit-트리거 일관성
  + 비어 있지 않은 backup 스케줄로 단순화. 정확한 cron 파싱 / duration
  파싱은 외부 의존성 도입 시 F01b/F02 에서 도착.

### Deferred to F01b

- 신규 스펙 reconcile 본체 (`ShardsSpec` → StatefulSet topology,
  `RouterSpec` → Deployment, `BackupSpec` → 자동 `BackupJob` 생성). 이번
  턴은 `// TODO(F01b)` 주석과 최소 noop reconcile (`status.phase=Provisioning`,
  `Ready=False reason=NotApplicable`) 만 남김.
- `internal/controller/builders.go` 헬퍼는 시그니처를 유지하고
  `//nolint:unused` 부착 — F01b reconcile 에서 배선될 예정.
- 2 개의 envtest (`postgrescluster_controller_test.go`,
  `cascade_delete_test.go`) 는 제거되고 F01b 에서 RFC 0001 스펙에 맞춰
  재작성될 예정.

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **재설계**: PostgreSQL 위에 자체 구축한 distributed-SQL 레이어로
  피벗. ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 이
  키스톤.
- 아카이브된 AGPL third-party-extension 격리 + vanilla-PG 기본 모델을
  대체. 본 단계부터 런타임은 그 extension 의 *코드 0 줄* 을 담지 않으며,
  격리 플러그인 모델은 폐기.
- 외부 의존 라이선스 정책 (ADR-0003): v1+ 안정성을 가진 BSD / Apache /
  MIT / PG License 만. **AGPL / BUSL / CSL / SSPL 은 영구 금지.**
- Helm 패키징 (ADR-0002): 단일 차트 + 컴포넌트 플래그 (router /
  resharder / rebalancer / keda / backup / monitoring).
- CRD 라이프사이클 (ADR-0004): operator manager 가 소유 (server-side
  apply). Helm 의 `crds/` 디렉토리는 향후 단계에서 폐기 예정.
- 버전 채널 (ADR-0005): alpha (P0–P3) → beta (P4–P5) → stable (P6+).
  CRD apiVersion v1alpha1 → v1beta1 → v1.

### Added (추가됨)

- 신규 ADR: 0001 (self-built distributed SQL — 키스톤), 0002 (단일
  차트 + 플래그), 0003 (라이선스 정책: no AGPL / BUSL / CSL / SSPL),
  0004 (operator-managed CRD lifecycle), 0005 (versioning + channels).
- 신규 RFC: 0001 (PostgresCluster CRD v2), 0002 (`ShardRange` CRD), 0003
  (`ShardSplitJob` 7-step 온라인 resharding), 0004 (pg-router 아키텍처),
  0005 (분산 트랜잭션 — 2PC + saga).
- `README.md` 재작성 — self-built distributed-SQL 정체성, 8-phase
  로드맵 (P0–P7, ~64 개월), 명시적 라이선스 정책.
- `TASKS.md` 재작성 — P0 task 표 + 다음 phase (P1) 프리뷰.
- `HANDOFF.md` 재작성 — 다음 세션의 진입점, 코드 제거 격리 가이드.

### Archived (아카이브)

- 원래의 ADR 0001–0010 이 `docs/kb/adr/_archive/v0.x/` 로 이동 (git
  history 보존).
- 원래의 RFC 0001–0005 가 `docs/rfcs/_archive/v0.x/` 로 이동.

### Deprecated (다음 세션에서 제거 예정)

- 서드파티 AGPL sharding extension 의 internal 패키지 — ADR-0003 위반.
- `charts/postgres-operator/` 에서 해당 extension 의 opt-in 메시징
  (레거시 DSN 필드, NOTES.txt 의 AGPL 안내).

## [0.2.0-alpha] - 2026-05-01

### Changed (BREAKING)

- 이전 단계의 ADR (현재 아카이브됨) — 기본 스택을 vanilla PostgreSQL
  18 로 전환. 서드파티 AGPL sharding-extension 통합은 Beta 채널의
  opt-in 으로 격리. 명시적으로 활성화한 사용자는 AGPL-3.0 §13 의 SaaS
  의무를 수용함 (operator 자체는 Apache-2.0 클린 유지).
- `VersionSpec` 의 레거시 extension 필드가 이제 Optional (`omitempty`)
  — 이전에는 Required. 비어 있거나 누락된 값은 vanilla PG 를 선택.
- Stable 채널: PG 16/17/18 vanilla. 모든 서드파티 sharding-extension
  조합은 Beta 로 다운그레이드.
- 차트의 `config/samples/*` 에서 서드파티 extension 기본값 제거. 권장
  기본은 이제 vanilla PG18.

### Added (추가됨)

- `internal/version/matrix.go` 에 PG 18 vanilla Stable 조합
  (`ghcr.io/keiailab/pg:18`) 추가.
- 이전 단계의 ADR (아카이브됨) — 라이선스 + sharding 전략. AGPL
  서드파티 sharding extension 격리 문서화 및 라이선스 의무 할당 기록.
- RFC 0005 (native sharding plugin) — 7 개의 핵심 distributed-SQL
  메커니즘 분해, 자체 플러그인 인터페이스의 초안 설계, Phase 2A →
  Phase 4 의 마일스톤.
- 차트의 `NOTES.txt` 에 라이선스 공시 메시지 (Apache-2.0 operator +
  opt-in AGPL 서드파티-extension 안내).
- 서드파티-extension 플러그인 패키지와 함수 docstring 에 AGPL §13 SaaS
  의무에 대한 문서 경고.

### Removed (삭제됨)

- stale 한 `ChannelPreviewPG18` placeholder 제거 — PG18 이 Stable 에
  들어온 이상 obsolete.
- webhook 의 PG18 + `PostgresEighteen` feature-gate 검사 제거 —
  Stable 에서는 더 이상 불필요.

## [0.1.1-alpha] - 2026-05-01

### Added (추가됨)

- `make validate`, `make gate`, `make release-preflight`, `make release`,
  `make helm-publish` 를 통한 로컬 릴리즈 자동화.
- `config/crd/kustomization.yaml` 가 `make install / uninstall` 와
  CRD-render 경로를 복원.
- `make sync-crds` 가 `config/crd/bases` 와
  `charts/postgres-operator/crds` 사이의 드리프트를 차단.
- Helm 차트의 `.helmignore`, `values.schema.json`, README, Artifact Hub
  메타데이터.
- `dist/install.yaml` 단일 설치 아티팩트 검증 경로.

### Fixed (수정됨)

- `go test` 를 직접 실행할 때 controller 테스트 suite 가 로컬 envtest-asset
  fallback 을 사용하도록 조정.
- 차트의 기본 image repository 를
  `ghcr.io/keiailab/postgres-operator` 로 정합.
- Helm RBAC 에 `BackupJob` 리소스 권한 포함.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
