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

// DeploymentMode는 production/development 두 운영 모드를 표현한다(RFC 0001 §3).
// development 모드는 webhook 검증을 완화하여 quickstart 5분을 보장한다(ADR 0003).
// +kubebuilder:validation:Enum=production;development
type DeploymentMode string

const (
	// DeploymentProduction은 운영 모드. coordinator/workers 멤버 ≥3 강제.
	DeploymentProduction DeploymentMode = "production"
	// DeploymentDevelopment는 quickstart 모드. members=1 허용.
	DeploymentDevelopment DeploymentMode = "development"
)

// VersionSpec은 PostgreSQL × (선택적) Citus 버전 조합을 지정한다.
// (postgres, citus) 쌍은 internal/version/matrix.go의 IsSupported를 통과해야 한다.
//
// 0.2.0-alpha 이후 (ADR 0010): Citus 필드는 선택. 미지정 또는 빈 문자열이면
// vanilla PostgreSQL (Stable 채널). 값을 지정하면 Citus 통합(Beta) 활성화 — 사용자가
// AGPL-3.0 §13 SaaS 의무를 명시 수용.
type VersionSpec struct {
	// Postgres는 메이저 버전 문자열("16" | "17" | "18"). 0.2.0-alpha 이후 "18"이 default 권장.
	// +kubebuilder:validation:Enum="16";"17";"18"
	// +kubebuilder:validation:Required
	Postgres string `json:"postgres"`

	// Citus는 minor 단위 버전 문자열(예: "12.1", "13.0"). 0.2.0-alpha부터 선택 필드.
	// 빈 문자열 또는 누락 시 vanilla PostgreSQL (Citus 없음). 명시 시 AGPL-3.0 라이센스 수용.
	// +optional
	Citus string `json:"citus,omitempty"`
}

