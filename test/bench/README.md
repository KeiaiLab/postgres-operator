# test/bench — postgres-operator G5 Benchmark Skeleton

> ROADMAP G5 §183 의 *invocation skeleton*. 실 측정은 cluster + 분산 인스턴스 도달 후 별 turn. 본 디렉터리는 *재현 가능한 측정 entry-point* 표준화 목적.

## 구성

| 파일 | 용도 |
|---|---|
| `pgbench.sh` | pgbench TPC-B-like / select-only / simple-update 측정 wrapper |
| `sysbench.sh` | sysbench OLTP read-write / read-only / write-only / point-select 측정 wrapper |
| `README.md` | 본 문서 |

결과는 `OUTPUT_DIR` (기본 `./bench-results/`) 에 timestamp 파일로 적재 — git 추적 안 함.

## 사용 전 prerequisite

- `pgbench` (postgresql-client 패키지) 또는 `sysbench` (별도 패키지) binary 설치
- 측정 대상 postgres 또는 ShardPlane (router) endpoint 도달 가능
- 측정 DB 접근 권한 (init/prepare 단계는 schema 생성 + data load → 권한 필요)

binary 부재 시 wrapper 가 `command -v` 로 graceful skip — 로컬 dev 환경에서 syntax 검증만 가능.

## pgbench 사용 예

```bash
# 단일 노드 baseline (tpcb-like, 10 client × 60s)
PGBENCH_HOST=primary.example.com PGBENCH_DB=postgres \
  PGBENCH_SCALE=10 PGBENCH_CLIENTS=10 DURATION_S=60 \
  ./test/bench/pgbench.sh

# Read-only 측정 (select-only)
PGBENCH_HOST=router.shardplane.example.com \
  PGBENCH_MODE=select-only PGBENCH_CLIENTS=50 DURATION_S=120 \
  ./test/bench/pgbench.sh

# 기존 데이터 재사용 (init skip)
PGBENCH_SKIP_INIT=1 PGBENCH_HOST=... ./test/bench/pgbench.sh
```

지원 mode: `tpcb-like` (기본, 4-statement tx), `select-only` (-S), `simple-update` (-N).

## sysbench 사용 예

```bash
# OLTP read-write 표준 (8 thread × 10 table × 100K rows × 60s)
SYSBENCH_HOST=router.shardplane.example.com SYSBENCH_PASSWORD=secret \
  SYSBENCH_MODE=oltp_read_write SYSBENCH_THREADS=8 DURATION_S=60 \
  ./test/bench/sysbench.sh

# Point-select stress (read scalability)
SYSBENCH_MODE=oltp_point_select SYSBENCH_THREADS=32 DURATION_S=120 \
  ./test/bench/sysbench.sh

# 측정 후 data 보존 (반복 측정 시간 단축)
SYSBENCH_KEEP_DATA=1 ./test/bench/sysbench.sh
```

지원 mode: `oltp_read_write`, `oltp_read_only`, `oltp_write_only`, `oltp_point_select`, `oltp_update_index`, `oltp_insert`.

## 측정 cross-reference

### ADR-0015 cross-shard transaction semantics

ShardPlane router 가 cross-shard 2PC 를 수행하는 경로의 *지연 오버헤드 측정* 이 G5 핵심. pgbench 의 tpcb-like 가 cross-shard tx 를 자연스럽게 유발 (account/teller/branch 3 테이블 join + update).

- single-shard baseline: 모든 데이터 1 shard 로 confined → 2PC 미발동
- cross-shard 2PC: distribution column hash → tpcb-like 자동 cross-shard
- 비교 metric: TPS, P50/P95/P99 latency, 2PC abort rate

### isolation-matrix cross-ref

D.10.3 isolation-matrix (`test/isolation/` skeleton) 의 per-isolation-level anomaly 측정과 본 bench 의 throughput 측정이 **trade-off matrix** 를 구성:

- RC (read committed): 높은 throughput, anomaly 허용
- SI (snapshot isolation): write skew 일부
- SSI (serializable): 낮은 throughput, anomaly 0

각 isolation level 별 sysbench oltp_read_write TPS 측정 → `docs/perf/baseline.md` matrix 채움.

## 결과 schema

각 측정 결과는 `docs/perf/baseline.md` 의 표 형식으로 적재:

- timestamp (UTC ISO 8601)
- workload (pgbench-tpcb / sysbench-oltp_rw / 등)
- topology (single-shard / N-shard / N-shard + 2PC)
- isolation level
- TPS, P50, P95, P99 latency (ms)
- 2PC abort rate (cross-shard 측정 시)
- comment (특이사항 / 환경 차이)

## Refs

- ROADMAP.md G5 §183 — Benchmarks line
- docs/perf/baseline.md — 측정 schema + 결과 placeholder
- docs/rfcs/0002-shardrange-crd.md — vindex lookup P99 < 10μs target
- docs/rfcs/0003-shardsplitjob-7step.md §286 — `_vindex_hash` < 5% overhead target
- docs/sharding/SHARDING.md §118 — sysbench-tpcc + pgbench --select-only on 4 shards
- ADR-0015 — cross-shard transaction semantics
