# 라우터 프로덕션 갭 분석 + 능력 사다리

> 분산 SQL(Citus급) 방향의 **척추 = 라우터**다. 본 문서는 `internal/router/` + `cmd/pg-router`
> 전수 audit으로 "무엇이 진짜 동작 / 골격 / 프로덕션까지 빠진 것"을 코드 레벨로 못박고,
> Vitess-검증 난이도 순서의 **능력 사다리**와 **첫 출하 슬라이스**를 정의한다.
>
> 작성 기준: 2026-06-26. 관련: [SHARDING.md](SHARDING.md) · [RFC-0004](../rfcs/0004-pg-router-architecture.md) ·
> [RFC-0002](../rfcs/0002-shardrange-crd.md) · [WORK_HANDOFF.ko.md §6](../WORK_HANDOFF.ko.md)

---

## 0. 좌표: 이건 Citus가 아니라 "Vitess-for-Postgres"

- **Citus** = PG **내부 확장(extension)**. ADR-0003에서 명시적으로 버린 길.
- 현재 아키텍처 = **stateless 라우터 + vanilla PG 샤드 + vindex + 토폴로지 메타데이터** = **Vitess 모델**.
- 함의: Vitess가 *이 아키텍처가 동작한다는 존재증명* + 그 난이도 순서가 최고의 지도. 단, 완전판은 수십 인년 → **유용한 부분집합**(단일샤드 라우팅 + 읽기 scatter + reference table + 보조 resharding)을 노린다.

---

## 1. 핵심 구조 결함: 라우터가 "두 반쪽"으로 분리됨

| | 반쪽 A — `cmd/pg-router` (실 프록시) | 반쪽 B — `internal/router` in-process 라이브러리 |
|---|---|---|
| 정체 | raw TCP 바이트 프록시 | 타입 잡힌 Go 라이브러리(scatter/executor/store/placement) |
| 라우팅 | **연결 단위**(startup의 db/user 이름) | 쿼리 단위 fan-out + merge |
| 토폴로지 | **2-shard 하드코딩**(`shardSpec()`) | (해당 없음) |
| 프로덕션 소비처 | 없음(배포 매니페스트 0) | **없음**(PoC 커맨드 + 테스트만) |
| 둘의 연결 | `vindex.ResolveShard`만 공유. scatter/executor/store는 **pg-router가 안 씀** | — |

**결론**: 두 반쪽이 따로 논다. pg-router는 바이트만 흘리고, 잘 만든 라이브러리는 orphan이다.
어느 쪽도 operator reconciler에 연결돼 있지 않다(유일 예외: ShardSplitJob이 `ValidateSplitPlan` 호출).
**토폴로지 흐름 ShardRange CRD → 라우터가 끊겨 있다** — pg-router가 CRD를 읽지 않고 하드코딩한다.

프로덕션 router import처: `internal/controller/shardsplitjob_controller.go` 단 1곳(split-plan 검증). 나머지는 `cmd/{pg-router,scatter-poc,reshard-copy-poc}`(전부 PoC).

---

## 2. 파일별 audit

