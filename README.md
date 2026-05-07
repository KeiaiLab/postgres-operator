# postgres-operator

> **Apache-2.0 PostgreSQL Kubernetes Operator** — vanilla PG18+, license-clean, PGO-class 운영 품질을 목표로 하되 외부 operator/backend 를 fork, embed, wrapper 로 사용하지 않는 독립 신규 구현.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql)](https://www.postgresql.org/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io/)
[![Container Image](https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github)](https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator)
[![Helm Chart](https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v)](https://keiailab.github.io/postgres-operator)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/postgres-operator)](https://artifacthub.io/packages/helm/postgres-operator/postgres-operator)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/keiailab/postgres-operator/badge)](https://scorecard.dev/viewer/?uri=github.com/keiailab/postgres-operator)
[![GitHub Discussions](https://img.shields.io/github/discussions/keiailab/postgres-operator?label=discussions&logo=github)](https://github.com/keiailab/postgres-operator/discussions)

---

## 정체성

본 operator 는 PostgreSQL 위에 *자체 분산 SQL 레이어*를 구축하는 신규 서비스다. PGO, Citus, Vitess, CloudNativePG, Patroni, CockroachDB 같은 외부 시스템의 공개 설계와 운영 idiom 은 참고할 수 있지만, 그 시스템을 그대로 내장하거나 런타임 의존성으로 감싸서 제공하지 않는다. 코드, CRD, reconciler, instance manager, router 는 본 repo 에서 Apache-2.0 호환 방식으로 직접 구현한다.

차별화 가치:

- **PostgreSQL 18+ 100% 호환** — application 코드 변경 없이 분산 채택. 모든 PG extension / 타입 / 함수 사용 가능.
- **라이선스 청정** — Apache-2.0 operator + (BSD/Apache/MIT/PG License) 의존만. SaaS 노출에 의무 없음.
- **K8s-native auto-sharding 로드맵** — `ShardRange` CRD = source of truth, KEDA 기반 자동 split, 7-step online resharding (cutover SLA p99 < 500ms 목표).
- **단일 endpoint 로드맵** — application 은 `pg-router` Deployment 에 PG wire protocol 로 연결, 샤딩 인지 없이 동작.

PGO-class 는 *품질 기준* 이지 PGO fork 또는 PGO 내장을 의미하지 않는다. Citus-class 분산 기능도 Citus extension 을 포함한다는 뜻이 아니라, Citus 가 검증한 문제 영역을 PostgreSQL-compatible 신규 서비스로 재구현한다는 뜻이다. Plugin SDK 는 v0.x archive 의 폐기된 메시지이며, 현행 방향은 필요한 확장점을 좁게 설계한 내부 모듈과 명시적 CRD 로 관리한다.

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
helm install pgo charts/postgres-operator \
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

**현재 (0.3.0-alpha.4, 2026-05-08)**: argos 실제 Kubernetes 클러스터에서 ArgoCD Application `platform-data-postgres-operator` 가 `Synced/Healthy` 이고, `data` namespace 의 `platform-data-postgres-operator-controller-manager` Deployment 가 `1/1` 로 실행 중이다. live image 는 `ghcr.io/keiailab/postgres-operator:0.3.0-alpha.4` (`sha256:394ec5eb4aa09d316d957a3c751bb7283f21bfa71f19a9d2871ccbc7ec974f2f`) 이며 `PostgresCluster/argos-postgres` 는 `Ready=True` 로 확인됐다. 단, 이는 Day-0 alpha-deployable single-shard 배포 완료를 뜻하며 0.4.0 production-ready 로 쓰려면 HA replica, backup/restore drill, PITR, 장기 안정성 검증이 추가로 필요하다.

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

## 커뮤니티

- **Discussions**: [GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) — 사용 질문, 기능 아이디어, 운영 사례 공유
- **Issues**: [GitHub Issues](https://github.com/keiailab/postgres-operator/issues) — 버그 / 기능 요청 (재현 가능한 사례 우선)
- **보안 보고**: [SECURITY.md](SECURITY.md) — 취약점은 *비공개* 채널 (GitHub Security Advisory) 사용
- **거버넌스**: [GOVERNANCE.md](GOVERNANCE.md) — 의사결정 절차 (lazy consensus / 2/3 supermajority)

## 라이선스

Apache-2.0. `LICENSE` 파일 참조.

## 메인테이너

[@phil](https://github.com/phil) — `eightynine01@gmail.com`
