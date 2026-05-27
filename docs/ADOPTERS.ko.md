<p align="center">
  <a href="ADOPTERS.md">English</a> |
  <b>한국어</b> |
  <a href="ADOPTERS.ja.md">日本語</a> |
  <a href="ADOPTERS.zh.md">中文</a>
</p>

# postgres-operator 채택자 (한국어)

> 영문 원본: [ADOPTERS.md](ADOPTERS.md) — canonical / 정본

본 문서는 `keiailab/postgres-operator` 를 production 또는 evaluation 환경에서 사용하는 조직 및 프로젝트의 *공개* 목록이다. 자발적 등록 환영 — 행을 추가하려면 PR 을 열어주세요.

> 본 operator 는 현재 **0.4.0-beta** 단계. production 배포 시 우선 SLA 가이드 (ROADMAP.md) 를 참고하세요.

## Production 사용자

postgres-operator 를 *production-grade SLA* 로 운영하는 사용자.

| 사용자 | 컴포넌트 | 사용 패턴 | 첫 버전 | 현재 버전 | 등록일 |
|---|---|---|---|---|---|
| _아직 없음 (alpha 단계)_ | — | G1 마일스톤 (single-shard production) 이후 추가 예정. | — | — | — |

## Evaluator (평가자)

operator 를 PoC / evaluation / Day-0 환경에서 운영하는 사용자.

| 사용자 | 컴포넌트 | 단계 | 등록일 |
|---|---|---|---|
| **platform-data** ([keiailab](https://github.com/keiailab)) | PostgresCluster (single-shard, PG18) | Day-0 배포. PG18 failover smoke PASS, RTO 21 s. HA replicas / backup-restore drill 는 아직 미통과 — production 전 ROADMAP G1 필요. | 2026-05-07 |

## 등록 방법

PR 을 열고 위 표에 행을 추가:

```markdown
| **<조직 / 프로젝트>** ([profile](<URL>)) | <컴포넌트 + 토폴로지> | <단계: Day-0 / G1 / G2 / G3> | <등록일 YYYY-MM-DD> |
```

비공개 또는 익명 등록을 원하면 SECURITY.md 채널을 통해 문의하면, 메인테이너가 *조직 익명화* 행을 추가한다.

## ROADMAP gate

Production 전환 단계는 ROADMAP.md 에 정의된 G1~G4 마일스톤을 따른다:

- **G1** — Single-shard production (HA replica + failover drill + backup/restore/PITR + upgrade rollback).
- **G2** — Native multi-shard (router + auto-split + cross-shard transactions).
- **G3** — pgBackRest 통합 GA.
- **G4** — chaos-mesh 검증 + 복수 조직 채택자.

## CNCF Sandbox 참조

본 ADOPTERS 목록은 CNCF graduation 기준 "≥ 1 public adopter (또는 명시된 의향을 가진 evaluator)" 을 충족하는 공개 참조로도 활용된다.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
