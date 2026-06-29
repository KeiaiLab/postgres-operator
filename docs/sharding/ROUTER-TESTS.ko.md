# 라우터/샤딩 테스트 카탈로그

> `internal/router` + `cmd/pg-router`(분산 SQL 라우터)의 테스트케이스를 영역별로 정리한
> *추후 참고용 색인*이다. 무엇을 검증하는지 + 어떻게 돌리는지 + 라이브 검증 절차를 담는다.
>
> 작성 기준: 2026-06-29 (per-query routing simple+extended, scatter forwarding, scram 인증대행,
> 온라인 resharding 전 phase 컨트롤러 결선, target 승격 Promote phase, router CPU HPA, 분산 처리량
> 측정 반영). 관련: [ROUTER-GAP-ANALYSIS.ko.md](ROUTER-GAP-ANALYSIS.ko.md)
> (설계·능력 사다리·백로그) · [`docs/perf/baseline.md`](../perf/baseline.md)(성능 실측) ·
> [`docs/TEST_ANALYSIS.md`](../TEST_ANALYSIS.md)(오퍼레이터 전체 테스트 분석).

---

## 1. 실행 방법

호스트(Windows)에 go/make 상주 안 함 → 단위는 **Windows wrapper** 또는 **컨테이너**, 통합/라이브는
**컨테이너/kind** (Dev Container 정식 절차는 dev-setup 문서).

```bash
# (A) 라우터 + pg-router 단위 테스트 (라이브 클러스터 불필요, 전부 순수/in-memory)
docker run --rm -v <repo>:/src -w /src golang:1.26 \
  sh -c "go test ./internal/router/... ./cmd/pg-router/... ./cmd/reshard-copy-poc/..."

# (B) 커버리지
... go test -cover ./internal/router/...

# (C) 전체 오퍼레이터 스위트(envtest 포함) — controller/webhook 등까지
... make test

# (D) resharding 컨트롤러 결선만 envtest focus (KUBEBUILDER_ASSETS 는 절대경로여야 함)
... KUBEBUILDER_ASSETS=$ASSETS go test ./internal/controller \
      --ginkgo.focus="ShardSplitJob|write-block|Promote phase|router autoscale"
```

```powershell
# (E) Windows 로컬 smoke wrapper (개발 중 빠른 단위 — Go test cache 유지, -Fresh 시 -count=1)
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset router
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -GinkgoFocus "Promote phase"
```

- **외부 SQL 파서 의존성 없음**: 라우팅 키 추출 parser 전략은 *제로 의존성 토크나이저*라
  별도 build-tag 불필요(전부 평이하게 컴파일·실행). (auxten 등 외부 파서는 genproto 충돌로
  기각 — ROUTER-GAP-ANALYSIS §5.)
- **라이브 게이트 테스트는 env-gated**: `reshard_cdc_live_test.go` 의 `TestCDCLive` 는 `RESHARD_LIVE_*`
  env 가 없으면 skip(라이브 PG 2대 `wal_level=logical` 필요). full e2e 는 kind 에서 별도 수행(§3).
- 알려진 flaky: `internal/controller/failover` 의 `TestLeaseElection`(타이밍 의존)이 전체
  병렬 부하에서 간헐 실패 → 단독 재실행 시 통과(`go test -count=1 ./internal/controller/failover/...`).
- Windows wrapper 는 최종 수용 검증이 아니다(smoke 전용). 기능 단위가 닫히면 Docker/kind 로 묶어 검증.

---

## 2. 단위 테스트 카탈로그 (영역별)

### 2.1 Vindex (키 → 샤드)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `vindex_test.go` `TestResolveShard` | hash/range vindex, murmur3/fnv/crc32, 범위 매칭, no-match 에러 |
| `vindex_consistent_test.go` `TestConsistentHash_Deterministic` | 같은 키→같은 샤드, 모든 샤드 사용 |
| `…_MinimalMovement` | **핵심 속성**: 샤드 3→4 추가 시 키 ~29%만 이동(modulo 해시 ~75%) |
| `…_DefaultVirtualNodes` | VirtualNodes=0 시 기본값(128) 링 구성 |
| `…_NoShards` | 샤드 0개 → 에러 |

