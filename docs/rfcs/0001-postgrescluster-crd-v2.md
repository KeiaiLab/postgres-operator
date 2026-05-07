# RFC-0001: PostgresCluster CRD v2 (재정의)

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P1 (~v0.4.0)
- Supersedes: `_archive/v0.x/0001-*` ~ `0005-*` (Citus backend 의존 모델 폐기)

## §1 Summary

`PostgresCluster` CRD 의 `spec` / `status` 를 재정의한다. 기존 0.2.0-alpha 의 `sharding.backend: vanilla|citus` 이중 모델을 폐기하고, **자체 native 분산 SQL** 단일 경로를 채택한다. `shards`, `router`, `autoSplit`, `backup` 4 개 핵심 섹션을 도입하고 status 에 `shards[]` array 를 추가하여 multi-shard 토폴로지를 가시화한다. Phase P1 에서는 `shards.initialCount=1` (single-shard) 만 GA 로 지원하며 `router` / `autoSplit` 필드는 P2+ 에서 활성화된다.

## §2 Motivation

### §2.1 폐기 사유

기존 `PostgresClusterSpec` 은 RFC 0005 (Phase 2A 동결) 에서 `ShardingPlugin` 인터페이스를 통해 Citus / vanilla 두 backend 를 추상화했다. 사용자 결정 (2026-05-02) 으로 외부 분산 SQL 의존 (Citus AGPL, CockroachDB BUSL, CNPG API drift) 을 모두 제거하고 *자체 분산 SQL* 으로 단일화함에 따라:

- backend 추상화 자체가 over-engineering 이 됨 (구현체 1 개).
- `sharding.backend` enum 은 의미 상실.
- shard 단위 메타데이터 (StatefulSet 명, primary endpoint, size) 가 status 에 부재.

### §2.2 사용자 시나리오

**시나리오 1: single-shard 시작**
```yaml
apiVersion: postgresql.tools/v1alpha1
kind: PostgresCluster
metadata: { name: foo, namespace: prod }
spec:
  postgresVersion: "18"
  shards: { initialCount: 1, storage: { size: 50Gi }, replicas: 2 }
  backup: { schedule: "0 2 * * *" }
```
사용자는 router 를 거치지 않고 primary Service (`foo-shard-0-primary`) 로 직접 연결. 운영 중 트래픽 증가 시 P2 이상 업그레이드 후 shard 추가.

**시나리오 2: multi-shard + router**
```yaml
spec:
  shards: { initialCount: 4, storage: { size: 100Gi }, replicas: 2 }
  router: { replicas: 3, autoscale: { enabled: true, minReplicas: 2, maxReplicas: 20 } }
  autoSplit: { enabled: true, triggers: { sizeThresholdGB: 100 } }
```
Application 은 `foo-router` Service 1 개에만 연결. operator 가 router Deployment + ShardRange 기본값 + KEDA ScaledObject 자동 생성.

### §2.3 비목표

- multi-tenant DB-per-tenant 격리 (별도 RFC, P5+).
- 외부 PostgreSQL 흡수 (already-running PG import) — P7+.
- declarative HBA / role 관리 — P3 별도 CRD.

## §3 Design / Specification

### §3.1 spec 전체 구조

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
    replicas: 2                    # per-shard async replica 수, default 1
    resources: { requests, limits }
    affinity: { ... }              # 표준 PodAffinity
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

### §3.2 status 전체 구조

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

### §3.4 Status 머신

```
Provisioning ──(all shards ready)──▶ Ready
Ready ──(shard add/split/replica scale)──▶ Reconfiguring ──▶ Ready
Ready ──(any shard primary down > 30s)──▶ Degraded ──(recover)──▶ Ready
```

`conditions[]` 표준 type: `Ready`, `Progressing`, `BackupHealthy`, `AutoSplitEligible`, `RouterReady` (P2+).

### §3.5 Default 동작