| 파일 | 상태 | 평가 |
|---|---|---|
| `vindex.go` | ✅ **진짜·견고** | murmur3(자체)/fnv/crc32 해시 + range vindex + overlap 검증. 순수·결정적. **가장 단단한 조각.** 미구현: consistent-hash·lookup(명시 보류). pg-router가 소비. |
| `resharding.go` | ✅ **진짜** | split-plan gap/overlap/coverage 보존 불변식 검증. ShardSplitJob이 소비. `adjacent/hexSuccessor`는 고정폭 hex 전제의 약식이나 컨벤션 내 동작. |
| `placement.go` | 🟡 **진짜지만 orphan** | drift detection(Missing/Extra/Zone/Node/NotReady/RangeUncovered) 순수 함수. 견고하나 **어떤 reconciler도 호출 안 함.** |
| `metadata_store.go` | 🟡 **진짜지만 orphan + 미배선** | `pg_keiailab` 스키마 + 마이그레이션 + Upsert/List/Delete(실 SQL). 그러나 ① CRD→store 채우는 reconciler 없음 ② 라우터가 store를 안 읽음 ③ store가 살 "coordinator Postgres"가 프로비저닝되지 않음. |
| `sql_executor.go` | 🟡 **진짜지만 PoC급 + 미연결** | lib/pq 실 구현. 그러나 **호출마다 sql.Open+Close**(풀링 없음 → 부하 시 치명), 원시 driver `any` 반환(PG wire 타입 아님), **pg-router가 안 씀**(바이트 프록시라서). |
| `scatter.go` | 🟡 **골격** | fan-out+gather+merge(concat/naive order-by) + 취소 전파. 그러나 merge가 `fmt.Sprintf("%v")` 문자열 비교(숫자/타입 부정확), aggregate/LIMIT pushdown 없음, k-way 스트리밍 없음, 컬럼 메타데이터 미전파. 주입된 ShardExecutor 의존. |
| `sql_route.go` | 🔴 **PoC(정규식)** | `VALUES('key'`·`WHERE col='key'`의 첫 single-quote 리터럴만 정규식 추출. **진짜 SQL 파서 아님.** prepared/parameterized(`$1`)·복합 predicate·JOIN·quoted ident·schema-qualified·주석·multi-statement 전부 누락. **단일샤드 라우팅의 #1 갭.** |

---

## 3. 프로덕션 척추까지 빠진 것 (능력 사다리 1·2단계 기준)

순수-함수 기반(vindex·validation·placement·metadata 스키마)은 이미 진짜. 빠진 건 **통합 척추**다:

- **(A) SQL 파싱** — 정규식을 **pure-Go PG 파서**로 교체해 WHERE/INSERT에서 샤딩키를 정확히 추출. CGO libpg_query는 distroless/`CGO_ENABLED=0`와 충돌하므로 pure-Go 선정이 선행(아래 §5 결정).
- **(B) CRD 기반 토폴로지** — 라우터가 ShardRange CRD를 watch(또는 reconciler가 채운 metadata_store를 read)하여 **라이브 라우팅 테이블 + 핫리로드**. 현재 하드코딩.
- **(C) 라우터 배포 + HA** — Deployment/Service 매니페스트. 라우터는 stateless라 Service 뒤 N replica. **라우터가 새 연결 엔드포인트(신규 SPOF)** 가 되므로 자체 HA 필수. 현재 매니페스트 0.
- **(D) 커넥션 풀링** — `SQLShardExecutor`의 per-call open/close 제거(풀 + prepared stmt 캐시).
- **(E) 두 반쪽 통합** — pg-router 바이트 프록시를 **메시지 인지(message-aware) 프록시**로 올려 단일샤드 fast-path(연결 핀)와 멀티샤드 read(scatter 라이브러리) 경로를 한 프로세스에서 처리. 아키텍처 결정 필요.

---

## 4. 능력 사다리 (Vitess-검증 난이도 순 · 매 단계 데모 수치)

| 단계 | 능력 | cross-shard 난이도 | 현재 | 첫 시장 필요? |
|---|---|---|---|---|
| 1 | 단일 샤드 라우팅(샤딩키 point query) | 없음 | A·E 필요 | ✅ 필수 |
| 2 | 읽기 scatter-gather(fan-out+merge) | 낮음 | scatter 골격→실연결 | 🟡 분석쿼리 |
| 3 | scatter + 집계/정렬 pushdown | 중간 | 미착수 | 선택 |
| 4 | **Reference table**(전 샤드 복제 → 조인 우회) | 낮음·고가치 | 미착수 | ✅ 권장 |
| 5 | 무중단 resharding(CDC 논리복제 + cutover) | 높음 | ShardSplitJob 골격(데이터 이동 X) | 운영 필수 |
| 6 | cross-shard 쓰기 / 2PC | 최고 | 미착수 | ❌ 명시적 범위 밖 |

