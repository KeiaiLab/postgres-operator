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

// ScheduledBackupOwnerReferencePolicy 는 생성된 BackupJob 의 ownerReference 대상을
// 정한다. CNPG 의 backupOwnerReference 의미론을 본 operator 의 BackupJob 모델에
// 맞춰 축소 이식한다.
// +kubebuilder:validation:Enum=none;self;cluster
type ScheduledBackupOwnerReferencePolicy string

const (
	ScheduledBackupOwnerReferenceNone    ScheduledBackupOwnerReferencePolicy = "none"
	ScheduledBackupOwnerReferenceSelf    ScheduledBackupOwnerReferencePolicy = "self"
	ScheduledBackupOwnerReferenceCluster ScheduledBackupOwnerReferencePolicy = "cluster"
)

// BackupConcurrencyPolicy 는 같은 ScheduledBackup 이 이전 실행을 아직 끝내지
// 못했을 때 새 BackupJob 을 만들지 결정한다.
// +kubebuilder:validation:Enum=Allow;Forbid
type BackupConcurrencyPolicy string

const (
	BackupConcurrencyAllow  BackupConcurrencyPolicy = "Allow"
	BackupConcurrencyForbid BackupConcurrencyPolicy = "Forbid"
)

// ScheduledBackupSpec 은 cron 기반 정기 백업 정책이다.
type ScheduledBackupSpec struct {
	// Schedule 은 초 필드를 포함한 6-field cron expression 이다.
	// 형식: "second minute hour day-of-month month day-of-week".
	// 예: "0 0 2 * * *" = 매일 02:00:00.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Cluster 는 백업 대상 PostgresCluster 참조 (같은 namespace).
	// +kubebuilder:validation:Required
	Cluster BackupClusterRef `json:"cluster"`

	// Tool 은 사용할 백업 도구 이름. BackupPlugin.Name() 과 일치해야 한다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Repo 는 다중 저장소 환경에서 어느 repo 를 쓸지 식별한다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Type 은 정기 백업 종류다. restore 는 atomic BackupJob 에서만 허용한다.
	// +kubebuilder:validation:Enum=full;incremental;differential
	// +kubebuilder:default=full
	// +optional
	Type string `json:"type,omitempty"`

	// Suspend=true 이면 새 BackupJob 생성을 중단한다. 기존 BackupJob 은 건드리지 않는다.
	// +kubebuilder:default=false
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Immediate=true 이면 ScheduledBackup 생성 후 첫 reconcile 에서 즉시 BackupJob
	// 1건을 만든 뒤, 이후에는 Schedule 기준으로 동작한다.
	// +kubebuilder:default=false
	// +optional
	Immediate bool `json:"immediate,omitempty"`

	// BackupOwnerReference 는 생성된 BackupJob 의 ownerReference 정책이다.
	// +kubebuilder:default=self
	// +optional
	BackupOwnerReference ScheduledBackupOwnerReferencePolicy `json:"backupOwnerReference,omitempty"`

	// ConcurrencyPolicy 는 이전 BackupJob 이 Running/Pending 일 때 새 실행을 만들지
	// 결정한다.
	// +kubebuilder:default=Forbid
	// +optional
	ConcurrencyPolicy BackupConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// Retention 은 생성되는 BackupJob 에 복사되는 보존 정책이다.
	// +optional
	Retention BackupRetentionSpec `json:"retention,omitempty"`

	// ExecutionMode 는 생성되는 BackupJob 에 복사되는 실행 모드다.
	// +kubebuilder:validation:Enum=sidecar;job;""
	// +optional
	ExecutionMode string `json:"executionMode,omitempty"`

	// JobTemplate 은 ExecutionMode="job"일 때 생성되는 BackupJob 에 복사할 runner
	// batch/v1 Job 템플릿이다.
	// +optional
	JobTemplate *batchv1.JobTemplateSpec `json:"jobTemplate,omitempty"`

	// Labels 는 생성되는 BackupJob 의 spec.labels 에 복사된다.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ScheduledBackupStatus 는 정기 백업 컨트롤러의 관찰 상태다.
type ScheduledBackupStatus struct {
	// LastScheduleTime 은 마지막으로 BackupJob 생성을 시도한 스케줄 시각이다.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// LastBackupJobName 은 마지막으로 생성한 BackupJob 이름이다.
	// +optional
	LastBackupJobName string `json:"lastBackupJobName,omitempty"`

	// NextScheduleTime 은 다음 생성 예정 시각이다.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// ObservedGeneration 은 reconciler 가 마지막 처리한 spec generation 이다.
	// +optional
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
// +kubebuilder:resource:scope=Namespaced,shortName=sb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="LastBackup",type=string,JSONPath=`.status.lastBackupJobName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduledBackup 은 cron schedule 에 따라 atomic BackupJob 을 생성한다.
type ScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduledBackupSpec   `json:"spec,omitempty"`
	Status ScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScheduledBackupList 는 ScheduledBackup 컬렉션이다.
type ScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScheduledBackup{}, &ScheduledBackupList{})
}
