# RFC 0006: 더 높은 수준의 Postgres Operator — 5R 리팩토링 청사진

- **Status**: Proposed → Accepted (R1 + R2 Implemented 2026-05-03)
- **Authors**: phil
- **Date**: 2026-05-03
- **Refs**: RFC 0001 (CRD v2), RFC 0003 (election + fencing), RFC 0005 (sharding plugin), ADR 0002 (PID 1 모델), `docs/operator-guide/cross-validation-cnpg.md`

## §1 Context — 왜 "더 높은 수준" 이 필요한가

cross-validation (CNPG 1.27 vs ours 0.3.0-alpha, kind v0.31, 동일 노드) 가 두 가지 진실을 드러냈다:

1. **자원 footprint 는 우리가 작다**: Pod RSS −23%, manager image −37%, LoC −94%. 그러나 이는 *기능이 결정적으로 부재* 한 표상.
2. **alpha-deployable 의 vaporware 부분**: 같은 측정이 우리 측에서만 3 개 production bug 를 드러냄 (RBAC escalation / plugin auto-register 강제 / Promote race + already-primary). unit + envtest 가 catch 하지 못한 *실 K8s 환경에서만 드러나는 클래스*.

두 진실을 동시에 봤을 때 결론: 단순 *기능 catch-up* 으로 CNPG 와 경쟁하면 *작은 게 미덕* 이라는 차별화를 잃고, 우리는 그저 *불완전한 CNPG 클론* 이 된다. **차별화 = 기존 OSS PG operator 가 구조적으로 못 하는 것** 을 식별하고 그것에 *코드 영역을 우리 LoC 의 형태로* 채워야 한다.

본 RFC 가 그 청사진 — 5 개의 atomic refactor (R1~R5) 가 단계별로 *unlock* 하는 capabilities 를 정의한다.

## §2 5R 청사진

### R1 — Plugin Registry per-cluster + spec.extensions opt-in (Implemented 2026-05-03 commit f7db838)

**Status**: ✅ Implemented.

**문제**: cross-validation bug 2 — 모든 cluster 에 6 종 extension 강제 → vanilla PG image FATAL.

**해결**: 
- `Registry.EnabledExtensions(names)` — 명시적 opt-in 으로 filter.
- `PostgresClusterSpec.Extensions []string` — 사용자가 cluster 별 결정.
- webhook 이 미등록 이름 admission 단계 차단.
- `cmd/main.go` 의 Register 호출 = *카탈로그 등록* (활성화 아님). spec.extensions = *활성화*.

**Unlock**: per-tenant 차별화 (한 operator 가 여러 cluster 에 다른 extension 집합).

### R2 — InstanceStatus Feedback Channel (Implemented 2026-05-03)

**Status**: ✅ Implemented.

**문제**: `PostgresCluster.status.shards[]` 가 reconcile-time 근사값. Endpoint 항상 ord-0 Pod, LagBytes=0 (placeholder), Ready=ReadyReplicas (election 무관).

**해결**:
- 새 `internal/instance/statusapi` 패키지 — `Status{Role, Ready, Endpoint, LagBytes, LastUpdate}` 데이터 모델.
- instance manager 가 5s 주기로 자기 Pod annotation `postgres.keiailab.io/instance-status` 에 patch.
- controller `aggregateShardStatus` — Pod label selector 로 list → annotation parse → primary/replicas 합성.
- split-brain 검출 (≥2 primary annotation) + stale heartbeat (>30s) 처리.

**Unlock**:
- 실시간 topology view — 실제 leader Pod 가 status.Primary.
- failover RTO 측정 가능 (annotation timestamp).
- 향후 R3 (standby.signal aware boot) 의 의사결정 신호.

### R3 — standby.signal-aware Boot + 자동 failover (Proposed)

**Status**: 🚧 Proposed (다음 cycle).

**문제**: 현재 instance manager 는 부팅 시 항상 *primary 진입 가정*. replicas≥1 시 ord!=0 Pod 가 primary 가 되어버림 (election 합의로 한 명만 leader 지만 supervise 측은 모두 primary 로 동작).

