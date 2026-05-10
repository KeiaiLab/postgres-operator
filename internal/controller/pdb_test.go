/*
Copyright 2026 Keiailab.
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestShouldAutoCreatePDB_members_lt_2_false(t *testing.T) {
	if shouldAutoCreatePDB(0) {
		t.Error("members=0 → false")
	}
	if shouldAutoCreatePDB(1) {
		t.Error("members=1 → false")
	}
}

func TestShouldAutoCreatePDB_members_ge_2_true(t *testing.T) {
	if !shouldAutoCreatePDB(2) {
		t.Error("members=2 → true (HA 보호)")
	}
	if !shouldAutoCreatePDB(5) {
		t.Error("members=5 → true")
	}
}

func TestBuildShardPDB_minAvailable_eq_members_minus_1(t *testing.T) {
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-prod", Namespace: "ns"},
	}
	pdb := BuildShardPDB(cluster, 0, 3)

	if pdb.Name != "pg-prod-shard-0-pdb" {
		t.Errorf("name: %q", pdb.Name)
	}
	if pdb.Namespace != "ns" {
		t.Errorf("namespace: %q", pdb.Namespace)
	}
	if pdb.Spec.MinAvailable == nil {
		t.Fatal("MinAvailable nil")
	}
	if pdb.Spec.MinAvailable.IntVal != 2 { // members(3) - 1 = 2
		t.Errorf("MinAvailable: got %d want 2", pdb.Spec.MinAvailable.IntVal)
	}
}

func TestBuildShardPDB_selector_matches_shard(t *testing.T) {
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "ns"},
	}
	pdb := BuildShardPDB(cluster, 2, 3)
	if pdb.Spec.Selector == nil {
		t.Fatal("Selector nil")
	}
	// shard 2 라벨 매핑.
	expected := SelectorLabels("pg", "shard", int32(2))
	for k, v := range expected {
		if pdb.Spec.Selector.MatchLabels[k] != v {
			t.Errorf("Selector mismatch %q: got %q want %q",
				k, pdb.Spec.Selector.MatchLabels[k], v)
		}
	}
}
