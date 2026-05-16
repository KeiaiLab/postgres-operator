# Performance Baseline — postgres-operator G5

> ROADMAP G5 §183 의 *측정 schema + 결과 placeholder*. 실 측정값은 cluster + 분산 인스턴스 도달 후 별 turn 에서 채워짐. 본 문서는 *재현 가능한 측정 protocol* 표준화 + 결과 형식 sealing 목적.
>
> **상태**: skeleton (pending live measurement)

## 1. 측정 환경 명시 표준

각 측정 결과는 *환경 metadata* 를 동반:

| 필드 | 예 | 비고 |
|---|---|---|
| date | 2026-MM-DD | UTC ISO 8601 |
| cluster | keiailab-prod / dev-kind / 등 | `kubectl config current-context` |
| postgres version | 16.4 | `SELECT version()` |
| operator version | v0.3.0-alpha.NN | helm chart appVersion |
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

## 3. 결과 표 — pending live measurement

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
