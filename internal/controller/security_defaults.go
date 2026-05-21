/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// D.6.4 Security defaults hardening (ROADMAP G2 L135).
//
// 본 파일은 *순수 함수* — operator namespace 자체에 부착할 PSA label set
// 과 PostgresCluster 단위 default-deny NetworkPolicy renderer 를 제공한다.
// 실 reconciler 가 Apply 시점에 본 함수 결과를 K8s API 로 보낸다 (§2 Simplicity).

// PodSecurityRestrictedLabels 는 Pod Security Standard `restricted` 모드를
// 강제하는 namespace label 묶음이다. operator-managed namespace 또는
// PostgresCluster ns 에 부착하면 apiserver 가 PSA admission 으로 강제.
//
// 정합 기준: PSA v1.29+, postgres workload 가 fsGroup / runAsUser 명시
// + capabilities.drop=ALL + readOnlyRootFilesystem=true 모두 충족함을 전제.
// (`internal/controller/builders.go` 의 podSpec 가 본 조건을 이미 충족.)
func PodSecurityRestrictedLabels() map[string]string {
	return map[string]string{
		"pod-security.kubernetes.io/enforce":         "restricted",
		"pod-security.kubernetes.io/enforce-version": "latest",
		"pod-security.kubernetes.io/audit":           "restricted",
		"pod-security.kubernetes.io/audit-version":   "latest",
		"pod-security.kubernetes.io/warn":            "restricted",
		"pod-security.kubernetes.io/warn-version":    "latest",
	}
}

// RestrictedSecurityContext 는 container.securityContext 의 PSA restricted
// 호환 default 를 반환한다. builders.go 의 container spec 가 본 함수를
// 호출하여 일관 적용.
func RestrictedSecurityContext() *corev1.SecurityContext {
	t := true
	f := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &f,
		Privileged:               &f,
		ReadOnlyRootFilesystem:   &t,
		RunAsNonRoot:             &t,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// NetworkPolicyInput 는 BuildDefaultDenyNetworkPolicies 의 입력이다.
type NetworkPolicyInput struct {
	// Namespace 는 정책이 적용될 ns (PostgresCluster 의 ns).
	Namespace string
	// ClusterName 은 PostgresCluster 이름 (labelSelector 의 target).
	ClusterName string
	// PostgresPort 는 wire-protocol port (default 5432).
	PostgresPort int32
	// PoolerPort 는 PgBouncer port (default 6432). 0 이면 Pooler ingress 미렌더.
	PoolerPort int32
	// MetricsPort 는 operator /metrics + exporter port (default 9187). 0 이면 metrics ingress 미렌더.
	MetricsPort int32
	// ClientNamespaceSelector 는 application namespace 의 selector. nil 이면
	// 모든 ns 의 client 허용 (label `postgres.keiailab.io/allow-egress=true` 필요).
	ClientNamespaceSelector *metav1.LabelSelector
	// AllowMonitoringNamespace 는 monitoring stack 의 ns label.
	AllowMonitoringNamespace string
}

// BuildDefaultDenyNetworkPolicies 는 PostgresCluster 단위 NetworkPolicy 목록을 렌더한다.
//
// 정책 구조:
//  1. <cluster>-default-deny: 모든 ingress + egress deny baseline.
//  2. <cluster>-allow-intra: shard pod ↔ shard pod (replication / pg_basebackup) 허용.
//  3. <cluster>-allow-client: 명시된 client ns + Pooler ns 에서 5432/6432 inbound 허용.
//  4. <cluster>-allow-metrics: monitoring ns 에서 /metrics scrape inbound 허용 (MetricsPort 가 0 이면 skip).
//  5. <cluster>-allow-egress: 같은 cluster 내부 DNS / apiserver / object-store egress 허용.
//
// 본 함수는 *순수 spec 생성* — Apply 는 호출자가 수행. unit test 가 spec 의 결정성을 검증.
func BuildDefaultDenyNetworkPolicies(in NetworkPolicyInput) []networkingv1.NetworkPolicy {
	if in.PostgresPort == 0 {
		in.PostgresPort = 5432
	}
	podSel := metav1.LabelSelector{
		MatchLabels: map[string]string{
			"postgres.keiailab.io/cluster": in.ClusterName,
		},
	}
	pol := []networkingv1.NetworkPolicy{
		// 1. default-deny baseline.
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      in.ClusterName + "-default-deny",
				Namespace: in.Namespace,
				Labels:    standardLabels(in.ClusterName),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: podSel,
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress,
				},
			},
		},
		// 2. intra-cluster shard-to-shard.
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      in.ClusterName + "-allow-intra",
				Namespace: in.Namespace,
				Labels:    standardLabels(in.ClusterName),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: podSel,
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress,
				},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &podSel}},
					Ports: tcpPorts(in.PostgresPort),
				}},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &podSel}},
					Ports: tcpPorts(in.PostgresPort),
				}},
			},
		},
		// 3. client (application + Pooler) ingress.
		buildClientIngress(in, podSel),
		// 5. essential egress (DNS + apiserver).
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      in.ClusterName + "-allow-egress",
				Namespace: in.Namespace,
				Labels:    standardLabels(in.ClusterName),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: podSel,
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{
					// DNS UDP/TCP 53
					{
						To: []networkingv1.NetworkPolicyPeer{{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
							},
						}},
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: protoPtr(corev1.ProtocolUDP), Port: portPtr(53)},
							{Protocol: protoPtr(corev1.ProtocolTCP), Port: portPtr(53)},
						},
					},
				},
			},
		},
	}

	// 4. metrics ingress (옵션).
	if in.MetricsPort > 0 && in.AllowMonitoringNamespace != "" {
		pol = append(pol, networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      in.ClusterName + "-allow-metrics",
				Namespace: in.Namespace,
				Labels:    standardLabels(in.ClusterName),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: podSel,
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"kubernetes.io/metadata.name": in.AllowMonitoringNamespace,
							},
						},
					}},
					Ports: tcpPorts(in.MetricsPort),
				}},
			},
		})
	}
	return pol
}

