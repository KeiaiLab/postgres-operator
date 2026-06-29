/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const poolerTestROName = "demo-ro"

func TestPoolerReconcileCreatesPgBouncerResources(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerPending {
		t.Fatalf("phase = %q, want Pending until Deployment reports ready replicas", got.Status.Phase)
	}
	if got.Status.ReadyReplicas != 0 {
		t.Fatalf("ReadyReplicas = %d, want observed deployment ready count 0", got.Status.ReadyReplicas)
	}

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	for _, want := range []string{
		"pool_mode = transaction",
		"max_client_conn = 2000",
		"default_pool_size = 20",
		"unix_socket_dir = ",
		"* = host=demo-shard-0-0.demo-shard-0-headless.default.svc port=5432",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("pgbouncer.ini missing %q:\n%s", want, config)
		}
	}

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
		t.Fatalf("replicas = %v, want 3", dep.Spec.Replicas)
	}
	container := dep.Spec.Template.Spec.Containers[0]
	if container.Name != "pgbouncer" {
		t.Fatalf("container name = %q, want pgbouncer", container.Name)
	}
	if strings.Join(container.Command, " ") != "/usr/bin/pgbouncer" {
		t.Fatalf("container command = %q, want explicit pgbouncer binary", strings.Join(container.Command, " "))
	}
	if strings.Join(container.Args, " ") != "/etc/pgbouncer/config/pgbouncer.ini" {
		t.Fatalf("container args = %q, want projected config path", strings.Join(container.Args, " "))
	}
	if container.Image != "example.com/pgbouncer:1.24" {
		t.Fatalf("image = %q, want custom image", container.Image)
	}
	if len(dep.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("volumes = %d, want config + auth secret", len(dep.Spec.Template.Spec.Volumes))
	}
	assertPgBouncerProbes(t, container)
	assertPoolerRollingStrategy(t, dep)

	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerServiceName(pooler.Name)}, &svc); err != nil {
		t.Fatalf("Service get: %v", err)
	}
	if svc.Spec.Ports[0].Name != "pgbouncer" || svc.Spec.Ports[0].Port != 5432 {
		t.Fatalf("service port mismatch: %+v", svc.Spec.Ports)
	}
}

