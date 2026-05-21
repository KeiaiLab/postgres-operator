<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator (한국어)

> **Kubernetes 용 Apache-2.0 PostgreSQL Operator — vanilla PG18+, license-clean, K8s-native auto-sharding 로드맵**

> English README: [README.md](README.md) — canonical / 정본

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go" alt="Go Version"/></a>
  <a href="https://www.postgresql.org/"><img src="https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql" alt="PostgreSQL"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes" alt="Kubernetes"/></a>
  <a href="https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator"><img src="https://img.shields.io/badge/ghcr.io-keiailab%2Fpostgres--operator-blue?logo=github" alt="Container Image"/></a>
  <a href="https://keiailab.github.io/postgres-operator"><img src="https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/keiailab/postgres-operator/main/charts/postgres-operator/Chart.yaml&label=helm%20v" alt="Helm Chart"/></a>
</p>

<p align="center">
  <a href="README.md">English</a> |
  <b>한국어</b> |
  <a href="README.ja.md">日本語</a> |
  <a href="README.zh.md">中文</a>
</p>

---

## 정체성

본 operator 는 upstream PostgreSQL 위에 *자체 구축 distributed SQL 레이어* 를 만든다. 외부 PostgreSQL operator runtime 을 embed 또는 wrap 하지 않으며, 코드 / CRD / reconciler / instance manager / router 모두 Apache-2.0 호환 조건 하에 본 저장소 안에서 직접 구현된다.

차별점:

- **100% PostgreSQL 18+ 호환** — application 코드 변경 없이 distribution 채택. PG extension / type / function 모두 그대로 사용 가능.
- **License-clean** — Apache-2.0 operator + BSD/Apache/MIT/PG-License 의존성만. SaaS 노출 시 copyleft 의무 0.
- **K8s-native auto-sharding 로드맵** — `ShardRange` CRD = source of truth, KEDA 트리거 auto-split, 7-step online resharding (cutover SLA target p99 < 500 ms).
- **Single-endpoint 로드맵** — application 은 `pg-router` Deployment 에 PostgreSQL wire protocol 로 연결, sharding 인지 불필요.

Plugin SDK 는 v0.x archive 에서 retired. 현 방향 = 좁게 정의된 internal module + 명시 CRD.

ADR 0001 (`docs/kb/adr/0001-self-built-distributed-sql.md`) 이 본 결정의 keystone.

## 아키텍처 (요약)

```
Application (libpq / JDBC / asyncpg)
    │  PostgreSQL wire protocol v3
pg-router  (stateless, HPA-scaled)
    │  - vindex 평가 (hash / range / consistent-hash / lookup)
    │  - single-shard fast path / multi-shard scatter-gather
    │  - distributed transaction coordinator (2PC + saga)
    ├──────┬──────┬──────┬──────
  Shard A  Shard B  Shard C  Shard D     (shard 별: 1 primary + N replica)
    │ instance manager (election + fencing + postgres 감독)
    │
operator manager
  - PostgresCluster reconciler
  - ShardRange reconciler  (source of truth)
  - ShardSplitJob reconciler (7-step workflow)
  - Rebalancer / Backup / Autoscaler glue
    │
  KEDA + Prometheus  (auto-split trigger: size + p99 + cpu)
```

상세: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## 기능

### 현재 출하 (v0.3.0-alpha.18)

helm chart 와 OperatorHub bundle 은 **8 owned CRD** 출하. CRD 상태는 production 클러스터에서 *오늘 reconcile 되는 범위* 반영:

| CRD | 역할 | 상태 |
|---|---|---|
| `PostgresCluster` | Shard-aware 토폴로지 (primary + standby + native sharding 로드맵) | ✅ 배포 가능 |
| `BackupJob` | 원자적 backup/restore Job (pgBackRest plugin) | ⚠️ controller 부분 |
| `ScheduledBackup` | Cron 기반 BackupJob 생성 (6-field schedule) | ⚠️ controller 부분 |
| `Pooler` | PgBouncer 연결 풀 레이어 | ⚠️ controller 부분 |
| `PostgresDatabase` | 선언적 database/schema/extension/FDW (ready-primary psql) | ⚠️ controller 부분 |
| `PostgresUser` | 선언적 role + password + membership (ready-primary psql) | ⚠️ controller 부분 |
| `ImageCatalog` | Namespace 범위 PostgreSQL runtime image 카탈로그 | ⚠️ rollout path |
| `ClusterImageCatalog` | Cluster 범위 공유 PostgreSQL runtime image 카탈로그 | ⚠️ rollout path |

