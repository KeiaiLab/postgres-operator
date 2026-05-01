# 변경 이력

본 프로젝트는 SemVer를 따른다.

## [Unreleased]

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