func TestPoolerReconcileRequiresAuthSecret(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileClearsOperationalStatusOnFailure(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	pooler.Status = postgresv1alpha1.PoolerStatus{
		Phase:          postgresv1alpha1.PoolerReady,
		Instances:      3,
		ReadyReplicas:  3,
		BackendTargets: []string{"stale.backend.default.svc"},
		ConfigHash:     "stale",
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.ReadyReplicas != 0 || got.Status.Instances != 0 ||
		len(got.Status.BackendTargets) != 0 || got.Status.ConfigHash != "" {
		t.Fatalf("operational status was not cleared on failure: %+v", got.Status)
	}
}

func TestPoolerReconcileRequiresAuthSecretUserlist(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileRejectsUnsupportedPgBouncerParameter(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Parameters["typo_parameter"] = "true"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "typo_parameter") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileAllowsStatsUsersPgBouncerParameter(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Parameters["stats_users"] = "keiailab_pooler_pgbouncer"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"keiailab_pooler_pgbouncer" "md5hash"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase == postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want stats_users accepted", got.Status.Phase)
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	if !strings.Contains(cm.Data["pgbouncer.ini"], "stats_users = keiailab_pooler_pgbouncer") {
		t.Fatalf("pgbouncer.ini missing stats_users:\n%s", cm.Data["pgbouncer.ini"])
	}
}

func TestPoolerReconcileRejectsOperatorOwnedPgBouncerParameter(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Parameters["listen_port"] = "6432"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "listen_port") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileRejectsUnixSocketDirOverride(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Parameters["unix_socket_dir"] = "/tmp"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "unix_socket_dir") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileKeepsRequiredIgnoreStartupParameters(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Parameters["ignore_startup_parameters"] = "application_name"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	if !strings.Contains(config, "ignore_startup_parameters = application_name,extra_float_digits,options") {
		t.Fatalf("pgbouncer.ini missing merged ignore_startup_parameters:\n%s", config)
	}
}

func TestPoolerReconcileRendersPgHBA(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.PgHBA = []string{
		"hostssl all app 10.0.0.0/8 scram-sha-256",
		"hostnossl all all 0.0.0.0/0 reject",
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	for _, want := range []string{
		"auth_type = hba",
		"auth_hba_file = /etc/pgbouncer/config/pg_hba.conf",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("pgbouncer.ini missing %q:\n%s", want, config)
		}
	}
	wantHBA := strings.Join(pooler.Spec.PgBouncer.PgHBA, "\n") + "\n"
	if cm.Data["pg_hba.conf"] != wantHBA {
		t.Fatalf("pg_hba.conf = %q, want %q", cm.Data["pg_hba.conf"], wantHBA)
	}

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	pgbouncer := findContainer(dep.Spec.Template.Spec.Containers, poolerContainerName)
	if pgbouncer == nil {
		t.Fatalf("containers = %+v, want pgbouncer", dep.Spec.Template.Spec.Containers)
	}
	if !hasVolumeMount(pgbouncer.VolumeMounts, "/etc/pgbouncer/config") {
		t.Fatalf("volumeMounts = %+v, want projected config directory mount", pgbouncer.VolumeMounts)
	}
	if got.Status.ConfigHash == "" || cm.Data["config.sha256"] != got.Status.ConfigHash {
		t.Fatalf("status.configHash = %q config.sha256 = %q, want matching non-empty hash", got.Status.ConfigHash, cm.Data["config.sha256"])
	}
}

func TestPoolerReconcileRejectsPgHBAAuthTypeConflict(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.PgHBA = []string{"hostssl all all 10.0.0.0/8 scram-sha-256"}
	pooler.Spec.PgBouncer.Parameters["auth_type"] = "scram-sha-256"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "pg_hba") || !strings.Contains(cond.Message, "auth_type") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileRendersTLSSecretRefs(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.ServerTLSSecret = &corev1.LocalObjectReference{Name: "server-tls"}
	pooler.Spec.PgBouncer.ServerCASecret = &corev1.LocalObjectReference{Name: "server-ca"}
	pooler.Spec.PgBouncer.ClientTLSSecret = &corev1.LocalObjectReference{Name: "client-tls"}
	pooler.Spec.PgBouncer.ClientCASecret = &corev1.LocalObjectReference{Name: "client-ca"}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(
			cluster,
			pooler,
			authSecret,
			tlsSecret("server-tls", "tls.crt", "tls.key"),
			tlsSecret("server-ca", "ca.crt"),
			tlsSecret("client-tls", "tls.crt", "tls.key"),
			tlsSecret("client-ca", "ca.crt"),
		).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	for _, want := range []string{
		"server_tls_key_file = /etc/pgbouncer/tls/server/tls.key",
		"server_tls_cert_file = /etc/pgbouncer/tls/server/tls.crt",
		"server_tls_ca_file = /etc/pgbouncer/tls/server-ca/ca.crt",
		"server_tls_sslmode = verify-ca",
		"client_tls_key_file = /etc/pgbouncer/tls/client/tls.key",
		"client_tls_cert_file = /etc/pgbouncer/tls/client/tls.crt",
		"client_tls_ca_file = /etc/pgbouncer/tls/client-ca/ca.crt",
		"client_tls_sslmode = verify-ca",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("pgbouncer.ini missing %q:\n%s", want, config)
		}
	}

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	pgbouncer := findContainer(dep.Spec.Template.Spec.Containers, poolerContainerName)
	if pgbouncer == nil {
		t.Fatalf("containers = %+v, want pgbouncer", dep.Spec.Template.Spec.Containers)
	}
	for name, secretName := range map[string]string{
		"pgbouncer-tls-server":    "server-tls",
		"pgbouncer-tls-server-ca": "server-ca",
		"pgbouncer-tls-client":    "client-tls",
		"pgbouncer-tls-client-ca": "client-ca",
	} {
		if !hasSecretVolume(dep.Spec.Template.Spec.Volumes, name, secretName) {
			t.Fatalf("volumes = %+v, want TLS volume %s for secret %s", dep.Spec.Template.Spec.Volumes, name, secretName)
		}
	}
	for _, want := range []string{
		"/etc/pgbouncer/tls/server",
		"/etc/pgbouncer/tls/server-ca",
		"/etc/pgbouncer/tls/client",
		"/etc/pgbouncer/tls/client-ca",
	} {
		if !hasVolumeMount(pgbouncer.VolumeMounts, want) {
			t.Fatalf("volumeMounts = %+v, want mount %s", pgbouncer.VolumeMounts, want)
		}
	}
}

func TestPoolerReconcileRequiresTLSSecretRefs(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.ServerTLSSecret = &corev1.LocalObjectReference{Name: "server-tls"}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "serverTLSSecret") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileRequiresTLSSecretKeys(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.ServerTLSSecret = &corev1.LocalObjectReference{Name: "server-tls"}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret, tlsSecret("server-tls", "tls.crt")).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec ||
		!strings.Contains(cond.Message, "tls.key") {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileROUsesAllReadyReplicas(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	cluster.Status.Shards[0].Replicas = []postgresv1alpha1.ShardEndpoint{{
		Pod:   "demo-shard-0-2",
		Ready: true,
	}, {
		Pod:   "demo-shard-0-3",
		Ready: false,
	}, {
		Pod:   "demo-shard-0-1",
		Ready: true,
	}}
	pooler := newPooler()
	pooler.Name = poolerTestROName
	pooler.Spec.Type = postgresv1alpha1.PoolerTypeRO
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerPending {
		t.Fatalf("phase = %q, want Pending until Deployment reports ready replicas", got.Status.Phase)
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	wantHosts := strings.Join([]string{
		"demo-shard-0-1.demo-shard-0-headless.default.svc",
		"demo-shard-0-2.demo-shard-0-headless.default.svc",
	}, ",")
	if !strings.Contains(config, "* = host="+wantHosts+" port=5432") {
		t.Fatalf("pgbouncer.ini missing ready replica host list %q:\n%s", wantHosts, config)
	}
	if strings.Contains(config, "demo-shard-0-0") || strings.Contains(config, "demo-shard-0-3") {
		t.Fatalf("pgbouncer.ini should not include primary or not-ready replica:\n%s", config)
	}
	if !strings.Contains(config, "server_round_robin = 1") {
		t.Fatalf("pgbouncer.ini missing server_round_robin for ro host list:\n%s", config)
	}
	if !strings.Contains(config, "server_login_retry = 2") {
		t.Fatalf("pgbouncer.ini missing fast server_login_retry for ro host list:\n%s", config)
	}
}

func TestPoolerReconcileROPreservesServerLoginRetryOverride(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	cluster.Status.Shards[0].Replicas = []postgresv1alpha1.ShardEndpoint{{
		Pod:   "demo-shard-0-1",
		Ready: true,
	}, {
		Pod:   "demo-shard-0-2",
		Ready: true,
	}}
	pooler := newPooler()
	pooler.Name = poolerTestROName
	pooler.Spec.Type = postgresv1alpha1.PoolerTypeRO
	pooler.Spec.PgBouncer.Parameters["server_login_retry"] = "7"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	if !strings.Contains(config, "server_login_retry = 7") {
		t.Fatalf("pgbouncer.ini should preserve server_login_retry override:\n%s", config)
	}
}

func TestPoolerReconcilePublishesOperationalStatus(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	cluster.Status.Shards[0].Replicas = []postgresv1alpha1.ShardEndpoint{{
		Pod:   "demo-shard-0-2",
		Ready: true,
	}, {
		Pod:   "demo-shard-0-1",
		Ready: true,
	}}
	pooler := newPooler()
	pooler.Name = poolerTestROName
	pooler.Spec.Type = postgresv1alpha1.PoolerTypeRO
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	wantTargets := []string{
		"demo-shard-0-1.demo-shard-0-headless.default.svc",
		"demo-shard-0-2.demo-shard-0-headless.default.svc",
	}
	if got.Status.Instances != 3 {
		t.Fatalf("status.instances = %d, want 3", got.Status.Instances)
	}
	if strings.Join(got.Status.BackendTargets, ",") != strings.Join(wantTargets, ",") {
		t.Fatalf("status.backendTargets = %+v, want %+v", got.Status.BackendTargets, wantTargets)
	}

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if got.Status.ConfigHash == "" {
		t.Fatalf("status.configHash is empty")
	}
	if _, found := dep.Spec.Template.Annotations[poolerConfigHashKey]; found {
		t.Fatalf("deployment template should not carry config hash annotation: %+v", dep.Spec.Template.Annotations)
	}
}

func TestPoolerReconcileWaitsForReadyPodsBeforePublishingUpdatedConfigHash(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	first := reconcilePoolerOnce(t, r, c, pooler)
	initialHash := first.Status.ConfigHash
	if initialHash == "" {
		t.Fatalf("initial status.configHash is empty")
	}

	var updated postgresv1alpha1.Pooler
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pooler), &updated); err != nil {
		t.Fatalf("get Pooler before update: %v", err)
	}
	updated.Spec.PgBouncer.Parameters["default_pool_size"] = "12"
	updated.Spec.PgBouncer.Parameters["max_client_conn"] = "120"
	if err := c.Update(context.Background(), &updated); err != nil {
		t.Fatalf("update Pooler parameters: %v", err)
	}

	got := reconcilePoolerOnce(t, r, c, &updated)
	if got.Status.ConfigHash != initialHash {
		t.Fatalf("status.configHash = %q, want previous hash %q until ready pods reload", got.Status.ConfigHash, initialHash)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonConfigReloadPending {
		t.Fatalf("Ready condition = %+v, want ConfigReloadPending", cond)
	}

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	config := cm.Data["pgbouncer.ini"]
	for _, want := range []string{
		"default_pool_size = 12",
		"max_client_conn = 120",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("pgbouncer.ini missing %q after parameter update:\n%s", want, config)
		}
	}

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if _, found := dep.Spec.Template.Annotations[poolerConfigHashKey]; found {
		t.Fatalf("deployment template should not carry config hash annotation for in-place reload: %+v", dep.Spec.Template.Annotations)
	}
	assertPoolerRollingStrategy(t, dep)
}

func TestPoolerReconcileReloadsReadyPodsForParameterUpdateWithoutDeploymentRollout(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Status.ConfigHash = "old-config-hash"
	pooler.Spec.PgBouncer.Parameters["default_pool_size"] = "12"
	pooler.Spec.PgBouncer.Parameters["max_client_conn"] = "120"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	dep := readyPoolerDeployment(pooler)
	pods := []client.Object{
		readyPoolerPodWithConfigHash(pooler, "demo-rw-0", "old-config-hash"),
		readyPoolerPodWithConfigHash(pooler, "demo-rw-1", "old-config-hash"),
		readyPoolerPodWithConfigHash(pooler, "demo-rw-2", "old-config-hash"),
	}
	objects := make([]client.Object, 0, 4+len(pods))
	objects = append(objects, cluster, pooler, authSecret, dep)
	objects = append(objects, pods...)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.ConfigHash == "" || got.Status.ConfigHash == "old-config-hash" {
		t.Fatalf("status.configHash = %q, want new config hash", got.Status.ConfigHash)
	}
	if exec.called != 3 {
		t.Fatalf("reload exec called %d times, want 3", exec.called)
	}
	for _, call := range exec.calls {
		if strings.Join(call.command, " ") != strings.Join(expectedPoolerReloadCommand(got.Status.ConfigHash), " ") {
			t.Fatalf("reload command = %q, want SIGHUP reload script", strings.Join(call.command, " "))
		}
		var pod corev1.Pod
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: call.target.Pod}, &pod); err != nil {
			t.Fatalf("get pod %s: %v", call.target.Pod, err)
		}
		if pod.Annotations[poolerConfigHashKey] != got.Status.ConfigHash {
			t.Fatalf("pod %s config hash annotation = %q, want %q", pod.Name, pod.Annotations[poolerConfigHashKey], got.Status.ConfigHash)
		}
	}

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerConfigMapName(pooler.Name)}, &cm); err != nil {
		t.Fatalf("ConfigMap get: %v", err)
	}
	if cm.Data["config.sha256"] != got.Status.ConfigHash {
		t.Fatalf("config.sha256 = %q, want %q", cm.Data["config.sha256"], got.Status.ConfigHash)
	}

	var observed appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &observed); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if _, found := observed.Spec.Template.Annotations[poolerConfigHashKey]; found {
		t.Fatalf("deployment template should not carry config hash annotation for in-place reload: %+v", observed.Spec.Template.Annotations)
	}
}

