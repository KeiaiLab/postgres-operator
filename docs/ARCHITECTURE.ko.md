<p align="center">
  <a href="ARCHITECTURE.md">English</a> |
  <b>한국어</b> |
  <a href="ARCHITECTURE.ja.md">日本語</a> |
  <a href="ARCHITECTURE.zh.md">中文</a>
</p>

# ARCHITECTURE — postgres-operator (한국어)

> 단일 페이지 아키텍처 설명. CRD 표면 / Gate / reconcile 패턴이 바뀔 때 갱신.

> English 원본: [ARCHITECTURE.md](ARCHITECTURE.md) — canonical / 정본

## 개요

- **목적**: Apache-2.0 PostgreSQL Kubernetes Operator — *자체 구축* 코드로 production-grade 운영 품질 + distributed SQL 제공. 외부 PostgreSQL operator fork 또는 wrapper 아님.
- **범위**: K8s 위의 vanilla PostgreSQL 18+, single-shard HA → sharding → online resharding → distributed SQL → GA.
- **안정성 단계**: v0.3.0-alpha.16 (G0 100% / G1 81% / G2 72% / G3 37% / G4-G5 0% / G6 12%)
- **License**: Apache-2.0 (의존성: BSD/Apache/MIT/PG-License 만 — SaaS 노출 시 copyleft 의무 0)
- **Module path**: `github.com/keiailab/postgres-operator`

## CRD 표면 (8 CRD)

| CRD | apiVersion | Scope | 설명 |
|---|---|---|---|
| `PostgresCluster` | `postgres.keiailab.com/v1alpha1` | Namespaced | Primary HA controller — StatefulSet + WAL + failover |
| `BackupJob` | `postgres.keiailab.com/v1alpha1` | Namespaced | pgBackRest backup / restore / PITR |
| `ScheduledBackup` | `postgres.keiailab.com/v1alpha1` | Namespaced | Cron 기반 BackupJob 트리거 |
| `PostgresDatabase` | `postgres.keiailab.com/v1alpha1` | Namespaced | 선언적 database + schema + privilege |
| `PostgresUser` | `postgres.keiailab.com/v1alpha1` | Namespaced | 선언적 role + password rotation |
| `Pooler` | `postgres.keiailab.com/v1alpha1` | Namespaced | PgBouncer 연결 풀 |
| `ImageCatalog` / `ClusterImageCatalog` | `postgres.keiailab.com/v1alpha1` | Namespaced / Cluster | 선언적 업그레이드용 이미지 catalog |
| (G3+ 계획) `ShardRange` / `ShardSplitJob` | — | — | Sharding 메타데이터 + 7-step online resharding |

## 자체 구축 distributed SQL 아키텍처

```
Application (libpq / JDBC / asyncpg)
    │ PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │ - vindex 평가 (hash / range / consistent-hash / lookup)
    │ - single-shard fast path / multi-shard scatter-gather
    │ - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (shard 별: 1 primary + N replica)
    │ instance manager (election + fencing + postgres 감독)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth — G3+)
  - ShardSplitJob reconciler (7-step workflow — G4+)
  - Rebalancer / Backup / Autoscaler glue
```

ADR-0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 이 keystone — *외부 operator embedding 없음*.

## RBAC 범위

- ClusterRole: CRD watch + cert-manager Certificate + ImageCatalog cluster-scope
- Role (ns 별): StatefulSet / Service / Secret / ConfigMap / PVC / PDB / NetworkPolicy / Job / PgBouncer
- ServiceAccount: `postgres-operator`

## operator-commons import 표면

채택: **5/8 (63%)**.

| 패키지 | 상태 | 사용 |
|---|---|---|
| `pkg/security` | ✅ | restricted PSA (it8) |
| `pkg/version` | ⏳ | 로컬 `version.Combo` 가 commons.MustList 보다 풍부 — delegation 보류 |
| `pkg/labels` | ✅ | 권장 labels (it28) |
| `pkg/monitoring` | ⏳ | ServiceMonitor 로컬 구현 — commons delegation 보류 |
| `pkg/networkpolicy` | ⏳ | NetworkPolicy 로컬 구현 — commons delegation 보류 |
| `pkg/webhook` | ✅ | Validation 헬퍼 (it34) |
| `pkg/finalizer` | ✅ | `Add` / `Remove` / `Has` |
| `pkg/status` | ✅ | Condition reason |

