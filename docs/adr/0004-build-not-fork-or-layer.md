# ADR 0004 — Build from Scratch (PGO Fork·Soft Layer 모두 거부)

- **상태**: Accepted
- **날짜**: 2026-04-27
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001 (미션 재정의), ADR 0002 (Patroni 미사용), ADR 0003 (QueryRouter Stateless)
- **선행 분석**: `/Users/phil/.claude/plans/squishy-squishing-harp.md` §7 (Crunchy PGO 비교), §8 (전략 옵션 A/B/C 평가), §9 (재정의된 미션)

## 컨텍스트

본 프로젝트의 미션이 "Citus + QueryRouter 단일 차별화"에서 **"PGO-class 풀스택 + Citus 1급 + Plugin SDK 기반 확장성"** (ADR 0001 갱신)으로 확장되면서, 다음 세 가지 통합 전략을 다시 평가했다.

| 옵션 | 본질 |
|---|---|
| **A. Hard fork** | `crunchydata/postgres-operator`를 fork → Patroni 제거 → Citus 추가 |
| **B. Soft layer (out-of-tree)** | PGO를 upstream 의존으로 두고 우리는 `CitusCluster` CRD로 합성만 |
| **C. Build from scratch** | PGO 코드를 사용하지 않고 처음부터 자체 작성, PGO가 검증한 운영 idiom만 차용 |

세 옵션 모두 라이선스(Apache-2.0 ↔ Apache-2.0)는 호환된다. 결정 근거는 **장기 통제권**, **유지보수 부채**, **차별화 정합성**, **상용 품질 약속의 책임 소재**에 있다.

## 결정

**옵션 C (Build from scratch)를 채택한다.** 옵션 A와 B는 모두 거부한다.

PGO 코드를 import 하지 않는다. PGO가 검증한 다음 idiom·패턴은 **개념 차원에서만 차용**한다(Apache-2.0 라이선스 호환이므로 안전):
- pgBackRest 통합 패턴 (P4)
- pgMonitor 대시보드 구조 (P6)
- Pod anti-affinity / fencing 운영 idiom (P2)
- `PostgresCluster` CRD 명명 (이미 ADR 0001 채택)

## 근거

### 옵션 A (Hard fork) 거부 사유

1. **Patroni 제거 비용 > 처음부터 짜는 비용**: PGO HA의 핵심이 Patroni이고 ADR 0002에 따라 우리는 "K8s API as DCS + 자체 instance manager"를 채택한다. Patroni를 들어내면 PGO의 reconciler 30%가 다시 작성되어야 하며, 이 시점에 fork는 사실상 별개 프로젝트가 된다.
2. **Upstream 따라잡기 부채**: PGO는 분기당 마이너 + 매월 패치 릴리즈. cherry-pick 또는 rebase 전략이 필수이며, 6개월 후 divergence가 보안 패치 흡수조차 어렵게 만든다.
3. **브랜드 희석**: "PGO fork"라는 꼬리표는 **"PGO-class + Citus 1급 + Plugin SDK"** 라는 우리 차별화 메시지를 흐린다.
4. **거버넌스 신뢰**: maintainer 신뢰 자산을 우리가 처음부터 쌓아야 하는데, fork에서 출발하면 출처에 대한 의문이 첫 인상을 결정한다.

### 옵션 B (Soft layer) 거부 사유

