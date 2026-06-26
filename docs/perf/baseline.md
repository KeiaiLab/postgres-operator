# Performance Baseline — postgres-operator G5

> ROADMAP G5 §183 의 *측정 schema + 결과 placeholder*. 실 측정값은 cluster + 분산 인스턴스 도달 후 별 turn 에서 채워짐. 본 문서는 *재현 가능한 측정 protocol* 표준화 + 결과 형식 sealing 목적.
>
> **상태**: 1차 로컬 실측 완료 (2026-06-27, §3.0 — single-shard). 분산/sysbench/2PC/전용 PV 는 pending.

## 1. 측정 환경 명시 표준

각 측정 결과는 *환경 metadata* 를 동반:

| 필드 | 예 | 비고 |
|---|---|---|
| date | 2026-MM-DD | UTC ISO 8601 |
| cluster | keiailab-prod / dev-kind / 등 | `kubectl config current-context` |
| postgres version | 16.4 | `SELECT version()` |
| operator version | v0.4.0-beta.1 | helm chart appVersion |
| shard count | 1 / 4 / 16 | ShardPlane spec.shards |
| client host | 동일 cluster / 외부 / 동일 node | network locality 영향 |
| client cores / RAM | 8C / 16G | bench 클라이언트 자원 |
| backend cores / RAM | per-pod 4C / 8G | postgres pod 자원 |
| storage class | ceph-rbd / local-path | IOPS 영향 |

## 2. Workload matrix

3 축 × N value 측정:

### 2.1 Topology (router fan-out 단계)

| ID | 설명 | 2PC | 비고 |
|---|---|---|---|
| T-1S | single-shard baseline | N/A | router 우회 가능 |
| T-NS-RO | N-shard read-only | N/A | scatter-gather 측정 |
| T-NS-2PC | N-shard cross-shard tx | YES | ADR-0015 핵심 측정 |

### 2.2 Workload type

| ID | 도구 | mode |
|---|---|---|
| W-pgb-tpcb | pgbench | tpcb-like |
| W-pgb-ro | pgbench | select-only |
| W-pgb-upd | pgbench | simple-update |
| W-sb-rw | sysbench | oltp_read_write |
| W-sb-ro | sysbench | oltp_read_only |
| W-sb-ps | sysbench | oltp_point_select |

### 2.3 Isolation level (D.10.3 cross-ref)

| ID | level | anomaly |
|---|---|---|
| I-RC | READ COMMITTED | dirty read X, non-repeatable read 허용 |
| I-RR | REPEATABLE READ (SI) | write skew 일부 |
| I-SER | SERIALIZABLE (SSI) | anomaly 0 |

## 3. 결과 표

> **1차 로컬 실측 추가됨 (2026-06-27, §3.0).** §3.1~ 는 여전히 schema placeholder
> (분산 N-shard / sysbench / 2PC / 전용 PV 미측정).

### 3.0 실측 1차 — 로컬 kind 단일샤드 (2026-06-27)

오퍼레이터를 호스트 kind(Docker Desktop/WSL2)에 배포 → 단일샤드 PostgresCluster Ready
→ pgbench. *제품 첫 baseline 수치* (single-shard HA-PG-on-K8s).

**환경**:

| 필드 | 값 |
|---|---|
| date | 2026-06-27 |
| cluster | 로컬 kind v0.32 (`pgop-dev`, 단일 노드) on Docker Desktop / WSL2 |
| operator | local build `pgop:dev` (브랜치 `chore/ha-pitr-e2e-consolidation`) |
| postgres | 18.3 (`ghcr.io/keiailab/pg:18`) |
| topology | shardingMode=none · 단일 shard · replicas=0 (HA 없음) |
| node 자원 | 16 vCPU / ~7.6 GiB (WSL2 VM 할당) |
| PG 설정 | shared_buffers=160MB · effective_cache_size=5GB · max_connections=100 |
| storage | local-path (kind 기본, WSL2 overlay) |
| client 위치 | **PG pod 내 co-located** (pgbench가 같은 16코어 공유 → TPS 보수적) |
| scale | `pgbench -s 50` (5M rows ≈ 750MB, OS 캐시 적재) · 각 30s · `-j 8` |

**결과**:

| workload | clients | TPS | latency avg |
|---|---|---|---|
| W-pgb-tpcb (read-write) | 8 | 496 | 16.1 ms |
| W-pgb-tpcb (read-write) | 16 | 646 | 24.8 ms |
| W-pgb-tpcb (read-write) | 32 | 889 | 36.0 ms |
| W-pgb-ro (select-only) | 32 | 9,035 | 3.5 ms |
| W-pgb-ro (select-only) | 64 | 10,493 | 6.1 ms |

**해석**:
- 읽기(select-only)가 쓰기(tpcb)의 **~10–18배** — 쓰기는 매 커밋 WAL fsync 가 로컬
  overlay 디스크에 바운드. 프로덕션 SSD/전용 PV 에선 쓰기 TPS 가 크게 오를 여지.
