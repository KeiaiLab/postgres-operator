# ADR-0010: License + Sharding Strategy (Citus AGPL Isolation, vanilla PG default)

- Date: 2026-05-01
- Status: Accepted
- Authors: @keiailab
- Supersedes: 부분적으로 ADR-0001 (stateless QueryRouter on Citus) 의 "Citus 1급" 전제

## Context

본 operator는 초기 ADR-0001 v2 ("Citus 1급") 이후 Citus extension을 분산 SQL의 *기본값*으로 가정해 왔다 — chart sample, version matrix, plugin model 등 다수 파일에 반영. 그러나 0.2.0-alpha 직전 라이센스 검토에서 다음이 확인되었다:

1. **Citus 라이센스 = AGPL-3.0** (GNU Affero General Public License v3, copyleft + network use clause).
   - LICENSE 파일 헤더 직접 검증.
   - Microsoft가 2019년 인수 후에도 AGPL 유지 — SaaS 경쟁자 차단을 위한 의도된 정책.
2. **본 operator 라이센스 = Apache-2.0** (Chart annotation, repo LICENSE).
3. AGPL-3.0의 §13 (network use)는 *수정한 AGPL 코드를 SaaS 형태로 사용자에게 제공*할 때 사용자에게 source code (수정분 포함) 제공 의무를 발생시킨다.
4. 본 operator는 Citus *소스를 포함하지 않으며*, 별도 프로세스로 실행되는 PostgreSQL extension을 SQL/제어 메시지로 *조작*만 한다 (mere aggregation 가능성 높음). operator 코드 자체는 Apache-2.0 청정 유지 가능.
5. 그러나 *operator를 사용하여 Citus를 활성화한 사용자*는 AGPL §13 의무를 부담하게 된다 — 의식 없이 default를 따라가면 라이센스 위반 가능.

또한 PostgreSQL 18.3이 2026-02-26에 stable release 되었으나, **Citus는 2026-05 시점에 PG18 호환 minor를 발표하지 않았다** (최신 Citus 14.0/13.2은 PG17까지). 즉 "Citus 1급 + 최신 PG" 조합은 단기적으로 불가능.

## Decision

본 ADR은 다음 4가지를 결정한다:

1. **Default stack을 vanilla PostgreSQL 18로 전환**한다. version matrix의 Stable 채널은 vanilla PG 16/17/18만 포함한다.
2. **Citus 통합은 Beta 채널의 opt-in**으로 격리한다. 사용자가 명시적으로 `spec.version.citus` + `spec.extensions: [{name: citus}]` + `citusLibPQ.dsn`을 설정한 경우에만 활성화된다. 활성화 사용자는 AGPL-3.0 §13 의무를 명시 수용한 것으로 간주한다.
3. **Operator 코드는 Apache-2.0 청정 유지**. Citus 소스를 포함하거나 link하지 않으며, 별도 프로세스로 실행되는 PostgreSQL extension을 SQL/제어 메시지로만 조작한다.
4. **Native sharding plugin 후속 RFC** (RFC-0005)를 통해 장기적으로 Citus 의존을 단계적으로 제거할 path를 확보한다. RFC-0005는 Citus 핵심 7개 메커니즘(distributed query planner, executor, placement, rebalancer, 2PC, reference tables, columnar storage)을 분해하고, 본 operator의 5-interface Plugin SDK를 확장한 ShardingPlugin 인터페이스를 도출한다.

## Consequences

### 긍정

- **라이센스 안전**: operator 자체는 Apache-2.0 청정. 사용자가 의식 없이 AGPL 의무를 부담하는 사고 차단.
- **PG 18 즉시 활성화**: Citus 호환 minor 발표를 기다릴 필요 없이 vanilla PG18을 Stable 채널로 즉시 제공 가능. PostgreSQL 18.3의 신규 기능(asynchronous I/O, partitioning improvements, virtual generated columns 등) 즉시 활용 가능.
- **단순한 default**: 신규 사용자가 별도 라이센스 검토 없이 production 사용 가능.
- **장기 자율성**: RFC-0005 Native sharding plugin이 구현되면 분산 SQL 시나리오에서도 Apache-2.0 호환 path 확보.

### 부정