func TestPoolerReconcileCreatesHAProtectionResources(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	spread := dep.Spec.Template.Spec.TopologySpreadConstraints
	if len(spread) != 2 {
		t.Fatalf("TopologySpreadConstraints = %+v, want zone + hostname defaults", spread)
	}
	for _, constraint := range spread {
		if constraint.MaxSkew != 1 || constraint.WhenUnsatisfiable != corev1.ScheduleAnyway {
			t.Fatalf("unexpected topology spread constraint: %+v", constraint)
		}
		if constraint.LabelSelector == nil ||
			constraint.LabelSelector.MatchLabels["postgres.keiailab.io/pooler"] != pooler.Name {
			t.Fatalf("topology spread selector = %+v, want Pooler labels", constraint.LabelSelector)
		}
	}

	var pdb policyv1.PodDisruptionBudget
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerPDBName(pooler.Name)}, &pdb); err != nil {
		t.Fatalf("PDB get: %v", err)
	}
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 2 {
		t.Fatalf("PDB minAvailable = %+v, want 2", pdb.Spec.MinAvailable)
	}
	if pdb.Spec.Selector == nil || pdb.Spec.Selector.MatchLabels["postgres.keiailab.io/pooler"] != pooler.Name {
		t.Fatalf("PDB selector = %+v, want Pooler labels", pdb.Spec.Selector)
	}
}

func TestPoolerReconcileHonorsDeploymentStrategyOverride(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.DeploymentStrategy = &appsv1.DeploymentStrategy{
		Type: appsv1.RecreateDeploymentStrategyType,
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("deployment strategy = %q, want Recreate", dep.Spec.Strategy.Type)
	}
}

func TestPoolerReconcileSetsServiceAccountName(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.ServiceAccountName = "pooler-iam"
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if dep.Spec.Template.Spec.ServiceAccountName != "pooler-iam" {
		t.Fatalf("serviceAccountName = %q, want pooler-iam", dep.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestPoolerReconcilePausesReadyPods(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.Paused = true
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	dep := readyPoolerDeployment(pooler)
	pods := []client.Object{
		readyPoolerPod(pooler, "demo-rw-0", false),
		readyPoolerPod(pooler, "demo-rw-1", false),
		readyPoolerPod(pooler, "demo-rw-2", false),
	}
	objects := make([]client.Object, 0, 4+len(pods))
	objects = append(objects, cluster, pooler, authSecret, dep)
	objects = append(objects, pods...)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerReady || !got.Status.Paused {
		t.Fatalf("status = phase %q paused %v, want Ready paused", got.Status.Phase, got.Status.Paused)
	}
	if exec.called != 3 {
		t.Fatalf("pause exec called %d times, want 3", exec.called)
	}
	for _, call := range exec.calls {
		if strings.Join(call.command, " ") != "/usr/bin/pkill -USR1 pgbouncer" {
			t.Fatalf("pause command = %q, want pkill SIGUSR1", strings.Join(call.command, " "))
		}
		var pod corev1.Pod
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: call.target.Pod}, &pod); err != nil {
			t.Fatalf("get pod %s: %v", call.target.Pod, err)
		}
		if pod.Annotations[poolerPausedAnnotation] != "true" {
			t.Fatalf("pod %s paused annotation = %q, want true", pod.Name, pod.Annotations[poolerPausedAnnotation])
		}
	}
}

func TestPoolerReconcileResumesPreviouslyPausedPods(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Status = postgresv1alpha1.PoolerStatus{Paused: true}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	dep := readyPoolerDeployment(pooler)
	pods := []client.Object{
		readyPoolerPod(pooler, "demo-rw-0", true),
		readyPoolerPod(pooler, "demo-rw-1", true),
		readyPoolerPod(pooler, "demo-rw-2", true),
	}
	objects := make([]client.Object, 0, 4+len(pods))
	objects = append(objects, cluster, pooler, authSecret, dep)
	objects = append(objects, pods...)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerReady || got.Status.Paused {
		t.Fatalf("status = phase %q paused %v, want Ready resumed", got.Status.Phase, got.Status.Paused)
	}
	if exec.called != 3 {
		t.Fatalf("resume exec called %d times, want 3", exec.called)
	}
	for _, call := range exec.calls {
		if strings.Join(call.command, " ") != "/usr/bin/pkill -USR2 pgbouncer" {
			t.Fatalf("resume command = %q, want pkill SIGUSR2", strings.Join(call.command, " "))
		}
	}
}

func TestPoolerReconcileSkipsAlreadyPausedPods(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.Paused = true
	pooler.Status = postgresv1alpha1.PoolerStatus{Paused: true}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	dep := readyPoolerDeployment(pooler)
	pods := []client.Object{
		readyPoolerPod(pooler, "demo-rw-0", true),
		readyPoolerPod(pooler, "demo-rw-1", true),
		readyPoolerPod(pooler, "demo-rw-2", true),
	}
	objects := make([]client.Object, 0, 4+len(pods))
	objects = append(objects, cluster, pooler, authSecret, dep)
	objects = append(objects, pods...)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerReady || !got.Status.Paused {
		t.Fatalf("status = phase %q paused %v, want Ready paused", got.Status.Phase, got.Status.Paused)
	}
	if exec.called != 0 {
		t.Fatalf("pause exec called %d times, want 0 for already-paused pods", exec.called)
	}
}

func TestPoolerReconcileSkipsHAProtectionForSingleInstance(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.Instances = 1
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	if len(dep.Spec.Template.Spec.TopologySpreadConstraints) != 0 {
		t.Fatalf("TopologySpreadConstraints = %+v, want empty for single-instance Pooler", dep.Spec.Template.Spec.TopologySpreadConstraints)
	}

	var pdb policyv1.PodDisruptionBudget
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerPDBName(pooler.Name)}, &pdb)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("PDB get error = %v, want NotFound", err)
	}
}