## Gate plan (G0 → G6)

| Gate | 목표 | 상태 |
|---|---|---|
| G0 | Day-0 deployment | **100%** (7/7) |
| G1 | Single-shard HA (failover + sync repl + PVC fence + lease) | 81% (HA election Lease 보류) |
| G2 | 운영 품질 (TLS auto / PrometheusRule / Grafana / Pooler / RBAC / ImageCatalog / Hibernation) | 72% (live drill 보류) |
| G3 | Sharding foundation (`ShardRange` CRD + pg-router PoC + 메타데이터) | 37% |
| G4 | Online resharding (`ShardSplitJob` 7-step) | 0% |
| G5 | Distributed SQL (scatter-gather + 2PC/saga + isolation + benchmark) | 0% |
| G6 | 1.0.0 GA (soak ≥7d + chaos + SBOM + cosign + 6 runbook) | 12% |

100% 까지의 계획 (G6): `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md` (59 sub-task).

## 테스트 레이어

| 레이어 | 위치 | 커버리지 |
|---|---|---|
| Unit | `internal/**/_test.go`, `api/**/_test.go` | `make test-unit` |
| Integration (envtest) | `test/integration/` | `make test-integration` |
| E2E (kind) | `test/e2e/{*,pg,failover,sharding}/` | `make test-e2e*` |
| Bench | `test/bench/` (G5) | sysbench / pgbench |
| Scorecard | `bundle/tests/scorecard/` | OLM v1alpha3 |

## 빌드 / 배포

- 컨테이너 이미지: `ghcr.io/keiailab/postgres-operator:v0.3.0-alpha.16`
- Helm chart: `charts/postgres-operator/` (`keiailab.github.io/postgres-operator`)
- OLM bundle: `bundle/`
- ArtifactHub: `keiailab-postgres-operator`
- pg-router: 별 binary `cmd/pg-router/` (G3+)

## 보안 공급망

- OpenSSF Scorecard 활성
- License audit allowlist (BSD/Apache/MIT/PG-License 만)
- ADR-0009 가 legacy GitHub Actions 금지 강제 (RFC-0002)
- Lefthook DCO + Conventional Commits + lint gate

## ADR cross-link (24 ADR)

Notable:
- ADR-0001: 자체 구축 distributed SQL (keystone)
- ADR-0006: GitOps deploy overlay 도입 (3-repo 정합)
- ADR-0007: Hook tooling — lefthook 대신 pre-commit
- ADR-0009: webhook validate — accumulate-errors
- ADR-0013: OperatorHub.io bundle scaffold cross-cut
- ADR-0014: community-operators upstream sync 자동화
- ADR-0019: GitHub Actions 유지 (operator family v2.0 dual-track)
- ADR-0022: GHA narrow exception — 3 workflow (helm-publish + release + scorecard)
- ADR-0023: v3.x-stable baseline 인정
- ADR-0024: lefthook pre-push incremental lint + envtest
- ADR-0025: Repmgr / PgBouncer / Barman 통합 (bitnami parity)
- ADR-0026: OperatorHub.io 자동 sync

전체 목록: `docs/kb/adr/INDEX.md`.

## Non-goals

- ❌ PostgreSQL < 18 (`pkg/version` 결정상 v18 최소)
- ❌ 외부 PostgreSQL operator 재패키징 (Apache-2.0 경계)
- ❌ 외부 sharding extension 동봉 (문제 공간을 *재구현*)
- ❌ 외부 HA agent runtime 의존 (자체 instance manager)
- ❌ Copyleft 의존성 (license-clean Apache-2.0 만)
- ❌ Plugin SDK (v0.x archive 에서 retired — 명시 CRD 로 대체)

## 참조

- `README.md` — 정체성 + 아키텍처 요약 + 기능
- `ROADMAP.md` — Gate matrix checkbox
- `CHANGELOG.md`
- `ADOPTERS.md`
- `CONTRIBUTING.md` + `MAINTAINERS.md`
- `GOVERNANCE.md`
- `SUPPORT.md`
- `AGENTS.md`
- `docs/kb/adr/INDEX.md` — 24 ADR

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