- **단기 분산 SQL 후퇴**: 0.2.0-alpha 시점에 Apache-2.0 호환 분산 SQL 옵션이 *없다*. 분산 SQL이 필수인 사용자는 (a) Citus opt-in으로 AGPL 부담, (b) RFC-0005 진행 대기, (c) 외부 분산 솔루션(YugabyteDB, CockroachDB) 검토 중 선택해야 한다.
- **ADR-0001 차별화 약화**: "Citus 1급 + stateless QueryRouter" 차별화의 절반(Citus 부분)이 default 밖으로 이동. stateless QueryRouter는 vanilla PG에서도 가치 있으나, 시장 메시징 재정렬 필요.
- **Native sharding plugin 구축 비용**: Citus는 10년 누적 ~500K LOC. 자체 sharding은 multi-year 작업. 상세는 RFC-0005.
- **breaking change**: `VersionSpec.Citus` Required → Optional, Stable 채널 항목 변경. SemVer 0.2.0 bump로 신호.

### 중립

- 기존 PG 16/17 + Citus 12.1/13.0 조합은 매트릭스에서 Beta로 강등되어 *유지*된다. 기존 사용자는 명시 opt-in 시 그대로 사용 가능.
- chart NOTES.txt + plugin doc + sample yaml에 license disclosure 추가 — 모든 사용자 진입점에서 라이센스 표면화.

## Alternatives Considered

### A. Citus 그대로 default 유지 + 라이센스 경고만 강화

- 거절 사유: 사용자가 의식 없이 AGPL §13 의무를 부담할 위험. "경고 강화"는 default 수용 압력에 의해 무시되기 쉬움.
- 트레이드오프: 단기 기능 풍부 vs 장기 라이센스 사고 위험.

### B. Citus 완전 제거 + Native sharding 즉시 구현

- 거절 사유: Native sharding 구현은 multi-year. 즉시 제거 시 분산 SQL 사용자 갈 곳 없음.
- 트레이드오프: 깔끔한 license boundary vs 기능 공백 (12-24개월).

### C. Citus FDW만 사용 (postgres_fdw + sharding 직접 구현)

- 거절 사유: postgres_fdw는 PostgreSQL License (BSD-like) 라 AGPL 영향 없음. 그러나 distributed query planning 부재 → push-down 한계. 본격 sharding 솔루션이 아님.
- 트레이드오프: 라이센스 안전 vs 기능 한계. 단기 placeholder로는 검토 가치 있음 (RFC-0005 Phase 2A 후보).

### D. 듀얼 라이센스 협상 (Citus 상용 사용권 구입)

- 거절 사유: 본 operator는 오픈소스 alpha. 비용 + 운영 가능성 부재.

### E. Citus를 fork하여 라이센스 변경 (예: PostgreSQL License로 재배포)

- 거절 사유: AGPL의 viral 조항상 fork도 AGPL 유지 의무. 라이센스 변경 fork는 무효 + 법적 책임 발생.

## 선택 근거 (B vs 본 결정)

- 단기적으로 Citus opt-in path 보존 → 기존 분산 SQL 사용자 이탈 방지.
- 장기적으로 RFC-0005 Native sharding 완성 시 Citus 제거 가능 → 점진 전환.
- operator 자체 라이센스 보호는 *기본값 변경*만으로 95% 달성 (사용자 default 행동이 라이센스 안전 영역에 머무름).

## Action Items

- [x] AI-001: `internal/version/matrix.go` Stable 채널을 vanilla PG로 재구성.
- [x] AI-002: `VersionSpec.Citus` Optional 화 + webhook PG18 feature gate 검증 제거.
- [x] AI-003: chart `config/samples/*` default extensions에서 citus 제거.
- [x] AI-004: Chart.yaml description + keywords 갱신 (Citus 1급 → vanilla default).
- [x] AI-005: NOTES.txt 라이센스 disclosure 추가.
- [x] AI-006: `internal/plugin/extension/citus/` 패키지 doc에 AGPL §13 경고 추가.
- [ ] AI-007: RFC-0005 Phase 2A 진입 시 ShardingPlugin 인터페이스 정의 PR.
- [ ] AI-008: README.md 갱신 — "Citus 1급" 메시징을 "vanilla PG default + plugin extensibility"로 재정렬.
