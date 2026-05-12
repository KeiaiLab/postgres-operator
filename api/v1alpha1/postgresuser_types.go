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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PostgresUserSpec exposes the core operational surface of CNPG's managed.roles as a separate CRD.
//
// +kubebuilder:validation:XValidation:rule="self.name != 'postgres' && !self.name.startsWith('pg_')",message="postgres and pg_* are reserved role names"
// +kubebuilder:validation:XValidation:rule="!(has(self.passwordSecretRef) && self.disablePassword)",message="passwordSecretRef and disablePassword cannot both be set"
type PostgresUserSpec struct {
	// Cluster is the PostgresCluster in which the role is created.
	// +kubebuilder:validation:Required
	Cluster DatabaseClusterRef `json:"cluster"`

	// Name is the PostgreSQL role name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Ensure is the desired role existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// Login controls whether the role has the LOGIN attribute.
	// +optional
	Login bool `json:"login,omitempty"`

	// Superuser controls whether the role has the SUPERUSER attribute.
	// +optional
	Superuser bool `json:"superuser,omitempty"`

	// CreateDB controls whether the role has the CREATEDB attribute.
	// +optional
	CreateDB bool `json:"createdb,omitempty"`

	// CreateRole controls whether the role has the CREATEROLE attribute.
	// +optional
	CreateRole bool `json:"createrole,omitempty"`

	// Replication controls whether the role has the REPLICATION attribute.
	// +optional
	Replication bool `json:"replication,omitempty"`

	// BypassRLS controls whether the role can bypass row-level security.
	// +optional
	BypassRLS bool `json:"bypassrls,omitempty"`

	// Inherit controls role privilege inheritance. nil reconciles to PostgreSQL's default of true.
	// +optional
	Inherit *bool `json:"inherit,omitempty"`

	// ConnectionLimit is the role's connection limit. nil leaves it unchanged; -1 means unlimited.
	// +kubebuilder:validation:Minimum=-1
	// +optional
	ConnectionLimit *int32 `json:"connectionLimit,omitempty"`

	// InRoles is the list of parent roles that this role becomes a member of.
	// +optional
	InRoles []string `json:"inRoles,omitempty"`

	// PasswordSecretRef is the Secret whose data.username/data.password values are applied as
	// the PostgreSQL role password. data.username must match spec.name.
	// +optional
	PasswordSecretRef *corev1.LocalObjectReference `json:"passwordSecretRef,omitempty"`

	// DisablePassword sets the role password to NULL, disabling password login.
	// +optional
	DisablePassword bool `json:"disablePassword,omitempty"`

	// ValidUntil is the expiry timestamp of the role password. Use any value PostgreSQL accepts
	// as a timestamp, or "infinity".
	// +optional
	ValidUntil string `json:"validUntil,omitempty"`

	// UserReclaimPolicy controls how the PostgreSQL role is handled when the
	// CR is deleted. Empty defaults to "retain" (CR deletion is non-destructive
	// — the role keeps existing in PostgreSQL). When set to "delete" the
	// reconciler attaches a finalizer and runs `DROP ROLE` before allowing
	// the CR to be garbage-collected. Mirrors PostgresDatabase.spec.databaseReclaimPolicy.
	// +kubebuilder:validation:Enum=retain;delete
	// +kubebuilder:default=retain
	// +optional
	UserReclaimPolicy DatabaseReclaimPolicy `json:"userReclaimPolicy,omitempty"`
}

// PostgresUserStatus is the observed reconcile state of the role.
type PostgresUserStatus struct {
	// Applied reports whether the latest observedGeneration was successfully applied to PostgreSQL.
	// +optional
	Applied bool `json:"applied,omitempty"`

	// ObservedGeneration is the last generation processed by the reconciler.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Message is a summary of the last reconcile or the failure cause.
	// +optional
	Message string `json:"message,omitempty"`

	// PasswordSecretResourceVersion is the resourceVersion of the password Secret that was last successfully applied.
	// +optional
	PasswordSecretResourceVersion string `json:"passwordSecretResourceVersion,omitempty"`

	// Conditions is the standard Kubernetes condition set.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=pguser,categories=postgres;role;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Login",type=boolean,JSONPath=`.spec.login`
// +kubebuilder:printcolumn:name="Applied",type=boolean,JSONPath=`.status.applied`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PostgresUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresUserSpec   `json:"spec,omitempty"`
	Status PostgresUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PostgresUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresUser{}, &PostgresUserList{})
}
