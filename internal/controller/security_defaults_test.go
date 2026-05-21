/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodSecurityRestrictedLabels(t *testing.T) {
	labels := PodSecurityRestrictedLabels()
	for _, mode := range []string{"enforce", "audit", "warn"} {
		k := "pod-security.kubernetes.io/" + mode
		if labels[k] != "restricted" {
			t.Fatalf("label %s want=restricted got=%s", k, labels[k])
		}
		kv := k + "-version"
		if labels[kv] != "latest" {
			t.Fatalf("label %s want=latest got=%s", kv, labels[kv])
		}
	}
}

func TestRestrictedSecurityContext(t *testing.T) {
	sc := RestrictedSecurityContext()
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("AllowPrivilegeEscalation must be false ptr")
	}
	if sc.Privileged == nil || *sc.Privileged {
		t.Fatalf("Privileged must be false ptr")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatalf("ReadOnlyRootFilesystem must be true ptr")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Fatalf("RunAsNonRoot must be true ptr")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("Capabilities.Drop must be [ALL], got %+v", sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("SeccompProfile must be RuntimeDefault, got %+v", sc.SeccompProfile)
	}
}

//nolint:gocyclo // table-driven test enumerates 5 deny policy variants
func TestBuildDefaultDenyNetworkPolicies(t *testing.T) {
	t.Run("기본 4 policy: deny + intra + client + egress", func(t *testing.T) {
		pol := BuildDefaultDenyNetworkPolicies(NetworkPolicyInput{
			Namespace:    "ns-a",
			ClusterName:  "cl-0",
			PostgresPort: 5432,
		})
		if len(pol) != 4 {
			t.Fatalf("want 4 policies (no metrics), got %d", len(pol))
		}
		names := map[string]bool{}
		for _, p := range pol {
			names[p.Name] = true
			if p.Namespace != "ns-a" {
				t.Fatalf("policy %s in wrong ns: %s", p.Name, p.Namespace)
			}
			if p.Labels["postgres.keiailab.io/cluster"] != "cl-0" {
				t.Fatalf("policy %s missing cluster label", p.Name)
			}
		}
		for _, want := range []string{
			"cl-0-default-deny", "cl-0-allow-intra", "cl-0-allow-client", "cl-0-allow-egress",
		} {
			if !names[want] {
				t.Fatalf("missing policy %s, got %+v", want, names)
			}
		}
	})

	t.Run("metrics 옵션 활성", func(t *testing.T) {
		pol := BuildDefaultDenyNetworkPolicies(NetworkPolicyInput{
			Namespace:                "ns-a",
			ClusterName:              "cl-1",
			MetricsPort:              9187,
			AllowMonitoringNamespace: "monitoring",
		})
		if len(pol) != 5 {
			t.Fatalf("want 5 policies (with metrics), got %d", len(pol))
		}
		var metricsPol *networkingv1.NetworkPolicy
		for i := range pol {
			if pol[i].Name == "cl-1-allow-metrics" {
				metricsPol = &pol[i]
			}
		}
		if metricsPol == nil {
			t.Fatalf("metrics policy not found")
		}
		if len(metricsPol.Spec.Ingress) != 1 {
			t.Fatalf("metrics ingress count want=1 got=%d", len(metricsPol.Spec.Ingress))
		}
		if len(metricsPol.Spec.Ingress[0].Ports) != 1 ||
			metricsPol.Spec.Ingress[0].Ports[0].Port.IntValue() != 9187 {
			t.Fatalf("metrics port mismatch: %+v", metricsPol.Spec.Ingress[0].Ports)
		}
	})

	t.Run("default-deny 가 ingress+egress 양쪽 포함", func(t *testing.T) {
		pol := BuildDefaultDenyNetworkPolicies(NetworkPolicyInput{
			Namespace: "ns-a", ClusterName: "cl-0",
		})
		var deny *networkingv1.NetworkPolicy
		for i := range pol {
			if pol[i].Name == "cl-0-default-deny" {
				deny = &pol[i]
			}
		}
		if deny == nil {
			t.Fatalf("deny policy missing")
		}
		hasIngress, hasEgress := false, false
		for _, pt := range deny.Spec.PolicyTypes {
			if pt == networkingv1.PolicyTypeIngress {
				hasIngress = true
			}
			if pt == networkingv1.PolicyTypeEgress {
				hasEgress = true
			}
		}
		if !hasIngress || !hasEgress {
			t.Fatalf("default-deny must have both Ingress+Egress PolicyTypes")
		}
		// 핵심: Ingress / Egress rules 가 *비어 있어야* 진정한 default-deny.
		if len(deny.Spec.Ingress) != 0 || len(deny.Spec.Egress) != 0 {
			t.Fatalf("default-deny rules must be empty (current: %d ingress, %d egress)",
				len(deny.Spec.Ingress), len(deny.Spec.Egress))
		}
	})

	t.Run("client ingress + Pooler port 추가", func(t *testing.T) {
		pol := BuildDefaultDenyNetworkPolicies(NetworkPolicyInput{
			Namespace: "ns-a", ClusterName: "cl-0",
			PoolerPort: 6432,
			ClientNamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "myapp"},
			},
		})
		var client *networkingv1.NetworkPolicy
		for i := range pol {
			if pol[i].Name == "cl-0-allow-client" {
				client = &pol[i]
			}
		}
		if client == nil {
			t.Fatalf("client policy missing")
		}
		if len(client.Spec.Ingress) != 1 ||
			len(client.Spec.Ingress[0].Ports) != 2 {
			t.Fatalf("client ingress port count want=2 (5432+6432) got=%+v", client.Spec.Ingress)
		}
		if len(client.Spec.Ingress[0].From) != 1 ||
			client.Spec.Ingress[0].From[0].NamespaceSelector == nil {
			t.Fatalf("client ingress NamespaceSelector missing")
		}
	})

	t.Run("결정성: 동일 입력 → 동일 출력", func(t *testing.T) {
		in := NetworkPolicyInput{Namespace: "ns", ClusterName: "c"}
		a := BuildDefaultDenyNetworkPolicies(in)
		b := BuildDefaultDenyNetworkPolicies(in)
		if len(a) != len(b) {
			t.Fatalf("length differs: %d vs %d", len(a), len(b))
		}
		for i := range a {
			if a[i].Name != b[i].Name {
				t.Fatalf("policy[%d] name differs: %s vs %s", i, a[i].Name, b[i].Name)
			}
		}
	})
}