**해결 설계**:
1. **Init container 가 standby.signal 결정** — ord==0 (또는 첫 부팅) 이면 init container 가 `pg_basebackup --pgdata $PGDATA --host=primary-endpoint` 로 데이터 복제 후 `touch $PGDATA/standby.signal`. ord==0 (첫 cluster) 은 initdb.
2. **instance manager 가 standby.signal 인식** — boot 시 PGDATA/standby.signal 존재 검사 → standby mode (pg_promote 호출 안 함).
3. **OnStartedLeading 가 Promote 시 standby.signal 제거** — `os.Remove(PGDATA/standby.signal)` 후 `pg_promote()`.
4. **OnStoppedLeading 가 standby.signal 재생성 + sup.Stop fast** — 자기 PVC fence 후 standby.signal 다시 만들고 instance exit. K8s Pod 재시작 → instance 가 standby mode 부팅.

**Unlock**:
- 자동 failover (replicas≥1 시) — primary kill → election 새 leader → 옛 primary Pod 가 새 standby 로 재진입.
- streaming replication 자동 부트스트랩.
- F03 active 측 완성.

**Open**:
- pg_basebackup 의 primary endpoint 결정 — R2 의 status feedback 으로 *현재 primary* 알 수 있음 (init container 가 controller status 읽기?). 또는 controller 가 init container env 로 주입.
- replication slot 관리 (primary 가 standby 별 slot 만들고 standby 가 사용). RFC 0003 부록 B.
- secret 기반 replication user 인증 (현재 trust → scram-sha-256).

### R4 — Multi-controller Split (Proposed)

**Status**: 🚧 Proposed (R3 이후).

**문제**: 단일 `PostgresClusterReconciler` 가 모든 sub-resource (RBAC + ConfigMap + STS + Service + Status aggregation + Router Deployment) 를 한 reconcile 에서 처리. Backup 추가 시 더 무거워짐. F04 (BackupController), F03-active (FailoverController) 가 별도 lifecycle 이 필요.

**해결 설계**:
- `PostgresClusterReconciler` — 토폴로지 (RBAC, ConfigMap, Service, STS, Router) + status 종합만.
- `ShardController` (신규) — 단일 shard 의 lifecycle (STS 부트스트랩, replication slot, standby join).
- `BackupController` (F04) — `BackupJob` CR 위주 + `PostgresCluster.spec.backup` cron schedule 평가.
- `FailoverController` (F03-active) — Pod annotation watch → primary stale → demote 트리거.

각 controller 는 독립 reconcile + own goroutine + 자기 watcher.

**Unlock**:
- 독립 fail isolation — backup reconcile 실패가 cluster reconcile 차단 안 함.
- 코드 리뷰 단위 분리 — 한 PR 이 한 controller 만 변경.
- 테스트 격리 — envtest 시나리오가 작아짐.

**Open**:
- inter-controller communication — Status subresource 만 사용? 또는 Pod annotation event?
- watcher 중복 — 여러 controller 가 같은 Pod 를 watch 하면 K8s API 부하.

### R5 — Native Distributed SQL Active (Proposed — 우리만의 차별화)

**Status**: 🚧 Proposed (장기 — P2+, 별도 RFC 분할 가능).

**문제**: `shardingMode: native` 가 schema + RFC 0005 plugin SDK 만 존재. 실 분산 SQL (cross-shard query, distributed catalog, range-based shard key, ShardSplitJob) 부재. CNPG / Zalando / CrunchyData 모두 single-shard primary + replicas 만 지원 — *multi-shard PG operator 는 OSS 에 없음* (Citus 는 AGPL extension, 우리는 vanilla PG 위에서 자체 layer).

**해결 설계** (RFC 0005 ShardingPlugin Active 측):

#### R5a — Distributed Catalog
- 신규 CRD `ShardCatalog` — cluster 별 shard key → shard ordinal 매핑.
- catalog 는 router 가 query 라우팅에 사용. shard 별 range (예: `user_id 0~999 → shard-0`) 보유.
- catalog 변경 (split, merge, rebalance) 은 ShardSplitJob (RFC 0003) 으로 atomic.

