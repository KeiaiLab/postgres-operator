# RFC 0002 — Metadata Sync 알고리즘 (`pg_dist_node` ↔ K8s)

- **상태**: Draft (P11-T1 spike와 동시 제안)
- **제출일**: 2026-04-27
- **작성자**: @keiailab/maintainers
- **코멘트 윈도우**: 14일 (마감 2026-05-11)
- **승인 기준**: 메인테이너 2/3 (GOVERNANCE.md "아키텍처 변경")
- **관련**: ADR 0001 v2 §차별화 1(Citus 1급), ADR 0002(K8s as DCS), ADR 0003(QueryRouter Stateless), ADR 0005(Plugin SDK), RFC 0001 §부록 D §Pillar 매핑(P11)
- **선행 산출물**: P10-T2(7 ExtensionPlugin 등록 + 정렬 회귀)

## 컨텍스트

본 오퍼레이터의 첫 번째 차별화(ADR 0001 v2)는 **Citus 분산 토폴로지의 1급 지원**이다. 이 차별화의 핵심 기능 한 가지를 본 RFC가 정의한다: **`pg_dist_node`(Citus 메타데이터 카탈로그)와 K8s 토폴로지(`PostgresCluster.spec.workers[]` + Service Endpoints) 사이의 자동 동기화**.

PGO/CNPG는 이 기능을 제공하지 않는다 — 사용자가 직접 `citus_add_node`/`citus_update_node`/`citus_remove_node`를 호출해야 한다. 본 오퍼레이터는 사용자가 `PostgresCluster.spec.workers`만 선언하면 reconciler가 권위적으로 sync 한다.

## 결정

### 1. Node 모델

본 오퍼레이터는 다음 7-tuple로 단일 Citus 노드를 표현한다.

```go
type Node struct {
    Group            int32  // Citus pg_dist_node.groupid
    Name             string // hostname (Pod DNS)
    Port             int32  // PG 포트 (5432)
    Role             string // "coordinator" | "worker"
    Pool             string // worker pool name (coordinator는 빈 문자열)
    Index            int32  // 같은 pool 내 ordinal (StatefulSet 순서)
    ShouldHaveShards bool   // pg_dist_node.shouldhaveshards
}
```

`Name`은 K8s headless Service가 보장하는 안정적 Pod DNS:

```
<sts-name>-<index>.<svc-name>.<namespace>.svc.cluster.local
```

예: `orders-worker-pool-a-0.orders-worker-pool-a.default.svc.cluster.local`

### 2. groupid 할당 규칙

| 역할 | groupid |
|---|---|
| Coordinator | **0** (Citus 표준) |
| Worker pool i (`spec.workers[i]`) | **i + 1** (1, 2, 3, ...) |

같은 worker pool 내 모든 멤버(StatefulSet replica)는 **동일 groupid를 공유**한다. Citus는 같은 groupid의 노드들을 streaming replication HA pair로 인식한다.

**불변식**: 사용자가 `spec.workers[]`의 순서를 바꾸면 groupid가 재배치되어 분산 테이블 shard 위치가 깨진다. 이를 막기 위해:
- webhook이 update 시 `spec.workers[].name` 순서 변경을 거절(P9-T5 시점). 본 RFC는 시그니처 동결만 하고 강제는 후속 RFC 0010(Upgrade)에 위임.
- alpha 단계(현 시점)에서는 사용자에게 "순서 고정"을 가이드 문서로만 안내.

### 3. ShouldHaveShards 기본값

| 역할 | 기본값 | 사유 |
|---|---|---|
| Coordinator | **false** | ADR 0003 §Coordinator: coordinator는 메타데이터+DDL 게이트웨이 역할에 집중. 분산 테이블 shard 미보유가 권장. 사용자가 `spec.coordinator.shouldHaveShards=true`로 override 가능. |
| Worker | **true** | 분산 테이블 shard 보유가 worker의 본질 책임 |

### 4. 변환 함수 — `DesiredNodes`

```go
func DesiredNodes(cluster *postgresv1alpha1.PostgresCluster) []Node
```

**입력**: `PostgresCluster` CR
**출력**: 기대 `pg_dist_node` 등재 항목들의 평탄화 리스트

**알고리즘** (M0 spike, 단순화):

1. coordinator: members 수만큼 Node 생성, 모두 group=0, role="coordinator". Name은 `<cluster>-coordinator-<idx>.<svc>.<ns>.svc.cluster.local` 형식.
2. 각 worker pool i: members 수만큼 Node 생성, 모두 group=i+1, role="worker", pool=name.

**결정성**: 동일 입력에 대해 동일 출력. 정렬 키는 (Group, Index).

**M1 보강 (후속 task)**:
- failover 시점에 primary가 아닌 standby가 응답할 수 있는 문제 처리
- Service Endpoints(Pod ready 상태)를 추가 입력으로 받아 unready Pod 제외

### 5. diff 알고리즘 — `ComputeActions`

```go
type Action struct {
    Op   string // "add" | "update" | "remove"
    Node Node   // 대상
}

func ComputeActions(current, desired []Node) []Action
```

**알고리즘**:

1. 두 슬라이스를 (group, name, port) 키로 map화
2. desired에 있고 current에 없으면 → `add`
3. desired에 있고 current에 있으면 → 필드 비교 → 다르면 `update`
4. current에 있고 desired에 없으면 → `remove`
5. 결과를 안정 정렬: remove → update → add (분산 테이블 가용성 보전 위해 add를 마지막)

**결정성**: 입력 슬라이스의 순서와 무관하게 동일 결과.

