# RFC-0001: PostgresCluster CRD v2 (redefinition)

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P1 (~v0.4.0)
- Supersedes: prior v0.x RFCs 0001–0005 (the previous external-extension-backed model is abandoned; history preserved in git)

## §1 Summary

Redefine the `spec` / `status` of the `PostgresCluster` CRD. Abandon the previous dual-backend `sharding.backend` model from 0.2.0-alpha, and adopt a single path of **self-built native distributed SQL**. Introduce 4 core sections — `shards`, `router`, `autoSplit`, `backup` — and add a `shards[]` array under status to expose the multi-shard topology. In Phase P1, only `shards.initialCount=1` (single-shard) is supported as GA; the `router` / `autoSplit` fields are activated from P2 onward.

## §2 Motivation

### §2.1 Reason for abandonment

The previous `PostgresClusterSpec` abstracted two backends (external extension / vanilla) via the `ShardingPlugin` interface in RFC 0005 (Phase 2A freeze). With the user decision (2026-05-02) to remove all external distributed-SQL dependencies and license-incompatible backends and unify on *self-built distributed SQL*:

- Backend abstraction itself becomes over-engineering (only 1 implementation).
- The `sharding.backend` enum loses meaning.
- Per-shard metadata (StatefulSet name, primary endpoint, size) is missing from status.

### §2.2 User scenarios

**Scenario 1: start single-shard**
```yaml
apiVersion: postgresql.tools/v1alpha1
kind: PostgresCluster
metadata: { name: foo, namespace: prod }
spec:
  postgresVersion: "18"
  shards: { initialCount: 1, storage: { size: 50Gi }, replicas: 2 }
  backup: { schedule: "0 2 * * *" }
```
The user connects directly to the primary Service (`foo-shard-0-primary`) without going through the router. When traffic grows during operation, upgrade to P2 or later and then add shards.

**Scenario 2: multi-shard + router**
```yaml
spec:
  shards: { initialCount: 4, storage: { size: 100Gi }, replicas: 2 }
  router: { replicas: 3, autoscale: { enabled: true, minReplicas: 2, maxReplicas: 20 } }
  autoSplit: { enabled: true, triggers: { sizeThresholdGB: 100 } }
```
The application connects to a single `foo-router` Service. The operator auto-creates the router Deployment, the ShardRange default, and the KEDA ScaledObject.

### §2.3 Non-goals

- Multi-tenant DB-per-tenant isolation (separate RFC, P5+).
- Importing external PostgreSQL (already-running PG import) — P7+.
- Declarative HBA / role management — separate CRD in P3.

## §3 Design / Specification

### §3.1 Full spec structure

```yaml
apiVersion: postgresql.tools/v1alpha1
kind: PostgresCluster
metadata: { name, namespace }
spec:
  postgresVersion: "18"            # required, enum ["17","18"]
  shardingMode: native              # enum ["native","none"], default "none"
  shards:
    initialCount: 4                # required, min 1
    storage:
      size: 100Gi                  # required
      storageClass: gp3-iops       # optional
      accessModes: ["ReadWriteOnce"]
    replicas: 2                    # per-shard async replica count, default 1
    resources: { requests, limits }
    affinity: { ... }              # standard PodAffinity
    tolerations: [...]
  router:
    enabled: true                  # default true if shardingMode=native
    replicas: 3
    autoscale:
      enabled: true
      minReplicas: 2
      maxReplicas: 20
      targetCPU: 70
      targetActiveConnections: 1000
    resources: { requests, limits }
  autoSplit:
    enabled: false
    requireApproval: true          # production safety
    triggers:
      sizeThresholdGB: 100
      p99LatencyMs: 200
      cpuPercent: 70
      durationMinutes: 10
  backup:
    enabled: true
    schedule: "0 2 * * *"
    retention: { full: 7d, incremental: 24h, walArchive: 14d }
    repo: { type: s3, bucket, region, ... }
  monitoring:
    serviceMonitor: { enabled: true, interval: 30s }
    prometheusRule: { enabled: true }
```

### §3.2 Full status structure