Helm chart 추가: PrometheusRule + Grafana dashboard (Pooler overview + Cluster overview), restricted PSA SecurityContext, deny-by-default NetworkPolicy, cert-manager TLS 통합, OpenTelemetry-ready hook.

### 로드맵 (phase plan)

| Phase | Version | 핵심 deliverable |
|---|---|---|
| **P0** | 0.3.0 | Redesign reset (ADR/RFC 0001–0014, ARCHITECTURE.md, runbook 스캐폴딩) |
| **P1** | 0.4.0 | Single-shard production-ready (HA / backup / PITR drill / Lease election) |
| **P2** | 0.5.0 | pg-router + `ShardRange` CRD (수동 multi-shard 운영) |
| **P3** | 0.6.0 | vindex extension + scatter-gather + read replica autoscale |
| **P4** | 0.7.0 | `ShardSplitJob` 7-step (수동 online split trigger) |
| **P5** | 0.8.0 | KEDA auto-split + rebalancer (auto-sharding 도달) |
| **P6** | 0.9.0 | Distributed transaction (2PC + saga) + cross-shard JOIN |
| **P7** | **1.0.0** | 안정화 + chaos / benchmark + Artifact Hub verified |

phase 세부 (sub-task / SLO / ADR/RFC 참조): [`docs/roadmap.md`](docs/roadmap.md) + [`ROADMAP.md`](ROADMAP.md).

## License policy (ADR 0003)

외부 OSS 의존성은 *모두* 다음을 만족해야 허용:
- License: BSD-2/3 / Apache-2.0 / MIT / PostgreSQL License / ISC / MPL-2.0
- API: v1+ 안정성 commitment (12 개월 deprecation 정책)

**영구 금지**: AGPLv3 / BUSL / CSL / SSPL.

자동 강제: `scripts/check-license-policy.sh` (P0 후속, lefthook L2 pre-push + `go-licenses.yml` GitHub Actions check).

## Quickstart

```bash
# 1. operator + 8 CRD 설치 (helm chart 또는 OperatorHub bundle)
helm install postgres-operator charts/postgres-operator

# 2. quickstart PostgresCluster 적용
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml

# 3. Ready 대기
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m

# 4. (선택) 선언적 database/user 적용
kubectl apply -f config/samples/postgres_v1alpha1_postgresdatabase.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresuser.yaml

# 5. (선택) PgBouncer Pooler + cron backup 적용
kubectl apply -f config/samples/postgres_v1alpha1_pooler.yaml
kubectl apply -f config/samples/postgres_v1alpha1_scheduledbackup.yaml

# 6. 모니터링 활성화 (prometheus-operator 필요)
helm upgrade postgres-operator charts/postgres-operator \
  --reuse-values \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

운영 playbook: [`docs/operator-guide/deployment.md`](docs/operator-guide/deployment.md) + [`docs/operator-guide/pooler-monitoring.md`](docs/operator-guide/pooler-monitoring.md).

## Production readiness

**현재 상태 (0.3.0-alpha.18)**: reference Kubernetes 클러스터에서 ArgoCD Application `platform-data-postgres-operator` 가 `Synced/Healthy`, `PostgresCluster/postgres` 가 `Ready=True`.

GA 거리:
- **P1** — production-ready single-shard 는 HA Lease 분산 lock controller + BackupJob/ScheduledBackup live drill + PITR checksum drill + chaos-mesh failover suite 필요. sub-task tracking: `~/.claude/plans/2026-05-14-4-operators-100pct/P-D.md`.
- **P2** — multi-shard 는 `ShardRange` CRD + pg-router PoC ([`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md)) 필요.
- 현 alpha 는 *자체 backup/restore 검증 없이는* production 데이터 권장 **안 함**.

## 알려진 제약

