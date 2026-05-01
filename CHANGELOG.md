# 변경 이력

본 프로젝트는 SemVer를 따른다.

## [Unreleased]

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