### 2.2 라우팅 키 추출 (regex / parser / auto)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `sql_route_test.go` `TestExtractRoutingKey` | regex first-literal 모드(VALUES/WHERE 등호), 빈 리터럴 거부 |
| `…_RoutesToShard` | 추출 키가 vindex로 단일 샤드에 결정적 매핑 |
| `route_extractor_test.go` `TestRegexExtractor_ColumnMode` | 지정 컬럼(WHERE/AND 등호 + INSERT 위치) 추출 |
| `…TestNewRouteKeyExtractor` | 전략 선택기(regex/parser/auto), 빈/오류 이름 |
| `…TestAutoExtractor_FallsBackToRegex` | auto가 parser 매치 실패 시 regex 폴백 |
| `route_extractor_parser_test.go` `TestParserExtractor` | 토크나이저 추출 — SELECT/INSERT/UPDATE/DELETE/복합 predicate/`t.col`/parameterized |
| `…TestParserBeatsRegex` | 따옴표 내부·주석 속 가짜 predicate를 오인하지 않음(정규식 대비 강점) |
| `…TestParserSelectableViaFactory` | "parser"/"auto" 선택이 실제 토크나이저 사용 |

### 2.3 읽기/쓰기 분류

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `route_extractor_parser_test.go` `TestIsReadOnlyQuery` | 보수적 분류 — SELECT/SHOW/VALUES/TABLE=읽기, `FOR UPDATE/SHARE`·DML·WITH=쓰기 |

### 2.4 토폴로지 (key→shard 공급)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `topology_test.go` `TestTopologyShard` | 키→샤드 vindex 평가 |
| `…TestCRDTopologyProvider` | ShardRange CRD에서 토폴로지 구성, cluster/keyspace 매칭, 캐시, 미매칭 에러 (fake lister) |

### 2.5 백엔드 해소 (failover-aware / 읽기 분산)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `topology_test.go` `TestStatusBackendResolver` | `status.primary.endpoint`(Ready만)에서 해소, not-ready/부재 에러, **failover 시 새 primary 추종** |
| `…TestStatusBackendResolver_ResolveRead` | Ready replica round-robin, replica 없으면 primary 폴백, 둘 다 없으면 에러 |

### 2.6 Reference table

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `reference_test.go` `TestExtractTables` | FROM/JOIN/INTO/UPDATE 테이블 추출, schema 한정 `s.t`→`t` |
| `…TestReferenceRouting` | reference-only 판정(전부 reference면 true, 샤딩 테이블 섞이면 false), AnyShard 결정성 |

### 2.7 쿼리 라우팅 결정 엔진 (E 핵심)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `query_router_test.go` `TestQueryRouter_WriteRoutesToPrimaryShard` | 쓰기 → primary 백엔드 |
| `…_ReadRoutesToReplica` | 읽기 → replica 백엔드 |
| `…_ReferenceOnlyUsesAnyShard` | reference 쿼리 → AnyShard |
| `…_NoKeySignalsScatter` | 키 부재 → Scatter=true + ErrNoRoutingKey |
| `…_BackendErrorPropagates` | 백엔드 해소 에러 전파(샤드 down) |

### 2.8 Scatter-gather

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `scatter_test.go` `TestScatterGather` | fan-out, FailFast/BestEffort 정책, ErrNoShards, 부분 실패 |
| `scatter_merge_test.go` `TestScatterGather_OrderByNumeric` | 타입 인지 정렬(숫자 `"10"<"9"` 버그 수정) |
| `…_Limit` | merge 후 LIMIT 적용 |

### 2.9 SQL Executor (연결 풀)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `sql_executor_test.go` `TestSQLShardExecutor_PoolReuse` | shard별 `*sql.DB` 재사용(per-call open 안 함), Close가 풀 비움 |
| `…_NoDSN` | DSN 없는 샤드 → ErrNoDSN |
| `…_SatisfiesInterface` | ScatterGather의 ShardExecutor로 주입 가능 |

