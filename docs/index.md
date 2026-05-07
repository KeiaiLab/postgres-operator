---
title: "keiailab/postgres-operator"
description: "Apache-2.0 PostgreSQL Kubernetes Operator — 외부 backend 내장 없는 독립 신규 구현"
---

본 오퍼레이터는 vanilla PostgreSQL 18+ 위에 *자체 분산 SQL 레이어* 를 K8s native 로 구축하는 독립 신규 구현이다 (ADR 0001 keystone). PGO, Citus, Vitess, CloudNativePG 같은 외부 시스템의 설계는 참고할 수 있지만, 그 시스템을 제품에 내장하거나 wrapper 로 재포장하지 않는다. 외부 backend 의존 (AGPL/BUSL/CSL/SSPL) 은 영구 금지다 (ADR 0003).

5분 안에 클러스터를 띄워보고 싶다면 [Quickstart](/tutorials/quickstart) 로 이동하세요. 설계 결정의 *왜* 가 궁금하다면 [ADR 0001](/adr/0001-self-built-distributed-sql) 을 먼저 읽으세요.

## 주요 특징

- **선언적 PostgresCluster**: operator 가 StatefulSet, Service, instance RBAC, network policy 를 생성한다.
- **K8s lease 기반 HA 로드맵** (RFC 0003): Patroni 미사용. K8s API 를 DCS 로 사용한다.
- **자체 ShardRange 메타데이터 로드맵** (RFC 0002): K8s CRD 가 source of truth — 외부 KV 레이어나 Citus `pg_dist_node` 불필요.
- **Stateless QueryRouter 로드맵** (RFC 0004): HPA 수평확장, PgBouncer 통합, Pod 재기동 무손실 목표.
- **분산 트랜잭션 로드맵** (RFC 0005): 자체 2PC + saga — backend extension 무관.

## 현재 검증 상태

- `0.3.0-alpha.4` image/chart/SBOM publish 및 release-smoke 12 PASS / 0 FAIL 완료.
- argos `platform-data-postgres-operator` ArgoCD Application `Synced/Healthy`, controller Deployment `1/1` live 확인.
- argos `data` namespace 에 `PostgresCluster/argos-postgres` single-shard `Ready=True` 확인.
- `ghcr.io/keiailab/pg:18` public pull 전환 후 pull secret 없이 재기동 확인.
- HA replica, backup/restore drill, 장기 soak 는 아직 GA 조건으로 남아 있다.

## 문서 구조

- [`/architecture/`](/architecture/) — 시스템 설계 개요
- [`/adr/`](/adr/) — Architecture Decision Records (0001~0005)
- [`/rfcs/`](/rfcs/) — Phase 별 설계 RFC (0001~0005)
- [`/api-reference/`](/api-reference/) — CRD 스펙
- [`/runbooks/`](/runbooks/) — 운영 절차
- [`/tutorials/`](/tutorials/) — Getting started

## 라이선스

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
