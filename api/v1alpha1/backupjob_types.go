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

// BackupJobSpec is the specification of a single backup invocation (RFC 0004 §2.1).
//
// Immutable fields (cannot be changed after creation; the webhook will reject — phase 2):
//   - cluster.name
//   - tool
//   - type
//   - executionMode
type BackupJobSpec struct {
	// Cluster references the PostgresCluster to back up (same namespace).
	// +kubebuilder:validation:Required
	Cluster BackupClusterRef `json:"cluster"`

	// Tool is the name of the backup tool to use; must match BackupPlugin.Name().
	// Examples: "pgbackrest" (RFC 0004 §4 primary reference), "walg", "barman".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Repo identifies which repository to use in multi-repo deployments.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Type is the backup kind.
	// +kubebuilder:validation:Enum=full;incremental;differential;restore
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Restore holds the PITR settings when Type=restore (RFC 0004 §5.1).
	// +optional
	Restore *BackupRestoreSpec `json:"restore,omitempty"`

	// Retention is the backup retention policy.
	// +optional
	Retention BackupRetentionSpec `json:"retention,omitempty"`

	// ExecutionMode controls *where* the plugin runs the backup (P1-6, RFC 0004 §3.1):
	//   - "sidecar": a container co-located with the PG Pod (pgBackRest pattern)
	//   - "job":     a standalone Kubernetes Job (WAL-G pattern)
	//   - "":        plugin default
	// +kubebuilder:validation:Enum=sidecar;job;""
	// +optional
	ExecutionMode string `json:"executionMode,omitempty"`

	// JobTemplate is the runner batch/v1 Job template used when ExecutionMode="job".
	// The operator only injects name/namespace/ownerReference/standard labels/env;
	// execution details such as image, command, volume, secret, resources, and
	// securityContext are taken verbatim from this template.
	// +optional
	JobTemplate *batchv1.JobTemplateSpec `json:"jobTemplate,omitempty"`

	// Labels are Kubernetes-style labels attached to the BackupResult metadata.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// BackupClusterRef is a reference to a PostgresCluster in the same namespace.
// The name is BackupClusterRef rather than ClusterRef to follow the kubebuilder convention
// of per-type prefixes, preventing future CRDs from clashing on the same type name.
type BackupClusterRef struct {
	// Name is the PostgresCluster.metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BackupRestoreSpec is the PITR restore specification (RFC 0004 §5.1).
type BackupRestoreSpec struct {
	// TargetTime is the target recovery time (UTC, ISO 8601 RFC3339).
	// +optional
	TargetTime *metav1.Time `json:"targetTime,omitempty"`

	// BackupID is the identifier of the backup to restore from (used instead of TargetTime).
	// +optional
	BackupID string `json:"backupID,omitempty"`
}

// BackupRetentionSpec is the backup retention policy.
type BackupRetentionSpec struct {
	// KeepFull is the number of full backups to retain. 0 means unlimited (plugin default).
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepFull int32 `json:"keepFull,omitempty"`

	// KeepIncremental is the number of incremental/differential backups to retain.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepIncremental int32 `json:"keepIncremental,omitempty"`
}

// BackupJobPhase is the progress phase of a BackupJob.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type BackupJobPhase string

const (
	BackupJobPending   BackupJobPhase = "Pending"
	BackupJobRunning   BackupJobPhase = "Running"
	BackupJobSucceeded BackupJobPhase = "Succeeded"
	BackupJobFailed    BackupJobPhase = "Failed"
)

// BackupJobStatus is the observed state of a BackupJob.
type BackupJobStatus struct {
	// Phase is the progress phase of the BackupJob.
	Phase BackupJobPhase `json:"phase,omitempty"`

	// BackupID is the unique identifier assigned by the plugin (populated after Succeeded).
	BackupID string `json:"backupID,omitempty"`

	// RunnerJobName is the name of the batch/v1 Job created when ExecutionMode="job".
	RunnerJobName string `json:"runnerJobName,omitempty"`

	// StartedAt is when the backup started.
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// EndedAt is when the backup ended.
	EndedAt *metav1.Time `json:"endedAt,omitempty"`

	// Bytes is the backup size in bytes. 0 is allowed (streaming tools).
	Bytes int64 `json:"bytes,omitempty"`

	// ObservedGeneration is the spec generation last processed by the reconciler.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the standard Kubernetes condition set (Ready, Available, Progressing).
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=bj,categories=postgres;backup;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.spec.tool`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupJob is a single backup-execution command (RFC 0004).
//
// This CRD is an *atomic unit* — any change requires creating a new BackupJob. Cron-based
// scheduled backups are separated out: the ScheduledBackup CRD creates BackupJobs.
type BackupJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupJobSpec   `json:"spec,omitempty"`
	Status BackupJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupJobList is a list response of BackupJob resources.
type BackupJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupJob{}, &BackupJobList{})
}