- BackupJob / ScheduledBackup / Pooler / PostgresDatabase / PostgresUser controller 는 *부분 구현* — CRD 표면 + 핵심 reconcile 경로 출하, live drill 검증 (rotation / PITR / retain-policy) 은 phase 별 추적 중.
- ImageCatalog / ClusterImageCatalog rollout-drift 측정은 StatefulSet annotation 레이어 구현. production rollout SLA 미인증.
- Sharding subsystem (`ShardRange`, `pg-router`, `ShardSplitJob`) 은 **설계만** — spec: [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md). runtime 코드 없음.
- 위 Phase 로드맵 = 다년간 horizon. 오늘 운영 범위 = single-shard HA 전용.

## Uninstall

```bash
# 1. CR 인스턴스 먼저 삭제 (finalizer 가 CRD 제거 차단)
kubectl delete postgrescluster --all -A
kubectl delete pooler --all -A
kubectl delete scheduledbackup --all -A

# 2. chart 제거
helm uninstall postgres-operator

# 3. CRD 제거 (선택; helm 은 cluster 상태 보존 위해 CRD 기본 유지)
kubectl delete crd postgresclusters.postgres.keiailab.com \
                  backupjobs.postgres.keiailab.com \
                  scheduledbackups.postgres.keiailab.com \
                  poolers.postgres.keiailab.com \
                  postgresdatabases.postgres.keiailab.com \
                  postgresusers.postgres.keiailab.com \
                  imagecatalogs.postgres.keiailab.com \
                  clusterimagecatalogs.postgres.keiailab.com
```

## Contributing

```bash
make lint test validate    # 로컬 4-layer L3 gate
make sync-crds              # config/crd/bases ↔ chart 동기 검증
make test-e2e PILLAR=p1     # Kind 클러스터 e2e
```

GitHub Actions 는 OSS 표준 suite (CI / scorecard / CodeQL / DCO / dependency-review / go-licenses / kube-linter / helm-install-test / stale) 를 실행. 로컬 pre-commit / pre-push hook 이 개발자 1차 gate, CI 는 수렴 check.

기여자 가이드: [`CONTRIBUTING.md`](CONTRIBUTING.md). 거버넌스 모델 (lazy consensus / 2/3 supermajority): [`GOVERNANCE.md`](GOVERNANCE.md). 행동 강령: [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## 문서

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — 단일 페이지 아키텍처 설명 (8 CRD surface + self-built distributed SQL + G0-G6 상태 + ADR cross-link)
- `docs/kb/adr/` — Architecture Decision Record (현재: 0001–0026)
- `docs/rfcs/` — RFC draft (현재: 0001–0007)
- `docs/operator-guide/` — Deployment / pooler-monitoring / community-operators-onboarding / HA
- `docs/runbooks/` — 운영 절차: ha / backup / restore / upgrade / security / migration (각 SLO target + verify command 보유)
- `docs/sharding/` — Sharding 아키텍처 spec (G3-G5)
- `docs/api-reference/` — CRD reference (자동 생성, 계획)
- `docs/tutorials/` — 단계별 사용자 가이드 (P1+ 계획)

## 취약점 신고

보안 신고는 *공개 issue 로 열지 마세요*. [`SECURITY.md`](SECURITY.md) 의 GitHub Security Advisory 비공개 채널 사용. 5 영업일 내 응답 + 고심각도 발견의 disclosure timeline 조율.

## 커뮤니티

- **Discussion**: [GitHub Discussions](https://github.com/keiailab/postgres-operator/discussions) — 사용 질문 / 기능 제안 / 운영 경험 공유.
- **Issue**: [GitHub Issues](https://github.com/keiailab/postgres-operator/issues) — 버그 + 기능 요청 (재현 가능 case 제출 권장; `question.yml` 템플릿 = Q&A 가이드).
- **거버넌스**: [`GOVERNANCE.md`](GOVERNANCE.md) — 결정 프로세스 (lazy consensus / 2/3 supermajority).
- **후원**: [`.github/FUNDING.yml`](.github/FUNDING.yml) — GitHub Sponsors 버튼.

## 라이선스

Apache-2.0. [`LICENSE`](LICENSE) 파일 참조.

## 메인테이너

[@phil](https://github.com/phil) — `eightynine01@gmail.com`. 메인테이너 명단: [`MAINTAINERS.md`](MAINTAINERS.md).

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a> ·
  <a href="https://github.com/keiailab/forgewise">forgewise</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
