---
title: "keiailab/postgres-operator"
description: "MongoDB Sharded Cluster 토폴로지를 차용한 Citus 분산 PostgreSQL Kubernetes Operator"
---

본 오퍼레이터는 PostgreSQL + Citus extension을 기반으로 분산 클러스터를 K8s에서 선언적으로 운영합니다. 토폴로지 모델은 MongoDB의 3계층(`mongos / config server RS / shard RS`)을 차용해 책임을 분리하고 운영 단순성을 달성합니다.

자세한 내용은 [Concepts › 3계층 토폴로지](/concepts/topology-mongo-shard-on-citus)를 참조하세요. 5분 안에 클러스터를 띄워보고 싶다면 [Quickstart](/tutorials/quickstart)로 이동하세요.

## 주요 특징

- **선언적 분산 토폴로지**: 단일 `PostgresCluster` CR로 CSS + SS[] + Router 표현
- **자동 메타데이터 동기화**: `pg_dist_node` ↔ K8s Endpoints drift 자동 복원
- **RS 단위 자동 failover**: K8s API as DCS, Patroni 미사용
- **PITR 정합성**: 분산 named restore point 강제
- **MongoDB 호환 운영 패턴**: zone-aware sharding, balancer window, schema-based sharding

## 라이선스

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
