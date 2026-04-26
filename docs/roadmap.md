---
title: "Roadmap"
---

# 로드맵

총 14개 Phase, 약 10개월. 자세한 명세는 [`/Users/phil/.claude/plans/citus-luminous-wilkes.md`](https://github.com/keiailab/postgres-operator) 또는 본 docs site의 `concepts/`를 참조.

| Phase | 기간 | 산출 |
|---|---|---|
| 0 | 1주 | 부트스트랩, LICENSE, ADR, CI |
| 1 | 4주 | `PostgresCluster` CRD + CSS/SS/Router 정적 부트스트랩 |
| 2 | 4주 | Instance Manager + 메타데이터 동기화 |
| 3 | 4주 | RS 단위 HA / Failover |
| 4 | 4주 | 백업 / PITR (pgBackRest) |
| 5 | 3주 | DistributedTable / ReferenceTable 선언적 관리 |
| 6 | 3주 | ShardBalancer (Mongo balancer 모델) |
| 7 | 3주 | ZoneSharding + Schema-based Sharding |
| 8 | 2주 | Pooler / PgUser / PgDatabase |
| 9 | 3주 | ClusterUpgrade (PG16/17/18) |
| 10 | 2주 | 관측성 (Grafana, PrometheusRule) |
| 11 | 2주 | 보안 / 거버넌스 (mTLS, NetworkPolicy) |
| 12 | 2주 | 확장 매트릭스 (pgvector, pg_cron) |
| 13 | 2주 | 문서 / 릴리즈 (v1.0.0 GA) |
| 14 | 4주 | 멀티리전 / DR (옵션) |

## 릴리즈

- v0.1.0-alpha — Phase 0~2 종료
- v0.5.0-beta — Phase 6 종료
- v0.9.0-rc — Phase 10 종료
- v1.0.0 GA — Phase 13 종료