### 2.10 Resharding 데이터이동 (copy · CDC)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `resharding_test.go` `TestValidateSplitPlan` / `TestHexSuccessor` | split 보존 불변식(gap/overlap/coverage), hex 인접성 |
| `reshard_copy_test.go` `TestBuildInsert` | INSERT SQL 생성(컬럼 따옴표·플레이스홀더) |
| `…TestCopyTable_RejectsInjection` / `TestCopyShardRange_RejectsInjection` | 테이블명 인젝션 거부(offline 범위 복사 포함) |
| `…TestFilterTables` | 사용자 테이블 발견에서 reference table 제외 |
| `…TestKeyString` / `TestIndexOfFold` | lib/pq `[]byte`→string 정규화, 대소문자 무시 컬럼 인덱스(헬퍼) |
| `reshard_cdc_live_test.go` `TestCDC_RejectsInjection` | pub/sub 이름·테이블 인젝션 거부(단위) |
| `…TestCDCLive` *(env-gated)* | **라이브**: subscription copy_data 초기복사 + 구독 후 라이브 INSERT/UPDATE 유실 0, `DeleteForeignRange` 자기범위만 잔존, PK 인덱스·CHECK 제약 target 복제 |

### 2.10b Placement · Metadata (orphan 라이브러리)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `placement_test.go` `TestPlacementDrift` / `TestValidatePlacement` | drift 감지(Missing/Extra/Zone/Node/NotReady/RangeUncovered), placement 검증 |
| `metadata_store_test.go` `TestPostgresStore` | `pg_keiailab` 스키마 마이그레이션 + Upsert/List/Delete |

### 2.11 pg-router (PG wire 프록시)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `main_test.go` `TestReadStartupParsesParams` / `TestReadStartupHandlesSSLRequest` | v3 startup 파싱, SSLRequest('N' 거절 후 재파싱) |
| `…TestShardSpecRoutesByVindex` | 정적 2-shard spec 라우팅 |
| `…TestBackendForUsesEnvMapping` / `TestTemplateResolver` / `TestEnvBackendResolver` | env/DNS 템플릿 백엔드 해소 |
| `…TestWritePgError` | 샤드 down 시 우아한 PostgreSQL `ErrorResponse`('E') 인코딩(조용한 drop 금지) |
| `dialer_test.go` `TestDialer_RetryThenSuccess` | dial retry/backoff(주입 dial) |
| `…_CircuitOpensAndCooldown` / `…_HalfOpenReopensOnFailure` / `…_SuccessResetsBreaker` | circuit open→fast-fail, half-open 단일 probe·재오픈, 성공 리셋(주입 clock) |
| `pgwire_test.go` `TestPgMessageRoundTrip` / `TestQuerySQL_NonQuery` / `TestReadMessage_BadLength` | v3 메시지 read/write round-trip, 'Q' SQL 추출, 잘못된 길이 거부 |
| `…TestParseSQL` / `TestBindParams` | extended 'P'(Parse) 쿼리 추출, 'B'(Bind) 파라미터 값 추출(NULL 포함) |
| `…TestSendTrustHandshake` | trust 핸드셰이크 시퀀스(R-S-S-S-K-Z) |
| `querymode_test.go` `TestQueryRouter_routeSQL` / `…_routeKey` | query-mode 라우팅 결정(SQL 인라인 / 값 직접), 같은 키 동일 샤드 |
| `…TestSession_KeylessWriteDoesNotScatter` | per-query 세션: 키 없는 *쓰기* 는 scatter 금지(읽기만 scatter) |
| `scram_test.go` `TestScramClientProof` / `TestParseScramAttrs` | scram-sha-256 백엔드 인증 대행(RFC 7677 client-proof 벡터, SASL attr 파싱) |
| `scattermode_test.go` `TestScatterQuery_NoShardsSendsReadyForQuery` | 샤드 0개 fan-out 시에도 `ReadyForQuery` 전송(클라이언트 hang 방지) |