func TestPoolerReconcilePreservesTemplateTopologySpread(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.Template = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
			MaxSkew:           3,
			TopologyKey:       "node-pool",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"custom": "pooler"}},
		}},
	}}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	spread := dep.Spec.Template.Spec.TopologySpreadConstraints
	if len(spread) != 1 || spread[0].TopologyKey != "node-pool" ||
		spread[0].WhenUnsatisfiable != corev1.DoNotSchedule ||
		spread[0].MaxSkew != 3 {
		t.Fatalf("TopologySpreadConstraints = %+v, want user override preserved", spread)
	}
}

func TestPoolerReconcileMergesServiceTemplatePorts(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.ServiceTemplate = &postgresv1alpha1.PoolerServiceTemplateSpec{
		Type: corev1.ServiceTypeLoadBalancer,
		Labels: map[string]string{
			"team": "platform",
		},
		Annotations: map[string]string{
			"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
		},
		Ports: []corev1.ServicePort{{
			Name:       "metrics",
			Port:       9127,
			TargetPort: intstr.FromInt32(9127),
			Protocol:   corev1.ProtocolTCP,
		}},
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerServiceName(pooler.Name)}, &svc); err != nil {
		t.Fatalf("Service get: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("service type = %q, want LoadBalancer", svc.Spec.Type)
	}
	if svc.Labels["team"] != "platform" {
		t.Fatalf("service labels = %+v, want custom team label", svc.Labels)
	}
	if svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-type"] != "nlb" {
		t.Fatalf("service annotations = %+v, want custom annotation", svc.Annotations)
	}
	if !hasServicePort(svc.Spec.Ports, "metrics", 9127) {
		t.Fatalf("service ports = %+v, want custom metrics port", svc.Spec.Ports)
	}
	if !hasServicePort(svc.Spec.Ports, "pgbouncer", 5432) {
		t.Fatalf("service ports = %+v, want default pgbouncer port", svc.Spec.Ports)
	}
}

func TestPoolerReconcileAddsExporterSidecarAndMetricsServicePort(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Exporter = &postgresv1alpha1.PgBouncerExporterSpec{
		Image: "example.com/pgbouncer-exporter:0.8",
		Port:  9127,
		Args:  []string{"--web.listen-address=:9127"},
		Env: []corev1.EnvVar{{
			Name:  "PGBOUNCER_EXPORTER_CONNECTION_STRING",
			Value: "postgres://pgbouncer@127.0.0.1:5432/pgbouncer?sslmode=disable",
		}},
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	exporter := findContainer(dep.Spec.Template.Spec.Containers, "pgbouncer-exporter")
	if exporter == nil {
		t.Fatalf("containers = %+v, want pgbouncer-exporter sidecar", dep.Spec.Template.Spec.Containers)
	}
	if exporter.Image != "example.com/pgbouncer-exporter:0.8" {
		t.Fatalf("exporter image = %q, want custom exporter image", exporter.Image)
	}
	if !hasContainerPort(exporter.Ports, poolerMetricsPortName, 9127) {
		t.Fatalf("exporter ports = %+v, want metrics:9127", exporter.Ports)
	}
	if exporter.SecurityContext == nil {
		t.Fatalf("exporter SecurityContext is nil")
	}
	assertExporterProbes(t, *exporter)

	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerServiceName(pooler.Name)}, &svc); err != nil {
		t.Fatalf("Service get: %v", err)
	}
	if !hasServicePort(svc.Spec.Ports, poolerMetricsPortName, 9127) {
		t.Fatalf("service ports = %+v, want metrics port for exporter", svc.Spec.Ports)
	}
}

func TestPoolerReconcileAddsStableMonitoringSelectorLabels(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	for key, want := range map[string]string{
		"postgres.keiailab.io/cluster":     "demo",
		"postgres.keiailab.io/pooler":      "demo-rw",
		"postgres.keiailab.io/pooler-type": "rw",
	} {
		if got := dep.Spec.Template.Labels[key]; got != want {
			t.Fatalf("pod template label %s = %q, want %q", key, got, want)
		}
	}

	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerServiceName(pooler.Name)}, &svc); err != nil {
		t.Fatalf("Service get: %v", err)
	}
	for key, want := range map[string]string{
		"postgres.keiailab.io/cluster":     "demo",
		"postgres.keiailab.io/pooler":      "demo-rw",
		"postgres.keiailab.io/pooler-type": "rw",
	} {
		if got := svc.Labels[key]; got != want {
			t.Fatalf("service label %s = %q, want %q", key, got, want)
		}
	}
}

