/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupJobSpec은 단일 백업 호출의 명세다 (RFC 0004 §2.1).
//
// immutable 필드 (생성 후 변경 불가, webhook이 reject 예정 — phase 2):
//   - cluster.name
//   - tool
//   - type
//   - executionMode
type BackupJobSpec struct {
	// Cluster는 백업 대상 PostgresCluster 참조 (같은 namespace).
	// +kubebuilder:validation:Required
	Cluster BackupClusterRef `json:"cluster"`

	// Tool은 사용할 백업 도구 이름. BackupPlugin.Name()과 일치해야 한다.
	// 예: "pgbackrest" (RFC 0004 §4 1차 reference), "walg", "barman".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Repo는 다중 저장소 환경에서 어느 repo를 쓸지 식별.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Type은 백업 종류.
	// +kubebuilder:validation:Enum=full;incremental;differential;restore
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Restore는 Type=restore 시 PITR 설정 (RFC 0004 §5.1).
	// +optional
	Restore *BackupRestoreSpec `json:"restore,omitempty"`

	// Retention은 백업 보존 정책.
	// +optional
	Retention BackupRetentionSpec `json:"retention,omitempty"`

	// ExecutionMode는 plugin이 백업을 *어디서 실행*할지 결정 (P1-6, RFC 0004 §3.1):
	//   - "sidecar": PG Pod 동거 컨테이너 (pgBackRest 패턴)
	//   - "job":     standalone K8s Job (WAL-G 패턴)
	//   - "":        plugin default
	// +kubebuilder:validation:Enum=sidecar;job;""
	// +optional
	ExecutionMode string `json:"executionMode,omitempty"`

	// JobTemplate은 ExecutionMode="job"일 때 생성할 runner batch/v1 Job 템플릿이다.
	// operator는 name/namespace/ownerReference/표준 label/env만 주입하고, image,
	// command, volume, secret, resource, securityContext 같은 실행 세부사항은 본
	// 템플릿을 그대로 따른다.
	// +optional
	JobTemplate *batchv1.JobTemplateSpec `json:"jobTemplate,omitempty"`

	// Labels는 BackupResult 메타데이터에 첨부될 K8s 스타일 레이블.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// BackupClusterRef는 같은 namespace의 PostgresCluster 참조.
// 이름이 ClusterRef가 아닌 BackupClusterRef인 이유: 향후 다른 CRD가 동일
// 이름을 사용하지 않도록 유형별 prefix 규약 (kubebuilder convention).
type BackupClusterRef struct {
	// Name은 PostgresCluster.metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BackupRestoreSpec은 PITR 복구 명세 (RFC 0004 §5.1).
type BackupRestoreSpec struct {
	// TargetTime은 복구 대상 시각 (UTC, ISO 8601 RFC3339).
	// +optional
	TargetTime *metav1.Time `json:"targetTime,omitempty"`

	// BackupID는 복구 대상 백업 식별자 (TargetTime 대신 직접 지정).
	// +optional
	BackupID string `json:"backupID,omitempty"`
}

// BackupRetentionSpec은 백업 보존 정책.
type BackupRetentionSpec struct {
	// KeepFull은 보존할 full 백업 개수. 0이면 무제한 (plugin 기본).
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepFull int32 `json:"keepFull,omitempty"`

	// KeepIncremental은 보존할 incremental/differential 백업 개수.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepIncremental int32 `json:"keepIncremental,omitempty"`
}

// BackupJobPhase는 BackupJob 진행 단계.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type BackupJobPhase string

const (
	BackupJobPending   BackupJobPhase = "Pending"
	BackupJobRunning   BackupJobPhase = "Running"
	BackupJobSucceeded BackupJobPhase = "Succeeded"
	BackupJobFailed    BackupJobPhase = "Failed"
)

// BackupJobStatus는 BackupJob의 관찰 상태.
type BackupJobStatus struct {
	// Phase는 BackupJob 진행 단계.
	Phase BackupJobPhase `json:"phase,omitempty"`

	// BackupID는 plugin이 부여한 고유 식별자 (Succeeded 후).
	BackupID string `json:"backupID,omitempty"`

	// RunnerJobName은 ExecutionMode="job"에서 생성한 batch/v1 Job 이름이다.
	RunnerJobName string `json:"runnerJobName,omitempty"`

	// StartedAt은 백업 시작 시각.
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// EndedAt은 백업 종료 시각.
	EndedAt *metav1.Time `json:"endedAt,omitempty"`

	// Bytes는 백업 크기 (bytes). 0 허용 (스트리밍 도구).
	Bytes int64 `json:"bytes,omitempty"`

	// ObservedGeneration은 reconcile이 마지막 처리한 spec generation.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions는 K8s 표준 상태 (Ready, Available, Progressing).
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=bj
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.spec.tool`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupJob은 단일 백업 실행 명령이다 (RFC 0004).
//
// 본 CRD는 *atomic 단위*다 — 변경 시 새 BackupJob 생성. cron 기반 정기 백업은
// ScheduledBackup CRD 가 BackupJob 을 생성하는 방식으로 분리한다.
type BackupJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupJobSpec   `json:"spec,omitempty"`
	Status BackupJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupJobList는 BackupJob 다중 응답.
type BackupJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupJob{}, &BackupJobList{})
}