1. **상용 품질 약속과 양립 불가**: ADR 0001 갱신은 "PGO 수준의 단일 PG HA 운영 품질을 우리가 책임진다"고 명시한다. 그러나 Soft layer에서 우리가 책임지는 표면은 `CitusCluster` 한 겹뿐이고, HA·백업·풀링·모니터링 품질은 PGO의 책임이다. **품질 약속을 했는데 품질 통제권은 외부에** 있는 모순.
2. **PGO API stability 인질**: PGO v5→v6 전환 같은 메이저 변경이 발생하면 우리 어댑터의 호환성 비용을 우리가 흡수해야 한다. 통제권 없는 의존은 장기적으로 우리의 출시 일정과 우리 사용자의 경험을 PGO 일정에 종속시킨다.
3. **Citus 우선순위 버그**(crunchydata/postgres-operator#3194)**가 사전 차단**: PGO가 `shared_preload_libraries`에 `pgaudit`를 자동 prepend 하므로 "Citus는 첫 번째여야 함" 규약이 깨진다. upstream PR 머지를 우리 출시 조건으로 두면 일정 통제권이 더 약화된다.
4. **확장성 불가**: Plugin SDK(P13)는 우리가 컨트롤러 코드 전체에 인터페이스 호출 규약을 강제할 때만 의미가 있다. PGO 컨트롤러를 우리가 수정할 수 없으므로 Soft layer에서는 Plugin SDK가 우리 영역(`CitusCluster` 합성기)에만 적용되어 메타-차별화 가치가 70% 사라진다.

### 옵션 C (Build) 채택 사유

1. **장기 통제권**: HA·백업·풀링·모니터링·보안·업그레이드의 **모든 코드 경로를 우리가 보유**. 품질 약속(ADR 0001 §양보할 수 없는 기준)의 책임 소재가 명확하다.
2. **Plugin SDK의 의미가 살아남**: P13 인터페이스 5종(Backup/Exporter/Extension/Router/Auth)이 컨트롤러 전 영역에 적용되어, 새 백업 도구 추가 = 인터페이스 구현 1주 라는 약속이 가능하다.
3. **차별화 정합성**: "PGO가 안 한 4가지(Citus 1급, Stateless Router, 분산 PITR, Plugin SDK)에 자원을 집중"이 코드 구조로 강제된다.
4. **법적 단순성**: PGO 코드를 import 하지 않으므로 Apache-2.0 NOTICE 누적이나 fork 명시 의무가 없다. 단순 idiom 차용은 라이선스 의무가 발생하지 않는다.

## 트레이드오프

- **단기 시간 비용 큼**: PGO가 6+년에 걸쳐 만든 단일 PG HA 운영 품질을 우리가 직접 짜야 한다. v1.0 GA까지의 작업량이 옵션 B 대비 약 3~4배.
  - **완화**: 14 Pillar 의존 그래프(plan §10.3)에 따라 6개 트랙 병렬 진행, Pillar 오너 컨트리뷰터 모집을 거버넌스에 명시.
- **단일 PG HA 영역에서 PGO와 직접 비교당함**: 사용자는 "왜 PGO 안 쓰고 이걸?"을 물을 수밖에 없다.
  - **완화**: README 비교표 상단에 "단일 PG HA만 필요하면 PGO/CNPG 권장. Citus 분산 + Plugin 확장이 필요하면 본 프로젝트" 명시. 정직한 포지셔닝.
- **운영 idiom 차용의 회색 지대**: pgBackRest 통합 코드 구조나 pgMonitor 대시보드 JSON을 어디까지 참고할 수 있는가.
  - **완화**: Apache-2.0이므로 코드 import 없이 패턴 차용은 안전. 단, 직접 복사가 필요한 경우(예: 대시보드 JSON 일부)는 NOTICE 파일에 출처 명시.

## 대안 (검토 후 기각된 추가 옵션)

- **CNPG fork**: 동일한 옵션 A 문제 + CNPG의 비-Citus 가정이 더 깊이 박혀 있어 비용 더 큼.
- **PGO/CNPG 어댑터(`PostgresBackend` 인터페이스)를 핵심에 두고 자체 백엔드 옵션을 v1.x에서 추가**: 결국 옵션 B와 같은 통제권 문제 + 두 코드베이스 동시 추적 부담.

## 강제 메커니즘

1. **`go.mod`에 `crunchydata/postgres-operator` 또는 `cloudnative-pg/cloudnative-pg` import 금지** — golangci-lint custom 규칙.
2. **NOTICE 파일에 차용한 idiom의 출처를 추적**: 대시보드 JSON, alert rule, 운영 가이드 등 직접 복사 영역에 한정.
3. **PR 리뷰 체크리스트**에 "PGO 코드 직접 참조 여부" 항목 추가 — `internal/`, `api/`, `cmd/` 어디든 PGO 코드 패턴이 식별되면 출처 주석 또는 자체 재작성 요구.
4. **본 ADR 변경(옵션 B/A로의 회귀)은 RFC 필수** — GOVERNANCE.md "아키텍처 변경" 절차.

## 결과

- 본 프로젝트의 모든 Pillar(P1~P14)는 자체 코드로 작성된다.
- ADR 0001 갱신과 함께 README의 "왜 또 다른 PG Operator인가" 표가 갱신된다 (큐-A2 작업).
- `docs/roadmap.md`는 14 Pillar × 4 Maturity Level + 68 task 구조로 재작성된다 (큐-A3 작업).
- 본 결정은 plan 파일 `/Users/phil/.claude/plans/squishy-squishing-harp.md` §10에 정의된 작업 큐의 모든 단위가 PGO 코드 import 없이 진행됨을 보장한다.