func TestPoolerReconcilePreservesTemplateProbeOverrides(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.Template = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: poolerContainerName,
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
			Command: []string{"/bin/custom-ready"},
		}}},
	}, {
		Name: poolerExporterName,
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
			Command: []string{"/bin/custom-live"},
		}}},
	}}}}
	pooler.Spec.PgBouncer.Exporter = &postgresv1alpha1.PgBouncerExporterSpec{
		Image: "example.com/pgbouncer-exporter:0.8",
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: PoolerDeploymentName(pooler.Name)}, &dep); err != nil {
		t.Fatalf("Deployment get: %v", err)
	}
	pgbouncer := findContainer(dep.Spec.Template.Spec.Containers, poolerContainerName)
	if pgbouncer == nil {
		t.Fatalf("containers = %+v, want pgbouncer", dep.Spec.Template.Spec.Containers)
	}
	if pgbouncer.ReadinessProbe == nil || pgbouncer.ReadinessProbe.Exec == nil ||
		strings.Join(pgbouncer.ReadinessProbe.Exec.Command, " ") != "/bin/custom-ready" {
		t.Fatalf("pgbouncer readiness probe was not preserved: %+v", pgbouncer.ReadinessProbe)
	}
	if pgbouncer.LivenessProbe == nil || pgbouncer.LivenessProbe.TCPSocket == nil {
		t.Fatalf("pgbouncer missing default liveness probe: %+v", pgbouncer.LivenessProbe)
	}

	exporter := findContainer(dep.Spec.Template.Spec.Containers, poolerExporterName)
	if exporter == nil {
		t.Fatalf("containers = %+v, want pgbouncer-exporter", dep.Spec.Template.Spec.Containers)
	}
	if exporter.LivenessProbe == nil || exporter.LivenessProbe.Exec == nil ||
		strings.Join(exporter.LivenessProbe.Exec.Command, " ") != "/bin/custom-live" {
		t.Fatalf("exporter liveness probe was not preserved: %+v", exporter.LivenessProbe)
	}
	if exporter.ReadinessProbe == nil || exporter.ReadinessProbe.HTTPGet == nil {
		t.Fatalf("exporter missing default readiness probe: %+v", exporter.ReadinessProbe)
	}
}

func TestPoolerReconcileRequiresExporterImage(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Spec.PgBouncer.Exporter = &postgresv1alpha1.PgBouncerExporterSpec{}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestPoolerReconcileRecordsFailedPhaseMetric(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	// Pooler 이름을 unique 로 둬서 t.Parallel() 다른 test 의 reconcile 이 동일
	// MetricPoolerPhase label set 을 0 으로 덮어쓰는 race 를 피한다.
	pooler.Name = "demo-rw-failmetric"
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	if got := testutil.ToFloat64(MetricPoolerPhase.WithLabelValues(
		"default", pooler.Name, "demo", "rw", "Failed",
	)); got != 1 {
		t.Fatalf("Pooler Failed phase metric = %v, want 1", got)
	}
}

func hasServicePort(ports []corev1.ServicePort, name string, port int32) bool {
	for _, item := range ports {
		if item.Name == name && item.Port == port {
			return true
		}
	}
	return false
}

func hasSecretVolume(volumes []corev1.Volume, name, secretName string) bool {
	for _, item := range volumes {
		if item.Name == name && item.Secret != nil && item.Secret.SecretName == secretName {
			return true
		}
	}
	return false
}

func hasVolumeMount(mounts []corev1.VolumeMount, mountPath string) bool {
	for _, item := range mounts {
		if item.MountPath == mountPath {
			return true
		}
	}
	return false
}

func assertPgBouncerProbes(t *testing.T, container corev1.Container) {
	t.Helper()
	if container.ReadinessProbe == nil || container.ReadinessProbe.TCPSocket == nil {
		t.Fatalf("pgbouncer readiness probe = %+v, want tcpSocket", container.ReadinessProbe)
	}
	if got := container.ReadinessProbe.TCPSocket.Port.String(); got != poolerContainerName {
		t.Fatalf("pgbouncer readiness port = %q, want %q", got, poolerContainerName)
	}
	if container.ReadinessProbe.InitialDelaySeconds != 3 || container.ReadinessProbe.PeriodSeconds != 3 {
		t.Fatalf("pgbouncer readiness timing = %+v, want initialDelay 3 period 3", container.ReadinessProbe)
	}
	if container.LivenessProbe == nil || container.LivenessProbe.TCPSocket == nil {
		t.Fatalf("pgbouncer liveness probe = %+v, want tcpSocket", container.LivenessProbe)
	}
	if got := container.LivenessProbe.TCPSocket.Port.String(); got != poolerContainerName {
		t.Fatalf("pgbouncer liveness port = %q, want %q", got, poolerContainerName)
	}
	if container.LivenessProbe.InitialDelaySeconds != 30 || container.LivenessProbe.PeriodSeconds != 10 {
		t.Fatalf("pgbouncer liveness timing = %+v, want initialDelay 30 period 10", container.LivenessProbe)
	}
	if container.StartupProbe == nil || container.StartupProbe.TCPSocket == nil {
		t.Fatalf("pgbouncer startup probe = %+v, want tcpSocket", container.StartupProbe)
	}
	if container.StartupProbe.FailureThreshold != 20 || container.StartupProbe.PeriodSeconds != 3 {
		t.Fatalf("pgbouncer startup timing = %+v, want failureThreshold 20 period 3", container.StartupProbe)
	}
}

func assertExporterProbes(t *testing.T, container corev1.Container) {
	t.Helper()
	if container.ReadinessProbe == nil || container.ReadinessProbe.HTTPGet == nil {
		t.Fatalf("exporter readiness probe = %+v, want httpGet", container.ReadinessProbe)
	}
	if container.ReadinessProbe.HTTPGet.Path != "/metrics" {
		t.Fatalf("exporter readiness path = %q, want /metrics", container.ReadinessProbe.HTTPGet.Path)
	}
	if got := container.ReadinessProbe.HTTPGet.Port.String(); got != poolerMetricsPortName {
		t.Fatalf("exporter readiness port = %q, want metrics", got)
	}
	if container.LivenessProbe == nil || container.LivenessProbe.HTTPGet == nil {
		t.Fatalf("exporter liveness probe = %+v, want httpGet", container.LivenessProbe)
	}
	if container.LivenessProbe.HTTPGet.Path != "/metrics" {
		t.Fatalf("exporter liveness path = %q, want /metrics", container.LivenessProbe.HTTPGet.Path)
	}
	if got := container.LivenessProbe.HTTPGet.Port.String(); got != poolerMetricsPortName {
		t.Fatalf("exporter liveness port = %q, want metrics", got)
	}
}

func assertPoolerRollingStrategy(t *testing.T, dep appsv1.Deployment) {
	t.Helper()
	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("deployment strategy = %q, want RollingUpdate", dep.Spec.Strategy.Type)
	}
	if dep.Spec.Strategy.RollingUpdate == nil {
		t.Fatalf("deployment rollingUpdate is nil")
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue(); got != 0 {
		t.Fatalf("maxUnavailable = %d, want 0", got)
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxSurge.IntValue(); got != 1 {
		t.Fatalf("maxSurge = %d, want 1", got)
	}
	if dep.Spec.MinReadySeconds != 5 {
		t.Fatalf("minReadySeconds = %d, want 5", dep.Spec.MinReadySeconds)
	}
	if dep.Spec.RevisionHistoryLimit == nil || *dep.Spec.RevisionHistoryLimit != 3 {
		t.Fatalf("revisionHistoryLimit = %v, want 3", dep.Spec.RevisionHistoryLimit)
	}
}

func hasContainerPort(ports []corev1.ContainerPort, name string, port int32) bool {
	for _, item := range ports {
		if item.Name == name && item.ContainerPort == port {
			return true
		}
	}
	return false
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// TestPoolerBuiltinAuth_RequeuesWhenPrimaryNotReady 는 AuthSecretRef 가 비고
// PostgresCluster primary 가 아직 ready 아닐 때 reconcile 이 PoolerPending
// 으로 표면화하고 짧은 간격 뒤 재시도하는지 검증한다.
func TestPoolerBuiltinAuth_RequeuesWhenPrimaryNotReady(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	// primary not ready — backupSidecarTarget 이 false 반환.
	cluster.Status.Shards[0].Primary.Ready = false
	pooler := newPooler()
	pooler.Name = "demo-rw-builtin-requeue"
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.Phase != postgresv1alpha1.PoolerPending {
		t.Fatalf("phase = %q, want Pending (built-in auth waiting for primary)", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != "BuiltinAuthWaitingForPrimary" {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	if exec.called != 0 {
		t.Fatalf("PodExecutor called %d times before primary ready, want 0", exec.called)
	}

	var secret corev1.Secret
	err := c.Get(context.Background(), client.ObjectKey{Namespace: pooler.Namespace, Name: pooler.Name + "-builtin-auth"}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("built-in auth Secret get error = %v, want NotFound (primary not ready)", err)
	}
}

// TestPoolerBuiltinAuth_CreatesSecretAndExecutesRoleSQL 는 AuthSecretRef 가
// 비고 PostgresCluster primary 가 ready 일 때 ensurePoolerBuiltinAuth 가
// LOGIN role SQL 을 적용하고 userlist.txt Secret 을 생성하는지 검증한다.
func TestPoolerBuiltinAuth_CreatesSecretAndExecutesRoleSQL(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-builtin-create"
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}
	reconcilePoolerOnce(t, r, c, pooler)

	// 1. PodExecutor 가 psql DO $$ CREATE/ALTER ROLE 로 호출됐는지 확인.
	if exec.called < 1 {
		t.Fatalf("PodExecutor called %d times, want >= 1 for role SQL", exec.called)
	}
	cmd := strings.Join(exec.calls[0].command, " ")
	if !strings.Contains(cmd, "psql") || !strings.Contains(cmd, "CREATE ROLE keiailab_pooler_pgbouncer") {
		t.Fatalf("first PodExecutor call did not invoke psql with CREATE ROLE: %s", cmd)
	}

	// 2. userlist.txt Secret 이 생성됐는지 + Pooler OwnerReference 가 붙었는지.
	secretName := pooler.Name + "-builtin-auth"
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: pooler.Namespace, Name: secretName}, &secret); err != nil {
		t.Fatalf("built-in auth Secret not created: %v", err)
	}
	if !metav1.IsControlledBy(&secret, pooler) {
		t.Fatalf("Secret %s OwnerReference does not point to Pooler", secretName)
	}
	userlist := string(secret.Data["userlist.txt"])
	if !strings.Contains(userlist, `"keiailab_pooler_pgbouncer"`) || !strings.Contains(userlist, ` "md5`) {
		t.Fatalf("Secret userlist.txt missing expected role/md5 hash: %q", userlist)
	}

	// 3. 두 번째 reconcile 은 SQL 재실행 없이 Secret 그대로 사용해야 한다 (idempotent).
	exec.called = 0
	exec.calls = nil
	reconcilePoolerOnce(t, r, c, pooler)
	if exec.called != 0 {
		t.Fatalf("PodExecutor called %d times on idempotent reconcile, want 0", exec.called)
	}
}

// TestPoolerAutoTLS_CreatesCertificate 는 spec.pgbouncer.autoTLS.clientEnabled=true 일 때
// reconcile 이 cert-manager Certificate CR 을 자동 생성하는지 + Pooler OwnerReference 가
// 붙는지 검증한다.
func TestPoolerAutoTLS_CreatesCertificate(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-autotls"
	pooler.Spec.PgBouncer.AutoTLS = &postgresv1alpha1.PoolerAutoTLSSpec{
		IssuerRef: &postgresv1alpha1.PoolerCertIssuerRef{
			Name: "ca-issuer",
			Kind: "ClusterIssuer",
		},
		ClientEnabled: true,
		ServerEnabled: false,
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	// Client Certificate 가 생성됐는지 + apiVersion / kind / spec.secretName / spec.issuerRef 검증.
	clientCert := &unstructured.Unstructured{}
	clientCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-client-tls",
	}, clientCert); err != nil {
		t.Fatalf("client Certificate get: %v", err)
	}
	gotSecretName, _, _ := unstructured.NestedString(clientCert.Object, "spec", "secretName")
	if gotSecretName != pooler.Name+"-client-tls" {
		t.Fatalf("client Certificate spec.secretName = %q, want %s-client-tls", gotSecretName, pooler.Name)
	}
	issuerMap, _, _ := unstructured.NestedMap(clientCert.Object, "spec", "issuerRef")
	if issuerMap["name"] != "ca-issuer" || issuerMap["kind"] != "ClusterIssuer" {
		t.Fatalf("client Certificate spec.issuerRef mismatch: %+v", issuerMap)
	}
	if !metav1.IsControlledBy(clientCert, pooler) {
		t.Fatalf("client Certificate OwnerReference does not point to Pooler")
	}

	// ServerEnabled=false 이므로 server Certificate 는 생성되지 않아야 한다.
	serverCert := &unstructured.Unstructured{}
	serverCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-server-tls",
	}, serverCert)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("server Certificate get error = %v, want NotFound (ServerEnabled=false)", err)
	}
}

