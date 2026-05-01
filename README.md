# keiailab/postgres-operator

> **vanilla PostgreSQL 18 default + 5-인터페이스 Plugin SDK + stateless QueryRouter — Apache-2.0 청정 라이센스의 Go 오퍼레이터**

상용 제품 수준의 오픈소스 PostgreSQL 쿠버네티스 오퍼레이터를 목표로 합니다. 단일 PG HA 운영(HA, 백업/PITR, 풀러, 모니터링, 보안, 업그레이드)은 Crunchy PGO 수준 품질을 자체 코드로 제공하며, 그 위에 **Plugin SDK 기반 확장성**과 **stateless QueryRouter**를 차별화로 둡니다.

상태: **alpha (개발 중, 0.2.0-alpha)**. 외부 PR/Issue/Pillar 오너 컨트리뷰터 환영.

> **단일 PG HA만 필요한 경우** Crunchy PGO 또는 CloudNativePG 사용을 권장합니다. **Plugin SDK로 백업·exporter·extension·라우터·인증을 자체 확장하고 싶은 팀**, 또는 **stateless QueryRouter로 connection scaling이 필요한 팀**이 본 프로젝트의 청중입니다.

> **분산 SQL이 필요한 경우**: 0.2.0-alpha 이후 default stack은 vanilla PostgreSQL입니다. Citus 통합은 Beta 채널 opt-in (AGPL-3.0 §13 SaaS 의무를 사용자가 부담). 자체 sharding plugin은 [RFC 0005](docs/rfcs/0005-native-sharding-plugin.md) Phase 2A부터 단계적 구현. 자세한 결정 근거는 [ADR 0010](docs/adr/0010-license-and-sharding-strategy.md).

---

## 미션 — 3축

본 프로젝트는 다음 세 가지 일을 한다 ([ADR 0001 v2](docs/adr/0001-stateless-query-router-on-citus.md), [ADR 0004](docs/adr/0004-build-not-fork-or-layer.md), [ADR 0010](docs/adr/0010-license-and-sharding-strategy.md) 참조):

1. **PGO-class 풀스택 (기본 품질)** — HA, pgBackRest 백업/PITR, PgBouncer 풀러, pgMonitor 호환 관측, TLS/mTLS, in-place + blue/green 업그레이드, 멀티 K8s standby. **모두 자체 코드** (Pillar P1~P10, P14).
2. **Plugin SDK (차별화 1, 메타)** — `BackupPlugin`/`ExporterPlugin`/`ExtensionPlugin`/`RouterPlugin`/`AuthPlugin` 5종 + `ShardingPlugin` 1종 (RFC 0005, alpha-frozen). 새 백업 도구 추가 = 인터페이스 구현 1주. in-process + gRPC over UDS 두 모델 (Pillar P13).
3. **Stateless QueryRouter (차별화 2)** — `coordinator + workers[]` 토폴로지를 위한 stateless routing 계층. PgBouncer 통합, HPA 수평확장, PVC 없음 (Pillar P12).

---

## 왜 또 다른 PostgreSQL Operator인가

| 비교 대상 | 단일 PG HA | Plugin SDK | Stateless 라우터 | 라이선스/스택 |
|---|---|---|---|---|
| **Crunchy PGO** | ✅ Patroni, pgBackRest, pgMonitor — **단일 PG HA의 사실상 표준** | ✗ | ✗ | Apache 2.0 / Go |
| CloudNativePG | ✅ K8s API as DCS | 부분(외부 plugin) | ✗ | Apache 2.0 / Go |
| Zalando postgres-operator | ✅ Patroni | ✗ | ✗ | MIT / Go |
| Percona | ✅ | ✗ | ✗ | Apache 2.0 / Go |
| StackGres | ✅ | ✗ | ✗ | **AGPL-3.0** / Java |
| **keiailab/postgres-operator** | **✅ PGO-class 자체 구현** | **✅ 6종 인터페이스 (5+1)** | **✅** | **Apache 2.0 / Go** |

본 프로젝트의 차별화 무게중심은 **Plugin SDK + Stateless QueryRouter** 두 곳입니다. 단일 PG HA는 PGO 수준 품질을 약속하는 "기본 품질"이지 차별화 자체는 아닙니다. 자세한 결정 근거는 [ADR 0004](docs/adr/0004-build-not-fork-or-layer.md) 참조 (PGO fork·soft layer 옵션을 모두 거부한 사유 기록).

### 분산 SQL 입장 (0.2.0-alpha 이후, ADR 0010)

