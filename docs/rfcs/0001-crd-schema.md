# RFC 0001 — CRD Schema (v1alpha1)

- **상태**: Draft
- **제출일**: 2026-04-26
- **작성자**: @keiailab/maintainers (TBD assigned)
- **코멘트 윈도우**: 14일 (마감 2026-05-10)
- **승인 기준**: 메인테이너 2/3 이상 찬성 (GOVERNANCE.md "아키텍처 변경" 절차)
- **관련**: ADR 0001 (Citus 표준 + QueryRouter), ADR 0002 (Patroni 미사용), ADR 0003 (QueryRouter Stateless)
- **선행 산출물**: 분석 계획 A1
- **후행 RFC**: 0002 (Metadata Sync), 0005 (Router Statelessness Gates), 0006 (Security/RBAC), 0007 (Observability), 0008 (Distributed Tables)

## 컨텍스트

Phase 1은 `PostgresCluster` CRD 정의와 정적 부트스트랩(StatefulSet/Deployment 생성)을 산출한다. Phase 1 코딩 착수 전, 모든 CRD의 **그룹/버전/이름·필수 필드·검증 규칙**이 확정되어야 다음을 막을 수 있다.

1. **Phase 1~13 전체에 영향하는 비가역적 결정** — 그룹명·버전 변경은 v1 GA 후 호환성 깨짐.
2. **컨트롤러 간 상호 의존** — `DistributedTable` reconciler는 `PostgresCluster`를 owner reference로 가지므로 두 CRD의 명명·식별 규약이 일관해야 함.
3. **Webhook 검증 누락** — ADR 0003의 무상태 5조건이 CRD 스키마 단계에서부터 표현되지 않으면 런타임 우회 위험.

본 RFC는 v1alpha1의 모든 CRD 골격을 한 번에 확정한다. **비-Phase 1 CRD의 의미론(예: DistributedTable의 colocation 정책)** 은 후행 RFC(0008 등)에 위임하되, **타입 시그니처와 필드 이름은 본 RFC에서 동결**한다.

## 결정

### 1. 그룹·버전·도메인

| 항목 | 값 | 근거 |
|---|---|---|
| API Group | `postgres.keiailab.io` | ADR 0001 §CRD 루트 표현 예시, `PROJECT.domain=keiailab.io` |
| Version | `v1alpha1` | breaking change 허용, GA 전 |
| Layout | `go.kubebuilder.io/v4`, multigroup=false | `PROJECT` 파일 그대로 |
| Scope | Namespaced (모든 CRD) | 멀티테넌시·RBAC 분리 용이 |

### 2. CRD 목록 (v1alpha1 동결 대상)

| Kind | Phase 도입 | Owner | 본 RFC 범위 |
|---|---|---|---|
| `PostgresCluster` | 1 | (root) | **상세 스키마 동결** |
| `DistributedTable` | 5 | `PostgresCluster` | 타입 시그니처만 동결, 의미론은 RFC 0008 |
| `ReferenceTable` | 5 | `PostgresCluster` | 타입 시그니처만 동결, 의미론은 RFC 0008 |
| `RebalanceJob` | 6 | `PostgresCluster` | 타입 시그니처만 동결 |
| `ShardPlacementPolicy` | 7 | `PostgresCluster` | 타입 시그니처만 동결 |
| `BackupJob` | 4 | `PostgresCluster` | 타입 시그니처만 동결, 의미론은 RFC 0004 |
| `PgUser` | 8 | `PostgresCluster` | 타입 시그니처만 동결, RBAC은 RFC 0006 |
| `PgDatabase` | 8 | `PostgresCluster` | 타입 시그니처만 동결 |

### 3. PostgresCluster 상세 스키마 (Phase 1 최소집합)