// TestPoolerAutoTLS_SelfSignedCreatesSecretAndMirrorsNotAfter (T29 stage 4)
// — when `spec.pgbouncer.autoTLS.selfSigned=true` and cert-manager is
// not present, the reconciler must generate an in-process RSA + x509
// self-signed cert, store it in a Secret with tls.crt/tls.key/ca.crt
// keys, and mirror the certificate's NotAfter onto Pooler.Status so the
// observability path stays uniform with the cert-manager-backed flow.
func TestPoolerAutoTLS_SelfSignedCreatesSecretAndMirrorsNotAfter(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-autotls-selfsigned"
	pooler.Spec.PgBouncer.AutoTLS = &postgresv1alpha1.PoolerAutoTLSSpec{
		SelfSigned:    true,
		ClientEnabled: true,
		ServerEnabled: false,
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	// No cert-manager Certificate CR — only the Secret should exist.
	clientCert := &unstructured.Unstructured{}
	clientCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-client-tls",
	}, clientCert); !apierrors.IsNotFound(err) {
		t.Fatalf("cert-manager Certificate get error = %v, want NotFound (self-signed path)", err)
	}

	// The Secret must exist with tls.crt / tls.key / ca.crt.
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-client-tls",
	}, &secret); err != nil {
		t.Fatalf("self-signed Secret get: %v", err)
	}
	for _, key := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey, "ca.crt"} {
		if len(secret.Data[key]) == 0 {
			t.Fatalf("self-signed Secret missing data[%q]", key)
		}
	}
	if secret.Type != corev1.SecretTypeTLS {
		t.Fatalf("self-signed Secret type = %q, want %q", secret.Type, corev1.SecretTypeTLS)
	}

	// The parsed cert's CommonName should include the pooler service DNS.
	cert := parseLeafCertFromPEM(secret.Data[corev1.TLSCertKey])
	if cert == nil {
		t.Fatalf("self-signed Secret tls.crt is unparseable")
	}
	expectedHostPrefix := PoolerServiceName(pooler.Name)
	hasMatchingSAN := false
	for _, d := range cert.DNSNames {
		if strings.HasPrefix(d, expectedHostPrefix) {
			hasMatchingSAN = true
			break
		}
	}
	if !hasMatchingSAN {
		t.Fatalf("self-signed cert SANs %v, want at least one starting with %q",
			cert.DNSNames, expectedHostPrefix)
	}
	if time.Until(cert.NotAfter) < 300*24*time.Hour {
		t.Fatalf("self-signed cert NotAfter = %v, want ~365 days from now", cert.NotAfter)
	}

	// Status notAfter must be mirrored from the issued cert.
	if got.Status.AutoTLSClientCertNotAfter == nil {
		t.Fatalf("status.autoTLSClientCertNotAfter is nil — self-signed mirror failed")
	}
	if !got.Status.AutoTLSClientCertNotAfter.Time.Equal(cert.NotAfter) {
		t.Fatalf("status.autoTLSClientCertNotAfter = %v, want %v (parsed cert)",
			got.Status.AutoTLSClientCertNotAfter.Time, cert.NotAfter)
	}

	// Idempotent: a second reconcile must NOT regenerate the Secret
	// (NotAfter is still ~1 year out).
	previousCert := secret.Data[corev1.TLSCertKey]
	_ = reconcilePoolerOnce(t, r, c, pooler)
	var second corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-client-tls",
	}, &second); err != nil {
		t.Fatalf("second-pass Secret get: %v", err)
	}
	if !bytes.Equal(second.Data[corev1.TLSCertKey], previousCert) {
		t.Fatalf("self-signed cert was regenerated on the second reconcile (not idempotent)")
	}
}

