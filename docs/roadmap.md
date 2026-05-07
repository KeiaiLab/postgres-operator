---
title: "Roadmap"
description: "postgres-operator Gate 기반 로드맵"
---

# Roadmap

`postgres-operator` 는 외부 PostgreSQL operator 나 distributed SQL backend 를 내장하지 않는 독립 신규 구현이다. PGO-class, Citus-class 같은 표현은 품질과 문제 영역을 설명하기 위한 비교 기준이며, 특정 제품을 fork 하거나 runtime dependency 로 포함한다는 뜻이 아니다.

## 설계 원칙

| 원칙 | 의미 |
|---|---|
| PGO-class quality | HA, backup, restore, upgrade, observability, security UX를 상용 운영 기준으로 맞춘다. PGO 코드는 사용하지 않는다. |
| Citus-class problem coverage | shard placement, routing, rebalance, distributed transaction 문제를 분석한다. Citus extension은 포함하지 않는다. |
| Plugin SDK message 폐기 | v0.x archive 의 broad Plugin SDK 포지셔닝은 폐기됐다. 필요한 확장점만 좁게 설계한다. |
| Apache-2.0 clean room | 허용 라이선스 의존만 사용하고 금지 라이선스 코드는 복사, 번역, 포팅하지 않는다. |
| GitOps first | argos production 배포는 GitOps path 와 Helm chart dependency 로 재현 가능해야 한다. |

## 현재 상태

| 영역 | 상태 | 남은 작업 |
|---|---|---|
| Naming | `postgres-operator` 로 repo/chart/GitOps path 정렬 | archive 문서는 history 로 보존 |
| Release | `0.3.0-alpha.4` image/chart/SBOM publish 및 release-smoke 12/12 PASS | 1.0.0 GA 전환 불가 |
| Runtime image | `ghcr.io/keiailab/pg:18` public pull 검증 | multi-arch/runtime SBOM 보강 |
| Production cluster | `platform-data-postgres-operator` Synced/Healthy, controller `1/1`, `argos-postgres` Ready=True | HA replica, backup/restore, long soak |
| Fencing | PVC fence로 split-brain fail-fast | operator-driven recovery/runbook 자동화 |
| Backup | `BackupJob` CRD 존재 | 실제 backup/restore controller 구현 필요 |

## Gate Plan

| Gate | 사용자 관점 성공 기준 | Verify |
|---|---|---|
| G0 Day-0 | 사용자는 GitOps로 operator와 single-shard Postgres를 배포한다. | ArgoCD Synced/Healthy, Pod 1/1 Running, `psql select version()` |
| G1 HA DB | 사용자는 primary 장애 시 replica 승격과 재합류를 확인한다. | replica>=1, primary delete drill, RTO/RPO 기록 |
| G2 Backup/Restore | 사용자는 장애 전 백업으로 새 클러스터 또는 기존 클러스터를 복구한다. | backup artifact, restore job, 데이터 checksum |
| G3 Operability | 사용자는 alert, dashboard, pooler, TLS, upgrade runbook으로 운영한다. | PrometheusRule/Grafana/upgrade smoke |
| G4 Native Sharding | 사용자는 Citus 없이 shard range와 router로 수동 분산 배치를 운영한다. | `ShardRange` + router e2e |
| G5 Online Split | 사용자는 쓰기 중단을 짧게 제한하고 shard를 split한다. | `ShardSplitJob` 7-step e2e |
| G6 GA 1.0.0 | 사용자는 상용 운영 DB로 채택할 수 있다. | soak, chaos, restore rehearsal, security/release gate |

## 명시적 비대상

- PGO fork, CNPG wrapper, Patroni bundle, Citus extension bundle.
- 외부 시스템의 CRD를 그대로 재노출하는 compatibility shell.
- 금지 라이선스 프로젝트의 코드 복사, 번역, 포팅.
- 아직 검증되지 않은 HA/backup 기능을 1.0.0 또는 production-ready 로 표기.

## Archive Policy

`docs/**/_archive/v0.x/` 문서는 과거 판단의 증거로 보존한다. archive 안의 "PGO-class + Citus 1급 + Plugin SDK" 표현은 현행 메시지가 아니며, 새 구현과 문서는 ADR-0001, ADR-0003, 본 Roadmap 을 기준으로 한다.
