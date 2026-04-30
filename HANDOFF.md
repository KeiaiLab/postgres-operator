# HANDOFF — postgresql-operator

> 다음 세션이 *대화 컨텍스트 없이* 재개 가능하도록 유지. 작업 시작 시 가장 먼저 읽고, 종료 시 갱신.

## 현재 상태 (2026-04-30, P0 100% 완료 후)

- 마지막 커밋 (main): `33fef9a feat(citus): LibPQExecutor phase 1 (P0-6, RFC 0002 §6 production path 토대)`
- 본 세션(2026-04-30) 누적: **6 PR 머지 + P0 권장 100% 완료 + 글로벌 §2 정합 달성**

### 본 세션 머지된 PR 6개 (모두 admin merge, rebase, branch 자동 삭제)

| # | merge commit | 영역 | 권장 ID |
|---|--------------|------|---------|
| 1 | `e7f6373`+ (5 commits) | P0-1, P0-5, 거버넌스, Trivy CVE 3건, e2e 진단 인프라 | P0-1 + P0-5 |
| 2 | `e0d3e4f` | GH Actions 폐기 + 로컬 4 계층 (RFC 0002, ADR 0009) | 글로벌 §2 정합 |
| 3 | `ae3e4e6` | 데이터플레인 PodSecurityContext defaults (ADR 0006) | P0-2 |
| 4 | `fa24a66` | Cascade delete envtest 회귀 차단 (ADR 0008) | P0-4 |
| 5 | `5bc0199` | NetworkPolicy 3개 활성화 (ADR 0006 §NetworkPolicy) | P0-3 |
| 6 | `33fef9a` | LibPQExecutor phase 1 (RFC 0002 §6 토대) | P0-6 phase 1 |

## 도달 마일스톤

- **P1-M1** Core Lifecycle Alpha
- **P2-M1** + **P2-T2** HA / Failover Alpha + PVC Fencing
  - election: 단위 97.4%, envtest 통합 2종
  - fencing: 단위 89.7%, RFC 0003 부록 A 동결, cmd/instance 통합
- **P10-M0** Extension Plugin SDK spike
- **P11-M0** Citus Topology spike + **P0-6 phase 1**(LibPQExecutor SQL 매핑 + 7 단위 테스트)
- **P13-T1** Plugin SDK 인터페이스 동결 + **P0-5**(AuthPlugin.RotateSecret 추가)
- **품질 개선 plan**(2026-04-30): P0 6/6 ✓ — Status.Conditions reason / SecurityContext / NetworkPolicy / Cascade test / AuthPlugin Rotate / LibPQExecutor 토대
- **거버넌스**: ADR 0006/0007/0008/0009 신설, 19 권장 ID TASKS.md 매핑, RFC 14건 매트릭스
- **글로벌 §2 정합**: `.github/workflows/` 0개. 로컬 4 계층(pre-commit + pre-push + Makefile + PR review) 활성

## 다음 단계 후보 (의존 만족 + 가치 우선순위)

다음 세션은 *fresh*하게 시작하여 다음 중 하나로 진입:

```bash
cd /Users/phil/WorkSpace/public/postgresql-operator
make lint && make test         # 회귀 0 확인 (internal/citus 74.0%, internal/controller 82.9%)
make audit                     # 0 vulnerabilities 확인

# 후보 1 (가치 큼): P0-6 phase 2 — cmd/main.go에서 LibPQExecutor 주입
git checkout -b feat/p0-6-phase2-cmd-injection
# DSNFunc 구현(election holder lookup + Secret 통합), reconciler.CitusExec에 주입.
# 후속 phase 3에서 RFC 0002 → Implemented 승격.

# 후보 2 (필요): e2e cert-manager 통합 (PR #1의 e2e fail 진짜 fix)
git checkout -b feat/p7-cert-manager-integration
# config/certmanager/ 신설 (Issuer + Certificate CR) + kustomization 갱신
# + e2e_test.go BeforeAll에 webhook secret ready wait.

# 후보 3 (사용자 채널): P1-4 Helm chart (ADR 0007 결정대로 P14에서 P1으로 앞당김)
git checkout -b feat/p1-4-helm-chart-skeleton
# charts/postgresql-operator/ 신설 (Chart.yaml, values.yaml, templates/...).

# 후보 4 (Plugin SDK 호출자): P1-1 BackupJob CRD
git checkout -b feat/p1-1-backupjob-crd
# api/v1alpha1/backupjob_types.go + internal/controller/backupjob_controller.go
# + RFC 0004 작성.
```

권장 진입: **후보 1 (P0-6 phase 2)** — 차별화 1 코드 차원 잠금의 *완전성*이 다음 단계의 토대.

