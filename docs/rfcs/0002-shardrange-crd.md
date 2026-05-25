# RFC-0002: ShardRange CRD — source of truth for distributed routing

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P2 (~v0.5.0)
- Supersedes: none (new)

## §1 Summary

Introduce the `ShardRange` CRD. This is the **single source of truth for the keyspace + key range → shard mapping**, and pg-router watches it to hot-reload the routing table. Previously up to 0.2.0-alpha, the sharding metadata lived in an external extension's PostgreSQL system catalog, but after the adoption of self-built distributed SQL (RFC 0001) the K8s API server replaces that role. The spec consists of `vindex` (hash/range function definition) + `ranges[]` (concrete partition boundaries), and the monotonic increase of `generation` in status is used as the cache-invalidation signal for the router.

## §2 Motivation

### §2.1 Problem

The **partition metadata** required for multi-shard routing must satisfy the following:

- **Strongly consistent**: all router replicas see the same routing table.
- **Versioned**: enables *atomic* switchover at split / merge / rebalance time.
- **Observable**: operators can immediately inspect it with `kubectl get`.
- **Declarative**: compatible with GitOps (Argo / Flux).

A PostgreSQL-extension catalog approach fails the declarative condition out of these 4, and requires an additional mechanism for multi-router synchronization. An etcd-backed CRD on the K8s API server naturally meets all 4.

### §2.2 User scenarios

**Scenario 1: initial 4-shard split**
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
The operator reconciler validates ranges for overlap / gap and then assigns `status.generation: 1`. All router pods receive the watch event and refresh their in-memory routing table.

**Scenario 2: metadata update after a split**
In the Cleanup phase of ShardSplitJob (RFC 0003), the reconciler atomically updates the ShardRange:
```yaml
ranges:
  - { lo: "0x00000000", hi: "0x1FFFFFFF", shard: shard-0 }     # half of the existing shard
  - { lo: "0x20000000", hi: "0x3FFFFFFF", shard: shard-0-1 }   # newly split shard
  - { lo: "0x40000000", hi: "0x7FFFFFFF", shard: shard-1 }
  ...
status: { generation: 2 }
```

### §2.3 Non-goals

- Multiple vindexes on the same keyspace (composite key) — separate RFC in P3+.
- Row-level lookup vindex (per-row mapping table) — when implemented in the P3 vindex extension, the metadata lives in a separate PG table.

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
  cluster: foo                    # required, PostgresCluster name
  keyspace: tenants                # required, logical partition unit (= distributed table group)
  vindex:
    type: hash                    # enum [hash, range, consistent-hash, lookup]
    column: tenant_id              # required (except for lookup type)
    function: murmur3              # enum [murmur3, fnv, crc32]; only when type=hash
    # range type: the column values themselves must be orderable
    # consistent-hash: requires the additional virtualNodes field
    virtualNodes: 1024             # consistent-hash only
    # lookup type: references a separate ShardLookup CRD (P3+)
    lookupRef: { name: tenants-lookup }
  ranges:
    - lo: "0x00000000"             # hex string (hash) or arbitrary (range)
      hi: "0x3FFFFFFF"
      shard: shard-0               # matches PostgresCluster.status.shards[].name
    - { lo: "0x40000000", hi: "0x7FFFFFFF", shard: shard-1 }
status:
  generation: 7                    # +1 every time spec changes; the refresh signal for router watches
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

### §3.2 vindex type spec

| type | column | function | additional fields | description |
|---|---|---|---|---|
| `hash` | required | required | — | match the result of `function(column) % 2^32` against ranges |
| `range` | required | — | — | match the column value (orderable type) itself against ranges |
| `consistent-hash` | required | required | `virtualNodes` | a virtual node on the hash ring → physical shard |
| `lookup` | — | — | `lookupRef` | row-level mapping (external PG table). P3+ |

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

Complex constraints (overlap / gap) exceed CEL → validate via admission webhook + reconciler.

### §3.4 Reconciler responsibilities

```go
func (r *ShardRangeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var sr v1alpha1.ShardRange
    if err := r.Get(ctx, req.NamespacedName, &sr); err != nil { ... }

    // 1. validate ranges for overlap / gap
    if err := validateRangesNoOverlapNoGap(sr.Spec.Ranges, sr.Spec.Vindex); err != nil {
        return r.setCondition(ctx, &sr, "Valid", "False", "RangeError", err.Error())
    }

    // 2. validate shard existence (cross-ref with PostgresCluster.status.shards)
    var pc v1alpha1.PostgresCluster
    if err := r.Get(ctx, types.NamespacedName{Name: sr.Spec.Cluster, Namespace: sr.Namespace}, &pc); err != nil { ... }
    if missing := findMissingShards(sr.Spec.Ranges, pc.Status.Shards); len(missing) > 0 {
        return r.setCondition(ctx, &sr, "ShardsExist", "False", "ShardMissing", fmt.Sprintf("%v", missing))
    }

    // 3. detect spec change → status.generation++
    if sr.Generation != sr.Status.ObservedGeneration {
        sr.Status.Generation++
        sr.Status.ObservedGeneration = sr.Generation
        r.Status().Update(ctx, &sr)
    }

    return ctrl.Result{}, nil
}
```

