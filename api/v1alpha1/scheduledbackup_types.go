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

// ScheduledBackupOwnerReferencePolicy selects the ownerReference target on the BackupJobs
// that are created. It is a reduced port of CNPG's backupOwnerReference semantics, adapted
// to this operator's BackupJob model.
// +kubebuilder:validation:Enum=none;self;cluster
type ScheduledBackupOwnerReferencePolicy string

const (
	ScheduledBackupOwnerReferenceNone    ScheduledBackupOwnerReferencePolicy = "none"
	ScheduledBackupOwnerReferenceSelf    ScheduledBackupOwnerReferencePolicy = "self"
	ScheduledBackupOwnerReferenceCluster ScheduledBackupOwnerReferencePolicy = "cluster"
)

// BackupConcurrencyPolicy decides whether the same ScheduledBackup creates a new BackupJob
// while a previous run has not yet completed.
// +kubebuilder:validation:Enum=Allow;Forbid
type BackupConcurrencyPolicy string

const (
	BackupConcurrencyAllow  BackupConcurrencyPolicy = "Allow"
	BackupConcurrencyForbid BackupConcurrencyPolicy = "Forbid"
)

// ScheduledBackupSpec is the cron-based scheduled-backup policy.
type ScheduledBackupSpec struct {
	// Schedule is a 6-field cron expression that includes a seconds field.
	// Format: "second minute hour day-of-month month day-of-week".
	// Example: "0 0 2 * * *" — every day at 02:00:00.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Cluster references the PostgresCluster to back up (same namespace).
	// +kubebuilder:validation:Required
	Cluster BackupClusterRef `json:"cluster"`

	// Tool is the name of the backup tool to use; must match BackupPlugin.Name().
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Repo identifies which repository to use in multi-repo deployments.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Type is the scheduled backup kind. The restore kind is only allowed on an atomic BackupJob.
	// +kubebuilder:validation:Enum=full;incremental;differential
	// +kubebuilder:default=full
	// +optional
	Type string `json:"type,omitempty"`

	// Suspend=true stops the creation of new BackupJobs. Existing BackupJobs are left alone.
	// +kubebuilder:default=false
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Immediate=true creates one BackupJob in the first reconcile after the ScheduledBackup is
	// created; subsequent runs follow Schedule.
	// +kubebuilder:default=false
	// +optional
	Immediate bool `json:"immediate,omitempty"`

	// BackupOwnerReference is the ownerReference policy applied to BackupJobs that are created.
	// +kubebuilder:default=self
	// +optional
	BackupOwnerReference ScheduledBackupOwnerReferencePolicy `json:"backupOwnerReference,omitempty"`

	// ConcurrencyPolicy decides whether to create a new run while the previous BackupJob is
	// Running/Pending.
	// +kubebuilder:default=Forbid
	// +optional
	ConcurrencyPolicy BackupConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// Retention is the retention policy copied to each created BackupJob.
	// +optional
	Retention BackupRetentionSpec `json:"retention,omitempty"`

	// ExecutionMode is the execution mode copied to each created BackupJob.
	// +kubebuilder:validation:Enum=sidecar;job;""
	// +optional
	ExecutionMode string `json:"executionMode,omitempty"`

	// JobTemplate is the runner batch/v1 Job template copied to each created BackupJob when
	// ExecutionMode="job".
	// +optional
	JobTemplate *batchv1.JobTemplateSpec `json:"jobTemplate,omitempty"`

	// Labels are copied to spec.labels of each created BackupJob.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ScheduledBackupStatus is the observed state of the scheduled-backup controller.
type ScheduledBackupStatus struct {
	// LastScheduleTime is the schedule time at which BackupJob creation was last attempted.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// LastBackupJobName is the name of the most recently created BackupJob.
	// +optional
	LastBackupJobName string `json:"lastBackupJobName,omitempty"`

	// NextScheduleTime is the next scheduled creation time.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// ObservedGeneration is the spec generation last processed by the reconciler.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the standard Kubernetes condition set.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=sb,categories=postgres;backup;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="LastBackup",type=string,JSONPath=`.status.lastBackupJobName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduledBackup creates atomic BackupJobs according to a cron schedule.
type ScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduledBackupSpec   `json:"spec,omitempty"`
	Status ScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScheduledBackupList is a collection of ScheduledBackup resources.
type ScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScheduledBackup{}, &ScheduledBackupList{})
}