**원칙**: 6번은 ROI 최저(Vitess도 수년 보류) → **명시적으로 "범위 밖" 선언**. 1·2·4번만으로
가장 큰 실세계 패턴(**tenant_id 샤딩 멀티테넌트 SaaS**)을 완전히 덮는다 — 거의 모든 쿼리가
단일 샤드로 떨어져 2PC가 필요 없다.

---

## 5. 첫 출하 슬라이스 + 결정해야 할 한 가지

**첫 슬라이스(능력 1단계)** = "tenant_id로 라우팅되는 단일샤드 fast-path가 실제 동작하고, 토폴로지는 ShardRange CRD에서 오며, 라우터가 배포 가능하고, single-shard 벤치 수치가 나온다."

작업: (A) SQL 파서 도입 → (B) CRD watch 토폴로지 → (C) Deployment/Service 매니페스트 → (E) 메시지 인지 프록시.

### 결정됨 — 라우팅 키 추출은 **교체 가능 전략 + lean 기본**

라우터 키 추출을 한쪽으로 하드코딩하지 않고 **`RouteKeyExtractor` 인터페이스(전략)** 로
노출한다 (`internal/router/route_extractor.go`). 세 전략 *모두 제로 외부 의존성* 이라
항상 컴파일되고 **런타임 선택** 가능하다:

| 전략 | 구현 | 의존성 | 정확도 |
|---|---|---|---|
| `regex` (기본) | `sql_route.go` 정규식(컬럼 인지: WHERE/AND 등호 + INSERT 위치) | **0** | point query 흔한 형태 |
| `parser` | `route_extractor_parser.go` *토크나이저* | **0** | 따옴표/이스케이프/주석/복합 predicate/INSERT 위치/UPDATE·DELETE/`t.col` 한정 |
| `auto` | parser 우선 + regex fallback | **0** | 둘의 합 |

**외부 SQL 파서 도입 시도 → 기각 (실측, 2026-06-26)**: `auxten/postgresql-parser
v1.0.1` 을 평가했으나 두 가지 이유로 **기각**:
1. **무게**: `go mod tidy` 시 ~25 transitive 모듈(cockroachdb/errors·gogo/protobuf·
   sentry·grpc-gateway·logrus 등) — distroless 미니멀리즘과 충돌.
2. **치명적 — 빌드 파괴**: auxten 이 *옛 monolithic* `google.golang.org/genproto` 를
   고정해, 오퍼레이터의 현대 deps(grpc 1.79·otel·cel-go 가 쓰는 *split* genproto)와
   **ambiguous import 충돌** → 빌드 자체가 깨짐. go.mod 는 모듈 전역이라 build-tag 로도
   격리 불가. (`pganalyze/pg_query_go`=CGO 탈락, `cockroachdb-parser`=동일 계열 더 무거움.)

**그래서 결정**: 외부 파서 대신 **제로 의존성 토크나이저**를 자체 구현(murmur3 자체
구현과 동일 철학). 정규식보다 정확하면서(따옴표 내부·주석의 가짜 predicate 오인 안 함)
의존성 0 을 지킨다. 단위 테스트로 SELECT/INSERT/UPDATE/DELETE/복합 predicate/
parameterized/`t.col`/주석·문자열 내부 오인 방지까지 검증. **기본 전략은 regex(현황
유지)**, 정확 라우팅이 필요한 배포는 `parser`/`auto` 선택. 런타임 선택은 (E) 메시지
인지 프록시에서 env 로 노출한다.

---

## 6. 백로그 — 향후 대작업 TODO

> "더 좋은 방향"이지만 규모가 큰 후속 작업을 *미리 기록*해 둔다(사용자 요청 2026-06-26).
> 각 항목은 능력 사다리(§4) 단계 또는 회복력/운영 축에 매핑된다. 우선순위는 가변.
>
> **2026-06-26 세션 진행** (검증·커밋 완료): 커넥션 풀링(D) ✅ · 라우터 HA(dial retry/backoff
> + circuit-breaker) ✅ · 읽기→replica *부품*(StatusBackendResolver.ResolveRead +
> IsReadOnlyQuery) ✅ · reference table *부품*(CRD referenceTables + ExtractTables/
> ReferenceOnly/AnyShard) ✅ · scatter merge(타입 정렬 + LIMIT) ✅ · **(E) 라우팅 결정
> 엔진 QueryRouter** ✅. — 이들의 *쿼리 단위 결선*(프록시가 실제로 호출)은 (E) 프로토콜
> 종단이 되어야 완성. 아래 항목은 그 종단/운영-코어/라이브검증 필요분.

