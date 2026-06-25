# postgres-operator — AI Agent Guide

본 문서는 AI agent (Claude Code, Cursor, Continue 등) 가 본 repo 에서 안전하고 효과적으로 작업하기 위한 *프로젝트별* 가이드입니다. 글로벌 규약 (`~/.claude/CLAUDE.md` + `standards/*`) 의 *추가 부속*이며, 충돌 시 글로벌 규약 우선. (mongodb-operator / valkey-operator AGENTS.md 와 sister 문서)

## Project Structure

```
cmd/main.go                      Manager entry (controllers/webhooks 등록)
cmd/instance/                    PID1 instance manager binary (데이터플레인)
cmd/pg-router/                   PoC PG wire-protocol v3 router proxy
cmd/{scatter,reshard-copy}-poc/  분산 SQL PoC binaries
api/v1alpha1/*_types.go          CRD schemas (PostgresCluster / Pooler / BackupJob /
                                 ScheduledBackup / PostgresDatabase / PostgresUser /
                                 ShardSplitJob / ImageCatalog)
api/v1alpha1/zz_generated.*      Auto-generated (DO NOT EDIT)
internal/controller/*            Reconciliation logic (cluster/pooler/backup/db/user/split)
internal/instance/               PID1 데이터플레인 — election / fencing / supervise / statusapi
internal/postgres/               SQL DSL (grants 등) + PG 도메인 헬퍼
internal/router/                 vindex shard 해석 (pg-router 소비)
internal/webhook/v1alpha1/       Admission webhooks
config/crd/bases/*               Generated CRDs (DO NOT EDIT)
config/rbac/role.yaml            Generated RBAC (DO NOT EDIT)
charts/postgres-operator/        Helm chart (publish 대상, crds 는 make sync-crds 동기)
bundle/                          OLM bundle (make bundle 산출)
build/images/                    PG 16/17/18 (Citus) + instance 이미지 Dockerfile
docs/kb/adr/                     Architecture Decision Records
PROJECT                          Kubebuilder metadata (DO NOT EDIT)
Makefile                         gate = lint + test + audit + validate
```

## Critical Rules (절대 위반 금지)

### Never Edit These (Auto-Generated)
- `config/crd/bases/*.yaml` — `make manifests` 산출 (chart crds 는 `make sync-crds`)
- `config/rbac/role.yaml` — `make manifests` 산출
- `**/zz_generated.*.go` — `make generate` 산출
- `PROJECT` — `kubebuilder` CLI 산출

### Never Remove Scaffold Markers
`// +kubebuilder:scaffold:*` 마커 삭제 금지. CLI 가 본 마커 위치에 코드 주입.

### keiailab-commons 채택 표면 (v0.11.0+)
- **finalizer**: commons `pkg/finalizer` + 표준 이름 `<resource>.keiailab.com/finalizer`
  (`postgresdatabase.keiailab.com/finalizer` / `postgresuser.keiailab.com/finalizer`).
  구 prefix (`postgres.keiailab.io/...-finalizer`) 는 Deprecated legacy 상수로 보존 —
  cleanup 경로가 **both-recognize** (양쪽 인식/제거). 라이브 잔존 0 확인 전 legacy 제거 금지.
- **status**: `setCondition` 은 commons `pkg/status` 위임 (Ready / Progressing=True) +
  `observedGeneration` 의무 인자. 신규 condition 호출은 반드시 `cluster.Generation` 전달.
- **certmanager**: Certificate CR emit 은 commons `pkg/certmanager` (`BuildCertificate` /
  `ServiceSANs` / `CertificateGVK`). 단 Pooler AutoTLS 는 usages 가변(server/client 상이) +
  issuerRef.group 미명시 거동이라 spec 조립 자체구현 유지 — commons BuildCertificate 로
  바꾸면 운영 cert spec 변경 → 재발급 트리거. 변경 금지.
- **reconcilemetrics**: reconcile SLO trio 는 commons `pkg/reconcilemetrics.New("postgrescluster")`.
  **시계열 이름 절대 보존** — subsystem 변경 금지 (`postgrescluster_reconcile_total` /
  `_duration_seconds` / `_errors_total` 가 Grafana/PrometheusRule 계약). 자체 trio 재정의 =
  duplicate registration panic.

