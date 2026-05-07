# ROADMAP — postgres-operator

본 ROADMAP 은 *현재* 와 *다음 6 개월* 의 우선순위를 명시합니다. 기간 기반 deadline 은 의도적으로 회피하며, *기능 단위* 로 진행을 추적합니다 (글로벌 §workflow.md "시간 기반 로드맵 금지").

## 현재 (1.x 라인 — Active)

### 안정성 / 성숙도
- [x] PodSecurity restricted compliance — TestPodSecurityComplianceStatefulSet 회귀 가드 (mongodb-operator 와 동일 패턴)
- [x] CNPG-compatible Cluster CR — primary/standby promotion + failover quorum
- [x] Backup / Restore — Barman + WAL archive + S3
- [x] PgBouncer Pooler CR — read-write split + connection pooling
- [ ] Citus integration (사실상 sharding) — schema-shard distribution + reference table
- [ ] Logical replication — Publication / Subscription CR + drift 감지

### 운영 / 배포
- [x] Helm chart `keiailab.github.io/postgres-operator` publish
- [x] argos 클러스터 deploy — `platform-data-cnpg` ArgoCD app, `postgres-default` 3-replica cluster
- [x] 3-repo (mongodb / postgresql / valkey) governance 자산 정합 (CODE_OF_CONDUCT / GOVERNANCE / MAINTAINERS / **ROADMAP** 본 문서)
- [ ] Failover quorum 자동 — 2-node 시 quorum 유실 방지 가드 (현재 부분 — `failoverquorums` CRD 도입 완료)
- [ ] release-smoke-test.sh 강화 — mongodb-operator 패턴 (image / sbom / trivy / chart index / smoke)

### 관측 / 보안
- [x] ServiceMonitor — Prometheus 자동 노출 (cnpg metrics endpoint)
- [ ] Grafana 대시보드 (replication lag / WAL throughput / connection saturation)
- [ ] OpenTelemetry trace propagation — controller reconcile span
- [x] Image SBOM (SPDX) + trivy HIGH/CRITICAL fixed-only 스캔 (3-repo 표준)

## 다음 (2.x 라인 — Planning)

### 기능
- [ ] PostgreSQL 18 지원 — extension 호환 + bootstrap script 갱신
- [ ] Multi-region replication — async streaming + quorum 동기 commit 옵션
- [ ] Cross-cluster failover — `ClusterImageCatalog` + `Cluster` 조합으로 zero-downtime upgrade
- [ ] Online schema migration — pg_squeeze / pg_repack 기반 lock-free DDL
- [ ] Tenant-level row security — RLS policy 자동 생성 helper

### 아키텍처
- [ ] Controller v2 — reconcile fan-out 최적화 + work queue rate limiter 튜닝
- [ ] CRD `Cluster` v2 — schema 안정화 + conversion webhook (현재 v1)

## Non-Goals (의식적 비대상)

- **Multi-tenancy 격리** — namespace 단위 격리만 제공. 더 강한 격리는 별도 클러스터로 위임.
- **자체 시크릿 관리** — ESO (External Secrets Operator) + OpenBao 위임. operator 자체 시크릿 회전 로직은 *추가하지 않음*.
- **GitHub Actions** — RFC 0002 (글로벌) 영구 금지. 모든 게이트는 로컬 4 계층.
- **Oracle / MS SQL 호환 모드** — 본 operator 는 PostgreSQL 전용. 호환성은 별도 마이그레이션 도구 (MOLT) 위임.

## 변경 이력

| Date | Change | Refs |
|---|---|---|
| 2026-05-07 | 본 문서 신설 — 3-repo governance 자산 정합 | INC-2026-05-07 |