### 2.12 Resharding 컨트롤러 결선 (envtest, `internal/controller`)

> 컨트롤러는 PG 에 직접 접속하지 않고 cluster 내부 reshard Job 을 생성·게이트한다. 아래는 그 Job
> lifecycle·phase 전이·write-block 신호를 envtest 로 검증한다.

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `shardsplitjob_copy_test.go` "InitialCopy 복사 Job 결선" | offline InitialCopy 가 target 별 복사 Job 멱등 생성·완료/실패 집계, DSN trust(무비번) env |
| `…` "Cleanup 이 delete-only Job 생성" | cutover 후 source 이동분 삭제 Job |
| `shardsplitjob_writeblock_test.go` "Cutover write-block" | Cutover→write-block ON, RoutingUpdate→flip+OFF, forward-only 는 write-block 미설정(비가역 거부) |
| `…` "Promote phase … shard-id adopt" | source 가 active set 에서 빠지고 target Pod Ready 일 때만 named `shard-id` adopt; source active/Pod not-Ready 중에는 보류 |
| `…` "online 모드 CDCCatchup" | cdc-setup Job → write-block → cdc-finalize Job 순서, phase별 실패 보고 |
| `…` "online abort cleanup" | cdc-abort Job 성공→write-block 해제·멱등, 실패→manual cleanup 필요 보고 |
| `aggregate_status_test.go` `TestAggregateNamedShardStatus_UsesReshardTargetLabel` | 활성 named reshard target 을 cluster shard status 에 편입 |

### 2.13 Router 수평확장 (HPA)

| 파일 · 테스트 | 검증 내용 |
|---|---|
| `builders_test.go` `TestBuildRouterHPA_CPUDefaultsAndTarget` | HPA 기본값(min/max, CPU utilization target), ScaleTargetRef=router Deployment |
| `…TestBuildRouterHPA_ExplicitMinAndCPU` | `spec.router.autoscale` 명시값 반영 |
| `…TestBuildRouterDeployment_LabelsAutoscaleManagedReplicas` | autoscale 활성 시 Deployment replicas 를 HPA 가 관리하도록 label 표시 |
| `postgrescluster_controller_test.go` "router autoscale creates/deletes HPA" | enabled→HPA upsert, disabled→HPA delete, 기존 Deployment replicas 보존 |
| `postgrescluster_webhook_test.go` (autoscale bounds) | `maxReplicas>0`, `maxReplicas≥effective minReplicas` admission 거부 |

### 2.14 분산 처리량 측정 (`cmd/router-bench`)

| 도구 | 측정 내용 |
|---|---|
| `cmd/router-bench/main.go` | `internal/router.ResolveShard` 로 키→샤드 배치 후 point 쿼리를 워커수↑ 던져 TPS 측정. `BENCH_PREPARED`(prepared 재사용), `BENCH_ROUTERS`(멀티 라우터 round-robin) 모드. 결과는 `docs/perf/baseline.md §3.0b~3.0f` (단위테스트 아님, 라이브 측정 도구) |

---

## 3. 라이브 검증 (실 클러스터)

단위 테스트로 못 잡는 부분은 호스트 kind(Docker Desktop/WSL2 — 컨테이너 안 중첩 아님)에서 검증.

### 3.1 완료된 라이브 검증 (누적)
- **성능 baseline (2026-06-27)**: 오퍼레이터 배포 → 단일샤드 PostgresCluster Ready → pgbench.
  결과·환경·재현은 [`docs/perf/baseline.md §3.0`](../perf/baseline.md).
- **query-mode 쿼리 라우팅 (2026-06-27)**: 2 trust postgres + `pgrouter:dev`(`PGROUTER_MODE=query`)
  → **alice→shard-0 / bob→shard-1 / carol→shard-0** 결정적 라우팅. 백엔드 핸드셰이크 미소비
  버그(`drainUntilReady`) 발견·수정.
