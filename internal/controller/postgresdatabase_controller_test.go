package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestPostgresDatabaseReconcileCreatesDatabaseOnReadyPrimary(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresDatabaseOnce(t, r, c, db)

	if !got.Status.Applied {
		t.Fatalf("Applied = false, want true: %+v", got.Status)
	}
	if got.Status.ObservedGeneration != db.Generation {
		t.Fatalf("ObservedGeneration = %d, want %d", got.Status.ObservedGeneration, db.Generation)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PostgresDatabaseConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != PostgresDatabaseReasonReconciled {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	call := executor.calls[0]
	if call.target.Namespace != "default" || call.target.Pod != "demo-db-shard-0-0" || call.target.Container != pgContainerName {
		t.Fatalf("target = %+v, want ready primary postgres container", call.target)
	}
	command := strings.Join(call.command, " ")
	for _, want := range []string{
		"psql",
		`CREATE DATABASE "appdb" OWNER "app"`,
		`ALTER DATABASE "appdb" OWNER TO "app"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresDatabaseReconcileAppliesDatabaseTablespace(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.Tablespace = "fastspace"
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresDatabaseOnce(t, r, c, db)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{
		`CREATE DATABASE "appdb" OWNER "app" TABLESPACE "fastspace"`,
		`ALTER DATABASE "appdb" SET TABLESPACE "fastspace"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresDatabaseReconcileDeletePolicyDropsDatabaseOnDeletion(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.DatabaseReclaimPolicy = postgresv1alpha1.DatabaseReclaimDelete
	db.Finalizers = []string{postgresDatabaseFinalizer}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	if err := c.Delete(context.Background(), db); err != nil {
		t.Fatalf("Delete PostgresDatabase: %v", err)
	}
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: db.Namespace, Name: db.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	if !strings.Contains(command, `DROP DATABASE "appdb"`) {
		t.Fatalf("delete command missing DROP DATABASE:\n%s", command)
	}
	var got postgresv1alpha1.PostgresDatabase
	err = c.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &got)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("Get back: %v", err)
	}
	if slices.Contains(got.Finalizers, postgresDatabaseFinalizer) {
		t.Fatalf("finalizers = %v, want postgres database finalizer removed", got.Finalizers)
	}
}

func TestPostgresDatabaseReconcileDeletePolicyAddsFinalizerBeforeApply(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.DatabaseReclaimPolicy = postgresv1alpha1.DatabaseReclaimDelete
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	// 새 패턴: 한 번의 reconcile 안에서 finalizer 부착 → SQL apply → status update 가
	// 모두 끝나야 한다. 핵심 invariant 는 finalizer 가 SQL apply *이전에* persisted
	// 되었다는 점이다 — fake client 가 SQL Exec 시점에 finalizer 를 이미 보고 있어야 한다.
	executor.assertFn = func(target BackupSidecarTarget) {
		var snapshot postgresv1alpha1.PostgresDatabase
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &snapshot); err != nil {
			t.Fatalf("snapshot during exec: %v", err)
		}
		if !slices.Contains(snapshot.Finalizers, postgresDatabaseFinalizer) {
			t.Fatalf("SQL Exec happened before finalizer was persisted; finalizers=%v", snapshot.Finalizers)
		}
	}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: db.Namespace, Name: db.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if !result.IsZero() {
		t.Fatalf("result = %+v, want zero (finalizer-add + apply in one pass)", result)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1 in single pass", len(executor.calls))
	}
	var got postgresv1alpha1.PostgresDatabase
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &got); err != nil {
		t.Fatalf("Get back: %v", err)
	}
	if !slices.Contains(got.Finalizers, postgresDatabaseFinalizer) {
		t.Fatalf("finalizers = %v, want postgres database finalizer", got.Finalizers)
	}
	if !got.Status.Applied {
		t.Fatalf("status.applied = false, want true after single-pass reconcile")
	}
}

