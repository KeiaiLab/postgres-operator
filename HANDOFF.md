# HANDOFF — postgresql-operator

> 다음 세션이 *컨버세이션 컨텍스트 없이* 재개 가능해야 한다. 시작 의식: 본 파일 → `TASKS.md` → 마지막 commit log 순서로 읽는다.

## 현재 상태 (2026-05-02)

- **마지막 commit (HEAD)**: df1f2e1 `feat!: 0.3.0-alpha redesign reset — 자체 분산 SQL, 의존 제로`
- **브랜치**: main (릴리스 게이트는 로컬, RFC 0002 archive 적용)
- **현재 phase**: **P0 완료**. T01~T14 모두 100%. P1 (0.4.0 single-shard production-ready) 진입 대기.
- **검증 결과**: make lint 0 issues / make test ALL PASS / make validate PASS / helm lint --strict PASS / runtime artifact citus refs 0건.
- **이전 미커밋 변경 (이미 commit 됨)**:
  - `docs/adr/_archive/v0.x/` 디렉토리 신설 + 기존 ADR 0001~0010 이동 (`git mv`, history 보존)
  - `docs/rfcs/_archive/v0.x/` 디렉토리 신설 + 기존 RFC 0001~0005 이동
  - 신규 `docs/adr/0001-self-built-distributed-sql.md` ~ `0005-versioning-and-channels.md` (5 파일, 약 380 줄)
  - 신규 `docs/rfcs/0001-postgrescluster-crd-v2.md` ~ `0005-distributed-transactions.md` (5 파일, 약 1635 줄)
  - `README.md` 재작성 (자체 분산 SQL 정체성)
  - `TASKS.md` 재작성 (P0 작업 표)
  - `HANDOFF.md` 본 파일
  - `CHANGELOG.md` `## [0.3.0-alpha]` 항목 추가 (breaking change 명시)

## 의사결정 기록 (본 세션 누적)

1. **2026-05-02 사용자 결정**: 풀 자체 분산 SQL (옵션 C) + 모든 외부 backend 의존 제거 + Single chart + flags. ADR-0001 (신규) 가 keystone.
2. ADR-0010 (legacy, Citus AGPL 격리) 와 RFC-0005 (legacy, native sharding plugin) 는 supersede 됨 (`_archive/v0.x/`).
3. 6+년 timeline (P0~P7) 정직 공시. 각 phase 끝에 production-deployable 보장.
4. 외부 의존 정책 (ADR-0003): BSD/Apache/MIT/PG License + v1+ stability 만. AGPL/BUSL/CSL/SSPL 영구 금지.
5. CRD 라이프사이클은 operator manager 가 소유 (ADR-0004) — Helm `crds/` 폐기 (T10 다음 세션).

## 다음 단계 (P1 진입 — 0.4.0 single-shard production-ready)

P0 완료 (commit df1f2e1). 다음 세션은 P1 부터 시작. TASKS.md §"다음 Phase 미리보기" 의 F01~F05 가 진입점:

1. **F01 — RFC 0001 PostgresCluster CRD v2 실장** (kubebuilder/CEL marker, Sharding spec 재정의). 본 commit 의 placeholder ShardingSpec 을 RFC 0001 정의로 교체.
2. **F02 — instance manager P2-T3+** (postgres 프로세스 supervise + promote/demote 실장). `cmd/instance/main.go` 의 todo 주석 ("supervise postgres process + 분산 SQL metadata 갱신 (RFC 0002 후속)") 가 진입점.
3. **F03 — RFC 0003 election / fencing 인터페이스 위에 실장 완성**.
4. **F04 — pgBackRest 통합** (`internal/controller/backup/`).
5. **F05 — single-shard E2E 테스트 시나리오 재설계** (chaos-mesh primary kill → failover < 30s).

후속 정리 작업 (별도 PR 권장):
- `docs/roadmap.md` 새 8-Phase (P0~P7) 로 본문 재작성 — 현재 deprecated stub.
- `docs/concepts/`, `docs/how-to/`, `docs/reference/` 의 Citus 의존 표현 정리 (ADR 본문은 의도적 보존).
- TASKS.md "Phase: P1" 섹션 신규 작성 + F01~F05 분해.

## 차단점

- 없음. P1 진입은 RFC 0001 CRD v2 정의를 따라 mechanical 진행 가능.

## 근거 링크

- 플랜: `/Users/phil/.claude/plans/eager-wobbling-torvalds.md` (사용자 승인 2026-05-02)
- 비교 분석: `/Users/phil/.claude/plans/eager-wobbling-torvalds-agent-a335628aa15778167.md`
- 신규 keystone ADR: `docs/adr/0001-self-built-distributed-sql.md`
- 신규 RFC 묶음: `docs/rfcs/0001-postgrescluster-crd-v2.md` ~ `0005-distributed-transactions.md`
- standards 적용: `~/Documents/ai-dev/standards/principles.md` §1, §2, §3, §4
- standards CI: `~/Documents/ai-dev/standards/ci.md` (4-layer 로컬 게이트)
- 폐기된 결정: `docs/adr/_archive/v0.x/0010-license-and-sharding-strategy.md`, `docs/rfcs/_archive/v0.x/0005-native-sharding-plugin.md`