- 클라이언트 증가에 TPS 단조 증가(RW 8→32 ≈ 1.8×, RO 32→64 ≈ 1.16× — 64에서 포화 접근).
- **caveats**: ① pgbench client co-located(동일 pod/코어) → 분리 클라이언트 대비 보수적
  ② percentile(P50/95/99)은 pgbench `-l` 로그 후처리 필요(미측정, 후속) ③ WSL2 overlay
  스토리지라 쓰기 IO 가 실 PV 보다 느림.

**재현**:
```bash
kubectl -n default exec quickstart-shard-0-0 -c postgres -- pgbench -i -s 50 -U postgres postgres
kubectl -n default exec quickstart-shard-0-0 -c postgres -- pgbench -c 32 -j 8 -T 30 -U postgres postgres      # tpcb
kubectl -n default exec quickstart-shard-0-0 -c postgres -- pgbench -S -c 64 -j 8 -T 30 -U postgres postgres   # select-only
```

### 3.1 Single-shard baseline (T-1S)

| date | workload | iso | clients | TPS | P50 ms | P95 ms | P99 ms | comment |
|---|---|---|---|---|---|---|---|---|
| _(pending)_ | W-pgb-tpcb | I-RC | 10 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | W-pgb-ro | I-RC | 50 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | W-sb-rw | I-RC | 8 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | W-sb-ps | I-RC | 32 | — | — | — | — | _(pending live measurement)_ |

### 3.2 N-shard read-only (T-NS-RO)

scatter-gather 의 fan-out overhead 측정.

| date | shards | workload | clients | TPS | P50 ms | P95 ms | P99 ms | comment |
|---|---|---|---|---|---|---|---|---|
| _(pending)_ | 4 | W-pgb-ro | 50 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 16 | W-pgb-ro | 50 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 4 | W-sb-ps | 32 | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 16 | W-sb-ps | 32 | — | — | — | — | _(pending live measurement)_ |

### 3.3 N-shard cross-shard 2PC (T-NS-2PC) — ADR-0015 핵심

cross-shard tx 의 2PC overhead 측정.

| date | shards | workload | iso | clients | TPS | P50 ms | P95 ms | P99 ms | 2PC abort% | comment |
|---|---|---|---|---|---|---|---|---|---|---|
| _(pending)_ | 4 | W-pgb-tpcb | I-RC | 10 | — | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 4 | W-pgb-tpcb | I-SER | 10 | — | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 16 | W-pgb-tpcb | I-RC | 10 | — | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 4 | W-sb-rw | I-RC | 8 | — | — | — | — | — | _(pending live measurement)_ |
| _(pending)_ | 4 | W-sb-rw | I-SER | 8 | — | — | — | — | — | _(pending live measurement)_ |

### 3.4 Isolation × throughput trade-off (D.10.3 cross-ref)

per-isolation level TPS 비교 (4-shard W-sb-rw 기준):

| iso | TPS | anomaly count (D.10.3) | comment |
|---|---|---|---|
| I-RC | — | per D.10.3 matrix | _(pending live measurement)_ |
| I-RR | — | per D.10.3 matrix | _(pending live measurement)_ |
| I-SER | — | 0 (expected) | _(pending live measurement)_ |

## 4. Target metric (G5 졸업 조건 후보)

ROADMAP G5 가 합의될 때 본 target 이 SLO 로 격상:

- W-pgb-ro on T-NS-RO 4-shard: TPS ≥ 4× single-shard (linear scale-out)
- W-pgb-tpcb on T-NS-2PC 4-shard: P99 < 2× single-shard (2PC overhead < 100%)
- 2PC abort rate < 5% under normal load (no chaos)
- vindex lookup P99 < 10μs (per RFC-0002 §297)
- `_vindex_hash` write overhead < 5% (per RFC-0003 §286)

## 5. 측정 protocol

1. cluster + N-shard ShardPlane provisioning (별 turn, 사용자 영역)
2. `test/bench/pgbench.sh` 또는 `sysbench.sh` env 변수 지정 후 실행
3. `bench-results/` log → 본 표 채움 (PR 또는 별 commit)
4. 환경 metadata §1 동반 명시
5. 동일 시나리오 3회 측정 → median 적재 (variance 별 column 후보)

## 6. Refs

- ROADMAP.md L183 G5 Benchmarks
- test/bench/README.md — 측정 wrapper 사용법
- docs/rfcs/0002-shardrange-crd.md §297 — vindex P99 target
- docs/rfcs/0003-shardsplitjob-7step.md §286 / §303 — overhead target
- docs/sharding/SHARDING.md §118 — sysbench-tpcc + pgbench --select-only
- ADR-0015 — cross-shard transaction semantics
- D.10.3 isolation-matrix — anomaly × throughput trade-off
