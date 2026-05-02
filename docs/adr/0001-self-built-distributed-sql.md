# ADR-0001: PostgreSQL 위 자체 분산 SQL 레이어 구축

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil
- Supersedes: `_archive/v0.x/0001-stateless-query-router-on-citus.md`, `_archive/v0.x/0010-license-and-sharding-strategy.md`

## Context

본 프로젝트는 Kubernetes 위에서 PostgreSQL 을 운영하는 operator 로 0.2.0-alpha 까지 진화했고, ADR-0010 (2026-05-01) 시점에는 *Citus extension 의 AGPL 격리 + vanilla PG18 default* 이라는 *이중 backend* 모델로 가는 중이었다. 그러나 2026-05-02 에 *유사 시스템 (Citus / YugabyteDB / CockroachDB / Vitess / CloudNativePG) 의 설계 결정을 비교 분석* 한 결과, 다음 조건을 동시에 충족하는 시스템이 시장에 존재하지 않음이 확인됐다:

1. **PostgreSQL wire/SQL 100% 호환** — application 코드 변경 없이 분산 채택 가능.
2. **라이선스 청정성** (Apache-2.0 / BSD / MIT / PG License 만) — 상용 SaaS 노출 시 의무 없음.
3. **K8s 네이티브 통합** — CRD + reconciler + KEDA 기반 자동 샤딩.
4. **자동 샤딩** (write-side scale-out) — 수동 split 만 지원하는 Citus 한계 보완.

사용자 (eightynine01@gmail.com) 가 2026-05-02 본 결정 시점에 4 가지 선택지 (A: Citus packaging, B: 실용적 통합 — pgcat + Citus rebalancer 위임 + KEDA 자동 split + CNPG HA 임베드, C: 풀 자체 분산 SQL — 모든 의존 제거, custom) 중 **C** 와 **모든 외부 backend 의존 제거** 와 **single chart + flags** 를 명시 선택했다. 이는 6+년 timeline 을 인지한 야심찬 결정이며, *long-term 차별화는 invention 이 아니라 invention + 라이선스 청정 + K8s 네이티브의 동시 달성에서 나온다* 는 product 정체성 재정의이다.

## Decision

PostgreSQL 위에 *자체 분산 SQL 레이어*를 구축한다. Citus / CloudNativePG / Patroni / CockroachDB 패턴 코드를 의존성 그래프에서 영구히 제외한다.

핵심 매개변수:

- **Operator core 라이선스**: Apache-2.0 (변경 없음).
- **외부 OSS 의존 정책**: ADR-0003 (license policy) 참조. AGPL/BUSL/CSL/SSPL 영구 금지. BSD/Apache/MIT/PG License + v1+ stability commitment 만 허용.
- **자체 구현 컴포넌트** (`docs/architecture/` 상세):
  - `pg-router` — PG wire protocol parser, vindex 평가, scatter-gather, 분산 txn coordinator.
  - `vindex` 모듈 — hash / range / consistent-hash / lookup.
  - `ShardRange` CRD — keyspace + key range + shard placement source of truth.
  - `ShardSplitJob` CRD + resharder controller — 7-step online resharding workflow.
  - `Rebalancer` controller — shard 배치 균형 자체 구현.
  - 분산 트랜잭션 coordinator — 2PC (PG `PREPARE TRANSACTION` 활용) + saga.
  - HA — instance manager 기반 (RFC 0003 P2-T1 frozen interface 활용, Patroni 미사용).
- **재활용 자산**: pgBackRest (BSD-2), pg_query_go (PG License), controller-runtime (Apache-2.0), KEDA (Apache-2.0). 모두 ADR-0003 정책 충족.
- **Backend 의존 제거 범위**: Citus extension, Citus 메타데이터 (`pg_dist_*`), CloudNativePG `Cluster` CR, Patroni DCS, CockroachDB range KV layer. 코드 0줄 + 문서/논문 차용만 허용.
- **Helm 패키징** (ADR-0002): 단일 chart + 컴포넌트 flag (router / resharder / rebalancer / keda / backup / monitoring).
- **CRD 라이프사이클** (ADR-0004): operator manager 가 소유 (server-side apply), Helm `crds/` 폐기.
- **버전 채널** (ADR-0005): alpha / beta / stable. CRD apiVersion v1alpha1 → v1beta1 → v1.
- **Phase 로드맵** (`docs/roadmap.md`): P0 (재설계 정리, 0.3.0) → P1 (single-shard production-ready, 0.4.0) → P2 (multi-shard 수동, 0.5.0) → P3 (vindex 확장 + read autoscale, 0.6.0) → P4 (online split, 0.7.0) → P5 (자동 split + rebalance, 0.8.0) → P6 (분산 txn, 0.9.0) → P7 (안정화 + ArtifactHub verified, 1.0.0). 1인 50% 가동 ~64개월 (5.3년) 추정.
- **Production 보장**: 각 phase 끝에 *deploy 가능한 안정 버전*. P1 부터 single-shard production 사용 가능.

