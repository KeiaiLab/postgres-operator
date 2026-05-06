# postgresql-operator

> **K8s-native auto-sharding PostgreSQL operator** — vanilla PG18+, license-clean (Apache-2.0), zero AGPL/BUSL/CSL/SSPL dependency.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql)](https://www.postgresql.org/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io/)
[![Container Image](https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github)](https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator)
[![Helm Chart](https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgresql-operator/Chart.yaml&label=helm%20v)](https://keiailab.github.io/postgres-operator)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/postgresql-operator)](https://artifacthub.io/packages/helm/postgresql-operator/postgresql-operator)

---

## 정체성

본 operator 는 PostgreSQL 위에 *자체 분산 SQL 레이어*를 구축한다. Citus / CloudNativePG / Patroni / CockroachDB 코드 의존을 *영구히* 두지 않는다. 차별화 가치:

- **PostgreSQL 18+ 100% 호환** — application 코드 변경 없이 분산 채택. 모든 PG extension / 타입 / 함수 사용 가능.
- **라이선스 청정** — Apache-2.0 operator + (BSD/Apache/MIT/PG License) 의존만. SaaS 노출에 의무 없음.
- **K8s-native auto-sharding** — `ShardRange` CRD = source of truth, KEDA 기반 자동 split, 7-step online resharding (cutover SLA p99 < 500ms).
- **단일 endpoint** — application 은 `pg-router` Deployment 에 PG wire protocol 로 연결, 샤딩 인지 없이 동작.

ADR 0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 가 본 결정의 keystone 이다.

## 아키텍처 (요약)

```
Application (libpq / JDBC / asyncpg)
    │  PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │  - vindex 평가 (hash / range / consistent-hash / lookup)
    │  - single-shard fast path / multi-shard scatter-gather
    │  - 분산 트랜잭션 coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (per-shard: 1 primary + N replica)
    │ instance manager (election + fencing + supervise postgres)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth)
  - ShardSplitJob reconciler (7-step workflow)
  - Rebalancer / Backup / Autoscaler glue
    │
  KEDA + Prometheus  (auto-split trigger: size + p99 + cpu)
```

상세: `docs/architecture/overview.md` (P0 신설 예정).

## Phase 로드맵

| Phase | 버전 | 핵심 산출물 | 추정 기간 |
|---|---|---|---|
| **P0** | 0.3.0 | 재설계 정리 (ADR/RFC 0001~0005, README, 코드 폐기) | 2개월 |
| **P1** | 0.4.0 | Single-shard production-ready (HA/backup/PITR) | 6개월 |
| **P2** | 0.5.0 | pg-router + ShardRange CRD (multi-shard 수동 운영) | 10개월 |
| **P3** | 0.6.0 | vindex 확장 + scatter-gather + read replica autoscale | 8개월 |
| **P4** | 0.7.0 | ShardSplitJob 7-step (online split 수동 트리거) | 12개월 |
| **P5** | 0.8.0 | KEDA 자동 split + rebalancer (자동 샤딩 도달) | 8개월 |
| **P6** | 0.9.0 | 분산 트랜잭션 (2PC + saga) + cross-shard JOIN | 12개월 |
| **P7** | **1.0.0** | 안정화 + chaos / benchmark + ArtifactHub verified | 6개월 |

**합계 ~64개월 (5.3년)** — 1인 50% 가동 추정. 각 phase 끝에 *production-deployable* 보장.

## 라이선스 정책 (ADR 0003)

외부 OSS 의존은 다음 *모두* 충족 시만 허용:
- License: BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API: v1+ stability commitment (12개월 deprecation 정책)

**영구 금지**: AGPLv3 / BUSL / CSL / SSPL.

자동 검증: `scripts/check-license-policy.sh` (lefthook L2 pre-push hook 으로 강제, P0 후속 작업).

## 빠른 시작 (P1 도달 후)

```bash
helm install pgo charts/postgresql-operator \
  --set router.enabled=false \
  --set autoscale.keda.enabled=false

kubectl apply -f - <<'YAML'
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: demo
spec:
  postgresVersion: "18"
  shardingMode: none      # P1: single-shard
  shards:
    initialCount: 1
    storage: { size: 10Gi }
    replicas: 1
YAML
```

**현재 (0.3.0-alpha)**: 재설계 정리 단계. P1 (single-shard) 도달 시 본 명령이 동작한다.

## 개발 (Contributing)

```bash
make lint test validate    # 4-layer 게이트 L3 (로컬)
make sync-crds              # config/crd/bases ↔ chart 동기화 검증
make test-e2e PILLAR=p1     # Kind cluster 기반 e2e
```

GitHub Actions 영구 금지 (RFC 0002 archive). 모든 게이트는 로컬 (pre-commit / pre-push / Makefile / PR 리뷰).

자세한 기여 가이드: `CONTRIBUTING.md`, 운영 규약: `GOVERNANCE.md`, 행동 강령: `CODE_OF_CONDUCT.md`.

## 문서

- `docs/architecture/` — 분산 시스템 설계 (overview / routing-layer / sharding-model / consistency / ha-and-fencing) — *P0 신설 예정*
- `docs/kb/adr/` — Architecture Decision Records (현재 0001~0005, archive 는 `_archive/v0.x/`)
- `docs/rfcs/` — RFC drafts (현재 0001~0005)
- `docs/api-reference/` — CRD reference (자동 생성 예정)
- `docs/runbooks/` — 운영 절차 (split / failover / backup, P4+ 작성)
- `docs/tutorials/` — 단계별 사용 가이드 (P1+ 작성)

## 라이선스

Apache-2.0. `LICENSE` 파일 참조.

## 메인테이너

[@phil](https://github.com/phil) — `eightynine01@gmail.com`
