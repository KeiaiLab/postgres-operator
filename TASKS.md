# TASKS — postgres-operator

> 작업 ID: F=기능 / I=개선 / B=버그 / T=그 외. 부여 후 재사용 금지.
> 단계: 설계(10%) / 구현(60%) / 테스트(90%) / 완료(100%).

## 현재 Phase: **P1** (0.4.0 — single-shard production-ready)

목표: RFC 0001 의 새 CRD spec 위에 single-shard reconcile + HA + pgBackRest 통합 + chaos-mesh E2E. 6+개월.

P0 (0.3.0-alpha redesign reset) 는 완료 (commit df1f2e1). 이전 phase 작업 표는 §"이전 Phase 기록" 참조.

## 작업 표 (P1)

| ID    | 기능명 / 요약 | 단계 | 완성도 | 의존 | 영향 | 비고 |
|-------|---------------|------|--------|------|------|------|
| F01a  | RFC 0001 PostgresCluster CRD v2 schema 실장 (types + CEL + webhook + deepcopy) | 완료 | 100% | - | F01b,F02~F05 | 2026-05-03. v1alpha1 in-place 본문 교체. 6-필드 spec, status.shards[], 3개 CEL XValidation. webhook 도메인 검증 (matrix/autoSplit/backup). |
| F01b  | F01a stub 해소 — 새 ShardsSpec/RouterSpec 기반 reconcile 본체 + builders 재배선 + envtest 재작성 | 완료 | 100% | F01a | F02~F05 | 2026-05-03. ShardStatefulSet/Service/ConfigMapName 도입 + Worker*/Coordinator* 제거. SelectorLabels(cluster, role, ordinal). Condition 카탈로그 RFC 0001 §3.4 (Ready/Progressing/ShardsReady/RouterReady/BackupHealthy/AutoSplitEligible). reconcile 본체: shard 루프 + router 분기 + status 종합 + applyClusterConditions. envtest 2종 (single-shard + native multi-shard) + cascade_delete (OwnerReference 검증 ADR 0008). lint 0 / test 3 specs pass / validate helm lint --strict pass. |
| F02   | instance manager P2-T3+ — postgres 프로세스 supervise + promote/demote 실장 | 테스트 | 90% | F01b,T15 | F03 | 2026-05-03. **5-cycle deployable 진행 완료**: (1) sample CR RFC 0001 v2 schema 재작성 (2) Dockerfile.pg 신규 — instance binary + postgres 이미지 (3a) builders.go env injection + pg_hba.conf + readiness/liveness probes (3b) per-cluster ServiceAccount/Role/RoleBinding + initdb init container + serviceAccountName (4) /readyz 에 sup.IsReady(PG round-trip) 통합 (5) hack/smoke.sh kind 스모크 + docs/operator-guide/deployment.md 가이드. lint 0 / 모든 패키지 test PASS / make validate (kustomize+helm lint --strict) PASS. 후속 (F02 100%): kind 실측 / WAL lag 측정 / chaos-mesh failover RTO 검증. |
| T15   | election lease 명명 규약 shard ordinal 모델로 마이그레이션 (F02 진입 정리) | 완료 | 100% | F01b | F02 | 2026-05-03. PrimaryLeaseName 시그니처 (cluster, role, pool string) → (cluster, role string, shardOrdinal int32). cmd/instance/main.go: POSTGRES_POOL → POSTGRES_SHARD_ORDINAL. router 호출 시 panic 으로 lease 부재 타입 강제. lint 0 / election test PASS (cov 97.5%). |
| F03   | RFC 0003 election / fencing 인터페이스 위 실장 완성 | 구현 | 60% | F02 | F04 | 2026-05-07. T19 에서 Pod UID 기반 election identity, `ReleaseOnCancel=false`, restarted ordinal-0 standby marker 재구성까지 실장. 남은 범위: 일반화된 fencing/old-primary 재합류, network partition/STONITH 계열 검증. |
| F04   | pgBackRest 통합 — backup controller (`internal/controller/backup/`) | 설계 | 10% | F01b | F05 | BackupJob CRD 는 이미 정의됨. RFC 0001 의 `spec.backup` reconcile 연결. |
| F05   | single-shard E2E 시나리오 재설계 — chaos-mesh primary kill → failover < 30s | 설계 | 10% | F01b,F02,F03,F04 | - | RFC 0001 spec 기준 envtest 신규 작성 포함. T19 로 PG18 primary Pod delete smoke 는 RTO 21s PASS. 남은 범위는 chaos-mesh primary kill/network partition 기반 E2E 와 pgBackRest 결합 검증. |
| T16   | GitOps deploy 오버레이 도입 (mongodb-operator / valkey-operator 와 3-repo 정합) | 완료 | 100% | - | - | 2026-05-06. ADR-0006. `deploy/overlays/prod/{kustomization,delete-namespace}.yaml` + `deploy/postgres-cluster.yaml` (db ns) + `deploy/README.md`. `kustomize build deploy/overlays/prod` 렌더 PASS, Namespace 리소스 0 건. patch target name 은 namePrefix 적용 전 raw `system` (config/manager 직접 import). |
| T17   | PG17/PG18 Kind smoke matrix — runtime image + CR version + HA WAL override | 완료 | 100% | F02 | F02,F03 | 2026-05-07. `hack/smoke.sh` 가 `PG_MAJOR`/`POSTGRES_VERSION`/`NS`/`SHARD_REPLICAS` override 를 지원. PG17, PG18 각각 Kind 에서 image build/load → PostgresCluster Ready → `psql SELECT 1` PASS. `SHARD_REPLICAS=1` 에서 primary+standby 2 Pod, `pg_stat_replication WHERE state='streaming'` PASS(PG17/PG18). |
| T18   | failover smoke false-pass 제거 + PG18 차단 원인 실측 | 완료 | 100% | T17 | F03,F05 | 2026-05-07. `SMOKE_FAILOVER=1` 이 RTO 미측정/standby 미승격을 성공으로 통과하던 false-pass 제거. macOS `date -d` 의존 제거. 초기 실측은 FAIL 로 원인 확보: 같은 Pod name lease 재점유, `*-1` recovery 유지, PVC fence label 없음. |
| T19   | PG18 failover 차단 수정 + status 수렴 gate 추가 | 완료 | 100% | T18 | F03,F05 | 2026-05-07. election identity=`podName/podUID`, `ReleaseOnCancel=false`, `POSTGRES_MEMBER_COUNT`, restarted ordinal-0 standby marker/`primary_conninfo` 재구성, CR status polling 추가. PG18 `SHARD_REPLICAS=1 SMOKE_FAILOVER=1` smoke PASS: WAL streaming, RTO 21s(<30s), CR status primary=`quickstart18fo-shard-0-1`, restarted `*-0` standby 확인. |

## 이전 Phase 기록 (P0 — 0.3.0-alpha redesign reset, commit df1f2e1)

T01 ~ T14 완료. 자체 분산 SQL 정체성 정착, ADR/RFC 0001~0005 작성, Citus 의존 코드 폐기, lint/test/validate PASS.

## 차단됨

(없음 — F01a 가 P1 진입 게이트였고 완료 상태. F01b 부터 순차 진행 가능.)

## 영향도 추적

F01a (CRD schema) 변경 시 → F01b reconcile 본체 + 모든 envtest 재검토.
F01b (reconcile 본체) 변경 시 → F02~F04 의 spec 인터페이스 점검.
F04 (pgBackRest) 변경 시 → BackupJob CRD `spec.tool="pgbackrest"` 통합 검증.
