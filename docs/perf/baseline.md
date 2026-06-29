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

### 3.0b 실측 2차 — pg-router 분산 라우팅 처리량 (2026-06-28)

`cmd/router-bench`(in-repo, `internal/router.ResolveShard` 로 데이터 배치 → 라우터 동일
vindex) 로 **per-query 라우팅**의 워커 수 × TPS 와 라우터 오버헤드를 실측. *제품 첫 분산
라우터 수치*.

**환경**:

| 필드 | 값 |
|---|---|
| date | 2026-06-28 |
| 구성 | Docker 컨테이너 2 PG 샤드(`postgres:18`, scram) + `pgrouter:dev`(query-mode, env backend) 동일 네트워크 |
| 호스트 | 16 vCPU / ~7.6 GiB (WSL2), **샤드 2개 + 라우터 + 클라이언트가 같은 코어 공유** |
| storage | Docker overlay (local) |
| topology | static murmur3 hash, 2-shard (shard-0/shard-1), 키 10,000 (shard-0=4,914 / shard-1=5,086) |
| 워크로드 | 점(point) 쿼리 `SELECT val FROM kv WHERE id=$1` (lib/pq, autocommit, 키당 unnamed Parse+Bind+Execute), 각 5s |
| 클라이언트 | `db.SetMaxOpenConns(W)` = W 연결, W 워커 goroutine |

**결과 (read point-query, TPS)**:

| 시나리오 | w=1 | w=2 | w=4 | w=8 | w=16 | w=32 |
|---|---|---|---|---|---|---|
| direct-shard0 (라우터 없음, 기준선) | 3,881 | 9,843 | 13,878 | 17,561 | 24,540 | 38,069 |
| router-1shard (라우터 경유, shard-0 키만) | 1,761 | 3,185 | 3,560 | 5,185 | 7,083 | 9,437 |
| router-2shard (라우터 경유, 전 키스페이스 분산) | 1,570 | 2,938 | 3,488 | 5,044 | 6,872 | 8,955 |

(avg latency: direct 0.26→0.84 ms, router 0.57→3.4 ms.)

**해석**:
- **워커 수 × TPS (라우터 경유)**: 점 읽기 처리량이 동시성에 따라 단조 증가 — 1,761(w=1) →
  9,437(w=32) TPS. per-query 라우팅이 다중 연결을 병렬 처리함을 실증.
- **라우터 오버헤드**: direct 대비 w=1 ~2.2×, w=32 ~4×. 원인 = 프록시 1-hop + **키당
  Parse 비용**(lib/pq `db.QueryRow` 는 unnamed Parse+Bind+Describe+Execute 를 매번 전송 →
  라우터가 describe 대행·라우팅을 매 쿼리 수행). prepared statement 재사용(샤드별 한 번만
  Parse, 이후 Bind/Execute)이면 이 비용이 크게 줄 것 — 후속 측정 항목.
- **2샤드 ≈ 1샤드 (point read)**: 분산이 처리량을 *늘리지 않음*. 이유 = 점 읽기에선 **라우터가
  병목**(~9K TPS 천장)이고 단일 샤드(38K)는 한참 여유 → 일을 두 샤드로 나눠도 라우터 천장이
  그대로. **수평 스케일이 드러나려면 샤드가 병목인 조건**(멀티호스트로 라우터·샤드 코어 분리,
  또는 라우터 다중 인스턴스, 또는 샤드 CPU 를 포화시키는 무거운 쿼리)이 필요 — 단일 호스트
  단일 라우터로는 분산 이득 미관측. 이것이 다음 측정의 핵심 과제.
- **쓰기(update)**: Docker overlay fsync + 단일 호스트 CPU 경합으로 변동 극심(direct w=32→64
  가 3,152→17,424 로 튐)해 분산 결론 부적합 — 전용 PV / 멀티호스트 재측정 필요(보류).

**caveats**: ① 샤드·라우터·클라이언트가 같은 16코어 공유 → 분산 스케일 관측 불가(설계상
한계) ② overlay 스토리지 ③ percentile 미측정(avg 만) ④ lib/pq unnamed-Parse 경로 →
prepared 재사용 시 라우터 처리량 상향 여지.

**재현**:
```bash
# 2 scram 샤드 + pgrouter:dev 동일 네트워크 기동 후:
BENCH_KEYS=10000 BENCH_DURATION=5s BENCH_WORKERS=1,2,4,8,16,32 BENCH_MODE=select \
  go run ./cmd/router-bench
```