## 차단점

- *PR #1 e2e fail* (manager Pod webhook TLS cert 부재): GH Actions 폐기로 *PR-blocking 효과는 해소*. 진짜 fix는 후보 2 (cert-manager 통합).
- *Node.js 20 deprecated 경고* (actions/checkout@v4 등): GH Actions 폐기로 *근본 해소*.

## 본 세션 의사결정 기록 (P0 + 거버넌스)

1. **plan §14에서 19 권장 매트릭스 동결** — Bitnami(sane defaults) + Crunchy PGO(Status.Conditions, Cascade) + keiailab USP(LibPQExecutor) 교차검증 결과를 P0/P1/P2로 분해.
2. **ADR 0006 (Security Defaults)** — 데이터플레인 *opt-out 강제* 결정 (운영자 누락 시에도 root 가능 상태로 안 떨어짐). PG postgres user UID 70.
3. **ADR 0007 (Helm chart을 P14에서 P1으로 분리)** — alpha 사용자 채널 조기 확보. P14에는 install.yaml + OLM + multi-arch만.
4. **ADR 0008 (Finalizer 회피 정책)** — ControllerReference + K8s GC 패턴. 외부 자원 cleanup은 별도 Job CRD로.
5. **ADR 0009 (GH Actions 폐기 + RFC 0002 적용)** — 글로벌 §2 정합 + 사고 트리거(2026-04-28 organization billing SPOF) 회피.
6. **P0-6 phase 1 SQL 매핑 simplification** — OpAdd는 ShouldHaveShards를 Citus 기본값으로 두고, 같은 노드의 ShouldHaveShards가 desired와 다르면 다음 reconcile의 OpUpdate가 정확히 set. ComputeActions 결정성 + 멱등성이 이 전략을 보장.
7. **citus_advisory_xact_lock 동시 reconcile 직렬화** — RFC 0002 §위험 §완화. 트랜잭션 종료 시 자동 해제로 reconciler crash에도 lock leak 없음.

## 검증 명령 (재현)

```bash
make lint                                          # 0 issues
make test                                          # 모든 패키지 PASS
make audit                                         # 0 vulnerabilities (HIGH+CRITICAL)
go tool cover -func=cover.out | grep -E "citus|election|fencing|plugin|controller"
# 핵심 패키지: election 97.4% / fencing 89.7% / plugin 93.0%
# 변동: citus 95.4% → 74.0% (LibPQExecutor Apply 본문 phase 3에서 cover)
# 변동: controller 81.0% → 82.9% (P0-2/P0-4 회귀 테스트 효과)

go build ./cmd/instance/... ./cmd/...              # 모든 binary 빌드 통과
```

## 근거 링크

- `docs/roadmap.md` — 14 Pillar × DoD (Helm 분리 ADR 0007 반영)
- `docs/adr/0001-stateless-query-router-on-citus.md` — 미션 3축
- `docs/adr/0002-no-patroni-instance-manager.md` — K8s API as DCS
- `docs/adr/0005-plugin-sdk-interface-model.md` — Plugin SDK 5 인터페이스
- `docs/adr/0006-security-defaults-rationale.md` — P0-2 결정 (본 세션)
- `docs/adr/0007-helm-chart-promoted-to-p1.md` — P1-4 ADR (본 세션)
- `docs/adr/0008-finalizer-avoidance-policy.md` — P0-4 ADR (본 세션)
- `docs/adr/0009-no-github-actions-rfc-0002.md` — RFC 0002 적용 (본 세션)
- `docs/rfcs/0002-metadata-sync.md` — Citus topology sync (LibPQExecutor phase 1 토대)
- `docs/rfcs/0003-ha-election.md` — Election + Fencing
- `TASKS.md` "품질 개선 plan" 섹션 — 19 권장 ID 매트릭스
- `/Users/phil/.claude/plans/1-https-artifacthub-io-packages-helm-bit-sunny-wozniak.md` — 본 세션 plan (사용자 승인)
- 코드 (본 세션 신규):
  - `internal/citus/libpq_executor.go` (P0-6 phase 1)
  - `internal/controller/builders.go` helper 5개 (P0-2)
  - `internal/controller/cascade_delete_test.go` (P0-4)
  - `internal/plugin/api.go` AuthPlugin.RotateSecret (P0-5)
  - `internal/controller/status.go` reason 6개 (P0-1)
  - `config/network-policy/dataplane-*.yaml` (P0-3)
  - `.pre-commit-config.yaml` + `Makefile audit` (RFC 0002)
- 외부 출처 (교차검증):
  - artifacthub.io Bitnami PostgreSQL Helm Chart
  - artifacthub.io Crunchy PGO v5.8.7 (Community OLM)