| 사용자 시나리오 | 권장 path |
|---|---|
| 단일 노드 PG로 충분 (대부분의 OLTP) | **vanilla PG18 default** — 별도 설정 없음 |
| 분산 SQL 필요, license 부담 수용 | Citus opt-in (`spec.sharding.backend: citus` + AGPL §13 의무 명시 수용) |
| 분산 SQL 필요, Apache-2.0 청정 유지 | RFC 0005 Phase 2A (postgres_fdw 기반 native sharding plugin) — 후속 |
| 분산 SQL + 즉시 production-grade | 외부 솔루션 (YugabyteDB, CockroachDB) 검토 권장 |

---

## 토폴로지

```
                      ┌──────────────────────────┐
                      │   App / Client           │
                      └───────────┬──────────────┘
                                  │ libpq (TLS)
                  ┌───────────────▼───────────────┐
                  │   QueryRouter (stateless)     │  무상태, HPA, PgBouncer 사이드카
                  │                               │  PVC 없음, 본 프로젝트의 핵심 차별화
                  └───────────────┬───────────────┘
                                  │
       ┌──────────────────────────┼─────────────────────────┐
       │                          │                         │
┌──────▼──────────┐       ┌───────▼────────┐        ┌───────▼────────┐
│  Coordinator    │       │  Worker pool A │        │  Worker pool B │
│  (HA RS)        │       │  (HA RS)       │        │  (HA RS)       │
│  DDL 게이트웨이 │       │  자체 election │        │  자체 election │
└─────────────────┘       └────────────────┘        └────────────────┘
   sync replication        streaming replication       streaming replication
```

| 계층 | 역할 | HA | 출처 |
|---|---|---|---|
| **QueryRouter** | 라우팅, PgBouncer 통합, HPA 수평확장 | 무상태 (Pod 재기동 무손실) | **본 프로젝트 신규** |
| **Coordinator** | DDL 게이트웨이, (Citus opt-in 시) `pg_dist_*` 권위 | streaming replication + lease election | 본 프로젝트 + Citus 표준 (opt-in) |
| **Worker** | Coordinator 백업 또는 (Citus opt-in 시) shard 보유 | streaming replication + lease election | 본 프로젝트 + Citus 표준 (opt-in) |

자세한 책임 경계는 [ADR 0003](docs/adr/0003-queryrouter-stateless-design.md) 참조.

---

## 핵심 기능 (계획)

- **선언적 토폴로지**: `PostgresCluster` 단일 CR로 Coordinator + Worker pools + QueryRouter 표현
- **HA**: K8s API as DCS (Patroni 미사용, [ADR 0002](docs/adr/0002-no-patroni-instance-manager.md))
- **백업/PITR**: pgBackRest 1차 통합, WAL-G/Barman 후속 (RFC 0004, Pillar P4)
- **백업 plugin**: pgBackRest / WAL-G / Barman 추상화 (BackupPlugin)
- **Sharding plugin** *(alpha-frozen 인터페이스, 0.2.0-alpha)*: ShardingPlugin 인터페이스 동결. 백엔드 구현은 RFC 0005 Phase 2A (postgres_fdw, Apache-2.0)부터 단계적 진행. 기존 Citus 백엔드는 Beta opt-in.
- **PG 16/17/18 vanilla**: Stable 채널. PG 18.3 latest 즉시 활용 가능.
- **PG × Citus 매트릭스 (Beta)**: AGPL opt-in 시 PG 16/17 + Citus 12.1/13.0 조합 지원.

---

## 지원 매트릭스 (0.2.0-alpha)

| PostgreSQL | Citus | 채널 | 라이센스 부담 |
|---|---|---|---|
| 16 vanilla | — | **Stable** | Apache-2.0 청정 |
| 17 vanilla | — | **Stable** | Apache-2.0 청정 |
| 18 vanilla | — | **Stable** (권장 default) | Apache-2.0 청정 |
| 16 | 12.1 / 13.0 | Beta | AGPL-3.0 §13 (사용자 부담) |
| 17 | 13.0 | Beta | AGPL-3.0 §13 (사용자 부담) |

`--feature-gates=PostgresEighteen=true` 는 **0.2.0-alpha 이후 불필요** (PG18 Stable 진입). 매트릭스 상세는 [`internal/version/matrix.go`](internal/version/matrix.go).

---

## Quickstart (Helm)

```bash
# 1. Helm chart 설치 (operator + RBAC + NetworkPolicy 보안 baseline 한 번에)
helm install my-operator ./charts/postgresql-operator \
  --namespace postgresql-operator-system --create-namespace

# 2. Sample PostgresCluster (vanilla PG18 — default)
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
```

NOTES.txt가 install 직후 다음 단계를 안내합니다 (라이센스 disclosure, HA replicas 권장 등).

### Citus opt-in (선택)

분산 SQL이 필요하고 AGPL-3.0 §13 SaaS 의무를 명시 수용하는 경우:

```bash
# CITUS_LIBPQ_DSN 활성화
helm upgrade my-operator ./charts/postgresql-operator \
  --reuse-values \
  --set citusLibPQ.dsn="host=<coord-svc-dns> port=5432 user=postgres dbname=postgres sslmode=require"

# CR에 Citus 활성화
cat <<EOF | kubectl apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata: {name: dist-cluster, namespace: default}
spec:
  deployment: development
  version: {postgres: "17", citus: "13.0"}  # Beta 채널 — AGPL §13 수용
  coordinator: {members: 1, storage: {size: 10Gi}}
  workers: [{name: pool-a, members: 1, storage: {size: 20Gi}}]
  routers: {replicas: 1}
  extensions: [{name: citus}]
EOF
```

자세한 정책: [ADR 0010](docs/adr/0010-license-and-sharding-strategy.md).

---

## 로드맵

자세한 계획은 [`docs/roadmap.md`](docs/roadmap.md), 설계 결정은 [`docs/adr/`](docs/adr/), 활성 RFC는 [`docs/rfcs/`](docs/rfcs/) 참조.

핵심 진행 항목:
- ✅ Pillar P0 (보안 baseline) 완료 (PSA restricted, NetworkPolicy default-deny, cascade-delete 회귀 테스트, LibPQExecutor)
- ✅ 0.2.0-alpha — vanilla PG18 default, Citus 격리, ADR 0010 + RFC 0005
- 🚧 RFC 0005 Phase 2A — postgres_fdw 기반 native sharding plugin (예상 2-3개월)
- 🚧 Pillar P11-M1 — dataplane image (`ghcr.io/keiailab/pg:18` vanilla) 빌드·게시
- 🚧 Pillar P4 — pgBackRest 백업/PITR (RFC 0004)

---

## Development (로컬 게이트)

본 프로젝트는 [ADR 0009](docs/adr/0009-no-github-actions-rfc-0002.md)에 따라 **GitHub Actions를 사용하지 않습니다** (글로벌 RFC 0002, 2026-04-29 사고 트리거). 모든 게이트(lint·test·audit·secrets)는 *로컬 4 계층*으로 일원화됐습니다.

### 1회 셋업

```bash
# 보안 도구 설치 (macOS)
brew install gitleaks trivy
# Linux는 apt/공식 binary 참조: https://aquasecurity.github.io/trivy/

# pre-commit hook 활성화 (1회 실행)
pip install pre-commit  # 또는 brew install pre-commit
pre-commit install --hook-type pre-commit --hook-type pre-push
```

### 4 계층 게이트

| 계층 | 시점 | 명령 | 차단 기준 |
|---|---|---|---|
| **L1 pre-commit** | `git commit` | `make lint` | lint error 1건 이상 |
| **L2 pre-push** | `git push` | `make test`, `make audit`, gitleaks, go.mod drift | error 1건 이상 |
| **L3 Makefile** | 개발자 수시 | `make validate`, `make test-e2e` (kind 기반), `make build` | 로컬 명시 검증 |
| **L4 PR review** | merge 전 | PR body의 "로컬 게이트 PASS" 증거 블록 | 증거 부재 시 머지 차단 |

### PR 머지 증거 블록 (필수)

PR 본문 또는 첫 commit 메시지에 다음 형식 포함 (`standards/ci.md §2`):

```
로컬 게이트 PASS:
- pre-commit run --all-files: PASS
- pre-push hooks: PASS
- make test: PASS
- make audit: PASS  (HIGH+CRITICAL = 0)
- make validate: PASS  (helm lint --strict + kustomize build + dist/install.yaml dry-run)
```

부재 시 리뷰어가 머지를 차단합니다. 우회(`--no-verify`)는 사고 보고 의무 (`incident-kb.md`).

---

## 기여하기

- 행동강령: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1)
- 기여 절차: [CONTRIBUTING.md](CONTRIBUTING.md) (DCO sign-off 필수)
- 거버넌스: [GOVERNANCE.md](GOVERNANCE.md) (RFC 절차 포함)
- 보안 신고: [SECURITY.md](SECURITY.md) (90일 비공개 윈도우)
- 메인테이너: [MAINTAINERS.md](MAINTAINERS.md)

`good first issue` 라벨이 붙은 이슈로 시작하시는 것을 권장합니다.

---

## 라이선스

[Apache License 2.0](LICENSE) © 2026 keiailab. 자세한 내용은 [NOTICE](NOTICE) 참조.

본 operator 코드는 Apache-2.0이며 Citus 소스를 포함하지 않습니다. Citus extension은 사용자가 명시적으로 opt-in 시 활성화되며, 활성화 사용자는 Citus의 AGPL-3.0 §13 SaaS 의무를 부담합니다 ([ADR 0010](docs/adr/0010-license-and-sharding-strategy.md)).