func TestPostgresDatabaseReconcileManagesFDWAndForeignServer(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.FDWs = []postgresv1alpha1.DatabaseFDWSpec{{
		Name:      "postgres_fdw",
		Handler:   "postgres_fdw_handler",
		Validator: "postgres_fdw_validator",
		Owner:     "postgres",
		Options: []postgresv1alpha1.DatabaseOptionSpec{{
			Name:  "fetch_size",
			Value: "1000",
		}},
		Usage: []postgresv1alpha1.DatabaseUsageSpec{{
			Name: "app",
			Type: postgresv1alpha1.DatabaseUsageGrant,
		}},
	}}
	db.Spec.Servers = []postgresv1alpha1.DatabaseServerSpec{{
		Name: "angus",
		FDW:  "postgres_fdw",
		Options: []postgresv1alpha1.DatabaseOptionSpec{{
			Name:  "host",
			Value: "angus-rw",
		}, {
			Name:  "dbname",
			Value: "app",
		}},
		Usage: []postgresv1alpha1.DatabaseUsageSpec{{
			Name: "app",
			Type: postgresv1alpha1.DatabaseUsageGrant,
		}},
	}}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresDatabaseOnce(t, r, c, db)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{
		`CREATE FOREIGN DATA WRAPPER "postgres_fdw" HANDLER "postgres_fdw_handler" VALIDATOR "postgres_fdw_validator" OPTIONS ("fetch_size"`,
		`ALTER FOREIGN DATA WRAPPER "postgres_fdw" OWNER TO "postgres"`,
		`ALTER FOREIGN DATA WRAPPER "postgres_fdw" OPTIONS (ADD "fetch_size"`,
		`GRANT USAGE ON FOREIGN DATA WRAPPER "postgres_fdw" TO "app"`,
		`CREATE SERVER IF NOT EXISTS "angus" FOREIGN DATA WRAPPER "postgres_fdw" OPTIONS ("host"`,
		`"dbname"`,
		`ALTER SERVER "angus" OPTIONS (ADD "host"`,
		`GRANT USAGE ON FOREIGN SERVER "angus" TO "app"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresDatabaseReconcileAppliesDatabaseAndSchemaPrivileges(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.Privileges = []postgresv1alpha1.DatabaseGrantSpec{{
		Role:       "app_rw",
		Privileges: []string{"CONNECT", "TEMP"},
		Type:       postgresv1alpha1.DatabaseUsageGrant,
	}, {
		Role:       "app_legacy",
		Privileges: []string{"CREATE"},
		Type:       postgresv1alpha1.DatabaseUsageRevoke,
	}}
	db.Spec.Schemas = []postgresv1alpha1.DatabaseSchemaSpec{{
		Name:  "app",
		Owner: "app",
		Privileges: []postgresv1alpha1.DatabaseGrantSpec{{
			Role:       "app_ro",
			Privileges: []string{"USAGE"},
			Type:       postgresv1alpha1.DatabaseUsageGrant,
		}},
	}}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresDatabaseOnce(t, r, c, db)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{
		`GRANT CONNECT, TEMPORARY ON DATABASE "appdb" TO "app_rw"`,
		`REVOKE CREATE ON DATABASE "appdb" FROM "app_legacy"`,
		`GRANT USAGE ON SCHEMA "app" TO "app_ro"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresDatabaseRejectsInvalidPrivilege(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.Privileges = []postgresv1alpha1.DatabaseGrantSpec{{
		Role:       "app",
		Privileges: []string{`CONNECT; DROP DATABASE postgres; --`},
	}}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresDatabaseOnce(t, r, c, db)

	if got.Status.Applied {
		t.Fatalf("Applied = true, want invalid privilege rejected")
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0 for invalid privilege", len(executor.calls))
	}
	if !strings.Contains(got.Status.Message, "spec.privileges[0].privileges[0]") {
		t.Fatalf("message = %q, want privilege path", got.Status.Message)
	}
}

func TestPostgresDatabaseReconcileDropsFDWAndForeignServerWhenAbsent(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.FDWs = []postgresv1alpha1.DatabaseFDWSpec{{
		Name:   "legacy_fdw",
		Ensure: postgresv1alpha1.DatabaseEnsureAbsent,
	}}
	db.Spec.Servers = []postgresv1alpha1.DatabaseServerSpec{{
		Name:   "legacy",
		FDW:    "legacy_fdw",
		Ensure: postgresv1alpha1.DatabaseEnsureAbsent,
	}}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	r := &PostgresDatabaseReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresDatabaseOnce(t, r, c, db)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{
		`DROP FOREIGN DATA WRAPPER IF EXISTS "legacy_fdw"`,
		`DROP SERVER IF EXISTS "legacy"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
	serverDrop := strings.Index(command, `DROP SERVER IF EXISTS "legacy"`)
	fdwDrop := strings.Index(command, `DROP FOREIGN DATA WRAPPER IF EXISTS "legacy_fdw"`)
	if serverDrop < 0 || fdwDrop < 0 || serverDrop > fdwDrop {
		t.Fatalf("drop order should remove foreign server before FDW:\n%s", command)
	}
}

type fakeDatabaseSQLExecutor struct {
	calls    []databaseSQLExecCall
	assertFn func(target BackupSidecarTarget)
}

type databaseSQLExecCall struct {
	target  BackupSidecarTarget
	command []string
}

func (f *fakeDatabaseSQLExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	if f.assertFn != nil {
		f.assertFn(target)
	}
	f.calls = append(f.calls, databaseSQLExecCall{
		target:  target,
		command: append([]string{}, command...),
	})
	return []byte("ok"), nil
}

func newPostgresDatabaseCluster() *postgresv1alpha1.PostgresCluster {
	cluster := newCluster()
	cluster.Status.Shards = []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod:   "demo-db-shard-0-0",
			Ready: true,
		},
	}}
	return cluster
}

func newPostgresDatabase() *postgresv1alpha1.PostgresDatabase {
	return &postgresv1alpha1.PostgresDatabase{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-appdb",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: postgresv1alpha1.PostgresDatabaseSpec{
			Cluster: postgresv1alpha1.DatabaseClusterRef{Name: "demo"},
			Name:    "appdb",
			Owner:   "app",
		},
	}
}

func reconcilePostgresDatabaseOnce(
	t *testing.T,
	r *PostgresDatabaseReconciler,
	c client.Client,
	db *postgresv1alpha1.PostgresDatabase,
) *postgresv1alpha1.PostgresDatabase {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: db.Namespace, Name: db.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	var got postgresv1alpha1.PostgresDatabase
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &got); err != nil {
		t.Fatalf("Get back: %v", err)
	}
	return &got
}