func buildClientIngress(in NetworkPolicyInput, podSel metav1.LabelSelector) networkingv1.NetworkPolicy {
	from := []networkingv1.NetworkPolicyPeer{}
	if in.ClientNamespaceSelector != nil {
		from = append(from, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: in.ClientNamespaceSelector,
		})
	}
	ports := tcpPorts(in.PostgresPort)
	if in.PoolerPort > 0 {
		ports = append(ports, tcpPort(in.PoolerPort))
	}
	return networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      in.ClusterName + "-allow-client",
			Namespace: in.Namespace,
			Labels:    standardLabels(in.ClusterName),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: podSel,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  from,
				Ports: ports,
			}},
		},
	}
}

func standardLabels(cluster string) map[string]string {
	return map[string]string{
		"postgres.keiailab.io/cluster":    cluster,
		"postgres.keiailab.io/managed-by": "postgres-operator",
		"postgres.keiailab.io/security":   "default-deny",
	}
}

func tcpPort(p int32) networkingv1.NetworkPolicyPort {
	return networkingv1.NetworkPolicyPort{
		Protocol: protoPtr(corev1.ProtocolTCP),
		Port:     portPtr(int(p)),
	}
}

func tcpPorts(ps ...int32) []networkingv1.NetworkPolicyPort {
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ps))
	for _, p := range ps {
		out = append(out, tcpPort(p))
	}
	return out
}

//nolint:modernize // helper kept for readability in NetworkPolicy port renderer
func protoPtr(p corev1.Protocol) *corev1.Protocol { return &p }
func portPtr(p int) *intstr.IntOrString {
	v := intstr.FromInt(p)
	return &v
}
