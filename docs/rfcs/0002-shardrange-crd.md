# RFC-0002: ShardRange CRD — 분산 라우팅의 source of truth

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P2 (~v0.5.0)
- Supersedes: 없음 (신규)

## §1 Summary

`ShardRange` CRD 를 도입한다. 이는 **keyspace + key range → shard 매핑의 단일 source of truth** 이며, pg-router 가 watch 하여 routing table 을 hot-reload 한다. 기존 0.2.0-alpha 까지는 sharding metadata 가 Citus 의 `pg_dist_partition` 시스템 카탈로그에 존재했으나, 자체 분산 SQL 채택 (RFC 0001) 후 K8s API server 가 그 역할을 대체한다. spec 은 `vindex` (해시/범위 함수 정의) + `ranges[]` (구체 분할 경계) 로 구성되고, status 의 `generation` 단조 증가가 router 의 캐시 무효화 신호로 사용된다.

## §2 Motivation

### §2.1 문제

multi-shard 라우팅에 필요한 **분할 메타데이터**는 다음 조건을 만족해야 한다:

- **stronglyconsistent**: 모든 router replica 가 동일 routing table 을 본다.
- **versioned**: split / merge / rebalance 시점에 *atomic* 전환 가능.
- **observable**: 운영자가 `kubectl get` 으로 즉시 확인.
- **declarative**: GitOps (Argo / Flux) 와 호환.

PostgreSQL extension 카탈로그 (Citus 방식) 는 위 4 조건 중 declarative 가 부재하고 다중 router 동기화에 추가 메커니즘이 필요하다. K8s API server 의 etcd-backed CRD 는 4 조건 모두 자연 충족.

### §2.2 사용자 시나리오

**시나리오 1: 초기 4-shard 분할**
```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardRange
metadata: { name: foo-tenants, namespace: prod }
spec:
  cluster: foo
  keyspace: tenants
  vindex: { type: hash, column: tenant_id, function: murmur3 }
  ranges:
    - { lo: "0x00000000", hi: "0x3FFFFFFF", shard: shard-0 }
    - { lo: "0x40000000", hi: "0x7FFFFFFF", shard: shard-1 }
    - { lo: "0x80000000", hi: "0xBFFFFFFF", shard: shard-2 }
    - { lo: "0xC0000000", hi: "0xFFFFFFFF", shard: shard-3 }
```
operator reconciler 가 ranges overlap / gap 검증 후 `status.generation: 1` 부여. 모든 router pod 가 watch event 받아 in-memory routing table 갱신.

**시나리오 2: split 후 메타데이터 갱신**
ShardSplitJob (RFC 0003) 의 Cleanup 단계에서 reconciler 가 ShardRange 를 atomic update:
```yaml
ranges:
  - { lo: "0x00000000", hi: "0x1FFFFFFF", shard: shard-0 }     # 기존 shard 절반
  - { lo: "0x20000000", hi: "0x3FFFFFFF", shard: shard-0-1 }   # 신규 split shard
  - { lo: "0x40000000", hi: "0x7FFFFFFF", shard: shard-1 }
  ...
status: { generation: 2 }
```

### §2.3 비목표

- 동일 keyspace 에 다중 vindex (composite key) — P3+ 별도 RFC.
- 행 단위 lookup vindex (per-row mapping table) — P3 vindex 확장에서 구현 시 메타데이터는 별도 PG table.

## §3 Design / Specification

### §3.1 spec / status

```yaml
apiVersion: postgresql.tools/v1alpha1
kind: ShardRange
metadata:
  name: foo-tenants
  namespace: prod
  ownerReferences:
    - apiVersion: postgresql.tools/v1alpha1
      kind: PostgresCluster
      name: foo
      controller: true
spec:
  cluster: foo                    # required, PostgresCluster 이름
  keyspace: tenants                # required, 논리 분할 단위 (= 분산 테이블 group)
  vindex:
    type: hash                    # enum [hash, range, consistent-hash, lookup]
    column: tenant_id              # required (lookup 타입 제외)
    function: murmur3              # enum [murmur3, fnv, crc32]; type=hash 일 때만
    # range 타입: column 값 자체가 정렬 가능
    # consistent-hash: virtualNodes 추가 필드
    virtualNodes: 1024             # consistent-hash 전용
    # lookup 타입: 별도 ShardLookup CRD 참조 (P3+)
    lookupRef: { name: tenants-lookup }
  ranges:
    - lo: "0x00000000"             # 16진수 문자열 (hash) 또는 임의 (range)
      hi: "0x3FFFFFFF"
      shard: shard-0               # PostgresCluster.status.shards[].name 와 매칭
    - { lo: "0x40000000", hi: "0x7FFFFFFF", shard: shard-1 }
status:
  generation: 7                    # spec 변경 시마다 +1, router watch 갱신 신호
  observedGeneration: 7
  totalRanges: 4
  rangesByShard: { shard-0: 1, shard-1: 1, shard-2: 1, shard-3: 1 }
  conditions:
    - type: Valid
      status: "True"
      reason: NoOverlapNoGap
    - type: ShardsExist
      status: "True"
      reason: AllShardsResolved
```

