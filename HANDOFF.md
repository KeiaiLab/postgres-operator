# HANDOFF — postgresql-operator

> 다음 세션이 *컨버세이션 컨텍스트 없이* 재개 가능해야 한다. 시작 의식: 본 파일 → `TASKS.md` → 마지막 commit log 순서로 읽는다.

## 현재 상태 (2026-05-02)

- **마지막 commit (HEAD)**: 71d2536 `fix(chart): NOTES.txt replicas 비교 type 불일치 (int64 vs float64)`
- **브랜치**: main (릴리스 게이트는 로컬, RFC 0002 archive 적용)
- **현재 phase**: P0 (0.3.0-alpha 재설계 정리). 진행률 ~70% (문서 reset 완료, 코드 폐기 + 검증 + 커밋 미진행).
- **미커밋 변경**:
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

## 다음 단계 (정확한 명령)

본 세션의 P0 잔여 작업은 *코드 폐기*가 핵심이며, 테스트 회귀 영향이 큼. **다음 세션에서 격리하여 처리** 권장:

1. **`internal/citus/` 디렉토리 삭제** (T10):
   ```fish
   git rm -r internal/citus/ internal/plugin/extension/citus/
   ```
2. **import 사용처 정리** — 다음 grep 결과가 0 건이어야 한다:
   ```fish
   grep -r "keiailab/postgresql-operator/internal/citus" --include="*.go"
   grep -r "extension/citus" --include="*.go"
   ```
   삭제 + 관련 테스트 (`*_citus_test.go`) 정리. `cmd/instance/main.go`, `internal/controller/postgrescluster_controller.go` 의 사용처가 영향 받음.
3. **Helm chart Chart.yaml description 갱신** (T11) — "vanilla PG (Stable, default) + 5-interface Plugin SDK + stateless QueryRouter. Distributed SQL via opt-in Citus" 메시징 제거. 신규 description 예: "K8s-native auto-sharding PostgreSQL operator. Apache-2.0, zero AGPL dependency."
4. **`docs/index.md` 갱신** (T12) — 새 docs 구조 (architecture / adr / rfcs / api-reference / runbooks / tutorials) 반영.
5. **검증 게이트** (T13):
   ```fish
   make lint test validate
   helm lint --strict charts/postgresql-operator
   grep -ri "citus" internal/ | grep -v "_test.go" | grep -v "^Binary"
   # 기대: 0건
   find docs -path '*/adr/*.md' -not -path '*/_archive/*' | wc -l   # 5
   find docs -path '*/rfcs/*.md' -not -path '*/_archive/*' | wc -l  # 5
   ```
6. **커밋** (T14, 1 atomic commit, RFC 0002 No GH Actions 정책 + commits.md Conventional 준수):
   ```
   feat!: 0.3.0 redesign reset — 자체 분산 SQL, 의존 제로

   ADR-0001 keystone: vanilla PG18+ 위에 자체 분산 SQL 레이어 구축.
   Citus / CNPG / Patroni / Cockroach 의존 영구 제거 (ADR-0003).
   ...
   BREAKING CHANGE: Citus extension support removed. AGPL/BUSL/CSL deps banned.

   Refs: ADR-0001, ADR-0002, ADR-0003, ADR-0004, ADR-0005
         RFC-0001, RFC-0002, RFC-0003, RFC-0004, RFC-0005
   Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
   ```
7. **PR 메시지 (해당 시)**: `로컬 게이트 PASS:` 블록 (`standards/ci.md` §2) 추가.

## 차단점

- 없음. 코드 삭제는 단순 mechanical 작업 + 회귀 테스트 통과 검증.

## 근거 링크

- 플랜: `/Users/phil/.claude/plans/eager-wobbling-torvalds.md` (사용자 승인 2026-05-02)
- 비교 분석: `/Users/phil/.claude/plans/eager-wobbling-torvalds-agent-a335628aa15778167.md`
- 신규 keystone ADR: `docs/adr/0001-self-built-distributed-sql.md`
- 신규 RFC 묶음: `docs/rfcs/0001-postgrescluster-crd-v2.md` ~ `0005-distributed-transactions.md`
- standards 적용: `~/Documents/ai-dev/standards/principles.md` §1, §2, §3, §4
- standards CI: `~/Documents/ai-dev/standards/ci.md` (4-layer 로컬 게이트)
- 폐기된 결정: `docs/adr/_archive/v0.x/0010-license-and-sharding-strategy.md`, `docs/rfcs/_archive/v0.x/0005-native-sharding-plugin.md`