```go
// api/v1alpha1/postgrescluster_types.go (개념 초안)

type PostgresCluster struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   PostgresClusterSpec   `json:"spec"`
    Status PostgresClusterStatus `json:"status,omitempty"`
}

type PostgresClusterSpec struct {
    // 필수: PG/Citus 버전. matrix.go의 supported 조합과 일치해야 함.
    Version VersionSpec `json:"version"`

    // 필수: Coordinator HA RS.
    Coordinator CoordinatorSpec `json:"coordinator"`

    // 필수: 1+ Worker pool. 각 pool은 자체 HA RS.
    Workers []WorkerPoolSpec `json:"workers"`

    // 필수: stateless QueryRouter 풀 (replicas >= 1).
    Routers RouterSpec `json:"routers"`

    // 선택: 활성화할 PG/Citus 확장 (예: pgvector, pg_cron, postgis).
    Extensions []ExtensionSpec `json:"extensions,omitempty"`

    // 선택: development | production. 디폴트 production.
    // development는 members 하한·스토리지 검증 완화 (quickstart용).
    Deployment DeploymentMode `json:"deployment,omitempty"`
}

type VersionSpec struct {
    // "16" | "17" | "18". "18"은 feature gate "PostgresEighteen" 필요.
    Postgres string `json:"postgres"`
    // "12.1" | "13.0" 등 minor 단위.
    Citus string `json:"citus"`
}

type CoordinatorSpec struct {
    // 홀수만 허용 (split-brain 방지, ADR 0003 §강제 메커니즘).
    // production: members >= 3 권장. development: members=1 허용.
    Members int32 `json:"members"`
    Storage StorageSpec `json:"storage"`
    Resources corev1.ResourceRequirements `json:"resources,omitempty"`
    // 선택: ShouldHaveShards. 디폴트 false (ADR 0003 §Coordinator).
    ShouldHaveShards *bool `json:"shouldHaveShards,omitempty"`
}

type WorkerPoolSpec struct {
    // pool 식별자. 동일 클러스터 내 unique. DNS-1123 label.
    Name string `json:"name"`
    // 홀수만 허용. production: members >= 3.
    Members int32 `json:"members"`
    Storage StorageSpec `json:"storage"`
    Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type RouterSpec struct {
    // replicas >= 1. HPA 부착 시 별도 HPA CR로 관리(본 CRD에서 직접 HPA 필드 두지 않음).
    Replicas int32 `json:"replicas"`
    Resources corev1.ResourceRequirements `json:"resources,omitempty"`
    // PgBouncer 사이드카 설정.
    PgBouncer PgBouncerSpec `json:"pgbouncer,omitempty"`

    // ❌ Storage 필드 의도적으로 부재 — ADR 0003 무상태 강제.
    // ❌ ShouldHaveShards 필드 부재 — 항상 false 강제.
}

type StorageSpec struct {
    Size resource.Quantity `json:"size"`
    StorageClassName *string `json:"storageClassName,omitempty"`
    AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

type ExtensionSpec struct {
    Name string `json:"name"`              // 예: "pgvector"
    Version string `json:"version,omitempty"`
}

type PgBouncerSpec struct {
    // transaction (디폴트) | session | statement.
    PoolMode string `json:"poolMode,omitempty"`
    // 백엔드 connection 상한 (per Pod).
    MaxClientConn *int32 `json:"maxClientConn,omitempty"`
}

type DeploymentMode string
const (
    DeploymentProduction DeploymentMode = "production" // 디폴트
    DeploymentDevelopment DeploymentMode = "development"
)
```

#### Status

