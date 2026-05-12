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

// PoolerType is the PostgreSQL service role that PgBouncer connects to.
// +kubebuilder:validation:Enum=rw;ro
type PoolerType string

const (
	// PoolerTypeRW connects to the primary's write endpoint.
	PoolerTypeRW PoolerType = "rw"
	// PoolerTypeRO connects to the replica read endpoint. If no replica is available, it does not
	// fail-closed by falling back to the primary.
	PoolerTypeRO PoolerType = "ro"
)

// PoolerPoolMode is the value of PgBouncer's pool_mode setting.
// +kubebuilder:validation:Enum=session;transaction;statement
type PoolerPoolMode string

const (
	PoolerPoolModeSession     PoolerPoolMode = "session"
	PoolerPoolModeTransaction PoolerPoolMode = "transaction"
	PoolerPoolModeStatement   PoolerPoolMode = "statement"
)

// PoolerClusterRef is a reference to a PostgresCluster in the same namespace.
type PoolerClusterRef struct {
	// Name is the PostgresCluster.metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// PgBouncerSpec is the PgBouncer runtime configuration created by the Pooler.
type PgBouncerSpec struct {
	// Image is the PgBouncer container image. PgBouncer 1.19+ is required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// PoolMode is the PgBouncer pool_mode value.
	// +kubebuilder:default=session
	// +optional
	PoolMode PoolerPoolMode `json:"poolMode,omitempty"`

	// Parameters is a free-form key/value set merged into the [pgbouncer] section of pgbouncer.ini.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// PgHBA is the list of access-control rules written to the PgBouncer HBA file.
	// +optional
	PgHBA []string `json:"pg_hba,omitempty"`

	// AuthSecretRef is the name of the Secret that supplies userlist.txt.
	// When empty, the operator enables the built-in auth path: it automatically creates a
	// `keiailab_pooler_pgbouncer` LOGIN role with a random password on the PostgresCluster's
	// ready primary Pod and creates a `<pooler-name>-builtin-auth` Secret holding userlist.txt
	// with the Pooler OwnerReference (compatible with the CNPG `cnpg_pooler_pgbouncer` pattern).
	// A user-supplied Secret takes precedence.
	// +optional
	AuthSecretRef *corev1.LocalObjectReference `json:"authSecretRef,omitempty"`

	// ServerTLSSecret is the tls.crt/tls.key Secret used for mTLS to the PostgreSQL server.
	// +optional
	ServerTLSSecret *corev1.LocalObjectReference `json:"serverTLSSecret,omitempty"`

	// ServerCASecret is the ca.crt Secret used to verify the PostgreSQL server certificate.
	// +optional
	ServerCASecret *corev1.LocalObjectReference `json:"serverCASecret,omitempty"`

	// ClientTLSSecret is the tls.crt/tls.key Secret used when accepting client TLS connections.
	// +optional
	ClientTLSSecret *corev1.LocalObjectReference `json:"clientTLSSecret,omitempty"`

	// ClientCASecret is the ca.crt Secret used to verify client certificates.
	// +optional
	ClientCASecret *corev1.LocalObjectReference `json:"clientCASecret,omitempty"`

	// AutoTLS auto-issues server/client TLS Secrets via cert-manager integration.
	// When this surface is set and ServerTLSSecret/ClientTLSSecret are empty, the operator
	// creates cert-manager Certificate CRs and delegates Secret issuance to cert-manager.
	// Compatible with the CNPG cert-manager integration surface (T29).
	// +optional
	AutoTLS *PoolerAutoTLSSpec `json:"autoTLS,omitempty"`

	// Exporter is the PgBouncer Prometheus exporter sidecar configuration.
	// +optional
	Exporter *PgBouncerExporterSpec `json:"exporter,omitempty"`
}

// PoolerAutoTLSSpec is the configuration for auto-issuing TLS Secrets via a cert-manager
// Issuer or ClusterIssuer. PoolerReconciler uses this spec to create cert-manager
// `Certificate` CRs and mounts the Secrets cert-manager issues into the PgBouncer Deployment.
//
// Issued Secret naming convention:
//   - Server: `<pooler-name>-server-tls`
//   - Client: `<pooler-name>-client-tls`
//
// If the user explicitly sets ServerTLSSecret/ClientTLSSecret, those take precedence over auto-issuance.
type PoolerAutoTLSSpec struct {
	// IssuerRef is a reference to a cert-manager Issuer or ClusterIssuer.
	// +kubebuilder:validation:Required
	IssuerRef PoolerCertIssuerRef `json:"issuerRef"`

	// ServerEnabled=true auto-issues a server TLS Secret (for connecting to the PostgreSQL backend).
	// +kubebuilder:default=false
	// +optional
	ServerEnabled bool `json:"serverEnabled,omitempty"`

	// ClientEnabled=true auto-issues a client TLS Secret (for accepting external application connections).
	// +kubebuilder:default=true
	// +optional
	ClientEnabled bool `json:"clientEnabled,omitempty"`

	// CommonName is the commonName of the issued Certificate. If empty, the Pooler Service DNS is used.
	// +optional
	CommonName string `json:"commonName,omitempty"`

	// DNSNames are the additional SANs on the issued Certificate. The defaults are
	// `<pooler>.<ns>.svc` and `<pooler>.<ns>.svc.cluster.local`; user-supplied entries are unioned
	// with these defaults.
	// +optional
	DNSNames []string `json:"dnsNames,omitempty"`
}

// PoolerCertIssuerRef is a reference to a cert-manager Issuer or ClusterIssuer.
type PoolerCertIssuerRef struct {
	// Name is the cert-manager Issuer/ClusterIssuer name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind is `Issuer` (namespace-scoped) or `ClusterIssuer` (cluster-scoped).
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +kubebuilder:default=Issuer
	// +optional
	Kind string `json:"kind,omitempty"`
}

// PgBouncerExporterSpec is the contract for the PgBouncer metrics sidecar.
type PgBouncerExporterSpec struct {
	// Image is the exporter container image.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Port is the exporter's HTTP metrics port. Defaults to 9127 when empty.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9127
	// +optional
	Port int32 `json:"port,omitempty"`

	// Args overrides the exporter container args.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env is the exporter container environment.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources is the exporter container resource requests/limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// PoolerServiceTemplateSpec is the safe override surface for the PgBouncer Service.
type PoolerServiceTemplateSpec struct {
	// Type is the Service type. Empty defaults to ClusterIP.
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Labels are added to Service metadata.labels.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are added to Service metadata.annotations.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Ports is the list of additional Service ports. If a name or port collides with the
	// default pgbouncer port, the user-supplied port wins.
	// +optional
	Ports []corev1.ServicePort `json:"ports,omitempty"`
}

// PoolerSpec ports the core operational surface of the CNPG Pooler to this operator's model.
type PoolerSpec struct {
	// Cluster is the PostgresCluster PgBouncer fronts.
	// +kubebuilder:validation:Required
	Cluster PoolerClusterRef `json:"cluster"`

	// Instances is the number of PgBouncer Pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Instances int32 `json:"instances,omitempty"`

	// Type selects the rw (primary) or ro (replica) endpoint.
	// +kubebuilder:default=rw
	// +optional
	Type PoolerType `json:"type,omitempty"`

	// Paused declaratively controls PgBouncer PAUSE/RESUME state.
	// Setting it to true causes the operator to apply PAUSE to ready PgBouncer Pods;
	// setting it back to false causes RESUME.
	// +kubebuilder:default=false
	// +optional
	Paused bool `json:"paused,omitempty"`

	// PgBouncer is the PgBouncer configuration.
	// +kubebuilder:validation:Required
	PgBouncer PgBouncerSpec `json:"pgbouncer"`

	// Template is the PgBouncer Pod template override.
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`

	// DeploymentStrategy is the PgBouncer Deployment replacement strategy. Empty uses a
	// zero-unavailable rolling update.
	// +optional
	DeploymentStrategy *appsv1.DeploymentStrategy `json:"deploymentStrategy,omitempty"`

	// ServiceTemplate is the PgBouncer Service override.
	// +optional
	ServiceTemplate *PoolerServiceTemplateSpec `json:"serviceTemplate,omitempty"`

	// ServiceAccountName is the name of an existing ServiceAccount used by Pooler Pods.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// PoolerPhase is the reconcile state of a Pooler.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type PoolerPhase string

const (
	PoolerPending PoolerPhase = "Pending"
	PoolerReady   PoolerPhase = "Ready"
	PoolerFailed  PoolerPhase = "Failed"
)

// PoolerStatus is the observed state of the PgBouncer subordinate resources.
type PoolerStatus struct {
	// Phase is the Pooler reconcile state.
	Phase PoolerPhase `json:"phase,omitempty"`

	// Instances is the replica count the PgBouncer Deployment is converging on.
	Instances int32 `json:"instances,omitempty"`

	// ReadyReplicas is the readyReplicas value observed on the Deployment.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Paused is the PAUSE/RESUME state last converged across all ready PgBouncer Pods.
	Paused bool `json:"paused,omitempty"`

	// BackendTargets is the list of PostgreSQL backend DNS names the current PgBouncer config routes to.
	// +optional
	BackendTargets []string `json:"backendTargets,omitempty"`

	// ConfigHash is the sha256 of the current PgBouncer config.
	ConfigHash string `json:"configHash,omitempty"`

	// BuiltinAuthLastRotation is the time the operator-managed built-in auth last rotated
	// its password. It is updated whenever the user triggers a forced rotation by applying
	// the `postgres.keiailab.io/rotate-pooler-password=true` annotation. It is unused on
	// the user-supplied path where spec.pgbouncer.authSecretRef is set.
	// +optional
	BuiltinAuthLastRotation *metav1.Time `json:"builtinAuthLastRotation,omitempty"`

	// ObservedGeneration is the last generation processed by the reconciler.
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
// +kubebuilder:resource:scope=Namespaced,shortName=pool,categories=postgres;pooler;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.spec.instances`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Pooler is the PgBouncer-based PostgreSQL connection pool layer.
type Pooler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoolerSpec   `json:"spec,omitempty"`
	Status PoolerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PoolerList is a collection of Pooler resources.
type PoolerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pooler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pooler{}, &PoolerList{})
}