// StorageSpec은 PVC 생성 파라미터다(RFC 0001 §3).
type StorageSpec struct {
	// Size는 PVC 요청 크기(예: "100Gi").
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// StorageClassName은 PVC StorageClass(nil이면 클러스터 디폴트).
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes는 PVC 접근 모드(빈 배열이면 ReadWriteOnce).
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// CoordinatorSpec은 Citus coordinator HA replica set을 표현한다(ADR 0003).
type CoordinatorSpec struct {
	// Members는 RS 멤버 수. 홀수만 허용(split-brain 방지, ADR 0003).
	// production 모드는 ≥3, development 모드는 ≥1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Members int32 `json:"members"`

	// Storage는 PVC 사양.
	// +kubebuilder:validation:Required
	Storage StorageSpec `json:"storage"`

	// Resources는 컨테이너 리소스 요구사항.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ShouldHaveShards는 coordinator가 분산 테이블 shard를 보유할지 여부.
	// nil이면 false(ADR 0003 권장).
	// +optional
	ShouldHaveShards *bool `json:"shouldHaveShards,omitempty"`
}

// WorkerPoolSpec은 Citus worker pool(HA RS) 하나를 표현한다.
type WorkerPoolSpec struct {
	// Name은 pool 식별자. 동일 클러스터 내 unique. DNS-1123 label 형식.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Members는 RS 멤버 수. 홀수, ≥1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Members int32 `json:"members"`

	// +kubebuilder:validation:Required
	Storage StorageSpec `json:"storage"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// PgBouncerSpec은 RouterSpec 내 PgBouncer 사이드카 설정이다.
type PgBouncerSpec struct {
	// PoolMode는 transaction|session|statement 중 하나(디폴트 transaction).
	// +kubebuilder:validation:Enum=transaction;session;statement
	// +kubebuilder:default=transaction
	// +optional
	PoolMode string `json:"poolMode,omitempty"`

	// MaxClientConn은 per-Pod 클라이언트 연결 상한. nil이면 PgBouncer 기본값.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxClientConn *int32 `json:"maxClientConn,omitempty"`
}

// RouterSpec은 stateless QueryRouter 풀 설정이다(ADR 0003).
//
// 본 구조체에는 Storage 필드가 의도적으로 부재한다. ADR 0003 무상태 강제를
// 타입 차원에서 표현하며, 사용자는 YAML에 storage를 쓸 수 없다.
type RouterSpec struct {
	// Replicas는 라우터 Pod 수. ≥1.
	// HPA를 부착하는 경우에도 본 필드는 minimum 역할.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Replicas int32 `json:"replicas"`

	// Resources는 라우터 컨테이너 리소스 요구사항.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// PgBouncer는 사이드카 설정.
	// +optional
	PgBouncer PgBouncerSpec `json:"pgbouncer,omitempty"`
}

// ExtensionSpec은 활성화할 PG/Citus extension 하나를 지정한다.
// 본 SDK의 ExtensionPlugin Registry에 등록된 이름이어야 한다.
type ExtensionSpec struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Version은 extension의 minor 단위 버전. 빈 문자열이면 호환 매트릭스 기본값.
	// +optional
	Version string `json:"version,omitempty"`
}

// PostgresClusterSpec은 PostgresCluster CR의 Spec이다.
type PostgresClusterSpec struct {
	// Version은 PG × Citus 버전 조합.
	// +kubebuilder:validation:Required
	Version VersionSpec `json:"version"`

	// Coordinator는 Citus coordinator HA RS.
	// +kubebuilder:validation:Required
	Coordinator CoordinatorSpec `json:"coordinator"`

	// Workers는 Citus worker pool들. ≥1.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Workers []WorkerPoolSpec `json:"workers"`

	// Routers는 stateless QueryRouter 풀.
	// +kubebuilder:validation:Required
	Routers RouterSpec `json:"routers"`

	// Extensions는 활성화할 확장 목록.
	// +optional
	Extensions []ExtensionSpec `json:"extensions,omitempty"`

	// Deployment는 production|development 모드(디폴트 production).
	// +kubebuilder:default=production
	// +optional
	Deployment DeploymentMode `json:"deployment,omitempty"`
}

// NodeStatus는 단일 PG 인스턴스(coordinator 또는 worker pool)의 상태다.
type NodeStatus struct {
	// Primary는 현재 primary Pod 이름.
	// +optional
	Primary string `json:"primary,omitempty"`

	// Replicas는 현재 standby Pod 이름들.
	// +optional
	Replicas []string `json:"replicas,omitempty"`

	// LeaseHolder는 K8s lease 보유자(primary와 동일한 것이 정상).
	// +optional
	LeaseHolder string `json:"leaseHolder,omitempty"`
}

// DistNodeRef는 pg_dist_node에 등록된 node 정보를 K8s에 반영한다.
type DistNodeRef struct {
	GroupID          int32  `json:"groupId"`
	NodeName         string `json:"nodeName"`
	NodePort         int32  `json:"nodePort"`
	ShouldHaveShards bool   `json:"shouldHaveShards"`
}

// WorkerPoolStatus는 worker pool 하나의 상태다.
type WorkerPoolStatus struct {
	Name string `json:"name"`

	// Node는 본 worker pool의 RS 상태.
	Node NodeStatus `json:"node"`

	// DistNode는 pg_dist_node 등록 결과.
	// +optional
	DistNode *DistNodeRef `json:"distNode,omitempty"`
}

// RouterPoolStatus는 라우터 풀 상태다.
type RouterPoolStatus struct {
	// ReadyReplicas는 Ready 조건을 통과한 라우터 Pod 수.
	ReadyReplicas int32 `json:"readyReplicas"`

	// MaxMetadataLagSeconds는 모든 라우터 Pod 중 router_metadata_lag_seconds
	// 메트릭의 최댓값. 임계치 초과 시 라우터 Pod readiness가 실패한다(ADR 0003).
	// +optional
	MaxMetadataLagSeconds *string `json:"maxMetadataLagSeconds,omitempty"`
}

// TopologyStatus는 토폴로지 현재 상태다.
type TopologyStatus struct {
	Coordinator NodeStatus         `json:"coordinator"`
	Workers     []WorkerPoolStatus `json:"workers,omitempty"`
	Routers     RouterPoolStatus   `json:"routers"`
}

// PostgresClusterStatus는 PostgresCluster CR의 Status다.
type PostgresClusterStatus struct {
	// Conditions는 표준 K8s Condition 집합. 권장 종류:
	// Ready, CoordinatorReady, WorkersReady, RoutersReady, MetadataInSync.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Topology는 reconcile된 현재 토폴로지.
	// +optional
	Topology TopologyStatus `json:"topology,omitempty"`

	// Channel은 활성 릴리즈 채널(stable | beta | preview-pg18).
	// internal/version/matrix.go의 Combo.Channel 결과를 반영한다.
	// +optional
	Channel string `json:"channel,omitempty"`

	// ObservedGeneration은 reconcile된 spec의 metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pgc
// +kubebuilder:printcolumn:name="PG",type=string,JSONPath=".spec.version.postgres"
// +kubebuilder:printcolumn:name="Citus",type=string,JSONPath=".spec.version.citus"
// +kubebuilder:printcolumn:name="Workers",type=integer,JSONPath=".spec.workers[*].members"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Channel",type=string,JSONPath=".status.channel"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// PostgresCluster는 Citus 분산 PostgreSQL 클러스터의 선언적 표현이다.
type PostgresCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresClusterSpec   `json:"spec,omitempty"`
	Status PostgresClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PostgresClusterList는 PostgresCluster의 컬렉션이다.
type PostgresClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresCluster{}, &PostgresClusterList{})
}