### §3.2 vindex 타입 spec

| type | column | function | 추가 필드 | 설명 |
|---|---|---|---|---|
| `hash` | required | required | — | `function(column) % 2^32` 결과를 range 와 매칭 |
| `range` | required | — | — | column 값 (정렬 가능 타입) 자체를 range 와 매칭 |
| `consistent-hash` | required | required | `virtualNodes` | hash ring 위 virtual node → physical shard |
| `lookup` | — | — | `lookupRef` | row 단위 mapping (외부 PG table). P3+ |

### §3.3 Validation rules

```go
type ShardRangeSpec struct {
    // +kubebuilder:validation:Required
    Cluster string `json:"cluster"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{0,62}$`
    Keyspace string `json:"keyspace"`

    // +kubebuilder:validation:Required
    Vindex VindexSpec `json:"vindex"`

    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=1024
    Ranges []ShardRangeEntry `json:"ranges"`
}

type VindexSpec struct {
    // +kubebuilder:validation:Enum=hash;range;consistent-hash;lookup
    Type string `json:"type"`

    // +optional
    Column string `json:"column,omitempty"`

    // +kubebuilder:validation:Enum=murmur3;fnv;crc32
    // +optional
    Function string `json:"function,omitempty"`

    // +kubebuilder:validation:Minimum=64
    // +kubebuilder:validation:Maximum=65536
    // +optional
    VirtualNodes int32 `json:"virtualNodes,omitempty"`

    // +optional
    LookupRef *corev1.LocalObjectReference `json:"lookupRef,omitempty"`
}

type ShardRangeEntry struct {
    // +kubebuilder:validation:Required
    Lo string `json:"lo"`

    // +kubebuilder:validation:Required
    Hi string `json:"hi"`

    // +kubebuilder:validation:Required
    Shard string `json:"shard"`
}
```

CEL:
```go
// +kubebuilder:validation:XValidation:rule="self.vindex.type != 'hash' || (has(self.vindex.column) && has(self.vindex.function))",message="hash vindex requires column + function"
// +kubebuilder:validation:XValidation:rule="self.vindex.type != 'lookup' || has(self.vindex.lookupRef)",message="lookup vindex requires lookupRef"
```

복잡한 제약 (overlap / gap) 은 CEL 한계 → admission webhook + reconciler 에서 검증.

### §3.4 Reconciler 책임

```go
func (r *ShardRangeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var sr v1alpha1.ShardRange
    if err := r.Get(ctx, req.NamespacedName, &sr); err != nil { ... }

    // 1. ranges overlap / gap 검증
    if err := validateRangesNoOverlapNoGap(sr.Spec.Ranges, sr.Spec.Vindex); err != nil {
        return r.setCondition(ctx, &sr, "Valid", "False", "RangeError", err.Error())
    }

    // 2. shard 존재 검증 (PostgresCluster.status.shards 와 cross-ref)
    var pc v1alpha1.PostgresCluster
    if err := r.Get(ctx, types.NamespacedName{Name: sr.Spec.Cluster, Namespace: sr.Namespace}, &pc); err != nil { ... }
    if missing := findMissingShards(sr.Spec.Ranges, pc.Status.Shards); len(missing) > 0 {
        return r.setCondition(ctx, &sr, "ShardsExist", "False", "ShardMissing", fmt.Sprintf("%v", missing))
    }

    // 3. spec 변경 감지 → status.generation++
    if sr.Generation != sr.Status.ObservedGeneration {
        sr.Status.Generation++
        sr.Status.ObservedGeneration = sr.Generation
        r.Status().Update(ctx, &sr)
    }

    return ctrl.Result{}, nil
}
```

### §3.5 Router 의 watch 패턴

router 는 `informers.NewSharedInformerFactory()` 로 ShardRange 를 watch:

```go
type RoutingTable struct {
    mu        sync.RWMutex
    keyspaces map[string]*KeyspaceRouting   // keyspace -> ranges + vindex
    generation int64
}

func (rt *RoutingTable) OnUpdate(old, new *v1alpha1.ShardRange) {
    if new.Status.Generation <= rt.generation { return }
    compiled := compileVindex(new.Spec.Vindex, new.Spec.Ranges)
    rt.mu.Lock()
    rt.keyspaces[new.Spec.Keyspace] = compiled
    rt.generation = new.Status.Generation
    rt.mu.Unlock()
    metrics.RoutingTableReloads.Inc()
}
```

