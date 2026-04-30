# TASKS — postgresql-operator

> 진척 추적: roadmap.md(14 Pillar × Maturity Level) 기반. 캘린더 미사용.
> 가중치: M0=0.25 / M1=0.5 / M2=0.75 / M3=1.0. 진척률 = Σ(가중치) ÷ 68.

## 작업 표

| ID  | 기능명/요약 | 단계 | 완성도 | 의존 | 영향 | 비고 |
|-----|-------------|------|--------|------|------|------|
| F01 | P1 Core Lifecycle (CRD + reconciler + webhook + envtest + e2e) | 테스트 | 90% | — | F03,F04,F08 | M1 도달, b13de93. M2 게이트: e2e 매트릭스 + 회귀 |
| F02 | P10 Extension Plugin SDK (depguard + 6 extensions) | 구현 | 60% | F08 | F11 | 3e948da. M1: 단위 ≥80% + e2e 1개 |
| F03 | P11 Citus Topology spike (DesiredNodes + ComputeActions + SQLExecutor) | 구현 | 25% | F02 | F12 | c7823bf, M0 도달 |
| F04 | P2 HA / Failover (election + fencing) | 테스트 | 60% | F01 | F03 | M1 도달 + P2-T2 fencing 추가. fencing 89.7%, RFC 0003 부록 A. M2 잔여: failover controller(P2-T3), pg_rewind(P2-T4), e2e 매트릭스 |
| F05 | P3 Storage / WAL | 설계 | 0% | F01 | F09 | RFC 미작성 |
| F06 | P4 Backup / PITR | 설계 | 0% | F04,F05 | F12 | RFC 0004 미작성 |
| F07 | P5 Connection / Pooling (PgBouncer) | 설계 | 0% | F01 | — | RFC 미작성 |
| F08 | P6 Observability (pgMonitor exporter) | 설계 | 0% | F01 | F04 | RFC 0007 미작성 |
| F09 | P7 Security / TLS (cert-manager + mTLS) | 설계 | 0% | F01 | F10 | RFC 0006 미작성 |
| F10 | P8 User / DB / RBAC | 설계 | 0% | F01,F09 | — | RFC 0006 통합 가능 |
| F11 | P9 Upgrade (in-place + blue/green) | 설계 | 0% | F04,F06 | — | RFC 0010 미작성 |
| F12 | P12 QueryRouter (stateless) | 설계 | 0% | F03,F07,F08 | — | RFC 0005,0009 미작성 |
| F13 | P13 Plugin SDK (gRPC out-of-process) | 구현 | 25% | F02,F06,F08 | F02 | T1만 동결. T2~T6 후속 |
| F14 | P14 Distribution (Helm/OLM/multi-arch) | 설계 | 0% | 모두 | — | 마지막 |
| I01 | TASKS.md / HANDOFF.md 운영 정착 | 완료 | 100% | — | 모두 | 본 세션 도입 |
| I02 | RFC 백로그 (0004~0012) 작성 | 설계 | 0% | 각 Pillar 진입 시 | — | 점진 작성 |

## 진행 중

(없음 — 다음 세션은 "다음 후보" 1순위 또는 사용자 지정으로 선택)

## 차단됨

(없음)

## 다음 후보 (의존 만족 순)

1. F04 P2-T3 — failover controller (election holder 변경 → PG primary promote/demote). 본격 PG supervise 시작.
2. F04 P2-T4 — pg_rewind 자동화 (fence 표시 후 인-flight write 회수).
3. F05 P3-M1 — PVC 관리 + WAL 아카이빙 + base restore. RFC 작성 선행.
4. F07 P5-M1 — PgBouncer 사이드카 + 독립 Deployment.
5. F08 P6-M1 — exporter + Grafana + PrometheusRule.
6. F02 P10-M1 — extension lifecycle e2e.

---

## 품질 개선 plan (2026-04-30 — Bitnami + Crunchy PGO 교차검증)

> 출처: `/Users/phil/.claude/plans/1-https-artifacthub-io-packages-helm-bit-sunny-wozniak.md` (사용자 승인 plan)
> 19 권장사항을 P0(즉시) / P1(중기) / P2(장기) 우선순위로 분해.
> *영향 Pillar* 컬럼은 기존 F01~F14에 매핑.

### P0 (1-2 sprint, 즉시) — 6개 — **6/6 완료 (P0-6은 phase 1+2a, phase 3은 kind e2e 후속)**