| 필드 | default | 비고 |
|---|---|---|
| `postgresVersion` | `"18"` | LTS |
| `shardingMode` | `"none"` | router/autoSplit 비활성 |
| `shards.replicas` | `1` | sync replica 1 + async 0 |
| `router.replicas` | `2` | HA 최소 |
| `autoSplit.enabled` | `false` | 사고 방지 |
| `autoSplit.requireApproval` | `true` | production safety |
| `backup.enabled` | `false` | 사용자 명시 opt-in |

## §4 Drawbacks / Trade-offs

- **호환성 깨짐**: 기존 0.2.0-alpha `spec.sharding.backend` 사용자는 manifest 재작성 필수. alpha 채널이라 허용되지만 사용자 통지 (CHANGELOG breaking change) 필수.
- **status 비대화**: shards 가 1024 개일 때 `status.shards[]` 가 매우 커짐 (~1MB 가능). etcd object size limit (1.5MB) 근접. P5+ 에서 별도 `ShardStatus` CRD 분리 검토.
- **field bloat**: 12 개 sub-spec 으로 학습 곡선 가파름. 완화: `kubectl explain postgrescluster.spec` + helm `values.yaml` 1-line preset.

## §5 Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| **CRD 분리** (`PostgresCluster` + `PostgresShardSet` + `PostgresRouter`) | reconcile 복잡도 ↑, single-shard 사용자에게 over-engineering |
| **annotation 기반 sharding** (CRD 변경 X) | type safety 부재, kubectl explain 불가, IDE 자동완성 불가 |
| **CNPG 호환 spec 채용** | 우리 결정 (의존 제거) 와 충돌, API drift 위험 영구 부담 |

## §6 Open Questions

1. `shards.replicas` 명칭 모호 (per-shard async replica 수 vs 전체 shard 수). → P1 구현 시 `shards.replicasPerShard` 로 명명 변경 검토.
2. `autoSplit.triggers` AND/OR 의미 (현재 모두 AND). 사용자 명시 표현식 (`expr: "size > 100 && cpu > 70"`) 도입 여부 → P5 결정.
3. `monitoring.grafanaDashboard` 자동 배포는 본 RFC 범위 외 (별도 RFC 0006 candidate).

## §7 Implementation Plan

### P0 (이번 세션)
- [x] 본 RFC Draft 작성.
- [ ] `api/v1alpha1/postgrescluster_types.go` 신규 spec/status 정의 (P1 작업).

### P1 (~v0.4.0)
- [ ] `api/v1alpha1/postgrescluster_types.go` 재구현 (kubebuilder marker 포함).
- [ ] `make manifests` → CRD yaml 생성 검증.
- [ ] `internal/controller/postgrescluster_controller.go` upsert 경로 갱신 (single-shard reconcile).
- [ ] CEL validation rule 단위 테스트 (`api/v1alpha1/postgrescluster_validation_test.go`).
- [ ] e2e: single-shard 배포 → primary write/read → status.shards[0].sizeBytes 갱신 확인.

### P2~P5 (점진 활성)
- P2: `router.*` 필드 reconcile (Deployment 생성).
- P4: `autoSplit.*` 필드 reconcile (ShardSplitJob 자동 생성).
- P5: `autoSplit.requireApproval` annotation gate 구현.

### 검증 명령

```bash
make manifests && make generate
go test ./api/v1alpha1/...                        # CEL + struct validation
make test                                          # 전체 단위
helm template charts/postgres-operator | kubectl apply --dry-run=server -f -
make test-e2e PILLAR=p1                            # single-shard 시나리오
```

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §3.2
- Archive: `docs/rfcs/_archive/v0.x/0001-vanilla-default.md` (구 single-backend 결정)
- Archive: `docs/rfcs/_archive/v0.x/0005-sharding-plugin-interface.md` (구 dual-backend 추상화)
- Kubebuilder CEL validation: https://book.kubebuilder.io/reference/markers/crd-validation.html
- Operator Capability Levels: https://operatorframework.io/operator-capabilities/ (Auto Pilot 도달 목표)
- ADR 0001: `docs/kb/adr/0001-self-built-distributed-sql.md`
- ADR 0004: `docs/kb/adr/0004-crd-managed-by-operator.md`
