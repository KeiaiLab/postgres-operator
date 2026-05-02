---
title: "keiailab/postgresql-operator"
description: "K8s-native auto-sharding PostgreSQL operator — 자체 분산 SQL, Apache-2.0 단일 라이선스"
---

본 오퍼레이터는 vanilla PostgreSQL 18+ 위에 *자체 분산 SQL 레이어* 를 K8s native 로 구축한다 (ADR 0001 keystone). 외부 backend 의존 (AGPL/BUSL/CSL/SSPL) 은 영구 금지 (ADR 0003).

5분 안에 클러스터를 띄워보고 싶다면 [Quickstart](/tutorials/quickstart) 로 이동하세요. 설계 결정의 *왜* 가 궁금하다면 [ADR 0001](/adr/0001-self-built-distributed-sql) 을 먼저 읽으세요.

## 주요 특징

- **선언적 분산 토폴로지**: 단일 `PostgresCluster` CR 로 Coordinator + Worker pools + QueryRouter 표현 (RFC 0001 v2 schema)
- **자체 ShardRange 메타데이터** (RFC 0002): K8s CRD 가 source of truth — 외부 KV 레이어 (Cockroach Range, `pg_dist_node`) 불필요
- **K8s lease 기반 HA** (RFC 0003): Patroni 미사용. K8s API 가 DCS.
- **Stateless QueryRouter** (RFC 0004): HPA 수평확장, PgBouncer 통합, Pod 재기동 무손실
- **분산 트랜잭션** (RFC 0005): 자체 2PC + named restore point — backend extension 무관

## 문서 구조

- [`/architecture/`](/architecture/) — 시스템 설계 개요
- [`/adr/`](/adr/) — Architecture Decision Records (0001~0005)
- [`/rfcs/`](/rfcs/) — Phase 별 설계 RFC (0001~0005)
- [`/api-reference/`](/api-reference/) — CRD 스펙
- [`/runbooks/`](/runbooks/) — 운영 절차
- [`/tutorials/`](/tutorials/) — Getting started

## 라이선스

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