```yaml
status:
  phase: Provisioning | Ready | Degraded | Reconfiguring
  observedGeneration: 7
  shards:
    - name: shard-0
      ordinal: 0
      primary:
        pod: foo-shard-0-0
        endpoint: foo-shard-0-primary.prod.svc:5432
        ready: true
      replicas:
        - { pod: foo-shard-0-1, endpoint: ..., lagBytes: 0, ready: true }
        - { pod: foo-shard-0-2, endpoint: ..., lagBytes: 142, ready: true }
      sizeBytes: 53687091200       # 50 GiB
      lastSplit: "2026-04-12T03:14:22Z"
  router:
    replicas: 3
    readyReplicas: 3
    endpoint: foo-router.prod.svc:5432
  conditions:
    - type: Ready
      status: "True"
      reason: AllShardsReady
      lastTransitionTime: "..."
    - type: AutoSplitEligible
      status: "False"
      reason: BelowThreshold
```

### §3.3 Validation rules (kubebuilder markers)

```go
// PostgresClusterSpec defines desired state.
type PostgresClusterSpec struct {
    // +kubebuilder:validation:Enum=17;18
    // +kubebuilder:default="18"
    PostgresVersion string `json:"postgresVersion"`

    // +kubebuilder:validation:Enum=native;none
    // +kubebuilder:default="none"
    ShardingMode string `json:"shardingMode,omitempty"`

    // +kubebuilder:validation:Required
    Shards ShardsSpec `json:"shards"`

    // +optional
    Router *RouterSpec `json:"router,omitempty"`

    // +optional
    AutoSplit *AutoSplitSpec `json:"autoSplit,omitempty"`

    // +optional
    Backup *BackupSpec `json:"backup,omitempty"`

    // +optional
    Monitoring *MonitoringSpec `json:"monitoring,omitempty"`
}

type ShardsSpec struct {
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=1024
    InitialCount int32 `json:"initialCount"`

    // +kubebuilder:validation:Required
    Storage StorageSpec `json:"storage"`

    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=15
    // +kubebuilder:default=1
    Replicas int32 `json:"replicas,omitempty"`

    // +optional
    Resources corev1.ResourceRequirements `json:"resources,omitempty"`
    // +optional
    Affinity *corev1.Affinity `json:"affinity,omitempty"`
    // +optional
    Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type RouterSpec struct {
    // +kubebuilder:default=true
    Enabled bool `json:"enabled,omitempty"`

    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:default=2
    Replicas int32 `json:"replicas,omitempty"`

    // +optional
    Autoscale *RouterAutoscaleSpec `json:"autoscale,omitempty"`
    // +optional
    Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type AutoSplitSpec struct {
    // +kubebuilder:default=false
    Enabled bool `json:"enabled,omitempty"`
    // +kubebuilder:default=true
    RequireApproval bool `json:"requireApproval,omitempty"`
    // +optional
    Triggers *AutoSplitTriggers `json:"triggers,omitempty"`
}

type AutoSplitTriggers struct {
    // +kubebuilder:validation:Minimum=10
    SizeThresholdGB int32 `json:"sizeThresholdGB,omitempty"`
    // +kubebuilder:validation:Minimum=10
    P99LatencyMs int32 `json:"p99LatencyMs,omitempty"`
    // +kubebuilder:validation:Minimum=10
    // +kubebuilder:validation:Maximum=100
    CPUPercent int32 `json:"cpuPercent,omitempty"`
    // +kubebuilder:validation:Minimum=1
    DurationMinutes int32 `json:"durationMinutes,omitempty"`
}
```

CEL validation (kubebuilder v0.15+):

```go
// +kubebuilder:validation:XValidation:rule="self.shardingMode != 'native' || self.shards.initialCount >= 1",message="native sharding requires shards.initialCount >= 1"
// +kubebuilder:validation:XValidation:rule="!has(self.router) || self.shardingMode == 'native'",message="router is only valid when shardingMode=native"
// +kubebuilder:validation:XValidation:rule="!has(self.autoSplit) || self.autoSplit.enabled == false || self.shardingMode == 'native'",message="autoSplit requires shardingMode=native"
```

### §3.4 Status machine

```
Provisioning ──(all shards ready)──▶ Ready
Ready ──(shard add/split/replica scale)──▶ Reconfiguring ──▶ Ready
Ready ──(any shard primary down > 30s)──▶ Degraded ──(recover)──▶ Ready
```

