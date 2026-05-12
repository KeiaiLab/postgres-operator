/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PoolerType 은 PgBouncer 가 연결할 PostgreSQL 서비스 역할이다.
// +kubebuilder:validation:Enum=rw;ro
type PoolerType string

const (
	// PoolerTypeRW 는 primary 쓰기 endpoint 로 연결한다.
	PoolerTypeRW PoolerType = "rw"
	// PoolerTypeRO 는 replica 읽기 endpoint 로 연결한다. replica 가 없으면 primary 로 fail-closed 하지 않는다.
	PoolerTypeRO PoolerType = "ro"
)

// PoolerPoolMode 는 PgBouncer pool_mode 값이다.
// +kubebuilder:validation:Enum=session;transaction;statement
type PoolerPoolMode string

const (
	PoolerPoolModeSession     PoolerPoolMode = "session"
	PoolerPoolModeTransaction PoolerPoolMode = "transaction"
	PoolerPoolModeStatement   PoolerPoolMode = "statement"
)

// PoolerClusterRef 는 같은 namespace 의 PostgresCluster 참조다.
type PoolerClusterRef struct {
	// Name 은 PostgresCluster.metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// PgBouncerSpec 은 Pooler 가 생성할 PgBouncer 런타임 설정이다.
type PgBouncerSpec struct {
	// Image 는 PgBouncer 컨테이너 이미지다. PgBouncer 1.19+ 를 요구한다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// PoolMode 는 PgBouncer pool_mode 값이다.
	// +kubebuilder:default=session
	// +optional
	PoolMode PoolerPoolMode `json:"poolMode,omitempty"`

	// Parameters 는 pgbouncer.ini [pgbouncer] 섹션에 병합할 자유형식 설정이다.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// PgHBA 는 PgBouncer HBA 파일에 기록할 접근 제어 규칙이다.
	// +optional
	PgHBA []string `json:"pg_hba,omitempty"`

	// AuthSecretRef 는 userlist.txt 를 제공하는 Secret 이름이다.
	// 비어 있으면 operator 가 built-in auth path 를 활성화한다 — PostgresCluster
	// 의 ready primary Pod 에 `keiailab_pooler_pgbouncer` LOGIN role 을 random
	// password 로 자동 생성하고, userlist.txt 를 담은 `<pooler-name>-builtin-auth`
	// Secret 을 Pooler OwnerReference 와 함께 생성한다 (CNPG `cnpg_pooler_pgbouncer`
	// 패턴 호환). 사용자가 명시한 Secret 이 우선한다.
	// +optional
	AuthSecretRef *corev1.LocalObjectReference `json:"authSecretRef,omitempty"`

	// ServerTLSSecret 은 PostgreSQL 서버에 mTLS 로 접속할 때 사용할 tls.crt/tls.key Secret 이다.
	// +optional
	ServerTLSSecret *corev1.LocalObjectReference `json:"serverTLSSecret,omitempty"`

	// ServerCASecret 은 PostgreSQL 서버 인증서를 검증할 ca.crt Secret 이다.
	// +optional
	ServerCASecret *corev1.LocalObjectReference `json:"serverCASecret,omitempty"`

	// ClientTLSSecret 은 클라이언트 TLS 연결을 받을 때 사용할 tls.crt/tls.key Secret 이다.
	// +optional
	ClientTLSSecret *corev1.LocalObjectReference `json:"clientTLSSecret,omitempty"`

	// ClientCASecret 은 클라이언트 인증서를 검증할 ca.crt Secret 이다.
	// +optional
	ClientCASecret *corev1.LocalObjectReference `json:"clientCASecret,omitempty"`

	// Exporter 는 PgBouncer Prometheus exporter sidecar 설정이다.
	// +optional
	Exporter *PgBouncerExporterSpec `json:"exporter,omitempty"`
}

// PgBouncerExporterSpec 은 PgBouncer metrics sidecar 계약이다.
type PgBouncerExporterSpec struct {
	// Image 는 exporter 컨테이너 이미지다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Port 는 exporter HTTP metrics port 다. 빈 값이면 9127.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9127
	// +optional
	Port int32 `json:"port,omitempty"`

	// Args 는 exporter 컨테이너 args override 다.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env 는 exporter 컨테이너 환경 변수다.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources 는 exporter 컨테이너 리소스 요청/제한이다.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// PoolerServiceTemplateSpec 은 PgBouncer Service 의 안전한 override 표면이다.
type PoolerServiceTemplateSpec struct {
	// Type 은 Service type 이다. 빈 값이면 ClusterIP.
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Labels 는 Service metadata.labels 에 추가된다.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations 는 Service metadata.annotations 에 추가된다.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Ports 는 Service 에 추가할 포트 목록이다. pgbouncer 기본 포트와 name 또는 port 가
	// 충돌하면 사용자가 지정한 포트를 우선한다.
	// +optional
	Ports []corev1.ServicePort `json:"ports,omitempty"`
}

// PoolerSpec 은 CNPG Pooler 의 핵심 운영 표면을 본 operator 모델로 이식한다.
type PoolerSpec struct {
	// Cluster 는 PgBouncer 가 바라볼 PostgresCluster 이다.
	// +kubebuilder:validation:Required
	Cluster PoolerClusterRef `json:"cluster"`

	// Instances 는 PgBouncer Pod 수다.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Instances int32 `json:"instances,omitempty"`

	// Type 은 rw(primary) 또는 ro(replica) endpoint 선택이다.
	// +kubebuilder:default=rw
	// +optional
	Type PoolerType `json:"type,omitempty"`

	// Paused 는 PgBouncer PAUSE/RESUME 상태를 선언적으로 제어한다.
	// true 로 바뀌면 operator 가 준비된 PgBouncer Pod 에 PAUSE 를 적용하고,
	// false 로 돌아오면 RESUME 을 적용한다.
	// +kubebuilder:default=false
	// +optional
	Paused bool `json:"paused,omitempty"`

	// PgBouncer 는 PgBouncer 설정이다.
	// +kubebuilder:validation:Required
	PgBouncer PgBouncerSpec `json:"pgbouncer"`

	// Template 은 PgBouncer Pod template override 다.
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`

	// DeploymentStrategy 는 PgBouncer Deployment 교체 전략이다. 빈 값이면 zero-unavailable rolling update 를 사용한다.
	// +optional
	DeploymentStrategy *appsv1.DeploymentStrategy `json:"deploymentStrategy,omitempty"`

	// ServiceTemplate 은 PgBouncer Service override 다.
	// +optional
	ServiceTemplate *PoolerServiceTemplateSpec `json:"serviceTemplate,omitempty"`

	// ServiceAccountName 은 Pooler Pod 가 사용할 기존 ServiceAccount 이름이다.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// PoolerPhase 는 Pooler reconcile 상태다.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type PoolerPhase string

const (
	PoolerPending PoolerPhase = "Pending"
	PoolerReady   PoolerPhase = "Ready"
	PoolerFailed  PoolerPhase = "Failed"
)

// PoolerStatus 는 PgBouncer 하위 리소스 관찰 상태다.
type PoolerStatus struct {
	// Phase 는 Pooler reconcile 상태다.
	Phase PoolerPhase `json:"phase,omitempty"`

	// Instances 는 PgBouncer Deployment 가 수렴하려는 replica 수다.
	Instances int32 `json:"instances,omitempty"`

	// ReadyReplicas 는 observed Deployment 의 readyReplicas 값이다.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Paused 는 모든 준비된 PgBouncer Pod 에 마지막으로 수렴된 PAUSE/RESUME 상태다.
	Paused bool `json:"paused,omitempty"`

	// BackendTargets 는 현재 PgBouncer config 로 라우팅되는 PostgreSQL backend DNS 목록이다.
	// +optional
	BackendTargets []string `json:"backendTargets,omitempty"`

	// ConfigHash 는 현재 PgBouncer config 의 sha256 이다.
	ConfigHash string `json:"configHash,omitempty"`

	// ObservedGeneration 은 마지막 처리 generation 이다.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions 는 K8s 표준 상태다.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=pool,categories=postgres;pooler;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.spec.instances`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Pooler 는 PgBouncer 기반 PostgreSQL connection pool 계층이다.
type Pooler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoolerSpec   `json:"spec,omitempty"`
	Status PoolerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PoolerList 는 Pooler 컬렉션이다.
type PoolerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pooler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pooler{}, &PoolerList{})
}