```go
type PostgresClusterStatus struct {
    // 표준 conditions: Ready, CoordinatorReady, WorkersReady, RoutersReady, MetadataInSync.
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // 토폴로지 현재 상태 (메타데이터 동기화 결과 반영).
    Topology TopologyStatus `json:"topology,omitempty"`

    // 활성 채널 (matrix.go의 stable | beta | preview-pg18).
    Channel string `json:"channel,omitempty"`

    // 마지막으로 reconcile된 spec 해시 (drift 추적).
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

type TopologyStatus struct {
    Coordinator NodeStatus `json:"coordinator"`
    Workers []WorkerPoolStatus `json:"workers,omitempty"`
    Routers RouterPoolStatus `json:"routers"`
}

type NodeStatus struct {
    Primary string `json:"primary,omitempty"`         // Pod 이름
    Replicas []string `json:"replicas,omitempty"`
    LeaseHolder string `json:"leaseHolder,omitempty"` // K8s lease 보유자
}

type WorkerPoolStatus struct {
    Name string `json:"name"`
    NodeStatus
    // pg_dist_node에 등록된 (host, port, shouldhaveshards).
    DistNode *DistNodeRef `json:"distNode,omitempty"`
}

type RouterPoolStatus struct {
    ReadyReplicas int32 `json:"readyReplicas"`
    // 가장 큰 router_metadata_lag_seconds (모든 router pod 중 max).
    MaxMetadataLagSeconds *float64 `json:"maxMetadataLagSeconds,omitempty"`
}

type DistNodeRef struct {
    GroupId int32 `json:"groupId"`
    NodeName string `json:"nodeName"`
    NodePort int32 `json:"nodePort"`
    ShouldHaveShards bool `json:"shouldHaveShards"`
}
```

### 4. 검증 규칙 (Validating Webhook 후보)

본 표는 RFC 0005(Router Statelessness Gates)와 일부 중복되나, CRD 스키마 단계에서 **표현 가능한 제약**을 모두 명시한다.

| 규칙 | 사유 | 출처 |
|---|---|---|
| `routers`에 `volumeClaimTemplates`/`storage` 필드 부재 → 타입 단계에서 강제 | ADR 0003 무상태 | 스키마 (필드 자체 부재) |
| `coordinator.members`는 홀수, ≥1 | split-brain 방지 | ADR 0003 §강제, webhook |
| `workers[].members`는 홀수, ≥1 | 동일 | webhook |
| `routers.replicas` ≥ 1 | 가용성 | webhook |
| `(spec.version.postgres, spec.version.citus)`는 `matrix.IsSupported` 통과 | 호환성 | webhook + `internal/version/matrix.go:52` |
| `spec.version.postgres="18"`은 feature gate `PostgresEighteen` 활성 시에만 허용 | 격리 채널 | matrix.go의 `FeatureGate` 분기 |
| `workers[].name` 동일 클러스터 내 unique, DNS-1123 label | 식별 | webhook |
| `deployment=production`이면 `coordinator.members ≥ 3`, `workers[].members ≥ 3` | 운영 안전 | webhook |
| `deployment=development`이면 `routers.replicas`≥1만 강제, 나머지 완화 | quickstart UX | webhook |
| `extensions[].name`은 화이트리스트(`pgvector`, `pg_cron`, `postgis`, ...) | 보안·호환 | webhook (Phase 12에서 확장) |

### 5. 기타 CRD 타입 시그니처 (의미론은 후행 RFC)

