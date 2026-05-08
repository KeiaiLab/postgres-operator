# 변경 이력

본 프로젝트는 SemVer를 따른다.

## [Unreleased]

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