### §3.5 Router watch pattern

The router watches ShardRange via `informers.NewSharedInformerFactory()`:

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

`compileVindex` compiles ranges into a sorted binary search tree (or, for hash, an array + binary search). Lookup latency target: **P99 < 10μs**.

### §3.6 Atomic update (at split time)

When the Cleanup phase of ShardSplitJob updates a ShardRange, it must use server-side apply or optimistic concurrency (resourceVersion). If two splits try to update the same ShardRange simultaneously, the K8s API conflict triggers a reconciler retry.

```go
patch := client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})
sr.Spec.Ranges = newRanges
if err := r.Patch(ctx, &sr, patch); err != nil {
    if apierrors.IsConflict(err) {
        return ctrl.Result{Requeue: true}, nil   // the operator retries automatically
    }
    return ctrl.Result{}, err
}
```

## §4 Drawbacks / Trade-offs

- **etcd load**: with 1024 ranges + frequent splits, the ShardRange object becomes ~tens of KB with frequent updates. Need to consider the etcd write QPS limit. Mitigation: split frequency in practice is 1~2 per hour (operational reality), and the object is single → load is negligible.
- **K8s API dependency SPOF**: when the API server is down, the router operates on a stale routing table (read-only fallback). Writes are rejected → availability degrades. Mitigation: router LRU cache + the `--rejectWritesIfStale=300s` option.
- **CRD evolution cost**: each new vindex type addition (P3's range / consistent-hash / lookup) requires a CRD spec change. Allowed because of the v1alpha1 channel.

## §5 Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| **PG system catalog** (external-extension approach) | not declarative, requires an additional mechanism for multi-router sync, incompatible with GitOps |
| **Separate metadata service** (e.g., direct etcd) | K8s already provides etcd; running an additional service is operational burden |
| **Inline within PostgresCluster.spec** | spec bloat, every split updates PostgresCluster itself → side effects (router/HPA reconcile) |
| **ConfigMap** | no versioning, no validation, awkward to express ownerRef |

## §6 Open Questions

1. Where to store the mapping table itself for a lookup vindex? (Separate ShardLookup CRD vs. a dedicated table inside PG) → decide in P3.
2. The type representation of `lo`/`hi` for a range vindex (currently string). Supporting int / time / uuid etc. multi-type → CEL validation has limits, delegate to a P3 webhook.
3. How to express colocated tables (joinable) across keyspaces — `colocationGroup: foo` annotation? A separate CRD? → decide in the P6 distributed JOIN RFC.

## §7 Implementation Plan

### P2 (~v0.5.0)

- [ ] Write `api/v1alpha1/shardrange_types.go` (with kubebuilder markers).
- [ ] `internal/controller/shardrange/controller.go` reconciler:
  - ranges overlap / gap validation
  - shard existence cross-ref
  - monotonic generation increase
- [ ] `internal/vindex/` module:
  - `hash.go` (murmur3 / fnv / crc32)
  - `compile.go` (ranges → binary-search index)
- [ ] Watch integration in the router's `internal/router/routing_table.go` (concurrent work with the P2 router).
- [ ] e2e: manually create a 4-shard ShardRange → INSERT through the router → exactly 1 row per shard.

### P3 (~v0.6.0)

- [ ] Extend vindex types (range, consistent-hash).
- [ ] Multi-type lo/hi validation via admission webhook.

### Verification commands

```bash
go test ./internal/vindex/...                      # vindex unit (golden test)
go test ./internal/controller/shardrange/...       # reconciler unit
make manifests
kubectl apply -f config/samples/shardrange-4shard.yaml
kubectl wait --for=condition=Valid shardrange/foo-tenants
make test-e2e PILLAR=p2 -- --focus="ShardRange routing"
```

Performance targets:
- vindex lookup P99 < 10μs (unit benchmark).
- ShardRange watch → router routing-table reload < 100ms (e2e).

## §8 References

- Murmur3: https://github.com/spaolacci/murmur3
- RFC 0001: PostgresCluster CRD v2
- RFC 0003: ShardSplitJob 7-step
- RFC 0004: pg-router architecture