```go
// DistributedTable — Phase 5, RFC 0008에서 의미론 확정
type DistributedTableSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Database string `json:"database"`
    Schema string `json:"schema,omitempty"`        // 디폴트 "public"
    Table string `json:"table"`
    Distribution DistributionSpec `json:"distribution"`
    ReplicationFactor *int32 `json:"replicationFactor,omitempty"`
}

type DistributionSpec struct {
    // 분산 컬럼명. Schema-based sharding 시 빈 문자열 허용.
    Column string `json:"column,omitempty"`
    // shard 수. 디폴트는 RFC 0008에서 결정.
    ShardCount *int32 `json:"shardCount,omitempty"`
    // colocation 그룹명.
    ColocationGroup string `json:"colocationGroup,omitempty"`
}

// ReferenceTable — Phase 5
type ReferenceTableSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Database string `json:"database"`
    Schema string `json:"schema,omitempty"`
    Table string `json:"table"`
}

// RebalanceJob — Phase 6
type RebalanceJobSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Window WindowSpec `json:"window"`
    // by_shard_count | by_disk_size
    Strategy string `json:"strategy"`
}
type WindowSpec struct {
    Cron string `json:"cron"`               // 예: "0 2 * * *"
    DurationSeconds int32 `json:"durationSeconds"`
}

// ShardPlacementPolicy — Phase 7
type ShardPlacementPolicySpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Zones []ZoneSpec `json:"zones"`
    TagSelectors map[string]string `json:"tagSelectors,omitempty"`
}
type ZoneSpec struct {
    Name string `json:"name"`
    NodeSelector map[string]string `json:"nodeSelector"`
}

// BackupJob — Phase 4, RFC 0004
type BackupJobSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Storage BackupStorageSpec `json:"storage"`
    Schedule string `json:"schedule,omitempty"` // cron, 빈 문자열이면 1회성
    Retention RetentionSpec `json:"retention"`
}
type BackupStorageSpec struct {
    // s3 | gcs | azure | pvc
    Type string `json:"type"`
    // 자격 증명·버킷·prefix는 Type별 sub-struct (RFC 0004에서 확정).
    S3 *S3StorageSpec `json:"s3,omitempty"`
    PVC *PVCStorageSpec `json:"pvc,omitempty"`
}
type S3StorageSpec struct {
    Bucket string `json:"bucket"`
    Prefix string `json:"prefix,omitempty"`
    Region string `json:"region"`
    // SecretRef는 access_key/secret_key. IRSA 사용 시 nil 허용.
    SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}
type PVCStorageSpec struct {
    ClaimName string `json:"claimName"`
}
type RetentionSpec struct {
    KeepDays *int32 `json:"keepDays,omitempty"`
    KeepCount *int32 `json:"keepCount,omitempty"`
}

// PgUser, PgDatabase — Phase 8, RFC 0006에서 RBAC 정책
type PgUserSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Username string `json:"username"`
    PasswordSecretRef corev1.SecretKeySelector `json:"passwordSecretRef"`
    // GRANT 범위는 RFC 0006에서 정의. 본 RFC는 필드 동결만.
    Privileges []PrivilegeSpec `json:"privileges,omitempty"`
}
type PgDatabaseSpec struct {
    ClusterRef corev1.LocalObjectReference `json:"clusterRef"`
    Name string `json:"name"`
    Owner *string `json:"owner,omitempty"` // PgUser 이름
    Encoding string `json:"encoding,omitempty"` // 디폴트 UTF8
    Locale string `json:"locale,omitempty"`
}
type PrivilegeSpec struct {
    On string `json:"on"`              // database | schema | table
    Target string `json:"target"`      // ref name
    Grants []string `json:"grants"`    // SELECT | INSERT | ALL | ...
}
```

### 6. Owner Reference 규약

- 모든 비-`PostgresCluster` CRD는 `spec.clusterRef.name`으로 부모를 지목한다.
- reconciler는 생성 시점에 `metav1.OwnerReference`(controller=true)를 자동 설정한다.
- Cascade delete는 K8s GC에 위임 (별도 finalizer 없음). 단, `BackupJob`은 외부 스토리지 정리 finalizer 보유 (Phase 4 결정).

### 7. 미해결 항목 (열린 질문 → 후행 RFC로 위임)

| 질문 | 위임 대상 |
|---|---|
| `DistributionSpec.ShardCount` 디폴트 | RFC 0008 |
| `BackupStorageSpec.Type` 1차 GA 도구 (pgBackRest 권장) | RFC 0004 |
| `PgUser.Privileges` GRANT 안전 화이트리스트 | RFC 0006 |
| Coordinator failover 시 lease duration/renew 값 | RFC 0003 |
| `extensions[]` 화이트리스트 | RFC 0012 (Phase 12) |
| `RouterSpec.PgBouncer.MaxClientConn` 권장 디폴트 | RFC 0007 (관측성과 함께 결정) |

## 근거

### 모든 CRD를 한 번에 동결하는 이유
- 그룹/버전/Owner 규약은 v1 GA까지 호환성을 유지해야 함. Phase별 분산 결정은 충돌 위험.
- 타입 시그니처 동결은 후행 RFC가 의미론에 집중하게 만들어 RFC 처리 속도 향상.
- `clusterRef` 패턴이 모든 자식 CRD에 일관되게 적용됨을 보장.