### 바이너리 commit 금지
`/pg-router` 등 로컬 빌드 바이너리는 `.gitignore` 등재 — repo 추적 금지
(12.6MB pg-router commit 사고 후속). 신규 바이너리 발생 시 `.gitignore` 에 추가.

### 컨테이너 빌드
`docker buildx build` (default 빌더) + `linux/amd64` 단일. 커스텀 빌더·멀티아키텍처 금지 (글로벌 §2.3).

### Webhook invariant 추가 시 cross-cut audit
새 invariant 추가 시 mongodb / valkey / postgres 3 operator 동시 점검 의무
(mongodb ADR-0016 패턴). PR 본문에 audit 표 포함.

## Build / Test / Gate

```bash
make test-unit        # 빠른 unit (envtest 불요)
make test             # unit + integration (envtest, manifests/generate 선행)
make gate             # lint + test + audit + validate — 릴리스 품질 게이트
make manifests generate   # CRD/RBAC/DeepCopy 재생성 (API 변경 시 의무)
make sync-crds        # config/crd/bases → charts crds 동기
make bundle VERSION=X.Y.Z  # OLM bundle 재생성
```

- CRD/RBAC 영향 변경 후 `make manifests generate` 미실행 = drift — 의무 실행.
- 테스트는 fake-client / envtest 기반 — 라이브 클러스터 의존 금지.

## Conventions

- Commit: Conventional Commits + 한국어 본문 허용 (`standards/commits.md`).
- 신규/수정 주석은 한국어 (코드 식별자 제외).
- 파일 라이선스 헤더: MIT (`Licensed under the MIT License. See the LICENSE file for details.`).
  Apache 헤더 발견 시 보고 후 교체 (pg-router 정리 선례).
- 의존성 갱신: `renovate.json` (mongodb-operator 와 동일 기준 — k8s.io 그룹 / major 분리).
- CI (`.gitlab-ci.yml`): golang 이미지 버전은 `go.mod` 의 go directive 와 정합 유지.

### 문서 작성 (`docs/`)

`docs/` 의 한국어 분석/작업 문서는 [`docs/DOCS_MAP.ko.md`](docs/DOCS_MAP.ko.md) 의 지침을 **구속력 있는 규칙**으로 따른다. Claude / Codex / Cursor 등 어떤 에이전트나 작업자라도 `docs/` 를 편집할 때 동일하게 준수한다.

- **주제별 SSOT 준수**: 같은 사실(검증 수치·CRLF 조치·E2E 명령 등)을 여러 문서에 복제하지 않는다. 본문은 DOCS_MAP §3 이 지정한 출처 1곳에만 두고, 나머지는 링크한다.
- **계층 유지**: 요약은 `PROJECT_OVERVIEW`, 상세 동작은 `FEATURE_DEEP_DIVE`. 개요 문서에 상세를 복붙하지 않는다.
- **보강 위치**: 새 내용을 어디에 쓸지는 DOCS_MAP §5 를 따른다(기능→OVERVIEW/DEEP_DIVE, 테스트→TEST_ANALYSIS/E2E_REPORT, 작업→WORK_HANDOFF+메모리, 환경→dev-setup).
- **지도 갱신 의무**: 새 분석/작업 문서를 추가하면 DOCS_MAP §1 표와 §2 관계도에 한 줄 추가한다.
- 미완 여부·현재 상태는 문서 단정 대신 코드/`git log` 로 확인한다(과거 "미구현" 오진 선례).
- **용어집 유지**: 각 분석 문서는 **마지막 장에 "용어집" 절**을 둔다. 용어 정의 SSOT 는 [`docs/GLOSSARY.ko.md`](docs/GLOSSARY.ko.md) — 각 문서는 등장 용어만 **그 정의를 그대로 발췌**(어디서 보든 동일) + 전체 링크. 새 용어는 GLOSSARY 에 먼저 추가하고 발췌 측을 맞춘다.