**라우팅 핵심 (능력 사다리)**
- [~] **(E) 프로토콜 종단 — 쿼리 단위 라우팅**: **query-mode 라이브 PG end-to-end 검증 완료 (2026-06-27)** 🎉. 2 trust postgres(shard-0/1) + pg-router `PGROUTER_MODE=query` → psql `SELECT located_on FROM probe WHERE id='alice'` 이 키 추출→murmur3 vindex→샤드 라우팅→실 쿼리 실행→결과 반환까지 동작. **alice→shard-0 / bob→shard-1 / carol→shard-0** 결정적·올바른 분산 확인(bob이 다른 샤드 = 해시 라우팅 실증). 라이브 검증에서 프로토콜 버그(백엔드 핸드셰이크 미소비) 발견·수정. **종단 작업 진척**: ① **scram-sha-256 / cleartext 백엔드 인증 대행 ✅완료**(RFC 7677 단위검증 + 라이브 scram PG 검증, `crypto/pbkdf2` stdlib, `PGROUTER_BACKEND_PASSWORD`) — **query-mode가 trust 아닌 실 프로덕션 PG와 동작** ② **extended protocol ✅완료**: inline-literal + parameterized(`$N`) 라우팅. **describe-round 대행 구현**(`describeround.go`) — lib/pq·JDBC 의 `Parse→Describe→Sync→Bind` 를, 임의 샤드로 describe round 대행 후 Bind 파라미터로 실 샤드 라우팅(다르면 re-Parse + 중복 ParseComplete 필터). **라이브 검증: lib/pq `WHERE id=$1` 정상 라우팅**(alice→shard-0/bob→shard-1) ⇒ **실 DB 드라이버와 동작** ③ 멀티샤드 scatter forwarding + 결과 재조립(남음) ④ 백엔드 연결 풀(남음) ⑤ ~~per-query 로그~~ ✅완료.
- [ ] **읽기 → replica 라우팅**: `status.shards[].replicas[]`(Ready)로 읽기 쿼리를 분산 → primary 부하 경감 + 읽기 확장. 사다리 2단계. (status backend 계층 재사용.)
- [ ] **scatter-gather 실연결**: `scatter.go` 골격 → 실 wire-protocol fan-out + merge + 집계/정렬 pushdown. 사다리 2~3단계.
- [ ] **Reference table**: 전 샤드 복제로 분산 조인 우회. 사다리 4단계. 고가치·저난도.
- [ ] **무중단 resharding 데이터 이동**: `ShardSplitJob`의 InitialCopy/CDC 실결선(현재 골격, 데이터 미이동). `router.CopyTable` DSN 결선 + 논리복제 + write-block cutover. 사다리 5단계.
- [ ] **cross-shard 2PC**: 사다리 6단계. **현재 명시적 범위 밖**(ROI 최저). 멀티테넌트 v1엔 불필요.
- [ ] **커넥션 풀링 (D)**: `SQLShardExecutor`의 per-call `sql.Open`/`Close` 제거 → 풀 + prepared stmt 캐시. scatter 경로 성능. (단일샤드 TCP 프록시엔 불필요.)