### 6. SQL 실행 — `SQLExecutor` 인터페이스

```go
type SQLExecutor interface {
    Apply(ctx context.Context, actions []Action) error
}
```

**구현체**:
- `LibPQExecutor` (production, P11-M1): `database/sql` + `github.com/lib/pq`로 coordinator primary에 연결, 각 Action을 다음 SQL로 변환:
  - `add` → `SELECT citus_add_node('<name>', <port>, groupid => <group>, ...)`
  - `update` → `SELECT citus_update_node(<old_id>, '<new_name>', <new_port>)` (pg_dist_node에서 nodeid lookup 후)
  - `remove` → `SELECT citus_remove_node('<name>', <port>)`
- `NullExecutor` (spike 기본값, M0): no-op. desired 상태만 Status에 반영, SQL은 호출 안 함. envtest와 cmd/main.go 양쪽이 본 구현을 사용.
- `MockExecutor` (단위 테스트): 호출된 Actions를 기록만 함.

**선택**: cmd/main.go가 컴파일 시점에 LibPQExecutor 또는 NullExecutor를 선택해 reconciler에 주입. 향후 RFC 0009(QueryRouter CRD 분리)에서 SQLExecutor 자체가 RouterPlugin 인터페이스에 통합될지 검토.

### 7. 동기화 시점

본 RFC v1은 **reconcile 매 회 권위적 동기화**를 채택한다.

- 트리거: PostgresCluster CR 변경 + Owns()로 등록된 모든 하위 자원 변경
- 절차: refreshStatus → 모든 자원 ready 확인 → DesiredNodes → (현재 pg_dist_node 조회) → ComputeActions → SQLExecutor.Apply
- 모든 자원이 ready가 아니면 sync skip + ConditionMetadataInSync=False(Reason=Progressing)

**대안 (기각)**: Citus metadata sync 자체(`pg_dist_node` → workers)에 위임 후 우리는 coordinator만 갱신. 선택 안 함 — 사용자가 `pg_dist_node`를 직접 수정한 drift 회복이 안 됨.

### 8. 동시성·순서

- coordinator primary 1대에서만 변경 SQL 실행
- 동일 PostgresCluster에 대한 reconcile은 controller-runtime이 직렬화(work queue 단일 worker per CR)
- 다른 PostgresCluster는 독립적으로 병렬 가능

### 9. 회복 메커니즘

- Drift 감지 (사용자가 직접 `citus_remove_node` 호출 등): 다음 reconcile 매 회 ComputeActions가 desired vs current 차이를 감지해 자동 복원
- coordinator failover: P2(election) 통합 시 새 primary로 SQLExecutor 재연결
- 부분 실패 (Action N개 중 K개만 적용): 다음 reconcile에서 잔여 Action 자동 적용 (멱등성)

### 10. Status 반영

`PostgresClusterStatus.Topology`에 다음 필드를 채운다 (RFC 0001 시그니처와 일치):

- `Coordinator.Primary`/`Replicas`/`LeaseHolder`: coordinator Pod 상태 (P2 통합 후 의미 부여)
- `Workers[].Name`: pool 이름
- `Workers[].DistNode.GroupID`: groupid 할당 결과
- `Workers[].DistNode.NodeName`/`NodePort`/`ShouldHaveShards`: 기대 값

`ConditionMetadataInSync`:
- True: SQLExecutor.Apply 마지막 호출이 0 actions 또는 성공
- False/Progressing: 자원 미준비 또는 마지막 Apply 실패
- Unknown/NotApplicable: NullExecutor 사용 중 (M0 spike 기본값)

## 강제 메커니즘

1. **`internal/citus/topology.go`의 DesiredNodes는 순수 함수** — 단위 테스트 ≥3건 (coordinator only / 1 pool / N pool)
2. **ComputeActions 결정성** — 입력 순서 셔플 회귀 테스트
3. **SQLExecutor 인터페이스 동결** — `var _ SQLExecutor = ...` 컴파일 가드
4. **본 RFC 변경(Node 모델, groupid 규칙)은 RFC 갱신 필수**

## 트레이드오프

- **groupid 순서 고정의 부담**: 사용자가 worker pool 순서를 바꾸면 분산 테이블이 깨짐. P9(Upgrade) RFC 0010에서 webhook 거절로 강제 예정. alpha 동안은 가이드 문서로만 안내.
- **NullExecutor 기본값의 의도**: spike 단계에서 실제 SQL 호출은 envtest로 검증 불가능. desired state 표면화만으로도 reconciler 통합과 Status 정합성을 검증 가능. P11-M1에서 LibPQExecutor + 실 PG 통합 e2e로 보강.
- **single coordinator primary 가정**: P2(election) 통합 전까지 "primary가 누구인가"는 단순화 (coordinator-0 가정). P2 후 K8s lease holder를 follow.

## 결과

- Pillar P11이 M0(spike) 도달 가능
- ADR 0001 v2의 차별화 1(Citus 1급)이 코드 차원에서 시작됨
- 본 RFC는 P11-T1 코드와 같은 PR로 commit (분리 시 정합성 깨짐)

## 검증 (How to verify)

```bash
cd /Users/phil/WorkSpace/public/postgresql-operator

# 1) topology + sync 단위 테스트
go test ./internal/citus/... -v

# 2) reconciler 통합(envtest)에서 Status.Topology.Workers[].DistNode 채워짐 확인
go test ./internal/controller/... -v -run "P11"

# 3) RFC 동결 시그니처 회귀 (DesiredNodes·ComputeActions·SQLExecutor)
go vet ./...
```