## Consequences

**긍정**:
- *라이선스 사고 영구 0 건* — AGPL 전염, BUSL 상용 금지, SSPL FUD 모두 회피. SaaS 노출 자유.
- *PostgreSQL 100% 호환* — fork (YugabyteDB) 또는 wire-only (Cockroach 40%) 의 한계 극복. 모든 PG 18+ extension / 타입 / 함수 사용 가능.
- *K8s 네이티브 metadata 통합* — ShardRange CRD = source of truth, etcd 가 분산 메타데이터 저장소. 별도 KV 레이어 (Cockroach Range, Citus `pg_dist_node`) 불필요.
- *명확한 차별화* — "K8s-native + license-clean + auto-sharding for vanilla PG" 는 시장에 없음.
- *학습 가치* — Citus 8년 / Vitess 10년 자산을 "직접 구현" 하면서 분산 SQL 내재화.

**부정 / 비용**:
- *6+년 timeline* — 1인 50% 가동 추정 64개월. 현실적으로 6년 이상 가능성. 중도 포기 위험은 phase 별 production-deployable 보장으로 완화 (P1 single-shard 부터 사용 가능).
- *재발명 비용* — Citus 의 검증된 rebalancer / shard placement / DDL propagation 을 직접 구현. 초기 버그 밀도 높음.
- *단위 테스트 + chaos test 부담 증가* — 분산 시스템 정합성 검증 (jepsen-style) 자체 구축 필요.
- *PG wire protocol drift 추적* — PG 19/20 출시 시 router 호환성 작업 부담.
- *기존 코드 폐기* — `internal/citus/`, `internal/plugin/extension/citus/` 삭제. 약 ~3K LoC 손실 (테스트 포함).
- *ADR-0001 (legacy) "PGO-class + Citus 1급" 메시징 무효화* — README + roadmap + tutorials 전면 재작성.

**트레이드오프**:
- *invention vs integration* — 본 결정은 invention 측. 만약 1인 maintainer burnout 시 *복귀 경로* 는 ADR-0010 (legacy) 의 Citus 격리 plugin 모델로 *되돌리기* 가능 (ShardingPlugin 인터페이스가 RFC-0005 (legacy) 에서 frozen 되어 있어 backend 교체점 보존됨).
- *시장 진입 시기* — Citus packaging (옵션 A, 12 개월) 으로 빠르게 reach 했으면 v1.0 을 2027 에 출시 가능했음. 본 결정으로 v1.0 이 2031~2032 로 밀림. 그 사이 *경쟁 솔루션이 같은 격차를 메울 위험* 존재.

## Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **A. K8s-native Citus + CNPG packaging** (12개월, ADR-0010 방향 유지) | AGPL 의존 + Citus 자동 split 부재 + 차별화가 *integration* 한정. 사용자 명시 거부 (2026-05-02). |
| **B. pgcat + Citus rebalancer 위임 + KEDA 자동 split + CNPG HA** (24개월, devil's advocate 추천) | pgcat 는 PG-호환이지만 query parser 한정 + scatter-gather 미지원. CNPG API drift 장기 위험. 사용자 명시 거부. |
| **D. CloudNativePG fork + sharding patch** | upstream 흐름 단절 → self-defeat. CNPG 1.5만 commit/yr 의 자산 손실. |
| **E. CockroachDB 임베드** | BUSL/CSL 상용 금지 + PG SQL feature parity 40%. ADR-0003 라이선스 정책 위반. |
| **F. YugabyteDB fork** | YSQL 은 PG11 fork — 일부 extension 미지원, 다른 product 가 됨. |
| **G. Apache ShardingSphere-Proxy** | JVM 운영 부담 + DDL/extension 일부 제한 + K8s operator 부재. |

## References

- 사용자 결정 기록: `/Users/phil/.claude/plans/eager-wobbling-torvalds.md` §1, §3
- 비교 분석: `/Users/phil/.claude/plans/eager-wobbling-torvalds-agent-a335628aa15778167.md`
- ADR-0002: 단일 chart + flags Helm 정책 (본 결정의 패키징 측면)
- ADR-0003: 라이선스 정책 — AGPL/BUSL/CSL/SSPL 영구 금지 (본 결정의 의존성 측면)
- ADR-0004: CRD 라이프사이클을 operator manager 가 소유
- ADR-0005: alpha → beta → stable 채널 + CRD apiVersion 진화
- RFC-0001~0005: 본 결정의 5 핵심 컴포넌트 RFC (CRD v2 / ShardRange / ShardSplitJob / pg-router / 분산 트랜잭션)
- 폐기된 결정: `_archive/v0.x/0010-license-and-sharding-strategy.md` (Citus AGPL 격리 + vanilla PG default 모델)
- standards 참조: `~/Documents/ai-dev/standards/principles.md` §1 (Think Before Coding), §2 (Simplicity First — *충돌* — 본 결정은 simplicity 보다 *long-term 라이선스 청정성 + 차별화* 우선), `standards/adr.md` (ADR 작성 규약)
