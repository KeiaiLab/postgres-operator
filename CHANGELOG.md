# 변경 이력

본 프로젝트는 SemVer를 따른다.

## [Unreleased]

### Added

- *(olm,docs)* `docs/operator-guide/community-operators-onboarding.md` — k8s-operatorhub/community-operators 채널 등록 절차 (사전조건 체크리스트, bundle 이미지 빌드/push, gh pr create, upgrade graph 운영, Artifact Hub OLM 검증). T28 1 차 산출물.
- *(ci)* `make hooks-install` / `make hooks-check` target — lefthook DCO/Conventional Commits 게이트 활성화 wrapper. CONTRIBUTING 안내 정합.
- *(ci)* `make validate` 게이트 4 종 보강 — bundle CRD count ≥ 8, `operator-sdk bundle validate` default + `suite=operatorframework`, Chart appVersion ↔ kustomize newTag ↔ dist image tag drift assertion, `.github/workflows/` 부재 (ADR-0009 enforce).
- *(olm)* CSV `customresourcedefinitions.owned[]` 에 ImageCatalog/ClusterImageCatalog/PostgresDatabase/PostgresUser description + displayName 추가 — operatorframework suite warning 4 건 0 화.
- *(oss)* `SUPPORT.md` 신설 (GitHub Discussions / Issues / PR 경로, 보안 신고 분기, 응답 기대치). CHANGELOG 0.3.0-alpha.17/.18 정합.
- *(docs)* README — 0.3.0-alpha.18 8 CRD 표면 표 + 빠른 시작 6 단계 워크플로 갱신.
- *(docs)* TASKS T26 (cross-cut OSS/OLM 정합 완료) + T27 (신규 CRD live smoke 자동화 진행 80%, ①~④ 모두 완결) + T28 (community-operators 등록 절차) 등록.
- *(smoke)* hack/smoke.sh 에 신규 CRD 4 종 시나리오 추가 — `SMOKE_DATABASE=1` (PostgresDatabase → status.applied + pg_database + reclaim=delete DROP), `SMOKE_USER=1` (PostgresUser → status.applied + pg_roles + DROP ROLE), `SMOKE_SCHEDULEDBACKUP=1` (cron immediate → BackupJob 생성 검증), `SMOKE_IMAGECATALOG=1` (ImageCatalog/ClusterImageCatalog schema + lookup). step number N/15 정합.
- *(api)* 8 owned CRD 에 `kubebuilder:resource:categories` marker 추가 — `kubectl get postgres`/`database`/`backup`/`pooler`/`image`/`role`/`all` 카테고리 단축 명령 + OperatorHub UI 그룹핑 지원.
- *(ci)* `make lint-k8s` target + `make validate` 통합 — kube-linter 로 dist/install.yaml + helm template 산출물의 K8s 리소스 보안/best-practice 정적 분석. liveness-port/readiness-port/non-root/readOnlyRoot/resource-limit 30+ check.
- *(docs)* ROADMAP 현재 상태 스냅샷 — alpha.18 + OLM bundle + CNPG 호환 표면 + 로컬 4 계층 게이트 4 행 반영.
- *(docs)* cross-validation-cnpg 매트릭스 — OLM bundle / Helm chart / Local supply chain gates / Security vulnerability scan / DCO sign-off enforcement 5 행 추가.

### Fixed

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1 (CVE-2026-35469 HIGH, Kubelet/CRI-O/kube-apiserver DoS via SPDY streaming). trivy fs `--severity HIGH,CRITICAL --exit-code 1` 게이트 회복.
- *(ci,kustomize)* manager Deployment 가 8081 health port 를 containerPorts 에 누락한 drift 해소 — config/manager/manager.yaml `ports: []` → `ports: [{name: health, containerPort: 8081, protocol: TCP}]`. helm chart 와 dist/install.yaml 의 manager Deployment 가 동일 표면이 되도록 정렬 (kube-linter liveness-port/readiness-port check).

## [0.3.0-alpha.18] - 2026-05-12

### Added

