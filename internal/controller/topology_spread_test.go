/*
Copyright 2026 Keiailab.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestDefaultedTopologySpread_user_provided_preserved(t *testing.T) {
	user := []corev1.TopologySpreadConstraint{{
		MaxSkew:     2,
		TopologyKey: "rack",
	}}
	got := defaultedTopologySpread(user, 3, map[string]string{"app": "x"})
	if len(got) != 1 || got[0].TopologyKey != "rack" {
		t.Errorf("user TSC overridden: %v", got)
	}
}

func TestDefaultedTopologySpread_replicas_0_no_inject(t *testing.T) {
	got := defaultedTopologySpread(nil, 0, map[string]string{"app": "x"})
	if got != nil {
		t.Errorf("replicas=0 (HA 미구성) → 미주입 expected, got %v", got)
	}
}

func TestDefaultedTopologySpread_replicas_ge_1_injects_2_axes(t *testing.T) {
	got := defaultedTopologySpread(nil, 1, map[string]string{"app": "x"})
	if len(got) != 2 {
		t.Fatalf("expected 2 default TSCs (zone + hostname), got %d", len(got))
	}
	if got[0].TopologyKey != "topology.kubernetes.io/zone" {
		t.Errorf("first TSC: %q", got[0].TopologyKey)
	}
	if got[1].TopologyKey != "kubernetes.io/hostname" {
		t.Errorf("second TSC: %q", got[1].TopologyKey)
	}
	for _, c := range got {
		if c.MaxSkew != 1 {
			t.Errorf("MaxSkew: %d", c.MaxSkew)
		}
		if c.WhenUnsatisfiable != corev1.ScheduleAnyway {
			t.Errorf("WhenUnsatisfiable: %q", c.WhenUnsatisfiable)
		}
	}
}

func TestDefaultedTopologySpread_label_selector_matches(t *testing.T) {
	selector := map[string]string{"app.kubernetes.io/name": "postgres", "app.kubernetes.io/instance": "x"}
	got := defaultedTopologySpread(nil, 2, selector)
	for _, c := range got {
		for k, v := range selector {
			if c.LabelSelector.MatchLabels[k] != v {
				t.Errorf("TSC label selector missing %q=%q", k, v)
			}
		}
	}
}