#### R5b — Router Active Logic
- 현재 router Deployment 는 placeholder (PG image 그대로). cmd/router 신규 binary.
- router = libpq protocol parser + query rewriter. 클라이언트 query 의 shard key 추출 → catalog lookup → shard 의 backend 로 forward.
- 단일-shard query: zero hop (router 가 shard 결정 후 직접 forward).
- Multi-shard query: router 가 fan-out + scatter-gather (집계 query).
- cross-shard transaction: 2PC (XA-like) — RFC 0005 Phase 3.

#### R5c — Auto-split 의 active 측
- `AutoSplit.Triggers` (sizeThresholdGB 등) 만족 시 controller 가 ShardSplitJob 생성.
- ShardSplitJob 은 RFC 0003 7-step (initdb, pg_basebackup, range copy, catalog update, primary cutover, drain, cleanup) 진행.
- requireApproval=true 시 운영자 annotation 이후만 진행.

**Unlock**:
- *유일한 OSS multi-shard PostgreSQL operator* (vanilla PG 기반).
- TB 단위 데이터셋 의 horizontal scale.
- 단일 cluster 가 multi-region (각 shard 가 다른 region) 도 가능.

**Open**:
- query rewriter 의 SQL parsing — 자체 구현 vs pgproto3 기반 wrapper.
- cross-shard 2PC 의 prepared transaction 안전성 — postgres prepared_transactions 설정 + recovery 시나리오.
- catalog consistency — etcd 같은 별도 DCS 사용 vs PostgresCluster.status 안 임베드.

## §3 우선순위 + Dependencies

```
R1 (extensions opt-in) ──┐
                         │
R2 (status feedback)  ───┼──→ R3 (standby boot)  ───→  R4 (multi-controller)
                         │                        ↘
                         └──────────────────────→  R5 (native sharding active)
```

- R1 + R2 = 본 commit chain (df7a0ca + f7db838 + 후속) 에서 완료. *production-grade single-shard* 의 마지막 빠진 조각.
- R3 = production HA 의 마지막 조각 (replicas≥1 자동 failover).
- R4 = R3 이후의 자연스러운 결과.
- R5 = 차별화 — 별도 multi-cycle 진행 (P2 본체).

## §4 Phase 정의

| Phase | 버전 | R1 | R2 | R3 | R4 | R5 | CNPG 비교 |
|---|---|---|---|---|---|---|---|
| **alpha** (현재) | 0.3.0 | ✅ | ✅ | ❌ | ❌ | schema only | single-shard primary only |
| **beta** | 0.4.0 | ✅ | ✅ | ✅ | ❌ | schema only | parity (single-shard HA) |
| **GA-single** | 1.0.0 | ✅ | ✅ | ✅ | ✅ | schema only | parity + 차별화 (lighter footprint) |
| **GA-distributed** | 2.0.0 | ✅ | ✅ | ✅ | ✅ | ✅ | *유일* multi-shard OSS |

본 RFC 의 acceptance 시점 (2026-05-03): **alpha 진입 — R1/R2 까지 마침**. 다음 cycle 가 R3 (beta).

## §5 Failure Modes (각 R 의 가설을 무너뜨리는 시나리오)

### R1 실패 가능성
- 사용자가 `extensions=[]` 둠 → vanilla 그대로 (의도된 default). 실패 아님.
- 사용자가 image 에 없는 extension 명시 → admission webhook 차단. 실패 아님.
- *Image catalog 문제*: webhook 은 plugin 등록 여부 검증, image 가 .so 보유 여부는 검증 X → 사용자 책임. **R1 Phase 2: per-extension image catalog + auto-image-selection**.

### R2 실패 가능성
- instance manager 의 patch 실패 (RBAC 부재, API 부하) → annotation stale → controller 가 stale heartbeat 검출 + Ready=false 강제. 실패 = degraded mode 로 graceful.
- annotation race (controller 읽는 동안 instance 가 patch) → strategic merge patch 가 atomic. 실패 아님.
- *2 primary annotation 동시*: split-brain 검출 + log warning + 첫 후보 유지. **이는 R5 + RFC 0003 fence 의 검증 책임이고, R2 자체는 graceful 보고만 한다**.

