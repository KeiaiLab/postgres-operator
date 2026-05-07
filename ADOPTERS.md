# Adopters of postgres-operator

본 문서는 `keiailab/postgres-operator` 를 운영 환경 또는 평가 환경에서 사용하는 조직/프로젝트의 *공개* 목록입니다. 자가 등록을 환영합니다 — PR 로 row 를 추가해주세요.

> 본 operator 는 현재 **0.3.0-alpha** 단계 — production deployment 는 별도 SLA 가이드 (ROADMAP.md) 검토 후 진행 권장.

## Production Users

운영 환경에서 postgres-operator 를 *production-grade SLA* 로 사용하는 사용자.

| 사용자 | 컴포넌트 | 사용 패턴 | 시작 버전 | 현재 버전 | 등재 일자 |
|---|---|---|---|---|---|
| _아직 없음 (alpha 단계)_ | — | G1 마일스톤 (single-shard production) 도달 후 추가 예정 | — | — | — |

## Evaluators

POC / 평가 / Day-0 환경에서 사용하는 사용자.

| 사용자 | 컴포넌트 | 단계 | 등재 일자 |
|---|---|---|---|
| **argos-platform-data** ([keiailab](https://github.com/keiailab)) | PostgresCluster (single-shard, PG18) | Day-0 배포. PG18 failover smoke RTO 21s 통과. HA replica / Backup-Restore drill 미통과 — production 전환 전 ROADMAP G1 게이트 필요. | 2026-05-07 |

## How to add yourself

PR 을 열어 위 표에 한 row 추가:

```markdown
| **<조직 / 프로젝트>** ([profile](<URL>)) | <컴포넌트 + 토폴로지> | <단계: Day-0 / G1 / G2 / G3> | <등재 일자 YYYY-MM-DD> |
```

비공개 또는 익명 등재를 원하시면 SECURITY.md 의 보안 채널로 알려주시면 maintainer 가 *organization-anonymized* row 로 등재합니다.

## ROADMAP 게이트

production 전환 단계는 ROADMAP.md 의 G1~G4 마일스톤 정의를 따릅니다:

- **G1** — Single-shard production (HA replica + failover drill + backup/restore/PITR + upgrade rollback)
- **G2** — Native multi-shard (router + autoSplit + cross-shard transactions)
- **G3** — pgBackRest 통합 GA
- **G4** — chaos-mesh 검증 통과 + 다중 organization adopters

## CNCF Sandbox Reference

본 ADOPTERS 목록은 CNCF graduation criteria 의 "≥1 public adopter (or evaluator with stated intent)" 요구사항을 충족하기 위한 공개 reference 로도 활용됩니다.