| ID | 권장 | 영향 Pillar | 단계 | 의존 | 완료 commit |
|----|------|------------|------|------|-------------|
| P0-1 | Status.Conditions reason 어휘 확장 (Promoting/Demoting/Election*/TopologyDrift/Rotating) | F04(P2), F03(P11), F09(P7) | **완료** | — | `4ec8162` (PR #1) |
| P0-2 | 데이터플레인 PodSecurityContext defaults (runAsUser=70, readOnlyRootFs, seccomp RuntimeDefault) | F01(P1) | **완료** | — | `ae3e4e6` (PR #3, ADR 0006) |
| P0-3 | NetworkPolicy 데이터플레인 표준 템플릿 (coordinator↔workers, router→coordinator/workers) | F01(P1), F09(P7) | **완료** | P0-2 | `5bc0199` (PR #5, ADR 0006 §NetworkPolicy) |
| P0-4 | Cascade Delete 회귀 테스트 (Finalizer 회피 정책) | F01(P1) | **완료** | — | `fa24a66` (PR #4, ADR 0008) |
| P0-5 | AuthPlugin.RotateSecret 인터페이스 추가 (additive, ADR 0005 §alpha rule) | F13(P13), F09(P7) | **완료** | — | `4623277` (PR #1) |
| P0-6 | LibPQExecutor 구현 (Citus 차별화 코드 차원 잠금, P2 → P0 승격) | F03(P11) | **phase 1+2a 완료** | P0-1 | `33fef9a` (PR #6 — SQL 매핑 + 7 단위 테스트), `7efbdaf` (PR #8 — env-based opt-in 주입). phase 2b(다중 cluster) + phase 3(kind e2e) → RFC 0002 Implemented 승격은 후속. |

### P1 (3-6 sprint, 중기) — 6개

| ID | 권장 | 영향 Pillar | 단계 | 의존 |
|----|------|------------|------|------|
| P1-1 | BackupJob CRD + reconciler (BackupPlugin 첫 호출자) | F06(P4) | 설계 | P0-2 |
| P1-2 | PgBouncer 사이드카 + cmd/router 통합 | F07(P5), F12(P12) | 설계 | P0-2 |
| P1-3 | Monitoring/Exporter 표준 통합 (ExporterPlugin 호출자) | F08(P6) | 설계 | P0-2 |
| P1-4 | Helm chart 패키징 (P14 → P1 앞당김, ADR 0007) | **신규 P1 트랙** (F14에서 분리) | 설계 | — |
| P1-5 | ClusterUpgrade CRD 시그니처 (in-place + blue/green) | F11(P9) | 설계 | P1-1 |
| P1-6 | pgBackRest 실행 모델 (BackupOptions.ExecutionMode: sidecar\|job) | F06(P4), F13(P13) | 설계 | P1-1 |

### P2 (6+ sprint, 장기 차별화) — 7개

| ID | 권장 | 영향 Pillar | 단계 | 의존 |
|----|------|------------|------|------|
| P2-1 | Citus rebalance / RebalanceJob CRD | F03(P11) | 설계 | P0-6 |
| P2-2 | Worker pool zero-downtime scale | F03(P11) | 설계 | P0-6, P2-1 |
| P2-3 | Plugin SDK wire-format golden test (reflect 기반 시그니처 hash) | F13(P13) | 설계 | — |
| P2-4 | gRPC out-of-process plugin + reference plugin (UDS, cosign) | F13(P13) | 설계 | P2-3, P0-2 |
| P2-5 | Declarative PgDatabase / PgRole CRD (PGO 미지원 차별화) | F10(P8), F03(P11) | 설계 | P0-5, P0-6 |
| P2-6 | Multi-region Standby Cluster (PGO Standby 차용 + Citus geo) | F14(P14)→독립 | 설계 | P1-1, P1-6 |
| P2-7 | Citus PGUpgrade orchestration | F11(P9), F03(P11) | 설계 | P1-5, P1-1, P2-6 |

### 거버넌스 산출물 매트릭스

| ID | 제목 | 트리거 권장 | 시작 상태 |
|----|------|-----------|----------|
| RFC 0002 | metadata-sync (기존) | P0-6 | Draft → **Implemented 예정** |
| RFC 0004 | Backup/PITR | P1-1, P1-6 | Draft (작성 예정) |
| RFC 0005 | QueryRouter | P1-2 | Draft (작성 예정) |
| RFC 0006 | Security/TLS (NetworkPolicy + Auth Rotation + Role/RBAC) | P0-3, P0-5, P2-5 | Draft (작성 예정) |
| RFC 0007 | Observability | P1-3 | Draft (작성 예정) |
| RFC 0008 | DistributedTable 의미론 | P2-1 | Draft (작성 예정) |
| RFC 0010 | Upgrade (Citus 절 포함) | P1-5, P2-2, P2-7 | Draft (작성 예정) |
| RFC 0012 | Plugin SDK 안정화 | P2-3, P2-4 | Draft (작성 예정) |
| RFC 0013 | Declarative DB/Role | P2-5 | Draft (작성 예정) |
| RFC 0014 | Multi-region & Standby | P2-6 | Draft (작성 예정) |
| ADR 0006 | Security Defaults Rationale | P0-2 | **Accepted (본 PR)** |
| ADR 0007 | Helm을 P14에서 P1로 분리 | P1-4 | **Accepted (본 PR)** |
| ADR 0008 | Finalizer 회피 정책 | P0-4 | **Accepted (본 PR)** |