### R3 실패 가능성
- pg_basebackup 실패 (네트워크 / 권한) → init container retry → Pod CrashLoopBackOff. controller 가 status.condition 으로 노출. 사용자 개입 필요.
- 옛 primary 가 fence 안 됨 (clock skew, lease 만료 race) → 양쪽 promote 시도. *PVC label fence (RFC 0003 부록 A) 가 fail-fast — primary 가 자기 PVC 의 fenced=true label 검사 후 거부*. 정상 동작.
- standby.signal 부재 race (Pod 재시작 직후 instance 가 먼저 부팅 → primary 진입) → init container 가 *반드시 먼저* 실행되어야. K8s 가 init container ordering 보장.

### R4 실패 가능성
- 여러 controller 가 같은 status.subresource 에 write → race. **단일 writer principle**: 각 controller 가 *자기 영역의 conditions 만* update (e.g. BackupController = backup-only conditions).
- watcher event flood — RBAC 광범위 + Pod 변경 빈번 시 reconcile loop 폭주. controller-runtime predicates 로 필터 + workqueue rate limit.

### R5 실패 가능성
- query rewriter 의 SQL 파싱 한계 — 모든 PG query 를 안전히 파싱 못함. *fallback to broadcast* — 의심스러우면 모든 shard 로 fan-out (slower but correct).
- cross-shard 2PC — coordinator 실패 시 prepared transaction 잔존. *2PC recovery daemon* 필요 (별도 RFC 0007).
- catalog consistency — operator 재시작 시 catalog state 일관성. K8s status subresource 가 단일 진실, etcd 의 RAFT 가 일관성 보장.

## §6 Open Questions (acceptance 후 결정)

1. **R3 의 replication user 인증** — 현재 trust (alpha). secret-backed scram-sha-256 으로 어느 시점에 강제? (제안: 0.4.0 beta).
2. **R5 의 router image** — cmd/router 가 별도 binary (Go) vs PgBouncer fork. (제안: Go native — protocol-level rewriter 자유도).
3. **R5 의 catalog 저장소** — PostgresCluster status 안 vs 별도 ShardCatalog CRD. (제안: 별도 CRD — RFC 0005 의 ShardSplitJob 과 정합).
4. **R4 의 controller binary 분리** — 단일 manager binary 안 다중 controller (현재 controller-runtime 패턴) vs 별도 deployment. (제안: 단일 binary — operational overhead 회피).
5. **alpha → beta 의 backwards compat** — R3 도입 시 spec/status schema 변화. (제안: in-place v1alpha1 갱신 + CRD storage migration 부재 — alpha 단계라 외부 사용자 0).

## §7 측정 가능한 성공 기준

| Phase | 측정 지표 | 목표값 |
|---|---|---|
| alpha (R1+R2) | cross-validation 재실측 시 alpha-deployable 통과 (smoke.sh) | Pod Ready < 60s |
| beta (R3) | replicas=2 cluster 의 primary kill → new primary 까지 RTO | < 30s |
| GA-single (R4) | Backup CR 적용 → backup 완료 동안 cluster reconcile 차단 0회 | independent reconcile |
| GA-distributed (R5) | shardCount=4 cluster 의 cross-shard query 정확도 | 100% 일치 (single-shard reference) |

## §8 본 RFC 의 cycle 5R 의 본질

cross-validation 이 우리에게 가르친 것: *기능이 적은 게 가벼운 게 아니라, 검증되지 않은 기능이 vaporware*. 5R 은 **각 R 이 자기만의 unit test + envtest + cross-validation 재측정** 으로 검증된다. R 1 개 = 1 atomic commit + 1 deployable cycle. RFC 가 *큰 그림* 을 그리되, 각 cycle 은 *작고 검증 가능한* 단위로 진행.

이것이 5,220 LoC 가 94,130 LoC (CNPG) 와 *경쟁* 하는 방법 — **모든 줄이 검증되어 있음**.
