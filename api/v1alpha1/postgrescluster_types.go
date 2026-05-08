/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// 본 파일은 RFC 0001 (PostgresCluster CRD v2) §3 의 spec/status 정의를 그대로
// Go 타입화한다. 0.3.0-alpha 기준 schema 는 alpha 채널 정책에 의해 호환성
// 보장 없이 재정의되었다 (CHANGELOG breaking change 명시). 이전 v0.x schema
// (`coordinator/workers/routers/extensions/sharding.backend`) 는 _archive 의
// RFC/ADR 에 보존된다.

// ShardingMode 는 단일 노드 (none) 와 자체 분산 SQL (native) 두 운영 형태를 표현한다.
// +kubebuilder:validation:Enum=none;native
type ShardingMode string

const (
	// ShardingModeNone 은 라우터 없이 단일 shard 만 사용. 0.4.0 (P1) 의 GA 형태.
	ShardingModeNone ShardingMode = "none"
	// ShardingModeNative 는 자체 분산 SQL (router + multi-shard). P2+ 활성화.
	ShardingModeNative ShardingMode = "native"
)

// StorageSpec 는 PVC 생성 파라미터다 (RFC 0001 §3).
type StorageSpec struct {
	// Size 는 PVC 요청 크기 (예: "100Gi").
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// StorageClass 는 PVC StorageClass 이름. 빈 문자열이면 클러스터 디폴트.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessModes 는 PVC 접근 모드 (빈 배열이면 ReadWriteOnce).
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// ShardsSpec 는 샤드 토폴로지 정의다 (RFC 0001 §3.1).
type ShardsSpec struct {
	// InitialCount 는 클러스터 초기 샤드 수. P1 GA 는 1 만 보장. 1024 까지 schema 허용.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:validation:Required
	InitialCount int32 `json:"initialCount"`

	// Storage 는 샤드 PVC 사양.
	// +kubebuilder:validation:Required
	Storage StorageSpec `json:"storage"`

	// Replicas 는 샤드당 비동기 복제본 수 (primary 제외). 0 이면 HA 미구성 (개발용).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=15
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Resources 는 샤드 PG 컨테이너 리소스 요구사항.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Affinity 는 샤드 Pod 친화성 규칙.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations 는 샤드 Pod 노드 toleration.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// RouterAutoscaleSpec 는 라우터 HPA 설정이다 (RFC 0001 §3.1).
type RouterAutoscaleSpec struct {
	// Enabled 는 HPA 부착 여부.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// MinReplicas 는 HPA 최소 복제본.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinReplicas int32 `json:"minReplicas,omitempty"`

	// MaxReplicas 는 HPA 최대 복제본.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxReplicas int32 `json:"maxReplicas,omitempty"`

	// TargetCPU 는 HPA CPU 사용률 목표 (%).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=70
	// +optional
	TargetCPU int32 `json:"targetCPU,omitempty"`

	// TargetActiveConnections 는 HPA active connection 목표.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1000
	// +optional
	TargetActiveConnections int32 `json:"targetActiveConnections,omitempty"`
}

// RouterSpec 는 무상태 QueryRouter Deployment 설정이다 (RFC 0001 §3.1, RFC 0004).
//
// 본 구조체에는 Storage 필드가 의도적으로 부재한다 — 라우터의 무상태성을
// 타입 차원에서 강제한다.
type RouterSpec struct {
	// Enabled 는 라우터 활성화 여부. shardingMode=native 시 default true.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Replicas 는 라우터 Pod 수. HPA 부착 시 minimum 역할.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Autoscale 은 HPA 설정.
	// +optional
	Autoscale *RouterAutoscaleSpec `json:"autoscale,omitempty"`

	// Resources 는 라우터 컨테이너 리소스 요구사항.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AutoSplitTriggers 는 자동 샤드 분할 트리거 임계치들이다 (모두 AND 의미).
type AutoSplitTriggers struct {
	// SizeThresholdGB 는 단일 샤드 크기 임계치 (GB). 0 이면 미사용.
	// +kubebuilder:validation:Minimum=0
	// +optional
	SizeThresholdGB int32 `json:"sizeThresholdGB,omitempty"`

	// P99LatencyMs 는 단일 샤드 P99 latency 임계치 (ms). 0 이면 미사용.
	// +kubebuilder:validation:Minimum=0
	// +optional
	P99LatencyMs int32 `json:"p99LatencyMs,omitempty"`

	// CPUPercent 는 단일 샤드 평균 CPU 사용률 임계치 (%). 0 이면 미사용.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUPercent int32 `json:"cpuPercent,omitempty"`

	// DurationMinutes 는 위 임계치들이 지속되어야 하는 시간 (분). 0 이면 즉시.
	// +kubebuilder:validation:Minimum=0
	// +optional
	DurationMinutes int32 `json:"durationMinutes,omitempty"`
}

// AutoSplitSpec 는 자동 샤드 분할 정책이다 (RFC 0001 §3.1, RFC 0003 후속).
type AutoSplitSpec struct {
	// Enabled 는 자동 분할 활성화 여부. P2+ 에서 의미를 갖는다.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// RequireApproval 은 자동 분할 대상 후보를 ShardSplitJob 으로 제출하되 수동
	// 승인 annotation 후에만 실행하도록 강제한다 (production safety).
	// +kubebuilder:default=true
	// +optional
	RequireApproval bool `json:"requireApproval,omitempty"`

	// Triggers 는 분할 임계치 집합.
	// +optional
	Triggers *AutoSplitTriggers `json:"triggers,omitempty"`
}

// ClusterBackupRetentionSpec 는 PostgresCluster.spec.backup.retention 정책.
//
// 명명 prefix 는 BackupJob CRD 의 BackupRetentionSpec 과의 type 충돌을 회피한다
// (둘 다 v1alpha1 패키지). BackupJob 은 atomic 작업 단위, 본 구조체는 cluster
// 레벨 정기 백업의 보존 정책으로 의미가 다르다.
type ClusterBackupRetentionSpec struct {
	// Full 은 full 백업 보존 기간 (duration: "7d", "168h" 등).
	// +optional
	Full string `json:"full,omitempty"`

	// Incremental 은 incremental 백업 보존 기간.
	// +optional
	Incremental string `json:"incremental,omitempty"`

	// WALArchive 는 WAL 아카이브 보존 기간.
	// +optional
	WALArchive string `json:"walArchive,omitempty"`
}

// ClusterBackupRepoSpec 는 백업 저장소 설정.
type ClusterBackupRepoSpec struct {
	// Type 은 저장소 종류 (s3 | gcs | azure | filesystem).
	// +kubebuilder:validation:Enum=s3;gcs;azure;filesystem
	// +optional
	Type string `json:"type,omitempty"`

	// Bucket 은 object storage 버킷 이름.
	// +optional
	Bucket string `json:"bucket,omitempty"`

	// Region 은 object storage 리전.
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint 는 S3 호환 endpoint URL (선택).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Path 는 filesystem 경로 (Type=filesystem).
	// +optional
	Path string `json:"path,omitempty"`
}

// ClusterBackupSpec 는 cluster 레벨 백업/PITR 정책 (RFC 0001 §3.1, RFC 0004).
type ClusterBackupSpec struct {
	// Enabled 는 백업 정책 활성화 여부 (사용자 명시 opt-in).
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Schedule 은 cron expression (예: "0 2 * * *").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Retention 은 백업 보존 정책.
	// +optional
	Retention *ClusterBackupRetentionSpec `json:"retention,omitempty"`

	// Repo 는 백업 저장소 설정.
	// +optional
	Repo *ClusterBackupRepoSpec `json:"repo,omitempty"`
}

// ServiceMonitorSpec 는 Prometheus ServiceMonitor 자동 배포 설정.
type ServiceMonitorSpec struct {
	// Enabled 는 ServiceMonitor 생성 여부.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Interval 은 scrape interval (예: "30s").
	// +kubebuilder:default="30s"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// PrometheusRuleSpec 는 PrometheusRule 자동 배포 설정.
type PrometheusRuleSpec struct {
	// Enabled 는 PrometheusRule 생성 여부.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// MonitoringSpec 는 monitoring 통합 설정 (RFC 0001 §3.1).
type MonitoringSpec struct {
	// ServiceMonitor 는 Prometheus operator ServiceMonitor 자동 배포.
	// +optional
	ServiceMonitor *ServiceMonitorSpec `json:"serviceMonitor,omitempty"`

	// PrometheusRule 은 알람 규칙 자동 배포.
	// +optional
	PrometheusRule *PrometheusRuleSpec `json:"prometheusRule,omitempty"`
}

// PostgresClusterSpec 는 PostgresCluster CR 의 desired state 다 (RFC 0001 §3).
//
// CEL 검증 (kubebuilder v0.15+):
//
// +kubebuilder:validation:XValidation:rule="self.shardingMode != 'native' || self.shards.initialCount >= 1",message="native sharding requires shards.initialCount >= 1"
// +kubebuilder:validation:XValidation:rule="!has(self.router) || self.shardingMode == 'native'",message="router is only valid when shardingMode=native"
// +kubebuilder:validation:XValidation:rule="!has(self.autoSplit) || self.autoSplit.enabled == false || self.shardingMode == 'native'",message="autoSplit requires shardingMode=native"
type PostgresClusterSpec struct {
	// PostgresVersion 은 메이저 버전 문자열. 0.3.0-alpha 는 18 만 GA, 17 호환 유지.
	// +kubebuilder:validation:Enum="17";"18"
	// +kubebuilder:default="18"
	// +optional
	PostgresVersion string `json:"postgresVersion,omitempty"`

	// ShardingMode 는 단일 샤드 (none) 또는 자체 분산 SQL (native).
	// +kubebuilder:default=none
	// +optional
	ShardingMode ShardingMode `json:"shardingMode,omitempty"`

	// Shards 는 샤드 토폴로지.
	// +kubebuilder:validation:Required
	Shards ShardsSpec `json:"shards"`

	// Router 는 무상태 QueryRouter 풀. shardingMode=native 시에만 의미.
	// +optional
	Router *RouterSpec `json:"router,omitempty"`

	// AutoSplit 은 자동 샤드 분할 정책.
	// +optional
	AutoSplit *AutoSplitSpec `json:"autoSplit,omitempty"`

	// Backup 은 백업/PITR 정책.
	// +optional
	Backup *ClusterBackupSpec `json:"backup,omitempty"`

	// Monitoring 은 monitoring 통합 설정.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`

	// Extensions 는 활성화할 PostgreSQL extension 의 이름 목록이다 (RFC 0006 R1).
	// 비어 있으면 vanilla PG (image base 외 추가 extension 없음). 각 이름은 operator
	// 부트스트랩 시 Registry 에 등록된 ExtensionPlugin 이어야 하며, webhook 이 이를
	// 검증한다.
	//
	// 예: ["pgaudit", "pgvector"] — postgres_v1alpha1 image 가 두 extension 의 .so 를
	// 보유한다는 전제. image 가 미보유 시 postgres FATAL — 사용자 책임.
	//
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// TLS 는 PostgreSQL server-side TLS 설정 (Pillar P7 §7).
	// 0.3.0-alpha.5+ Phase 1 facade — CRD field 만 정의. Phase 2 에서 cert-manager
	// Certificate CR 통합 + Phase 3 에서 reconciler 의 server.crt/server.key emit
	// + postgresql.conf ssl=on 봉인. 본 alpha 버전 에서는 enabled=false (default)
	// 만 의미 있음 — true 설정 시 webhook reject (NotImplemented Phase 1).
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`
}

// TLSSpec 은 PostgreSQL server-side TLS 설정.
//
// Roadmap (3-phase):
//
//	Phase 1 (alpha.5): 본 CRD field facade. enabled=true 시 webhook reject.
//	Phase 2 (alpha.6): cert-manager Certificate CR 자동 생성 (Issuer 참조).
//	Phase 3 (alpha.7): reconciler 가 server cert secret 을 STS volume mount +
//	                   postgresql.conf 의 ssl=on + ssl_cert_file/ssl_key_file 명시.
//
// 회복 대상 = 외부 client (예: argos cluster 의 infisical) 의 sslmode=verify-ca
// 정공. 현재 ssl=off → sslmode=disable 회귀 (ADR-0062 cycle 봉인).
type TLSSpec struct {
	// Enabled 는 server-side TLS 활성 여부. Phase 1 에서는 false 만 동작.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// IssuerRef 는 cert-manager Issuer 또는 ClusterIssuer 참조.
	// Phase 2 부터 의미 있음. 미설정 시 self-signed 자체 발급 (Phase 3 default).
	// +optional
	IssuerRef *TLSIssuerRef `json:"issuerRef,omitempty"`

	// CertSecretName 은 server cert 가 저장될 Secret 이름. 미설정 시
	// "<cluster>-tls" default. Phase 2 에서 cert-manager Certificate.spec.secretName
	// 으로 사용됨.
	// +optional
	CertSecretName string `json:"certSecretName,omitempty"`
}

// TLSIssuerRef 는 cert-manager Issuer/ClusterIssuer 참조.
type TLSIssuerRef struct {
	// Name 은 Issuer 이름.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Kind 는 Issuer 또는 ClusterIssuer.
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +kubebuilder:default=Issuer
	// +optional
	Kind string `json:"kind,omitempty"`
}

// ClusterPhase 는 cluster 의 상위 단계.
// +kubebuilder:validation:Enum=Provisioning;Ready;Reconfiguring;Degraded
type ClusterPhase string

const (
	ClusterPhaseProvisioning  ClusterPhase = "Provisioning"
	ClusterPhaseReady         ClusterPhase = "Ready"
	ClusterPhaseReconfiguring ClusterPhase = "Reconfiguring"
	ClusterPhaseDegraded      ClusterPhase = "Degraded"
)

// ShardEndpoint 는 샤드 멤버 (primary 또는 replica) 1 개의 관찰 상태.
type ShardEndpoint struct {
	// Pod 는 K8s Pod 이름.
	// +optional
	Pod string `json:"pod,omitempty"`

	// Endpoint 는 호스트:포트 형태의 접속 endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Ready 는 readiness 통과 여부.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// LagBytes 는 replica 의 WAL lag (bytes). primary 는 0 또는 미설정.
	// +optional
	LagBytes int64 `json:"lagBytes,omitempty"`
}

// ShardStatus 는 단일 샤드의 관찰 상태.
type ShardStatus struct {
	// Name 은 샤드 식별자 (예: "shard-0").
	Name string `json:"name"`

	// Ordinal 은 샤드 순서 (0-based).
	Ordinal int32 `json:"ordinal"`

	// Primary 는 현재 primary endpoint.
	// +optional
	Primary *ShardEndpoint `json:"primary,omitempty"`

	// Replicas 는 현재 replica endpoint 목록.
	// +optional
	Replicas []ShardEndpoint `json:"replicas,omitempty"`

	// SizeBytes 는 샤드 데이터 크기 (bytes). 0 이면 미관측.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// LastSplit 은 마지막 분할 완료 시각. 분할 이력 없음 = nil.
	// +optional
	LastSplit *metav1.Time `json:"lastSplit,omitempty"`
}

// ClusterRouterStatus 는 라우터 풀의 관찰 상태.
type ClusterRouterStatus struct {
	// Replicas 는 desired 라우터 Pod 수.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas 는 ready 상태 라우터 Pod 수.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Endpoint 는 라우터 Service endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// PostgresClusterStatus 는 PostgresCluster CR 의 관찰 상태 (RFC 0001 §3.2).
type PostgresClusterStatus struct {
	// Phase 는 cluster 의 상위 단계.
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`

	// ObservedGeneration 은 reconciler 가 관측한 spec.metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Shards 는 샤드별 관찰 상태 배열 (ordinal 오름차순).
	// +optional
	Shards []ShardStatus `json:"shards,omitempty"`

	// Router 는 라우터 풀 관찰 상태 (shardingMode=native 시).
	// +optional
	Router *ClusterRouterStatus `json:"router,omitempty"`

	// Conditions 는 K8s 표준 condition 집합.
	// 권장 type: Ready / Progressing / BackupHealthy / AutoSplitEligible / RouterReady.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pgc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Shards",type=integer,JSONPath=".spec.shards.initialCount"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".spec.postgresVersion"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// PostgresCluster 는 K8s-native 자체 분산 PostgreSQL 클러스터의 선언적 표현이다 (ADR 0001).
type PostgresCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresClusterSpec   `json:"spec,omitempty"`
	Status PostgresClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PostgresClusterList 는 PostgresCluster 의 컬렉션이다.
type PostgresClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresCluster{}, &PostgresClusterList{})
}
