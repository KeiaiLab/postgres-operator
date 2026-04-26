---
title: "keiailab/postgres-operator"
description: "Citus 분산 PostgreSQL을 K8s native로 만드는 오퍼레이터 — 핵심은 Stateless QueryRouter 계층"
---

본 오퍼레이터는 PostgreSQL + Citus extension을 기반으로 분산 클러스터를 K8s에서 선언적으로 운영합니다. Citus 표준 토폴로지(coordinator + workers)에 **stateless QueryRouter 계층**을 추가해 라우팅을 무상태로 수평확장 가능하게 만든 것이 본 프로젝트의 본질적 기여입니다.

자세한 내용은 [Concepts › 토폴로지](/concepts/topology-citus-plus-router)를 참조하세요. 5분 안에 클러스터를 띄워보고 싶다면 [Quickstart](/tutorials/quickstart)로 이동하세요.

## 주요 특징

- **선언적 분산 토폴로지**: 단일 `PostgresCluster` CR로 Coordinator + Worker pools + QueryRouter 표현
- **자동 메타데이터 동기화**: `pg_dist_node` ↔ K8s Endpoints drift 자동 복원
- **HA**: K8s API as DCS, Patroni 미사용 (CNPG 모델)
- **PITR 정합성**: 분산 named restore point 강제 (`citus_create_restore_point`)
- **Stateless QueryRouter**: HPA 수평확장, PgBouncer 통합, Pod 재기동 무손실

## 라이선스

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
