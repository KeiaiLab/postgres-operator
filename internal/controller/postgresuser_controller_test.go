/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestPostgresUserReconcileCreatesRoleOnReadyPrimary(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.CreateDB = true
	user.Spec.ConnectionLimit = int32Ptr(25)
	user.Spec.ValidUntil = "infinity"
	user.Spec.InRoles = []string{"readonly", "writers"}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresUserOnce(t, r, c, user)

	if !got.Status.Applied {
		t.Fatalf("Applied = false, want true: %+v", got.Status)
	}
	if got.Status.ObservedGeneration != user.Generation {
		t.Fatalf("ObservedGeneration = %d, want %d", got.Status.ObservedGeneration, user.Generation)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PostgresUserConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != PostgresUserReasonReconciled {
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
		`CREATE ROLE "app" WITH LOGIN NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT CONNECTION LIMIT 25 VALID UNTIL`,
		`ALTER ROLE "app" WITH LOGIN NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT CONNECTION LIMIT 25 VALID UNTIL`,
		`infinity`,
		`GRANT "readonly" TO "app"`,
		`GRANT "writers" TO "app"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresUserReconcileDropsRoleWhenAbsent(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.Ensure = postgresv1alpha1.DatabaseEnsureAbsent
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresUserOnce(t, r, c, user)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	if !strings.Contains(command, `DROP ROLE IF EXISTS "app"`) {
		t.Fatalf("command missing DROP ROLE:\n%s", command)
	}
}

func TestPostgresUserReconcileRevokesMembershipsOutsideSpec(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.InRoles = []string{"readonly"}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresUserOnce(t, r, c, user)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{
		`SELECT quote_ident(parent.rolname)`,
		`NOT parent.rolname = ANY`,
		`readonly`,
		`::name[]`,
		`managed_role='"app"'`,
		`REVOKE $parent_role FROM $managed_role`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresUserReconcileAppliesPasswordSecret(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "app-password"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "app-password",
			Namespace:       "default",
			ResourceVersion: "42",
		},
		Data: map[string][]byte{
			"username": []byte("app"),
			"password": []byte("secret-password"),
		},
	}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user, secret).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresUserOnce(t, r, c, user)

	if got.Status.PasswordSecretResourceVersion != "42" {
		t.Fatalf("PasswordSecretResourceVersion = %q, want 42", got.Status.PasswordSecretResourceVersion)
	}

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	for _, want := range []string{`PASSWORD`, `secret-password`} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
}

func TestPostgresUserReconcileDisablesPassword(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.DisablePassword = true
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	reconcilePostgresUserOnce(t, r, c, user)

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	command := strings.Join(executor.calls[0].command, " ")
	if !strings.Contains(command, `PASSWORD NULL`) {
		t.Fatalf("command missing PASSWORD NULL:\n%s", command)
	}
}

func TestPostgresUserRejectsPasswordSecretUsernameMismatch(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "app-password"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-password",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"username": []byte("other"),
			"password": []byte("secret-password"),
		},
	}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user, secret).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresUserOnce(t, r, c, user)

	if got.Status.Applied {
		t.Fatalf("Applied = true, want false for password Secret username mismatch")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PostgresUserConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != PostgresUserReasonPasswordSecretError {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0 for password Secret username mismatch", len(executor.calls))
	}
}

func TestPostgresUserRejectsPasswordSecretWithDisablePassword(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	user := newPostgresUser()
	user.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "app-password"}
	user.Spec.DisablePassword = true
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	r := &PostgresUserReconciler{
		Client:      c,
		Scheme:      scheme,
		SQLExecutor: executor,
	}

	got := reconcilePostgresUserOnce(t, r, c, user)

	if got.Status.Applied {
		t.Fatalf("Applied = true, want false for passwordSecretRef+disablePassword")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, PostgresUserConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != PostgresUserReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0 for invalid spec", len(executor.calls))
	}
}

func TestPostgresUserMapsPasswordSecretChangesToReferencingUsers(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	matching := newPostgresUser()
	matching.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "app-password"}
	otherSecret := newPostgresUser()
	otherSecret.Name = "demo-other-secret"
	otherSecret.Spec.Name = "other-secret"
	otherSecret.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "other-password"}
	otherNamespace := newPostgresUser()
	otherNamespace.Name = "other-namespace-user"
	otherNamespace.Namespace = "other"
	otherNamespace.Spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "app-password"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-password",
			Namespace: "default",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(matching, otherSecret, otherNamespace).
		Build()
	r := &PostgresUserReconciler{
		Client: c,
		Scheme: scheme,
	}

	requests := r.postgresUsersForPasswordSecret(context.Background(), secret)

	if len(requests) != 1 {
		t.Fatalf("requests = %v, want exactly matching PostgresUser", requests)
	}
	if requests[0].String() != "default/demo-app" {
		t.Fatalf("request = %s, want default/demo-app", requests[0].String())
	}
}

func newPostgresUser() *postgresv1alpha1.PostgresUser {
	return &postgresv1alpha1.PostgresUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-app",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: postgresv1alpha1.PostgresUserSpec{
			Cluster: postgresv1alpha1.DatabaseClusterRef{Name: "demo"},
			Name:    "app",
			Ensure:  postgresv1alpha1.DatabaseEnsurePresent,
			Login:   true,
		},
	}
}

func reconcilePostgresUserOnce(
	t *testing.T,
	r *PostgresUserReconciler,
	c client.Client,
	user *postgresv1alpha1.PostgresUser,
) *postgresv1alpha1.PostgresUser {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: user.Namespace, Name: user.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	var got postgresv1alpha1.PostgresUser
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: user.Namespace, Name: user.Name}, &got); err != nil {
		t.Fatalf("Get back: %v", err)
	}
	return &got
}

//nolint:modernize // typed-value pointer helper (int32Ptr(5) ≠ new(int32))
func int32Ptr(value int32) *int32 {
	return &value
}

// TestPostgresUserReconcileScriptDoesNotUseEval mirrors the same
// regression guard added for PostgresDatabase — the rendered shell
// script must not use `eval`, which re-tokenises the SQL on whitespace
// after the outer shell has stripped the surrounding quotes.
func TestPostgresUserReconcileScriptDoesNotUseEval(t *testing.T) {
	t.Parallel()
	user := newPostgresUser()
	script := postgresUserReconcileScript(user, "PASSWORD 'app'")
	if strings.Contains(script, "eval ") || strings.Contains(script, "eval\t") {
		t.Fatalf("rendered script must not use `eval` — it re-tokenises SQL on whitespace:\n%s", script)
	}
}
