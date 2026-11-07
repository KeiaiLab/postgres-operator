/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestManagedRolesStatusForUsersBucketsAndPasswordState(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}
	applied := newManagedRoleUser("app", "demo")
	applied.Generation = 2
	applied.Status.Applied = true
	applied.Status.ObservedGeneration = 2
	applied.Status.PasswordSecretResourceVersion = "rv-2"

	stale := newManagedRoleUser("writer", "demo")
	stale.Generation = 3
	stale.Status.Applied = true
	stale.Status.ObservedGeneration = 2

	failed := newManagedRoleUser("broken", "demo")
	failed.Generation = 1
	failed.Status.Message = "could not perform UPDATE_MEMBERSHIPS on role broken: role \"poets\" does not exist"

	otherCluster := newManagedRoleUser("ignored-other-cluster", "other")
	otherNamespace := newManagedRoleUser("ignored-other-namespace", "demo")
	otherNamespace.Namespace = otherNamespaceName

	status := managedRolesStatusForUsers(cluster, []postgresv1alpha1.PostgresUser{
		*stale,
		*failed,
		*otherCluster,
		*otherNamespace,
		*applied,
	})

	if got := status.ByStatus["reserved"]; len(got) != 2 || got[0] != "postgres" || got[1] != "streaming_replica" {
		t.Fatalf("reserved roles = %v, want postgres,streaming_replica", got)
	}
	if got := status.ByStatus["reconciled"]; len(got) != 1 || got[0] != "app" {
		t.Fatalf("reconciled roles = %v, want app", got)
	}
	if got := status.ByStatus["pending-reconciliation"]; len(got) != 2 || got[0] != "broken" || got[1] != "writer" {
		t.Fatalf("pending roles = %v, want broken,writer", got)
	}
	if got := status.CannotReconcile["broken"]; len(got) != 1 || got[0] != failed.Status.Message {
		t.Fatalf("cannotReconcile[broken] = %v, want failure message", got)
	}
	if got := status.PasswordStatus["app"].SecretResourceVersion; got != "rv-2" {
		t.Fatalf("passwordStatus[app].secretResourceVersion = %q, want rv-2", got)
	}
}

func TestPostgresClusterMapsPostgresUserToOwningCluster(t *testing.T) {
	t.Parallel()

	r := &PostgresClusterReconciler{
		Client: fake.NewClientBuilder().WithScheme(newScheme(t)).Build(),
	}
	user := newManagedRoleUser("app", "demo")
	requests := r.postgresClustersForUser(context.Background(), user)

	if len(requests) != 1 {
		t.Fatalf("requests = %v, want one owning cluster request", requests)
	}
	if requests[0].NamespacedName != (types.NamespacedName{Namespace: "default", Name: "demo"}) {
		t.Fatalf("request = %s, want default/demo", requests[0].String())
	}
}

func newManagedRoleUser(roleName, clusterName string) *postgresv1alpha1.PostgresUser {
	return &postgresv1alpha1.PostgresUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:       roleName,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: postgresv1alpha1.PostgresUserSpec{
			Cluster: postgresv1alpha1.DatabaseClusterRef{Name: clusterName},
			Name:    roleName,
		},
	}
}