`compileVindex` 는 ranges 를 정렬된 binary search tree (또는 hash 의 경우 array + binary search) 로 컴파일. lookup latency 목표: **P99 < 10μs**.

### §3.6 Atomic 업데이트 (split 시점)

ShardSplitJob 의 Cleanup 단계가 ShardRange 를 update 할 때 *반드시* server-side apply 또는 optimistic concurrency (resourceVersion) 사용. 두 split 이 동시에 같은 ShardRange 를 갱신하려는 경우 K8s API conflict → reconciler retry.

```go
patch := client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})
sr.Spec.Ranges = newRanges
if err := r.Patch(ctx, &sr, patch); err != nil {
    if apierrors.IsConflict(err) {
        return ctrl.Result{Requeue: true}, nil   // operator 가 자동 재시도
    }
    return ctrl.Result{}, err
}
```

## §4 Drawbacks / Trade-offs

- **etcd 부하**: ranges 가 1024 개 + 빈번한 split 시 ShardRange object 가 ~수십 KB 빈번 update. etcd write QPS 한계 고려 필요. 완화: split 빈도가 시간당 1~2 회 수준 (운영 현실), object 단일 → 부하 미미.
- **K8s API 의존성 SPOF**: API server 다운 시 router 는 stale routing table 로 동작 (read-only fallback). write 는 거부 → 가용성 저하. 완화: router 의 LRU 캐시 + `--rejectWritesIfStale=300s` 옵션.
- **CRD 진화 비용**: vindex 타입 추가 (P3 의 range / consistent-hash / lookup) 마다 CRD spec 변경. v1alpha1 채널이라 허용.

## §5 Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **PG system catalog** (Citus 방식) | declarative 부재, multi-router 동기화 추가 메커니즘 필요, GitOps 비호환 |
| **별도 metadata service** (e.g. etcd 직접 사용) | K8s 가 이미 etcd 제공, 추가 서비스 운영 부담 |
| **PostgresCluster.spec 안에 inline** | spec 비대화, split 마다 PostgresCluster 자체 update → 부수효과 (router/HPA reconcile) |
| **ConfigMap** | versioning 부재, validation 부재, ownerRef 표현 어색 |

## §6 Open Questions

1. lookup vindex 의 mapping table 자체는 어디에 저장? (별도 ShardLookup CRD vs PG 내 dedicated table) → P3 결정.
2. range vindex 의 `lo`/`hi` 타입 표현 (현재 string). int / time / uuid 등 multi-type 지원 → CEL 검증 한계, P3 webhook 으로 위임.
3. cross-keyspace 의 colocated table (JOIN 가능) 표현 — `colocationGroup: foo` annotation? 별도 CRD? → P6 분산 JOIN RFC 에서 결정.

## §7 Implementation Plan

### P2 (~v0.5.0)

- [ ] `api/v1alpha1/shardrange_types.go` 작성 (kubebuilder marker).
- [ ] `internal/controller/shardrange/controller.go` reconciler:
  - ranges overlap / gap 검증
  - shard 존재 cross-ref
  - generation 단조 증가
- [ ] `internal/vindex/` 모듈:
  - `hash.go` (murmur3 / fnv / crc32)
  - `compile.go` (ranges → binary search 인덱스)
- [ ] router 의 `internal/router/routing_table.go` watch 통합 (P2 router 와 동시 작업).
- [ ] e2e: 4-shard 수동 ShardRange 생성 → router 통한 INSERT → 각 shard 1 row 정확.

### P3 (~v0.6.0)

- [ ] vindex 타입 확장 (range, consistent-hash).
- [ ] admission webhook 으로 multi-type lo/hi 검증.

### 검증 명령

```bash
go test ./internal/vindex/...                      # vindex 단위 (golden test)
go test ./internal/controller/shardrange/...       # reconciler 단위
make manifests
kubectl apply -f config/samples/shardrange-4shard.yaml
kubectl wait --for=condition=Valid shardrange/foo-tenants
make test-e2e PILLAR=p2 -- --focus="ShardRange routing"
```

성능 목표:
- vindex lookup P99 < 10μs (단위 벤치마크).
- ShardRange watch → router routing table reload < 100ms (e2e).

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §3.2, §3.3
- Vitess VSchema (참조 only, 코드 차용 없음): https://vitess.io/docs/reference/features/vschema/
- Citus shard metadata (참조 only): https://docs.citusdata.com/en/stable/develop/api_metadata.html
- Murmur3: https://github.com/spaolacci/murmur3
- RFC 0001: PostgresCluster CRD v2
- RFC 0003: ShardSplitJob 7-step
- RFC 0004: pg-router architecture