- **scram 인증 대행 + describe-round (2026-06-27)**: scram-sha-256 백엔드(2샤드) + lib/pq(extended,
  describe-first) → 인증 대행 후 정확 라우팅. 실 드라이버(lib/pq/pgx) 동작 증명. (scratchpad/pqclient)
- **per-query routing simple+extended (2026-06-28)**: **한 연결**에서 매 쿼리 독립 라우팅(vtgate 모델),
  scatter(키없음 양샤드), tx pin, prepared statement 샤드별 lazy(prepare-on-first-use). (scratchpad/extclient)
- **scatter forwarding (2026-06-28)**: 키 없는 simple Query 를 모든 샤드 병렬 fan-out→병합(UNION ALL).
- **reference / read-replica (2026-06-28)**: reference→AnyShard, 읽기→replica(failover-aware, replica
  미설정 시 primary fallback), 쓰기→primary.
- **분산 처리량 실측 (2026-06-28)**: `router-bench` 로 라우터경유 점읽기 동시성 스케일·prepared·bufio·
  멀티샤드/멀티라우터 측정 → `baseline.md §3.0b~3.0f`. 단일호스트 물리한계(2샤드 ≤ 1샤드) 확정.
- **🎉 온라인 resharding full e2e (2026-06-28, kind 실 K8s+실 PG)**: 단일샤드(키 100)→ShardRange+
  ShardSplitJob → **offline·online 양 경로** 전 phase(Bootstrap→InitialCopy/CDCCatchup→Cutover→
  RoutingUpdate→Cleanup→Completed) → **t0=44 / t1=56 / source=0, 합=100 키유실 0**, PK 인덱스 target
  복제, ShardRange flip + writeBlock 해제. e2e 가 실제 갭 2건 발견·수정(이미지명, target 테이블 부재).
- **referenceTables CRD**: 실 apiserver 수용 검증(server-side apply).

### 3.2 미완(라이브 환경 필요 — 무검증 랜딩 금지)
- **native router 동시쓰기 무중단 resharding e2e**: `shardingMode=native`(라우터 경유)에서 write
  부하 중 online CDC·write-block·routing flip 무중단 실증 (현 e2e 는 정적 데이터; 라이브쓰기 포착은
  `TestCDCLive` 로 별도 증명). [ROUTER-GAP-ANALYSIS §Batch 3 시나리오].
- **target 승격 후 chaos/failover drill**: Promote 로 named shard 편입된 target 의 HA/failover 거동.
- **source-down abort cleanup fallback**: source 접속 불가 시 `cdc-abort` 가 `AbortCleanup=False` 로
  남는 현 안전동작에 대한 target-only 강제정리 fallback (live drill 후 보강).
- 자동 failover chaos drill / PITR restore drill (HA·backup 영역; `make test-e2e-failover` / `-e2e`).
- 멀티머신 수평스케일 분산 수치(별 CPU+별 스토리지), percentile, sysbench, 전용 PV.

---

## 4. 용어집

> 정의는 [GLOSSARY.ko.md](../GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| Vindex (가상 인덱스) | 샤딩 키 → 샤드를 결정하는 함수/정책(hash·range·consistent-hash 등). Vitess 용어 차용. |
| Scatter-gather | 한 쿼리를 여러 샤드에 fan-out하고 결과를 모아 merge하는 분산 읽기 패턴. |
| Reference table | 모든 샤드에 복제해 두는 작은 공통 테이블. 분산 조인을 우회하는 수단. |
| Failover (장애 조치) | Primary 장애 감지 후 Replica 하나를 새 Primary로 자동 승격해 서비스를 잇는 동작. |
| Topology (토폴로지) | 어떤 키 범위가 어떤 샤드에 있는지의 라우팅 메타데이터(ShardRange CRD). |
| envtest | 실제 클러스터 없이 API 서버/etcd만 띄워 컨트롤러를 통합 테스트하는 도구. |
| Circuit breaker | 반복 실패하는 대상으로의 호출을 일정 시간 빠르게 차단해 장애 전파를 막는 패턴. |