`conditions[]` standard types: `Ready`, `Progressing`, `BackupHealthy`, `AutoSplitEligible`, `RouterReady` (P2+).

### §3.5 Default behavior

| Field | default | Note |
|---|---|---|
| `postgresVersion` | `"18"` | LTS |
| `shardingMode` | `"none"` | router/autoSplit disabled |
| `shards.replicas` | `1` | sync replica 1 + async 0 |
| `router.replicas` | `2` | HA minimum |
| `autoSplit.enabled` | `false` | accident prevention |
| `autoSplit.requireApproval` | `true` | production safety |
| `backup.enabled` | `false` | requires explicit user opt-in |

## §4 Drawbacks / Trade-offs

- **Compatibility break**: existing 0.2.0-alpha users of `spec.sharding.backend` must rewrite their manifests. Allowed because of the alpha channel, but user notification (CHANGELOG breaking change) is required.
- **status bloat**: when there are 1024 shards, `status.shards[]` becomes very large (~1MB possible). Approaches the etcd object size limit (1.5MB). Consider splitting into a separate `ShardStatus` CRD from P5+.
- **field bloat**: the learning curve is steep with 12 sub-specs. Mitigation: `kubectl explain postgrescluster.spec` + 1-line helm `values.yaml` presets.

## §5 Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| **CRD split** (`PostgresCluster` + `PostgresShardSet` + `PostgresRouter`) | reconcile complexity ↑, over-engineering for single-shard users |
| **annotation-based sharding** (no CRD change) | no type safety, no kubectl explain, no IDE autocompletion |
| **Adopt an external operator's spec verbatim** | conflicts with our decision (dependency removal); permanent risk of API drift |

## §6 Open Questions

1. The name `shards.replicas` is ambiguous (per-shard async replica count vs. total shard count). → Consider renaming to `shards.replicasPerShard` at P1 implementation time.
2. AND/OR semantics of `autoSplit.triggers` (currently all AND). Whether to introduce a user-explicit expression (`expr: "size > 100 && cpu > 70"`) → decide at P5.
3. Automatic deployment of `monitoring.grafanaDashboard` is out of scope for this RFC (candidate for separate RFC 0006).

## §7 Implementation Plan

### P0 (this session)
- [x] Draft this RFC.
- [ ] Define the new spec/status in `api/v1alpha1/postgrescluster_types.go` (P1 work).

### P1 (~v0.4.0)
- [ ] Reimplement `api/v1alpha1/postgrescluster_types.go` (including kubebuilder markers).
- [ ] Verify CRD yaml generation via `make manifests`.
- [ ] Update the upsert path in `internal/controller/postgrescluster_controller.go` (single-shard reconcile).
- [ ] Unit-test CEL validation rules (`api/v1alpha1/postgrescluster_validation_test.go`).
- [ ] e2e: single-shard deployment → primary write/read → confirm status.shards[0].sizeBytes updates.

### P2~P5 (gradual activation)
- P2: reconcile `router.*` fields (create Deployment).
- P4: reconcile `autoSplit.*` fields (auto-create ShardSplitJob).
- P5: implement the `autoSplit.requireApproval` annotation gate.

### Verification commands

```bash
make manifests && make generate
go test ./api/v1alpha1/...                        # CEL + struct validation
make test                                          # all unit tests
helm template charts/postgres-operator | kubectl apply --dry-run=server -f -
make test-e2e PILLAR=p1                            # single-shard scenario
```

## §8 References

- Prior v0.x RFC-0001 (old single-backend `vanilla-default` decision) — history preserved in git.
- Prior v0.x RFC-0005 (old dual-backend `sharding-plugin-interface` abstraction) — history preserved in git.
- Kubebuilder CEL validation: https://book.kubebuilder.io/reference/markers/crd-validation.html
- Operator Capability Levels: https://operatorframework.io/operator-capabilities/ (Auto Pilot reach goal)
- ADR 0001: `docs/kb/adr/0001-self-built-distributed-sql.md`
- ADR 0004: `docs/kb/adr/0004-crd-managed-by-operator.md`