**회복력 / 운영**
- [ ] **stable per-shard primary Service (운영자 측)**: 운영자가 각 샤드의 *현재 primary*를 가리키는 안정 Service를 publish하면, 라우터가 status polling 없이 DNS만으로 즉시 failover-follow → status 모드보다 빠르고 단순. (현재는 `PGROUTER_BACKEND=status`로 status polling.)
- [ ] **ShardRange/status watch (informer)**: 현재 interval(`PGROUTER_REFRESH`) polling → watch 기반 즉시 hot-reload(failover window 단축).
- [ ] **라우터 자체 HA 강화**: readiness가 백엔드 도달성 반영, circuit-breaker, dial retry/backoff, replica 읽기 폴백.
- [ ] **failover 전용 lease 제대로 (RFC 0007 P2-T3)**: 이전에 production 무효 배선을 제거하고 building block으로 보존한 `internal/controller/failover/lease.go`를, failover를 reconcile 루프 밖 leader-election-agnostic runnable로 분리한 뒤 그 lease로 게이팅.

**상품화 (수치)**
- [ ] **single-shard 성능 baseline 실측**: 라이브 K8s에서 `test/bench/*.sh`로 TPS/p99/QPS 수치(`docs/perf/baseline.md` 채우기). 샤딩 없이도 "HA PG operator"로서의 첫 영업 숫자.
- [ ] **N-shard 분산 수치**: scatter-gather 도달 후 샤드 수별 QPS/latency(분산처리능력 증명).

---

**로직 보강 / 견고성** (2026-06-27 코드 검토). 일부는 (E)/planner 결선 후 가치:
- [x] 라우팅 키 *모호성 bail* — 같은 샤딩 컬럼에 다른 리터럴(서브쿼리/OR) 시 추측 거부 (커밋 f50cedd). **틀린 샤드 쓰기 방지(P0)**.
- [x] *dollar-quote 토큰화* — `$$...$$` 본문 가짜 predicate 누출 차단 (f50cedd). **P0**.
- [x] *ResolveRead lag 임계* — bounded staleness (8f51a0a).
- [x] **consistent-hash 링 캐시** — fingerprint 캐시(cec61bb).
- [x] **scatter ORDER BY 다중컬럼/방향** — OrderByCol/OrderByDesc(59c310a).
- [x] **LIMIT per-shard pushdown** — PushDownLimit 보수적 주입(59c310a).
- [x] **circuit-breaker half-open 단일 probe** — allow gate(0dca370).
- [x] **SQLShardExecutor ConnMaxLifetime** — 기본 30m(0dca370).
- [x] **CRDTopologyProvider stale 캐시 정책** — ClearCacheOnMissing(0dca370).

> ✅ **위 보강 6건 + NULL 정렬 PG 정합(NULLS LAST asc/FIRST desc)까지 build/vet/test
> 검증 완료 (2026-06-27).** 정적 리뷰에서 예상한 대로 compile/crash 버그 없었음.

---

## 7. 용어집

> 정의는 [GLOSSARY.ko.md](../GLOSSARY.ko.md)에서 발췌해 동일하게 유지한다. 전체 용어는 해당 문서 참고.

| 용어 | 정의 |
|---|---|
| Vindex (가상 인덱스) | 샤딩 키 → 샤드를 결정하는 함수/정책(hash·range 등). Vitess 용어 차용. |
| Scatter-gather | 한 쿼리를 여러 샤드에 fan-out하고 결과를 모아 merge하는 분산 읽기 패턴. |
| Reference table | 모든 샤드에 복제해 두는 작은 공통 테이블. 분산 조인을 우회하는 수단. |
| Resharding | 샤드 키 범위를 다른 샤드로 분할/재배치하는 작업. 무중단이 되려면 CDC+cutover 필요. |
| 2PC (Two-Phase Commit) | 여러 샤드에 걸친 쓰기를 원자적으로 커밋하는 분산 트랜잭션 프로토콜. |
| CDC (Change Data Capture) | 원본의 변경분을 잡아 대상으로 흘리는 기법. 여기선 PG 논리복제. |
| Topology (토폴로지) | 어떤 키 범위가 어떤 샤드에 있는지의 라우팅 메타데이터(ShardRange CRD). |
| Stateless 라우터 | 자체 영속 상태 없이 토폴로지만 보고 라우팅하는, 수평 확장 가능한 프록시. |