### `Storage`/`ShouldHaveShards` 필드를 RouterSpec에 두지 않는 이유
- ADR 0003의 무상태 강제는 webhook 거절보다 **타입 부재**가 더 강력.
- 타입에 없으면 사용자는 YAML에 쓸 수 없고, 컨트롤러는 코드 경로에서 처리할 수 없음.

### `members`를 홀수로 강제하는 이유
- ADR 0003 §강제 메커니즘 명시. K8s lease 기반 election은 짝수에서 split-brain 위험.

### `deployment` 모드 분리
- ADR 0003 §트레이드오프: development quickstart 5분 보장. webhook이 모드별 다른 검증 적용.

## 트레이드오프

- **alpha 단계 변경 비용**: v1alpha1이므로 자유롭게 변경 가능하나, 본 RFC가 동결한 필드명을 후속 마이너에서 바꾸려면 alpha→alpha 변환이 필요.
- **Owner Reference 자동화의 어색함**: 사용자가 `clusterRef`를 명시했는데 `OwnerReferences`가 자동으로 채워지면 직관에 반할 수 있음. 문서화로 완화.
- **타입 시그니처 동결 vs 의미론 분리의 인지 부담**: "이 필드가 v1alpha1에 있지만 동작은 Phase N에서야 가능"이라는 상태가 발생. 각 필드에 `// Available: Phase N` 주석으로 표기.

## 강제 메커니즘

1. `api/v1alpha1/*_types.go`에 본 RFC의 타입 정의 그대로 반영.
2. `internal/webhook/postgrescluster_webhook.go`에 §4 검증 규칙 구현.
3. `make manifests`로 CRD YAML 자동 생성, `config/crd/bases/`에 커밋.
4. e2e 테스트 (`test/e2e/`)에 각 검증 규칙 위반 케이스 ≥ 1건 포함.
5. `docs/api/v1alpha1.md` 자동 생성 (`controller-gen` 또는 `crd-ref-docs`).
6. CI 게이트: `golangci-lint`, `kubebuilder validate`, `make test` (coverage ≥ 80% per CONTRIBUTING.md).

## 결과

- v1alpha1의 모든 CRD 타입 시그니처 동결 → Phase 1~13 reconciler 작성 시 타입 호환성 보장.
- `PostgresCluster`만 의미론까지 확정, 나머지는 후행 RFC 위임.
- 본 RFC 변경(필드 추가/삭제/타입 변경)은 GOVERNANCE.md "중간 변경"(필드 추가) 또는 "아키텍처 변경"(필드 제거/타입 변경)에 따라 처리.

## 검증 (How to verify)

본 RFC 채택 후 다음으로 검증한다:

```bash
cd /Users/phil/WorkSpace/public/postgresql-operator

# 1) 타입 정의 → CRD 매니페스트 생성이 깨지지 않는가
make manifests generate

# 2) webhook 단위 테스트 (각 검증 규칙별 케이스)
make test

# 3) sample CR 적용/거절 e2e
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_min.yaml      # 통과
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_router_pvc.yaml # 거절 예상
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_even_members.yaml # 거절 예상

# 4) 매트릭스 검증
go test ./internal/version/... -run TestIsSupported
```

## 부록 A — 샘플 CR (development 모드)

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quickstart
  namespace: default
spec:
  deployment: development
  version:
    postgres: "17"
    citus: "13.0"
  coordinator:
    members: 1
    storage:
      size: 10Gi
  workers:
  - name: pool-a
    members: 1
    storage:
      size: 20Gi
  routers:
    replicas: 1
