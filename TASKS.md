# TASKS — postgresql-operator

> 작업 ID: F=기능 / I=개선 / B=버그 / T=그 외. 부여 후 재사용 금지.
> 단계: 설계(10%) / 구현(60%) / 테스트(90%) / 완료(100%).

## 현재 Phase: **P0** (0.3.0-alpha — 재설계 정리)

목표: 자체 분산 SQL 정체성 정착. 기존 Citus 의존 코드 폐기. ADR/RFC 0001~0005 작성. 다음 phase 진입 전 lint/test/validate PASS 보장.

## 작업 표

| ID  | 기능명 / 요약 | 단계 | 완성도 | 의존 | 영향 | 비고 |
|-----|---------------|------|--------|------|------|------|
| T01 | 기존 ADR 0001-0010 → `docs/adr/_archive/v0.x/` 이동 (git mv) | 완료 | 100% | - | T03 | 2026-05-02 |
| T02 | 기존 RFC 0001-0005 → `docs/rfcs/_archive/v0.x/` 이동 | 완료 | 100% | - | T04 | 2026-05-02 |
| T03 | 신규 ADR 0001 (self-built distributed SQL) — keystone | 완료 | 100% | T01 | 모든 후속 | 2026-05-02 |
| T04 | 신규 ADR 0002-0005 (helm/license/crd/versioning) | 완료 | 100% | T01,T03 | T11,T12 | 2026-05-02 |
| T05 | 신규 RFC 0001-0005 (CRD v2 / ShardRange / split / router / dtxn) | 완료 | 100% | T02,T03 | F01~F05 | 2026-05-02 |
| T06 | README 재작성 (자체 분산 SQL 정체성, 8 phase) | 완료 | 100% | T03 | - | 2026-05-02 |
| T07 | TASKS.md 갱신 (본 파일) | 완료 | 100% | T06 | - | 2026-05-02 |
| T08 | HANDOFF.md 갱신 (다음 세션 진입점) | 완료 | 100% | T07 | - | 2026-05-02 |
| T09 | CHANGELOG.md `## [0.3.0]` 항목 추가 | 완료 | 100% | T03,T04,T05 | - | breaking change 명시 |
| T10 | citus 코드/스키마 Full Removal — `internal/citus/` + `internal/plugin/extension/citus/` 삭제 + 14개 비-test 파일 + CRD 스키마 + Helm chart citus 잔재 제거 | 완료 | 100% | T03 | I01 | 2026-05-02 (Full removal 결정) |
| T11 | `charts/postgresql-operator/Chart.yaml` description 갱신 + version bump 0.3.0-alpha + values/README 정리 | 완료 | 100% | T04 | - | 2026-05-02 |
| T12 | `docs/index.md` + `docs/mint.json` 새 구조 반영 + `docs/roadmap.md` deprecated stub | 완료 | 100% | T05 | - | 2026-05-02 |
| T13 | `make lint test validate` PASS + `helm lint --strict` PASS | 완료 | 100% | T07~T12 | - | 2026-05-02 lint 0 issues / test PASS / validate PASS |
| T14 | git commit `feat!: 0.3.0 redesign reset` | 진행 | 60% | T13 | - | 다음 단계 |

## 차단됨

(없음 — 모든 작업이 의존 그래프 내 진행 가능)

## 다음 Phase 미리보기

**P1 (0.4.0 — single-shard production-ready, ~6개월)**:
- F01 — RFC 0001 PostgresCluster CRD v2 실장 (Sharding spec 재정의, kubebuilder/CEL marker)
- F02 — instance manager P2-T3+ (postgres 프로세스 supervise + promote/demote 실장)
- F03 — RFC 0003 election / fencing 인터페이스 위에 실장 완성
- F04 — pgBackRest 통합 (backup controller — `internal/controller/backup/`)
- F05 — single-shard E2E 테스트 시나리오 재설계 (chaos-mesh primary kill → failover < 30s)

## 영향도 추적

T03 (ADR 0001) 변경 시 → 모든 신규 ADR/RFC 의 References 섹션 점검.
T05 (RFC 0001~0005) 변경 시 → P1~P6 작업 분해 재검토.
T10 (Citus 코드 삭제) 변경 시 → `cmd/instance/main.go`, `internal/controller/postgrescluster_controller.go` import 사용처 확인 + e2e Pillar=p1 라벨 회귀.