// TestPoolerAutoTLS_MirrorsNotAfterToStatus (T29 stage 5) — when the
// cert-manager Certificate has `status.notAfter`, the reconciler must
// mirror it onto Pooler.Status.AutoTLSClientCertNotAfter so that
// operators can `kubectl get poolers -o ...` for upcoming renewals.
func TestPoolerAutoTLS_MirrorsNotAfterToStatus(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-autotls-notafter"
	pooler.Spec.PgBouncer.AutoTLS = &postgresv1alpha1.PoolerAutoTLSSpec{
		IssuerRef: &postgresv1alpha1.PoolerCertIssuerRef{
			Name: "ca-issuer",
			Kind: "ClusterIssuer",
		},
		ClientEnabled: true,
		ServerEnabled: false,
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "demo-pooler-auth",
		Namespace: "default",
	}, Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}

	// Pre-seed a cert-manager Certificate CR that already has status.notAfter,
	// simulating the post-issuance state.
	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	preexistingCert := &unstructured.Unstructured{}
	preexistingCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	preexistingCert.SetNamespace(pooler.Namespace)
	preexistingCert.SetName(pooler.Name + "-client-tls")
	if err := unstructured.SetNestedField(preexistingCert.Object, "ca-issuer", "spec", "issuerRef", "name"); err != nil {
		t.Fatalf("seed spec.issuerRef.name: %v", err)
	}
	if err := unstructured.SetNestedField(preexistingCert.Object, notAfter.Format(time.RFC3339), "status", "notAfter"); err != nil {
		t.Fatalf("seed status.notAfter: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret, preexistingCert).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	got := reconcilePoolerOnce(t, r, c, pooler)

	if got.Status.AutoTLSClientCertNotAfter == nil {
		t.Fatalf("status.autoTLSClientCertNotAfter is nil — expected mirror of cert-manager Certificate.status.notAfter")
	}
	if !got.Status.AutoTLSClientCertNotAfter.Time.Equal(notAfter) {
		t.Fatalf("status.autoTLSClientCertNotAfter = %v, want %v",
			got.Status.AutoTLSClientCertNotAfter.Time, notAfter)
	}
	// ServerEnabled=false → server notAfter stays nil.
	if got.Status.AutoTLSServerCertNotAfter != nil {
		t.Fatalf("status.autoTLSServerCertNotAfter = %v, want nil (ServerEnabled=false)",
			got.Status.AutoTLSServerCertNotAfter)
	}
}

// TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence — when the user
// 명시한 경우 AutoTLS 의 client 발급 path 가 비활성되는지 검증한다.
func TestPoolerAutoTLS_UserSuppliedSecretTakesPrecedence(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-autotls-userref"
	pooler.Spec.PgBouncer.ClientTLSSecret = &corev1.LocalObjectReference{Name: "user-client-tls"}
	pooler.Spec.PgBouncer.AutoTLS = &postgresv1alpha1.PoolerAutoTLSSpec{
		IssuerRef:     &postgresv1alpha1.PoolerCertIssuerRef{Name: "ca-issuer"},
		ClientEnabled: true,
	}
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "user-client-tls", Namespace: "default"},
				Data: map[string][]byte{"tls.crt": []byte("crt"), "tls.key": []byte("key")}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "demo-pooler-auth", Namespace: "default"},
				Data: map[string][]byte{"userlist.txt": []byte(`"app" "SCRAM-SHA-256$4096:salt$stored:server"`)}}).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	reconcilePoolerOnce(t, r, c, pooler)

	clientCert := &unstructured.Unstructured{}
	clientCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	err := c.Get(context.Background(), client.ObjectKey{
		Namespace: pooler.Namespace,
		Name:      pooler.Name + "-client-tls",
	}, clientCert)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("client Certificate get error = %v, want NotFound (user-supplied ClientTLSSecret takes precedence)", err)
	}
}

// TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation 은 사용자가 force rotation
// annotation 을 적용하면 reconcile 이 새 password 를 생성하고 Secret 을 in-place
// update 한 뒤 annotation 을 제거하고 status.builtinAuthLastRotation 을 기록하는지
// 검증한다.
func TestPoolerBuiltinAuth_RotatesPasswordOnAnnotation(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	pooler := newPooler()
	pooler.Name = "demo-rw-builtin-rotate"
	pooler.Spec.PgBouncer.AuthSecretRef = nil
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	exec := &fakePoolerPodExecutor{}
	r := &PoolerReconciler{Client: c, Scheme: scheme, PodExecutor: exec}

	// 1. 첫 reconcile — Secret 생성.
	got := reconcilePoolerOnce(t, r, c, pooler)
	if got.Status.BuiltinAuthLastRotation == nil {
		t.Fatalf("first reconcile must set status.builtinAuthLastRotation")
	}
	firstRotation := got.Status.BuiltinAuthLastRotation.Time
	secretName := pooler.Name + "-builtin-auth"
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: pooler.Namespace, Name: secretName}, &secret); err != nil {
		t.Fatalf("Secret get: %v", err)
	}
	firstUserlist := string(secret.Data["userlist.txt"])

	// 2. force rotation annotation 적용.
	exec.called = 0
	exec.calls = nil
	patched := got.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}
	patched.Annotations[PoolerRotateAuthAnnotation] = poolerPausedValueTrue
	if err := c.Update(context.Background(), patched); err != nil {
		t.Fatalf("apply rotate annotation: %v", err)
	}

	// 3. 두 번째 reconcile — Secret 갱신 + annotation 제거 + status timestamp 갱신.
	// fake client 의 시간 정밀도가 같은 second 단위라 timestamp 비교는 != 로만 검증.
	time.Sleep(1100 * time.Millisecond)
	rotated := reconcilePoolerOnce(t, r, c, patched)
	if exec.called < 1 {
		t.Fatalf("rotation reconcile must invoke ALTER ROLE psql: PodExecutor called %d", exec.called)
	}
	if v, ok := rotated.Annotations[PoolerRotateAuthAnnotation]; ok {
		t.Fatalf("rotate annotation should be removed after rotation, got %q", v)
	}
	if rotated.Status.BuiltinAuthLastRotation == nil ||
		!rotated.Status.BuiltinAuthLastRotation.After(firstRotation) {
		t.Fatalf("status.builtinAuthLastRotation must advance after rotation: first=%v rotated=%v",
			firstRotation, rotated.Status.BuiltinAuthLastRotation)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: pooler.Namespace, Name: secretName}, &secret); err != nil {
		t.Fatalf("Secret get post-rotation: %v", err)
	}
	if string(secret.Data["userlist.txt"]) == firstUserlist {
		t.Fatalf("Secret userlist.txt did not change after rotation")
	}
}

// TestPoolerReconcileTargetNotFoundIsPendingWithRequeue is a regression
// guard for the PG18 kind smoke iter#4 race where the Pooler reconciled
// 4 s *before* the PostgresCluster primary flipped to Ready and was
// stuck in phase=Failed with no re-trigger. The reconciler now marks
// Pending + returns RequeueAfter so subsequent passes can converge.
func TestPoolerReconcileTargetNotFoundIsPendingWithRequeue(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPoolerCluster()
	// primary endpoint exists but not yet ready — exactly the smoke race.
	cluster.Status.Shards[0].Primary.Ready = false
	pooler := newPooler()
	pooler.Name = "demo-rw-targetnotfound"
	authSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-pooler-auth", Namespace: pooler.Namespace},
		Data:       map[string][]byte{"userlist.txt": []byte(`"app" "test"`)},
	}
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, pooler, authSecret).
		WithStatusSubresource(&postgresv1alpha1.Pooler{}).
		Build()

	r := &PoolerReconciler{Client: c, Scheme: scheme}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(pooler),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("result.RequeueAfter = %v, want positive for Pending TargetNotFound", result.RequeueAfter)
	}

	var got postgresv1alpha1.Pooler
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pooler), &got); err != nil {
		t.Fatalf("get Pooler: %v", err)
	}
	if got.Status.Phase != postgresv1alpha1.PoolerPending {
		t.Fatalf("phase = %q, want Pending (target not ready, expecting re-trigger)", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PoolerConditionReady)
	if cond == nil || cond.Reason != PoolerReasonTargetNotFound {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}

	// Make sure no Deployment / ConfigMap / Service / PDB was prematurely created.
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: pooler.Namespace, Name: PoolerDeploymentName(pooler.Name)}, &dep); !apierrors.IsNotFound(err) {
		t.Fatalf("Deployment get = %v, want NotFound while target not ready", err)
	}
}

type fakePoolerPodExecutor struct {
	called int
	calls  []poolerPodExecCall
}

type poolerPodExecCall struct {
	target  BackupSidecarTarget
	command []string
}

func (f *fakePoolerPodExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	f.called++
	f.calls = append(f.calls, poolerPodExecCall{
		target:  target,
		command: append([]string{}, command...),
	})
	return []byte("ok"), nil
}

func readyPoolerDeployment(pooler *postgresv1alpha1.Pooler) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: PoolerDeploymentName(pooler.Name), Namespace: pooler.Namespace},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 3,
		},
	}
}

func readyPoolerPod(pooler *postgresv1alpha1.Pooler, name string, paused bool) *corev1.Pod {
	annotations := map[string]string{}
	if paused {
		annotations[poolerPausedAnnotation] = poolerPausedValueTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   pooler.Namespace,
			Labels:      poolerLabels(pooler),
			Annotations: annotations,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

func readyPoolerPodWithConfigHash(pooler *postgresv1alpha1.Pooler, name string, configHash string) client.Object {
	pod := readyPoolerPod(pooler, name, false)
	pod.Annotations[poolerConfigHashKey] = configHash
	return pod
}

func expectedPoolerReloadCommand(configHash string) []string {
	return []string{
		"/bin/sh",
		"-ec",
		`i=0
while [ "$i" -lt 60 ]; do
    current="$(cat /etc/pgbouncer/config/config.sha256 2>/dev/null || true)"
    if [ "$current" = "$1" ]; then
        exec /usr/bin/pkill -HUP pgbouncer
    fi
    i=$((i + 1))
    sleep 2
done
echo "timed out waiting for projected PgBouncer config hash $1" >&2
exit 1`,
		"--",
		configHash,
	}
}

func newPoolerCluster() *postgresv1alpha1.PostgresCluster {
	cluster := newCluster()
	cluster.Status.Shards = []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod:   "demo-shard-0-0",
			Ready: true,
		},
	}}
	return cluster
}

func newPooler() *postgresv1alpha1.Pooler {
	return &postgresv1alpha1.Pooler{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rw", Namespace: "default"},
		Spec: postgresv1alpha1.PoolerSpec{
			Cluster:   postgresv1alpha1.PoolerClusterRef{Name: "demo"},
			Instances: 3,
			Type:      postgresv1alpha1.PoolerTypeRW,
			PgBouncer: postgresv1alpha1.PgBouncerSpec{
				Image:         "example.com/pgbouncer:1.24",
				PoolMode:      postgresv1alpha1.PoolerPoolModeTransaction,
				AuthSecretRef: &corev1.LocalObjectReference{Name: "demo-pooler-auth"},
				Parameters: map[string]string{
					"default_pool_size": "20",
					"max_client_conn":   "2000",
				},
			},
		},
	}
}

func tlsSecret(name string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, key := range keys {
		data[key] = []byte("test")
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       data,
	}
}

func reconcilePoolerOnce(
	t *testing.T,
	r *PoolerReconciler,
	c client.Client,
	pooler *postgresv1alpha1.Pooler,
) *postgresv1alpha1.Pooler {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(pooler),
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var got postgresv1alpha1.Pooler
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pooler), &got); err != nil {
		t.Fatalf("get Pooler: %v", err)
	}
	return &got
}
