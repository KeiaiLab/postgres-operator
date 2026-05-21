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

// This file expresses the spec/status definitions of RFC 0001 (PostgresCluster CRD v2) §3
// as Go types. As of 0.3.0-alpha, the schema has been redefined without compatibility
// guarantees under the alpha-channel policy (the breaking change is documented in the
// CHANGELOG). The earlier v0.x schema
// (`coordinator/workers/routers/extensions/sharding.backend`) is preserved in the
// archived RFC/ADR documents under _archive.

// ShardingMode represents the two operating modes: single-node (none) and native distributed SQL (native).
// +kubebuilder:validation:Enum=none;native
type ShardingMode string

const (
	// ShardingModeNone uses a single shard with no router. GA form for 0.4.0 (P1).
	ShardingModeNone ShardingMode = "none"
	// ShardingModeNative is native distributed SQL (router + multi-shard). Enabled in P2+.
	ShardingModeNative ShardingMode = "native"
)

// StorageSpec is the PVC provisioning parameter set (RFC 0001 §3).
type StorageSpec struct {
	// Size is the requested PVC size (e.g. "100Gi").
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// StorageClass is the PVC StorageClass name. Empty string uses the cluster default.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessModes are the PVC access modes (defaults to ReadWriteOnce when empty).
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// ShardsSpec defines the shard topology (RFC 0001 §3.1).
type ShardsSpec struct {
	// InitialCount is the initial number of shards in the cluster. P1 GA guarantees 1 only;
	// the schema permits up to 1024.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:validation:Required
	InitialCount int32 `json:"initialCount"`

	// Storage is the per-shard PVC specification.
	// +kubebuilder:validation:Required
	Storage StorageSpec `json:"storage"`

	// Replicas is the number of asynchronous replicas per shard (excluding the primary).
	// 0 means no HA (development only).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=15
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Resources is the resource requirements for the shard PG container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Affinity is the shard Pod affinity ruleset.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations is the node toleration set for shard Pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PriorityClassName is the priorityClass name for shard Pods. Modern HA Layer 3 (eviction priority).
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// TopologySpreadConstraints is the multi-node spread policy for shard Pods. Modern HA Layer 2 (SPOF avoidance).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// RouterAutoscaleSpec is the router HPA configuration (RFC 0001 §3.1).
type RouterAutoscaleSpec struct {
	// Enabled controls whether an HPA is attached.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// MinReplicas is the HPA minimum replica count.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinReplicas int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the HPA maximum replica count.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxReplicas int32 `json:"maxReplicas,omitempty"`

	// TargetCPU is the HPA CPU utilization target (%).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=70
	// +optional
	TargetCPU int32 `json:"targetCPU,omitempty"`

	// TargetActiveConnections is the HPA active-connection target.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1000
	// +optional
	TargetActiveConnections int32 `json:"targetActiveConnections,omitempty"`
}

// RouterSpec is the stateless QueryRouter Deployment configuration (RFC 0001 §3.1, RFC 0004).
//
// This struct intentionally omits a Storage field — the router's statelessness is enforced
// at the type level.
type RouterSpec struct {
	// Enabled controls whether the router is enabled. Defaults to true when shardingMode=native.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Replicas is the number of router Pods. When an HPA is attached this acts as the minimum.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Autoscale is the HPA configuration.
	// +optional
	Autoscale *RouterAutoscaleSpec `json:"autoscale,omitempty"`

	// Resources is the resource requirements for the router container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AutoSplitTriggers holds the threshold conditions for automatic shard splitting (all AND-ed).
type AutoSplitTriggers struct {
	// SizeThresholdGB is the per-shard size threshold (GB). 0 disables this trigger.
	// +kubebuilder:validation:Minimum=0
	// +optional
	SizeThresholdGB int32 `json:"sizeThresholdGB,omitempty"`

	// P99LatencyMs is the per-shard P99 latency threshold (ms). 0 disables this trigger.
	// +kubebuilder:validation:Minimum=0
	// +optional
	P99LatencyMs int32 `json:"p99LatencyMs,omitempty"`

	// CPUPercent is the per-shard average CPU utilization threshold (%). 0 disables this trigger.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUPercent int32 `json:"cpuPercent,omitempty"`

	// DurationMinutes is how long the thresholds above must be sustained (minutes). 0 means immediate.
	// +kubebuilder:validation:Minimum=0
	// +optional
	DurationMinutes int32 `json:"durationMinutes,omitempty"`
}

// AutoSplitSpec is the automatic shard-split policy (RFC 0001 §3.1, follow-up to RFC 0003).
type AutoSplitSpec struct {
	// Enabled controls whether automatic splitting is enabled. Only meaningful in P2+.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// RequireApproval submits auto-split candidates as ShardSplitJobs but requires a manual
	// approval annotation before they execute (production safety).
	// +kubebuilder:default=true
	// +optional
	RequireApproval bool `json:"requireApproval,omitempty"`

	// Triggers is the set of split thresholds.
	// +optional
	Triggers *AutoSplitTriggers `json:"triggers,omitempty"`
}

// ClusterBackupRetentionSpec is the retention policy for PostgresCluster.spec.backup.
//
// The Cluster prefix avoids a type collision with BackupJob's BackupRetentionSpec
// (both live in v1alpha1). BackupJob is an atomic unit of work, whereas this struct
// represents the retention policy for cluster-level scheduled backups — distinct semantics.
type ClusterBackupRetentionSpec struct {
	// Full is the full-backup retention period (duration, e.g. "7d", "168h").
	// +optional
	Full string `json:"full,omitempty"`

	// Incremental is the incremental-backup retention period.
	// +optional
	Incremental string `json:"incremental,omitempty"`

	// WALArchive is the WAL-archive retention period.
	// +optional
	WALArchive string `json:"walArchive,omitempty"`
}

// ClusterBackupRepoSpec is the backup repository configuration.
type ClusterBackupRepoSpec struct {
	// Type is the repository kind (s3 | gcs | azure | filesystem).
	// +kubebuilder:validation:Enum=s3;gcs;azure;filesystem
	// +optional
	Type string `json:"type,omitempty"`

	// Bucket is the object-storage bucket name.
	// +optional
	Bucket string `json:"bucket,omitempty"`

	// Region is the object-storage region.
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint is the S3-compatible endpoint URL (optional).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Path is the filesystem path (used when Type=filesystem).
	// +optional
	Path string `json:"path,omitempty"`
}

// ClusterBackupSpec is the cluster-level backup/PITR policy (RFC 0001 §3.1, RFC 0004).
type ClusterBackupSpec struct {
	// Enabled controls whether the backup policy is active (explicit user opt-in).
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Schedule is the cron expression (e.g. "0 2 * * *").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Retention is the backup retention policy.
	// +optional
	Retention *ClusterBackupRetentionSpec `json:"retention,omitempty"`

	// Repo is the backup repository configuration.
	// +optional
	Repo *ClusterBackupRepoSpec `json:"repo,omitempty"`
}

// ServiceMonitorSpec is the configuration for auto-deployed Prometheus ServiceMonitors.
type ServiceMonitorSpec struct {
	// Enabled controls whether a ServiceMonitor is created.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Interval is the scrape interval (e.g. "30s").
	// +kubebuilder:default="30s"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// PrometheusRuleSpec is the configuration for auto-deployed PrometheusRules.
type PrometheusRuleSpec struct {
	// Enabled controls whether a PrometheusRule is created.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// MonitoringSpec is the monitoring-integration configuration (RFC 0001 §3.1).
type MonitoringSpec struct {
	// ServiceMonitor auto-deploys a Prometheus Operator ServiceMonitor.
	// +optional
	ServiceMonitor *ServiceMonitorSpec `json:"serviceMonitor,omitempty"`

	// PrometheusRule auto-deploys alerting rules.
	// +optional
	PrometheusRule *PrometheusRuleSpec `json:"prometheusRule,omitempty"`
}

// ExternalClusterSpec defines connection information for an external PostgreSQL cluster
// used as a replica cluster source or bootstrap source. It starts with the minimum streaming
// path compatible with the CloudNativePG externalClusters surface.
type ExternalClusterSpec struct {
	// Name is the identifier referenced from bootstrap/replica.source.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// ConnectionParameters is a PostgreSQL connection string in key/value form.
	// Supported keys: host, port, user, dbname, sslmode. host is required;
	// port defaults to 5432 when omitted.
	// +optional
	ConnectionParameters map[string]string `json:"connectionParameters,omitempty"`

	// Password is a Secret key reference used for libpq password authentication.
	// Mirrors the CloudNativePG externalClusters[].password shape.
	// +optional
	Password *corev1.SecretKeySelector `json:"password,omitempty"`

	// SSLKey is a Secret key reference holding the private key used for client-certificate authentication.
	// +optional
	SSLKey *corev1.SecretKeySelector `json:"sslKey,omitempty"`

	// SSLCert is a Secret key reference holding the certificate used for client-certificate authentication.
	// +optional
	SSLCert *corev1.SecretKeySelector `json:"sslCert,omitempty"`

	// SSLRootCert is a Secret key reference holding the CA used to verify the source cluster's server certificate.
	// +optional
	SSLRootCert *corev1.SecretKeySelector `json:"sslRootCert,omitempty"`
}

// PgBaseBackupBootstrapSpec is the bootstrap mode that performs pg_basebackup from an
// external cluster.
type PgBaseBackupBootstrapSpec struct {
	// Source references spec.externalClusters[].name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Source string `json:"source"`
}

// BootstrapSpec selects the cluster bootstrap method.
type BootstrapSpec struct {
	// PgBaseBackup is the bootstrap method that takes a physical base backup from an
	// external source via the streaming replication protocol.
	// +optional
	PgBaseBackup *PgBaseBackupBootstrapSpec `json:"pg_basebackup,omitempty"`
}

// ReplicaClusterSpec is the declarative surface for CloudNativePG standalone/distributed
// replica clusters. In 0.3.0-alpha the continuous-recovery path for a standalone streaming
// replica cluster is prioritized.
type ReplicaClusterSpec struct {
	// Enabled activates continuous recovery for a standalone replica cluster.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Source references spec.externalClusters[].name.
	// +optional
	Source string `json:"source,omitempty"`

	// Primary is the name of the current global primary cluster in a distributed topology.
	// In 0.3.0-alpha only the API surface is provided; controlled switchover is implemented later.
	// +optional
	Primary string `json:"primary,omitempty"`

	// Self specifies the topology-local name when it differs from the resource name.
	// +optional
	Self string `json:"self,omitempty"`

	// PromotionToken is the token used for controlled switchover/promotion.
	// In 0.3.0-alpha only the API surface is provided.
	// +optional
	PromotionToken string `json:"promotionToken,omitempty"`
}

// PostgresClusterSpec is the desired state of a PostgresCluster CR (RFC 0001 §3).
//
// CEL validation (kubebuilder v0.15+):
//
// +kubebuilder:validation:XValidation:rule="self.shardingMode != 'native' || self.shards.initialCount >= 1",message="native sharding requires shards.initialCount >= 1"
// +kubebuilder:validation:XValidation:rule="!has(self.router) || self.shardingMode == 'native'",message="router is only valid when shardingMode=native"
// +kubebuilder:validation:XValidation:rule="!has(self.autoSplit) || self.autoSplit.enabled == false || self.shardingMode == 'native'",message="autoSplit requires shardingMode=native"
// +kubebuilder:validation:XValidation:rule="!has(self.postgresql) || !has(self.postgresql.synchronous) || self.shards.replicas >= self.postgresql.synchronous.number",message="postgresql.synchronous.number must be <= shards.replicas"
type PostgresClusterSpec struct {
	// PostgresVersion is the major-version string. 0.3.0-alpha makes 18 GA while keeping 17 compatible.
	// +kubebuilder:validation:Enum="17";"18"
	// +kubebuilder:default="18"
	// +optional
	PostgresVersion string `json:"postgresVersion,omitempty"`

	// ExternalClusters is the list of external PostgreSQL clusters used as bootstrap
	// or replica sources.
	// +optional
	// +listType=map
	// +listMapKey=name
	ExternalClusters []ExternalClusterSpec `json:"externalClusters,omitempty"`

	// Bootstrap defines a non-initdb bootstrap method. The current implementation supports
	// streaming bootstrap via pg_basebackup.
	// +optional
	Bootstrap *BootstrapSpec `json:"bootstrap,omitempty"`

	// Replica defines standalone/distributed replica-cluster behavior.
	// +optional
	Replica *ReplicaClusterSpec `json:"replica,omitempty"`

	// ImageCatalogRef selects the PostgreSQL runtime image from an ImageCatalog or
	// ClusterImageCatalog. The field shape mirrors CNPG's spec.imageCatalogRef; when set,
	// Major (rather than PostgresVersion) becomes the single source of truth for image
	// and bin-directory selection.
	// +optional
	ImageCatalogRef *ImageCatalogRef `json:"imageCatalogRef,omitempty"`

	// ShardingMode selects single shard (none) or native distributed SQL (native).
	// +kubebuilder:default=none
	// +optional
	ShardingMode ShardingMode `json:"shardingMode,omitempty"`

	// Shards is the shard topology.
	// +kubebuilder:validation:Required
	Shards ShardsSpec `json:"shards"`

	// Router is the stateless QueryRouter pool. Only meaningful when shardingMode=native.
	// +optional
	Router *RouterSpec `json:"router,omitempty"`

	// AutoSplit is the automatic shard-split policy.
	// +optional
	AutoSplit *AutoSplitSpec `json:"autoSplit,omitempty"`

	// Backup is the backup/PITR policy.
	// +optional
	Backup *ClusterBackupSpec `json:"backup,omitempty"`

	// Monitoring is the monitoring-integration configuration.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`

	// Extensions is the list of PostgreSQL extensions to enable (RFC 0006 R1).
	// Empty means vanilla PG (no extensions beyond the image base). Each name must be
	// an ExtensionPlugin registered in the operator's Registry at bootstrap; the webhook
	// enforces this.
	//
	// Example: ["pgaudit", "pgvector"] — assuming the postgres_v1alpha1 image carries
	// the .so files for both extensions. If the image lacks them, PostgreSQL will FATAL —
	// this is the user's responsibility.
	//
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// PostgreSQL is the PostgreSQL runtime configuration. GUCs that may not be set directly,
	// such as synchronous_standby_names, are generated only via this structured surface.
	// +optional
	PostgreSQL *PostgreSQLSpec `json:"postgresql,omitempty"`

	// TLS is the PostgreSQL server-side TLS configuration (Pillar P7 §7).
	// 0.3.0-alpha.5+ Phase 1 facade — only the CRD field is defined. Phase 2 integrates
	// the cert-manager Certificate CR; Phase 3 has the reconciler emit server.crt/server.key
	// and finalize ssl=on in postgresql.conf. In this alpha release only enabled=false
	// (the default) is meaningful — setting true causes the webhook to reject with
	// NotImplemented (Phase 1).
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`
}

// TLSSpec is the PostgreSQL server-side TLS configuration.
//
// Roadmap (3-phase):
//
//	Phase 1 (alpha.5): this CRD-field facade. enabled=true is rejected by the webhook.
//	Phase 2 (alpha.6): cert-manager Certificate CR is created automatically (using Issuer reference).
//	Phase 3 (alpha.7): reconciler mounts the server cert Secret to the STS volume and sets
//	                   ssl=on plus ssl_cert_file/ssl_key_file in postgresql.conf.
//
// Recovery target: restore the sslmode=verify-ca happy path for external TLS clients.
// Currently ssl=off, forcing sslmode=disable regression (ADR-0062 cycle sealed).
type TLSSpec struct {
	// Enabled controls whether server-side TLS is active. Only false works in Phase 1.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// IssuerRef is a cert-manager Issuer or ClusterIssuer reference.
	// Meaningful from Phase 2 onward. If unset, the operator self-issues a self-signed
	// certificate (Phase 3 default).
	// +optional
	IssuerRef *TLSIssuerRef `json:"issuerRef,omitempty"`

	// CertSecretName is the name of the Secret that stores the server cert. If unset,
	// defaults to "<cluster>-tls". Used as cert-manager Certificate.spec.secretName in Phase 2.
	// +optional
	CertSecretName string `json:"certSecretName,omitempty"`
}

// TLSIssuerRef is a reference to a cert-manager Issuer or ClusterIssuer.
type TLSIssuerRef struct {
	// Name is the Issuer name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Kind is Issuer or ClusterIssuer.
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +kubebuilder:default=Issuer
	// +optional
	Kind string `json:"kind,omitempty"`
}

// ClusterPhase is the high-level phase of the cluster.
// +kubebuilder:validation:Enum=Provisioning;Ready;Reconfiguring;Degraded;Hibernated
type ClusterPhase string

const (
	ClusterPhaseProvisioning  ClusterPhase = "Provisioning"
	ClusterPhaseReady         ClusterPhase = "Ready"
	ClusterPhaseReconfiguring ClusterPhase = "Reconfiguring"
	ClusterPhaseDegraded      ClusterPhase = "Degraded"
	ClusterPhaseHibernated    ClusterPhase = "Hibernated"
)

// SynchronousReplicationMethod represents the quorum/priority selection mode used in
// PostgreSQL's synchronous_standby_names.
// +kubebuilder:validation:Enum=any;first
type SynchronousReplicationMethod string

const (
	// SynchronousReplicationMethodAny is the quorum-based ANY N (...) mode.
	SynchronousReplicationMethodAny SynchronousReplicationMethod = "any"
	// SynchronousReplicationMethodFirst is the priority-based FIRST N (...) mode.
	SynchronousReplicationMethodFirst SynchronousReplicationMethod = "first"
)

// SynchronousReplicationDataDurability is the durability/availability priority for
// synchronous replication.
// +kubebuilder:validation:Enum=required;preferred
type SynchronousReplicationDataDurability string

const (
	// SynchronousReplicationDataDurabilityRequired blocks commits when fewer synchronous standbys
	// than requested are available.
	SynchronousReplicationDataDurabilityRequired SynchronousReplicationDataDurability = "required"
	// SynchronousReplicationDataDurabilityPreferred lowers the quorum to match the number of
	// Ready replicas so that write availability is preserved.
	SynchronousReplicationDataDurabilityPreferred SynchronousReplicationDataDurability = "preferred"
)

// SynchronousReplicationSpec is the PostgreSQL physical synchronous replication configuration.
// It exposes the minimum core fields (method/number/dataDurability) compatible with
// CloudNativePG's `.spec.postgresql.synchronous` surface.
type SynchronousReplicationSpec struct {
	// Method selects the synchronous-standby strategy: quorum (any) or priority (first).
	// +kubebuilder:validation:Required
	Method SynchronousReplicationMethod `json:"method"`

	// Number is the count of synchronous standbys a commit must wait for.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Number int32 `json:"number"`

	// DataDurability controls whether writes block when replicas are insufficient (required)
	// or the quorum is lowered to match the number of Ready replicas (preferred).
	// +kubebuilder:validation:Enum=required;preferred
	// +kubebuilder:default=required
	// +optional
	DataDurability SynchronousReplicationDataDurability `json:"dataDurability,omitempty"`
}

// PostgreSQLSpec is the PostgreSQL runtime configuration surface.
type PostgreSQLSpec struct {
	// Synchronous is the physical synchronous-replication configuration. nil disables it.
	// +optional
	Synchronous *SynchronousReplicationSpec `json:"synchronous,omitempty"`
}

// ShardEndpoint is the observed state of a single shard member (primary or replica).
type ShardEndpoint struct {
	// Pod is the Kubernetes Pod name.
	// +optional
	Pod string `json:"pod,omitempty"`

	// Endpoint is the connection endpoint in host:port form.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Ready reports whether readiness has passed.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// LagBytes is the replica's WAL lag in bytes. For the primary this is 0 or unset.
	// +optional
	LagBytes int64 `json:"lagBytes,omitempty"`

	// Reason is the machine-readable cause when the endpoint is Ready=false or degraded.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is the human-readable detail of the endpoint state.
	// +optional
	Message string `json:"message,omitempty"`
}

// ShardStatus is the observed state of a single shard.
type ShardStatus struct {
	// Name is the shard identifier (e.g. "shard-0").
	Name string `json:"name"`

	// Ordinal is the 0-based shard ordinal.
	Ordinal int32 `json:"ordinal"`

	// Primary is the current primary endpoint.
	// +optional
	Primary *ShardEndpoint `json:"primary,omitempty"`

	// Replicas is the list of current replica endpoints.
	// +optional
	Replicas []ShardEndpoint `json:"replicas,omitempty"`

	// SizeBytes is the shard's data size in bytes. 0 means not yet observed.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// LastSplit is the timestamp of the last completed split. nil if no split has occurred.
	// +optional
	LastSplit *metav1.Time `json:"lastSplit,omitempty"`
}

// ClusterRouterStatus is the observed state of the router pool.
type ClusterRouterStatus struct {
	// Replicas is the desired number of router Pods.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of router Pods in the Ready state.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Endpoint is the router Service endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// ManagedRolePasswordStatus reports the apply state of a managed role's password.
type ManagedRolePasswordStatus struct {
	// SecretResourceVersion is the resourceVersion of the password Secret that was last successfully applied.
	// +optional
	SecretResourceVersion string `json:"secretResourceVersion,omitempty"`

	// ObservedGeneration is the PostgresUser generation that produced this applied password.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ManagedRolesStatus aggregates the state of PostgresUser CRs that belong to this PostgresCluster.
// It mirrors CloudNativePG's status.managedRolesStatus surface with the same
// byStatus / cannotReconcile / passwordStatus shape.
type ManagedRolesStatus struct {
	// ByStatus lists role names grouped by their current status.
	// Typical statuses: reserved, reconciled, pending-reconciliation.
	// +optional
	ByStatus map[string][]string `json:"byStatus,omitempty"`

	// CannotReconcile records, per role, errors the operator cannot recover from automatically.
	// +optional
	CannotReconcile map[string][]string `json:"cannotReconcile,omitempty"`

	// PasswordStatus records the Secret revision of each role whose password Secret was successfully applied.
	// +optional
	PasswordStatus map[string]ManagedRolePasswordStatus `json:"passwordStatus,omitempty"`
}

// PostgresClusterStatus is the observed state of a PostgresCluster CR (RFC 0001 §3.2).
type PostgresClusterStatus struct {
	// Phase is the high-level phase of the cluster.
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec.metadata.generation observed by the reconciler.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Shards is the per-shard observed state, ordered by ascending ordinal.
	// +optional
	Shards []ShardStatus `json:"shards,omitempty"`

	// Router is the observed state of the router pool (present when shardingMode=native).
	// +optional
	Router *ClusterRouterStatus `json:"router,omitempty"`

	// ManagedRolesStatus aggregates the state of PostgresUser CRs that reference this cluster.
	// +optional
	ManagedRolesStatus *ManagedRolesStatus `json:"managedRolesStatus,omitempty"`

	// Conditions is the standard Kubernetes condition set.
	// Recommended types: Ready / Progressing / BackupHealthy / AutoSplitEligible / RouterReady.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pgc,categories=postgres;database;all
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Shards",type=integer,JSONPath=".spec.shards.initialCount"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".spec.postgresVersion"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// PostgresCluster is the declarative representation of a Kubernetes-native, natively
// distributed PostgreSQL cluster (ADR 0001).
type PostgresCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresClusterSpec   `json:"spec,omitempty"`
	Status PostgresClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PostgresClusterList is a collection of PostgresCluster resources.
type PostgresClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresCluster{}, &PostgresClusterList{})
}