### 3.0c 실측 3차 — prepared statement 재사용 효과 (2026-06-28)

`BENCH_PREPARED=1`: 워커마다 연결을 고정하고 stmt 를 *한 번만* Parse 한 뒤 Bind/Execute 를
반복(라우터는 샤드별 prepare-on-first-use 로 lazy prepare). §3.0b 와 동일 환경.

**결과 (read point-query, prepared, TPS)**:

| 시나리오 | w=1 | w=2 | w=4 | w=8 | w=16 | w=32 |
|---|---|---|---|---|---|---|
| direct-shard0 | 13,784 | 21,587 | 26,964 | 35,551 | 50,246 | 81,596 |
| router-1shard | 3,816 | 6,857 | 8,058 | 9,500 | 12,887 | 17,306 |
| router-2shard | 3,044 | 5,620 | 6,360 | 9,313 | 12,618 | 17,090 |

**prepared vs unprepared (router-2shard, TPS)**:

| w | unprepared(§3.0b) | prepared | 개선 |
|---|---|---|---|
| 1 | 1,570 | 3,044 | 1.9× |
| 8 | 5,044 | 9,313 | 1.85× |
| 32 | 8,955 | 17,090 | 1.9× |

**해석**:
- **prepared statement 재사용이 라우터 처리량을 ~1.9× 로 끌어올린다** (9K→17K TPS @ w=32).
  키당 Parse(+describe 대행) 비용이 라우터 오버헤드의 *약 절반* 이었음을 정량 확인. ⇒ 제품
  권고: 드라이버 prepared statement / connection pool 사용 시 라우터 처리량 대폭 향상.