- *(api,controller)* `ImageCatalog` + `ClusterImageCatalog` CRD 이식 (TASKS T24). CloudNativePG 호환 `spec.imageCatalogRef.{apiGroup,kind,name,major}` 표면, namespaced/cluster-scoped lookup, catalog → StatefulSet image 반영, image hash annotation 기반 rollout drift 추적.
- *(api,controller)* `PostgresDatabase` + `PostgresUser` CRD (TASKS T22). ready primary Pod `psql` reconcile 로 database/tablespace/schema/extension/FDW/foreign server + role flags/membership/connectionLimit/passwordSecretRef/disablePassword/validUntil 적용. `databaseReclaimPolicy=delete` finalizer + status `applied/observedGeneration/conditions` + `managedRolesStatus` 집계.
- *(controller,instance)* standalone replica cluster + externalClusters streaming path (TASKS T25). `spec.externalClusters[]`, `bootstrap.pg_basebackup.source`, `replica.enabled/source`. `POSTGRES_REPLICA_CLUSTER=standalone` 영구 follower election, password Secret passfile + TLS Secret projected mount, source mismatch fail-closed.
- *(api,controller)* `Pooler` CRD + PgBouncer connection pool 계층 (F05). `instances`, `type=rw/ro`, `pgbouncer.{poolMode,parameters,pg_hba}`, auth/TLS Secret, exporter sidecar, `spec.paused` PAUSE/RESUME, `pgbouncer.parameters` SIGHUP reload, HA topology/PDB.
- *(observability)* metrics + Grafana dashboards + PrometheusRule + ServiceMonitor (F05). BackupJob/Pooler phase metric, replication lag bytes, PgBouncer exporter alert, cluster overview + Pooler dashboard ConfigMap, kube-prometheus-stack sidecar 호환.
- *(controller,instance)* failover promoter execution + Follower election (F03 후속, PR #38/#39 합류). replica Pod postgres container exec → `pg_ctl promote` → `pg_is_in_recovery()` polling → primary annotation patch.
- *(backup)* `ScheduledBackup` CRD + sidecar exec runner + pgbackrest command-runner plugin (F04). 6-field cron + concurrencyPolicy Allow/Forbid + retention + JobTemplate.
- *(release,ci)* Artifact Hub 자동 등록/조회 hack/artifacthub_*.sh + Makefile artifacthub-{register,smoke} targets. Kind smoke 에 `SMOKE_HIBERNATION=1` (cnpg.io/hibernation annotation + PVC marker 보존) + `SMOKE_POOLER=1` (PgBouncer Service psql/PAUSE/RESUME/config reload) 시나리오 추가. `make validate` CRD count 2 → 8 + monitoring 렌더 18 grep.
- *(olm)* `bundle/manifests/` 0.3.0-alpha.18 — 8 CRD + alm-examples 정합 (operator-sdk bundle validate 0 warning). config/samples 7 종 owned CRD 모두 활성화.

### Fixed

- *(security)* `github.com/moby/spdystream` v0.5.0 → v0.5.1 (CVE-2026-35469 HIGH, Kubelet/CRI-O/kube-apiserver DoS via SPDY streaming). k8s.io/client-go indirect 표면 갱신.

### Changed

- *(chart)* version 0.3.0-alpha.16 → 0.3.0-alpha.18, appVersion 0.3.0-alpha.17 → 0.3.0-alpha.18, manager image newTag 0.3.0-alpha.18. 직전 alpha.17 bump 의 `version: 0.3.0-alpha.16` 누락분 정렬.

## [0.3.0-alpha.17] - 2026-05-12

### Fixed

- *(bootstrap)* non-empty stale `postmaster.pid` 의 PID alive check (argos INC-0046 P19 ⑲). 좀비 file 이 남았을 때 새 PG 부팅이 막히던 회귀 해결.

## [0.3.0-alpha.16] - 2026-05-10

### Bug Fixes

- *(lint)* SA1019 + gocyclo nolint (mongodb ADR-0022 패턴 정합)
- *(bundle)* Generate-kustomize-manifests 단계 제거 (PR-B9.4, mongodb ADR-0023 정합) (#25)

### Chores

- *(oss)* CITATION.cff 추가 (#23)

### Features

- *(bundle)* OperatorHub.io bundle scaffold + ADR-0013 (PR-B9 cross-cut) (#24)

## [0.3.0-alpha.12] - 2026-05-08

### Fixed

- copySpec panic — *unstructured.Unstructured (cert-manager Certificate CR) 미지원. switch case 추가 (NestedMap spec + Labels).

## [0.3.0-alpha.11] - 2026-05-08

### Fixed

- helm chart 의 rbac.yaml 에 cert-manager.io/certificates rule 누락 (alpha.10 의 controller-gen 갱신은 config/rbac/role.yaml 만 sync, helm chart rbac.yaml 은 manually maintained). live cluster 의 ClusterRole 가 동기화되지 않아 Certificate Forbidden 지속.

## [0.3.0-alpha.10] - 2026-05-08

### Fixed

- ClusterRole 에 cert-manager.io/certificates RBAC 누락 → Phase 2 Certificate CR upsert 시 Forbidden. kubebuilder:rbac marker 추가.

## [0.3.0-alpha.9] - 2026-05-08

### Fixed

- buildCertificate panic — unstructured.SetNestedField 의 dnsNames 를 []string → []any 변환 (deep copy 호환). alpha.8 첫 라이브 적용에서 발견.

## [0.3.0-alpha.8] - 2026-05-08

### Added (Pillar P7 §7 — TLS 통합 3-phase 완결)

- **Phase 1 (alpha.5)**: `spec.tls` field facade — `TLSSpec{Enabled, IssuerRef, CertSecretName}`. `enabled=true` 시 webhook NotImplemented reject.
- **Phase 2 (alpha.6)**: cert-manager `Certificate` CR 자동 emit (unstructured, cert-manager Go SDK 의존 0). IssuerRef 명시 + Enabled=true 시 reconciler 가 `<cluster>-tls` Secret 자동 발급 위임. SAN = cluster name + 모든 shard headless service DNS 4 form. ECDSA P-256 + rotationPolicy=Always.
- **Phase 3a (alpha.7)**: STS `Volumes` + `VolumeMounts` 의 server cert mount (`/etc/ssl/postgres`, `defaultMode=0o400` PG 키 파일 권한 검사 통과).
- **Phase 3b (alpha.8)**: `postgresql.conf` 의 `ssl=on` + `ssl_cert_file`/`ssl_key_file`/`ssl_ca_file` + `ssl_min_protocol_version=TLSv1.2`. `pg_hba.conf` 의 `host` → `hostssl` 강제 (외부 client plaintext connection 차단, replication 은 pod-to-pod 신뢰 boundary 라 host 유지).

### Refactored

- `Reconcile` 의 cyclomatic complexity 절감 — `reconcileInstanceRBAC` (3 upsert 단일화) + `reconcileTLS` helper extract. gocyclo < 30 baseline 정합.

## [0.3.0-alpha.4] - 2026-05-08

### Fixed

- `dist/install.yaml` / Helm chart / live GitOps dry-run 검증 흐름을 복구해
  `PostgresCluster` 설치 번들이 server-side dry-run 기준을 다시 통과하도록 했다.
- 릴리스 게이트 기준을 Go 1.25.10 builder image로 동기화해 stdlib 보안 기준을 맞췄다.

## [0.3.0-alpha.3] - 2026-05-07

### Fixed

- 기존 PGDATA 를 가진 Postgres Pod 재시작 시 kubelet `fsGroup` 적용 뒤에도
  bootstrap init container 가 `chmod 0700 "$PGDATA"` 를 다시 수행하도록 수정했다.
  이 회귀는 `data/argos-postgres-shard-0-0` 재생성 중 PostgreSQL 의
  `invalid permissions` 종료로 실측됐다.

## [0.3.0-alpha.2] - 2026-05-07

### Added

- `hack/smoke.sh` PG17/PG18 matrix override (`PG_MAJOR`, `POSTGRES_VERSION`,
  `SHARD_REPLICAS`) 와 HA WAL streaming gate.
- PG18 failover smoke gate: primary Pod delete 후 standby promote RTO 측정,
  CR status primary 수렴, restarted old primary standby 재진입 검증.
- `deploy/overlays/prod/` GitOps 진입점 — kubebuilder `config/{crd,rbac,manager}` 를
  prod namespace 로 정렬 + 자동 생성 Namespace 리소스 제거. ArgoCD 단방향 동기 전제.
- `deploy/postgres-cluster.yaml` — production PostgresCluster CR sample (db ns,
  shardingMode=none, replicas=2, ceph-block, monitoring on).
- `deploy/README.md` — 운영 런북 (사전 조건, 적용, 롤백 절차).
- ADR-0006 — GitOps deploy 오버레이 도입 결정 (mongodb-operator / valkey-operator 와
  3-repo 구조 정합).

### Fixed

- election identity 를 `podName/podUID` 로 전환하여 같은 StatefulSet ordinal 이
  재생성될 때 이전 primary lease 를 즉시 재점유하지 못하게 했다.
- restarted ordinal-0 primary 의 `standby.signal` / `primary_conninfo` 재구성,
  `ReleaseOnCancel=false`, status polling 을 추가해 PG18 failover smoke 에서
  RTO 21s(<30s)를 확인했다.

## [0.3.0-alpha.1] - 2026-05-06

### Changed

- Chart.yaml `version` + `appVersion` 0.3.0-alpha → 0.3.0-alpha.1 (iterative
  pre-release 표기, mongodb-operator beta.N + valkey-operator alpha.N 패턴 정합).
- `config/manager/kustomization.yaml` 의 `newTag` 도 동일 동기.
- `dist/install.yaml` 재생성 (`make build-installer`) — image tag 0.3.0-alpha.1.

### Fixed

- `release` 타겟의 image build/push 를 `docker buildx build --platform
  linux/amd64 --push` 로 통합 (글로벌 §2 강제 — default builder 명시).
  단일 호출로 build + push 원자화 (`$(CONTAINER_TOOL) build` 분리 호출 제거).

### Changed (BREAKING)

- **PostgresCluster CRD schema 재정의 (RFC 0001 v2 — F01a)**: `spec.coordinator` / `spec.workers[]` / `spec.routers` / `spec.extensions` / `spec.sharding.backend` / `spec.deployment` 모두 폐기. 새 6-필드 구조 (`postgresVersion` / `shardingMode` / `shards` / `router` / `autoSplit` / `backup` / `monitoring`) 로 교체. status 도 `topology` / `channel` 폐기, `phase` / `shards[]` / `router` 신설. v0.x manifest 는 호환되지 않음 (alpha 채널 정책).
- CRD 자체에 RFC 0001 §3.3 의 3 개 CEL XValidation 규칙 (`shardingMode↔shards`, `router↔native`, `autoSplit↔native`) 박힘 — K8s API server 가 직접 거절.
- webhook 검증 단순화: PostgresVersion matrix lookup + autoSplit trigger 일관성 + backup schedule 비어있지 않음만 강제. cron 정밀 parse / duration parse 는 F01b/F02 에서 외부 의존 추가 후 도입.

### Deferred to F01b

- 새 spec 기반 reconcile 본체 (ShardsSpec → StatefulSet 토폴로지, RouterSpec → Deployment, BackupSpec → BackupJob 자동 생성). 본 turn 에서는 `// TODO(F01b)` 주석 + minimal noop reconcile (`status.phase=Provisioning`, `Ready=False reason=NotApplicable`).
- `internal/controller/builders.go` 의 helper 들은 시그너처 보존 + `//nolint:unused` directive — F01b 에서 reconcile 본체가 호출.
- envtest 2 종 (`postgrescluster_controller_test.go`, `cascade_delete_test.go`) 삭제 — F01b 에서 RFC 0001 spec 기반으로 새로 작성.

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **재설계**: PostgreSQL 위 자체 분산 SQL 레이어 구축으로 전환. ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 가 keystone 결정.
- ADR-0010 (legacy, Citus AGPL 격리 + vanilla PG default) supersede. 본 phase 부터 Citus 코드 의존 *0줄*. Citus 격리 plugin 모델 폐기.
- 외부 의존 라이선스 정책 (ADR-0003): BSD/Apache/MIT/PG License + v1+ stability 만. **AGPL/BUSL/CSL/SSPL 영구 금지**.
- Helm 패키징 (ADR-0002): 단일 chart + 컴포넌트 flag (router / resharder / rebalancer / keda / backup / monitoring).
- CRD 라이프사이클 (ADR-0004): operator manager 가 소유 (server-side apply). Helm `crds/` 디렉토리는 향후 phase 에서 폐기 예정.
- 버전 채널 (ADR-0005): alpha (P0~P3) → beta (P4~P5) → stable (P6~). CRD apiVersion v1alpha1 → v1beta1 → v1.

### Added

- 신규 ADR 0001 (self-built distributed SQL — keystone), 0002 (single chart with flags), 0003 (license policy: no AGPL/BUSL/CSL/SSPL), 0004 (CRD managed by operator), 0005 (versioning and channels).
- 신규 RFC 0001 (PostgresCluster CRD v2), 0002 (ShardRange CRD), 0003 (ShardSplitJob 7-step online resharding workflow), 0004 (pg-router architecture), 0005 (distributed transactions — 2PC + saga).
- `README.md` 재작성 — 자체 분산 SQL 정체성, 8 phase 로드맵 (P0~P7, ~64개월), 라이선스 정책 명시.
- `TASKS.md` 재작성 — P0 작업 표 + 다음 phase (P1) 미리보기.
- `HANDOFF.md` 재작성 — 다음 세션 진입점, 코드 폐기 작업 격리 안내.

### Archived

- 기존 ADR 0001-0010 → `docs/kb/adr/_archive/v0.x/` (git history 보존).
- 기존 RFC 0001-0005 → `docs/rfcs/_archive/v0.x/`.

### Deprecated (다음 세션 처리 예정)

- `internal/citus/` 디렉토리 — ADR-0003 라이선스 정책 위반.
- `internal/plugin/extension/citus/` 디렉토리.
- `charts/postgres-operator/` 의 Citus opt-in 메시징 (`citusLibPQ.dsn`, NOTES.txt AGPL 안내).

## [0.2.0-alpha] - 2026-05-01

### Changed (BREAKING)

- ADR 0010 — default stack을 vanilla PostgreSQL 18로 전환. Citus 통합은 Beta 채널 opt-in으로 격리됨.
  Citus 활성화 사용자는 AGPL-3.0 §13 SaaS 의무를 명시 수용한다 (operator 자체는 Apache-2.0 청정 유지).
- `VersionSpec.Citus` 필드를 Required → Optional (omitempty) 로 변경. 빈 문자열 또는 누락 시 vanilla PG.
- Stable 채널: PG 16/17/18 vanilla. Citus 조합은 모두 Beta로 강등.
- chart `config/samples/*` 의 default `extensions: [citus]` 제거. 권장 default가 vanilla PG18로 전환.

### Added

- `internal/version/matrix.go` 에 PG 18 vanilla Stable 조합 (`ghcr.io/keiailab/pg:18`) 추가.
- ADR 0010 (license + sharding strategy) — Citus AGPL 격리 결정 + 라이센스 의무 분배 기록.
- RFC 0005 (native sharding plugin) — Citus 핵심 7개 메커니즘 분해 + 자체 plugin 인터페이스 design draft +
  Phase 2A~Phase 4 마일스톤.
- chart NOTES.txt 의 license disclosure 메시지 (Apache-2.0 operator + opt-in AGPL Citus 안내).
- `internal/plugin/extension/citus/` 패키지 doc + 함수 doc 에 AGPL §13 SaaS 의무 경고.

### Removed

- 매트릭스 호환성 도구로서의 stale `ChannelPreviewPG18` placeholder 제거 (PG18 Stable 진입으로 무용).
- webhook의 PG18 + `PostgresEighteen` feature gate 검증 로직 (Stable 진입으로 불필요).

## [0.1.1-alpha] - 2026-05-01

### Added

- `make validate`, `make gate`, `make release-preflight`, `make release`, `make helm-publish` 로컬 릴리스 자동화 추가.
- `config/crd/kustomization.yaml` 추가로 `make install/uninstall` 및 CRD 렌더 경로 복구.
- `make sync-crds` 추가로 `config/crd/bases`와 `charts/postgres-operator/crds` drift 차단.
- Helm chart `.helmignore`, `values.schema.json`, README, Artifact Hub metadata 추가.
- `dist/install.yaml` 단일 설치 산출물 검증 경로 추가.

### Fixed

- 직접 `go test` 실행 시 로컬 envtest asset fallback을 사용하도록 controller test suite 보정.
- chart 기본 image repository를 `ghcr.io/keiailab/postgres-operator`로 정렬.
- Helm RBAC가 `BackupJob` 리소스 권한을 포함하도록 정렬.
