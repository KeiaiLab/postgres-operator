/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DatabaseEnsure is the declarative existence state of a PostgreSQL database/schema/extension.
// +kubebuilder:validation:Enum=present;absent
type DatabaseEnsure string

const (
	DatabaseEnsurePresent DatabaseEnsure = "present"
	DatabaseEnsureAbsent  DatabaseEnsure = "absent"
)

// DatabaseReclaimPolicy controls how the PostgreSQL database is handled when the PostgresDatabase CR is deleted.
// +kubebuilder:validation:Enum=retain;delete
type DatabaseReclaimPolicy string

const (
	DatabaseReclaimRetain DatabaseReclaimPolicy = "retain"
	DatabaseReclaimDelete DatabaseReclaimPolicy = "delete"
)

// DatabaseUsageType is the reconcile action for FDW/server USAGE privileges.
// +kubebuilder:validation:Enum=grant;revoke
type DatabaseUsageType string

const (
	DatabaseUsageGrant  DatabaseUsageType = "grant"
	DatabaseUsageRevoke DatabaseUsageType = "revoke"
)

// DatabaseClusterRef is a reference to a PostgresCluster in the same namespace.
type DatabaseClusterRef struct {
	// Name is the PostgresCluster.metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// DatabaseExtensionSpec is an extension managed inside the target database.
type DatabaseExtensionSpec struct {
	// Name is the PostgreSQL extension name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Ensure is the desired extension existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// Version is the extension version to install or upgrade to.
	// +optional
	Version string `json:"version,omitempty"`

	// Schema is the schema in which the extension is placed.
	// +optional
	Schema string `json:"schema,omitempty"`
}

// DatabaseSchemaSpec is a schema managed inside the target database.
type DatabaseSchemaSpec struct {
	// Name is the PostgreSQL schema name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Owner is the schema owner role.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Ensure is the desired schema existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// Privileges is the list of role privilege grants/revokes on this schema.
	// +optional
	Privileges []DatabaseGrantSpec `json:"privileges,omitempty"`
}

// DatabaseOptionSpec is the declared state of a single FDW/server option.
type DatabaseOptionSpec struct {
	// Name is the option name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Value is the option value. May be omitted when ensure=absent.
	// +optional
	Value string `json:"value,omitempty"`

	// Ensure is the desired option existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`
}

// DatabaseUsageSpec is a reconcile entry for granting or revoking role USAGE on an FDW/server.
type DatabaseUsageSpec struct {
	// Name is the PostgreSQL role to grant or revoke privileges from.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is grant or revoke. Empty defaults to grant.
	// +kubebuilder:default=grant
	// +optional
	Type DatabaseUsageType `json:"type,omitempty"`
}

// DatabaseGrantSpec is a database- or schema-level privilege grant/revoke entry.
type DatabaseGrantSpec struct {
	// Role is the PostgreSQL role to grant or revoke privileges from.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Role string `json:"role"`

	// Privileges is the list of PostgreSQL privileges to apply to the target object.
	// For databases: CONNECT, CREATE, TEMPORARY, TEMP, ALL PRIVILEGES.
	// For schemas: USAGE, CREATE, ALL PRIVILEGES.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Privileges []string `json:"privileges"`

	// Type is grant or revoke. Empty defaults to grant.
	// +kubebuilder:default=grant
	// +optional
	Type DatabaseUsageType `json:"type,omitempty"`
}

// DatabaseFDWSpec is a foreign data wrapper managed inside the target database.
type DatabaseFDWSpec struct {
	// Name is the foreign data wrapper name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Ensure is the desired FDW existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// Handler is the FDW handler function name. "-" means NO HANDLER.
	// +optional
	Handler string `json:"handler,omitempty"`

	// Validator is the FDW validator function name. "-" means NO VALIDATOR.
	// +optional
	Validator string `json:"validator,omitempty"`

	// Owner is the FDW owner role.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Options is the list of FDW options.
	// +optional
	Options []DatabaseOptionSpec `json:"options,omitempty"`

	// Usage is the list of FDW USAGE privilege entries.
	// +optional
	Usage []DatabaseUsageSpec `json:"usage,omitempty"`
}

// DatabaseServerSpec is a foreign server managed inside the target database.
type DatabaseServerSpec struct {
	// Name is the foreign server name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// FDW is the foreign data wrapper name this server uses.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	FDW string `json:"fdw"`

	// Ensure is the desired server existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// Options is the list of server options.
	// +optional
	Options []DatabaseOptionSpec `json:"options,omitempty"`

	// Usage is the list of server USAGE privilege entries.
	// +optional
	Usage []DatabaseUsageSpec `json:"usage,omitempty"`
}

// PostgresDatabaseSpec ports the core operational surface of the CNPG Database CRD to this operator's model.
//
// +kubebuilder:validation:XValidation:rule="self.name != 'postgres' && self.name != 'template0' && self.name != 'template1'",message="postgres, template0, template1 are reserved database names"
type PostgresDatabaseSpec struct {
	// Cluster is the PostgresCluster in which the database is created.
	// +kubebuilder:validation:Required
	Cluster DatabaseClusterRef `json:"cluster"`

	// Name is the PostgreSQL database name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Owner is the database owner role.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// Ensure is the desired database existence state. Empty defaults to present.
	// +kubebuilder:default=present
	// +optional
	Ensure DatabaseEnsure `json:"ensure,omitempty"`

	// DatabaseReclaimPolicy controls how the database is handled when the CR is deleted. Empty defaults to retain.
	// +kubebuilder:default=retain
	// +optional
	DatabaseReclaimPolicy DatabaseReclaimPolicy `json:"databaseReclaimPolicy,omitempty"`

	// Tablespace is the default tablespace for the database.
	// +optional
	Tablespace string `json:"tablespace,omitempty"`

	// Extensions is the list of extensions declaratively managed inside the target database.
	// +optional
	Extensions []DatabaseExtensionSpec `json:"extensions,omitempty"`

	// Schemas is the list of schemas declaratively managed inside the target database.
	// +optional
	Schemas []DatabaseSchemaSpec `json:"schemas,omitempty"`

	// FDWs is the list of foreign data wrappers declaratively managed inside the target database.
	// +optional
	FDWs []DatabaseFDWSpec `json:"fdws,omitempty"`

	// Servers is the list of foreign servers declaratively managed inside the target database.
	// +optional
	Servers []DatabaseServerSpec `json:"servers,omitempty"`

	// Privileges is the list of role privilege grants/revokes on the database object itself.
	// +optional
	Privileges []DatabaseGrantSpec `json:"privileges,omitempty"`
}

// PostgresDatabaseStatus is the observed reconcile state of the database.
type PostgresDatabaseStatus struct {
	// Applied reports whether the latest observedGeneration was successfully applied to PostgreSQL.
	// +optional
	Applied bool `json:"applied,omitempty"`

	// ObservedGeneration is the last generation processed by the reconciler.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Message is a summary of the last reconcile or the failure cause.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions is the standard Kubernetes condition set.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=pgdb,categories=postgres;database;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Applied",type=boolean,JSONPath=`.status.applied`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PostgresDatabase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresDatabaseSpec   `json:"spec,omitempty"`
	Status PostgresDatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PostgresDatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresDatabase `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresDatabase{}, &PostgresDatabaseList{})
}
