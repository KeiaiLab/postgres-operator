# 변경 이력

본 프로젝트는 SemVer를 따른다.

## [Unreleased]

## [0.3.0-alpha] - 2026-05-02

### Changed (BREAKING)

- **재설계**: PostgreSQL 위 자체 분산 SQL 레이어 구축으로 전환. ADR-0001 (`docs/adr/0001-self-built-distributed-sql.md`) 가 keystone 결정.
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

- 기존 ADR 0001-0010 → `docs/adr/_archive/v0.x/` (git history 보존).
- 기존 RFC 0001-0005 → `docs/rfcs/_archive/v0.x/`.

### Deprecated (다음 세션 처리 예정)

- `internal/citus/` 디렉토리 — ADR-0003 라이선스 정책 위반.
- `internal/plugin/extension/citus/` 디렉토리.
- `charts/postgresql-operator/` 의 Citus opt-in 메시징 (`citusLibPQ.dsn`, NOTES.txt AGPL 안내).

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
- `make sync-crds` 추가로 `config/crd/bases`와 `charts/postgresql-operator/crds` drift 차단.
- Helm chart `.helmignore`, `values.schema.json`, README, Artifact Hub metadata 추가.
- `dist/install.yaml` 단일 설치 산출물 검증 경로 추가.

### Fixed

- 직접 `go test` 실행 시 로컬 envtest asset fallback을 사용하도록 controller test suite 보정.
- chart 기본 image repository를 `ghcr.io/keiailab/postgres-operator`로 정렬.
- Helm RBAC가 `BackupJob` 리소스 권한을 포함하도록 정렬.