- 그래도 direct(81K) 대비 router(17K)는 ~4.7× — 남은 오버헤드는 라우터의 **동기 per-query
  proxy 루프**(메시지마다 read→route→relay, 미버퍼 syscall). **식별된 코드 최적화**: 연결당
  `bufio` 버퍼링(헤더+payload 2 syscall/메시지 → 배치)으로 syscall 수 감소 — 별도 집중
  변경으로 회귀 테스트 후 적용 예정(WORK_HANDOFF #2).
- 2샤드 ≈ 1샤드는 prepared 에서도 동일 — 라우터가 여전히 천장(단일 호스트). 수평 스케일은
  멀티호스트 측정으로(§3.0d 예정).

**재현**: `BENCH_PREPARED=1 BENCH_MODE=select go run ./cmd/router-bench`

### 3.0d 수평 스케일 — 단일 호스트의 물리적 한계 (2026-06-28)

"샤드를 늘리면 처리량이 스케일하는가?"(분산처리능력의 핵심)를 단일 호스트에서 *여러
워크로드로* 검증한 결과, **2샤드 ≤ 1샤드** 가 일관되게 관측됐다.

| 워크로드 | router-1shard | router-2shard | 결론 |
|---|---|---|---|
| read point (prepared, w=32) | 17,306 | 17,090 | ≈ (라우터 천장) |
| write hot-row (sync_commit=off, w=64) | 24,857 | **17,895** | 2샤드가 *더 느림* |

**근본 원인 — 자원 공유**: 단일 16 vCPU 호스트에서 샤드 2개 + 라우터 + 클라이언트가
**같은 CPU 코어와 같은 overlay 스토리지(fsync)를 공유**한다. 따라서:
- CPU-bound(읽기/연산): 총 코어가 고정 → 샤드를 나눠도 합산 CPU 불변 → 스케일 없음.
- 스토리지-bound(쓰기 fsync): 두 샤드가 같은 디스크에 commit → fsync 직렬화 → 스케일 없음.
- 게다가 PG 인스턴스 2개의 오버헤드(2× 백그라운드 프로세스·shared_buffers·WAL)가 더해져
  단일 호스트에선 샤딩이 throughput 을 *오히려 떨어뜨린다*.

**결론**: **진짜 수평 스케일 수치는 물리적으로 분리된 노드(별도 CPU + 별도 스토리지)에서만
측정 가능**하다. 멀티노드 kind 도 노드가 같은 호스트 커널/코어를 공유하므로 이 한계를 벗어나지
못한다(오퍼레이터의 노드 분산 *배포* 능력 검증엔 유효하나 스케일 *수치* 는 못 냄). 단일
호스트 측정은 **라우터 종단 처리량의 상한**(점읽기 ~17K TPS prepared, 라우터가 병목)을 주는
데 의의가 있다.

**진짜 분산 수치 측정 방법(멀티머신)**: `router-bench` 는 이미 샤드별 DSN 을 환경변수로
받는다(`BENCH_SHARD0`/`BENCH_SHARD1`/`BENCH_ROUTER`). 서로 다른 물리 머신(또는 클라우드 VM)에
샤드를 띄우고 라우터를 별 머신에 두면, 같은 벤치를 그대로 가리켜 N-shard 스케일을 측정할 수
있다. (예: 샤드당 1 VM × 2 + 라우터 1 VM → 1-shard 대비 2-shard 처리량 비교.)

### 3.0e 실측 4차 — bufio 라우터 최적화 효과 (2026-06-28)

§3.0c 에서 식별한 라우터 코드 최적화를 적용: ① `writeMessage` 를 헤더+payload 단일 Write
(메시지당 syscall 2→1) ② 연결당 읽기 버퍼링(`bufConn`, 쓰기는 즉시 전송이라 flush/deadlock
무관). §3.0b/c 와 동일 환경(2 scram 샤드, 단일 호스트).

**before → after (router, TPS)**:

| 워크로드 | w | before | after | 개선 |
|---|---|---|---|---|
| unprepared, router-2shard | 1 | 1,570 | 2,325 | +48% |
| unprepared, router-2shard | 32 | 8,955 | 13,391 | +50% |
| prepared, router-1shard | 32 | 17,306 | 23,207 | +34% |
| prepared, router-2shard | 32 | 17,090 | ~16,155 | ≈ (노이즈) |

**해석**:
- **읽기 syscall 이 많은 경로(unprepared: 쿼리마다 Parse+Describe+Bind+Execute 메시지 다수)
  에서 ~1.5× 향상** — 메시지당 read syscall 2→1(버퍼) + write syscall 2→1(단일 Write).
- prepared(메시지 적음)는 1-shard +34%, 2-shard 는 측정 노이즈 범위. 효과는 메시지량에 비례.
- 버퍼링은 *읽기 전용* — 쓰기는 즉시 전송하므로 request/response 교착 위험 0(회귀: per-query·
  scatter·tx·reference·replica 전부 정상). connection-mode(blind io.Copy)는 미적용.
- 남은 라우터 오버헤드(prepared direct 86K vs router 16~23K)는 프록시 1-hop 의 본질적 왕복
  지연 — 멀티 라우터 인스턴스로 수평 확장하는 영역.

### 3.0f 실측 5차 — 멀티 라우터 인스턴스 (2026-06-28)

라우터가 병목(§3.0e)이므로 *라우터 인스턴스*를 늘리면 단일 호스트에서도 스케일할까?
`router-bench BENCH_ROUTERS`(워커를 라우터 인스턴스에 round-robin)로 1 vs 2 인스턴스 비교
(prepared select, 2 샤드 + 2 라우터 + 클라이언트 모두 같은 16 vCPU 호스트).

| 시나리오 | w=8 | w=16 | w=32 | w=64 |
|---|---|---|---|---|
| router-1inst | 10,969 | 16,709 | 21,882 | 26,355 |
| router-2inst | 10,643 | 17,551 | 19,345 | 24,406 |

**해석**:
- **2 인스턴스 ≈ 1 인스턴스 (오히려 약간 낮음)** — 멀티샤드(§3.0d)와 동일한 단일 호스트
  물리 한계. 라우터가 병목이지만, 라우터 프로세스를 2개로 늘려도 *같은 16코어*를 2 샤드·
  2 라우터·클라이언트가 공유하므로 합산 CPU 가 불변 → 스케일 없음(+인스턴스 오버헤드로 미세
  감소).
- 라우터는 **stateless**(per-instance 토폴로지 캐시·per-connection 세션·per-instance breaker)
  라 N replica 가 독립·정확. 오퍼레이터는 이미 `RouterSpec.Replicas`(+HPA `RouterAutoscaleSpec`)
  로 router Deployment 를 스케일한다 — **capability 는 완비**.
- ⇒ 멀티 라우터 수평 스케일도 **물리 분리 노드(별 CPU)에서만 수치로 드러난다**. router-bench
  의 `BENCH_ROUTERS` 가 멀티머신 라우터 DSN 을 그대로 받으므로 그 환경에서 측정하면 된다.

**재현**: `BENCH_ROUTERS="host=r1...,host=r2..." BENCH_PREPARED=1 go run ./cmd/router-bench`

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