```

## 부록 B — 샘플 CR (production 모드)

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: prod-cluster
  namespace: pg-system
spec:
  deployment: production
  version:
    postgres: "17"
    citus: "13.0"
  coordinator:
    members: 3
    storage:
      size: 100Gi
      storageClassName: fast-ssd
    resources:
      requests: { cpu: "2", memory: "8Gi" }
  workers:
  - name: pool-a
    members: 3
    storage:
      size: 500Gi
      storageClassName: fast-ssd
    resources:
      requests: { cpu: "4", memory: "16Gi" }
  - name: pool-b
    members: 3
    storage:
      size: 500Gi
      storageClassName: fast-ssd
  routers:
    replicas: 3
    pgbouncer:
      poolMode: transaction
      maxClientConn: 1000
  extensions:
  - name: pgvector
    version: "0.7"
```

## 부록 C — 변경 이력

| 날짜 | 변경 | 작성자 |
|---|---|---|
| 2026-04-26 | Draft 제출 | @keiailab/maintainers |
| 2026-04-27 | Git 추적 시작 + Addendum D 추가 (Pillar 매핑, RFC 0009 위임, 미션 재정의 반영) | @keiailab/maintainers |

---

## 부록 D — 후속 RFC 위임 + Pillar 매핑 (2026-04-27 추가)

본 RFC는 ADR 0001 v1("Citus + Stateless QueryRouter 단일 차별화") 기반으로 작성되었다. ADR 0001 v2(2026-04-27)에서 미션이 **"PGO-class 풀스택 + Citus 1급 + Plugin SDK"** 3축으로 재정의되었으나, 본 RFC가 동결한 **타입 시그니처는 모두 유효**하다. 영향 범위는 다음 두 가지로 한정된다.

### D.1 `QueryRouter` 표현 — RFC 0009로 위임

본 RFC §3은 `RouterSpec`을 `PostgresClusterSpec.routers` 서브필드로 정의했다. ADR 0001 v2에서 Plugin SDK(`RouterPlugin`) 도입이 결정되면서, **별도 CRD `QueryRouter`로 분리**할지 여부가 새로 검토 대상이 된다. 결정은 RFC 0009에 위임한다.

| 옵션 | 장점 | 단점 |
|---|---|---|
| 서브필드 (현 RFC) | `PostgresCluster` 단일 CR로 토폴로지 완결 | RouterPlugin이 PostgresCluster reconciler와 강결합 |
| 별도 CRD | RouterPlugin이 독립 reconciler 생애주기, 다중 라우터 풀 가능 | 두 CR 동기화 필요, owner reference 복잡 |

### D.2 Pillar 매핑

본 RFC가 동결한 8개 CRD의 Pillar(plan §10) 소속:

| CRD | Pillar | 도입 마일스톤 |
|---|---|---|
| `PostgresCluster` | P1 | v0.1 alpha (P1-T1) |
| `BackupJob` | P4 | v0.5 beta |
| `PgUser`, `PgDatabase` | P8 | v0.5 beta |
| `ClusterUpgrade` | P9 | v0.7 beta (RFC 0001에 미포함, 후속 RFC 0010) |
| `DistributedTable`, `ReferenceTable` | P11 | v0.5 beta (M1) |
| `RebalanceJob` | P11 | v0.7 beta |
| `ShardPlacementPolicy` | P11 | v0.7 beta |
| `QueryRouter` (RFC 0009 결정 시) | P12 | v0.7 beta |

### D.3 Plugin SDK 인터페이스 동결과의 관계

ADR 0005(작성 예정) 기반 `internal/plugin/api.go` 5종 인터페이스(`BackupPlugin`/`ExporterPlugin`/`ExtensionPlugin`/`RouterPlugin`/`AuthPlugin`)는 본 RFC의 CRD 시그니처를 직접 참조하지 않는다. 인터페이스는 reconciler 내부 호출 규약이며, CRD 시그니처는 사용자 표면이다. 두 동결은 **독립적으로 진행 가능**.

### D.4 코멘트 윈도우

원래 마감 2026-05-10. 본 Addendum 추가는 본문 시그니처를 변경하지 않으므로 코멘트 윈도우 재시작 불요 (GOVERNANCE.md "보강 변경" 카테고리에 해당).
