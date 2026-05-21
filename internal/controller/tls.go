/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// pgTLSMountPath 는 postgres pod 가 server cert/key 를 읽는 경로.
// postgresql.conf 의 ssl_cert_file / ssl_key_file 가 본 경로 참조 (Phase 3b 별 cycle).
const pgTLSMountPath = "/etc/ssl/postgres"

// Pillar P7 §7 TLS 통합 3-phase roadmap (ADR-0062 후속, 본 cluster 의 외부 TLS 클라이언트
// 회복 대상):
//
//   Phase 1 (alpha.5): CRD spec.tls field facade. enabled=true 시 webhook reject.
//   Phase 2 (alpha.6): 본 파일. cert-manager Certificate CR 자동 생성 (IssuerRef 참조).
//                      Phase 3 까지는 Certificate 만 emit, STS volume mount 없음.
//   Phase 3 (alpha.7): 별 cycle. STS template 의 volumes/volumeMounts + postgresql.conf
//                      ssl=on + ssl_cert_file/ssl_key_file + bootstrap container 의
//                      pg_hba.conf hostssl 강제. webhook NotImplemented 제거.
//
// Certificate CR 은 cert-manager.io/v1 group. operator 는 unstructured 로 emit하여
// cert-manager Go SDK 의존을 회피 — cert-manager 미설치 cluster 도 본 operator 가
// 동작 (TLS off path). 단 TLS.Enabled=true + cert-manager 부재 시 cert-manager
// 가 Certificate CR 을 reconcile 못해 Secret 자동 발급 실패 — 사용자 책임.

// CertificateGVK 는 cert-manager Certificate CR 의 GroupVersionKind.
var CertificateGVK = schema.GroupVersionKind{
	Group:   "cert-manager.io",
	Version: "v1",
	Kind:    "Certificate",
}

// TLSCertSecretName 은 cluster 의 server cert Secret 이름을 결정한다.
// 사용자 명시 (spec.tls.certSecretName) 우선, 미설정 시 "<cluster>-tls" default.
func TLSCertSecretName(cluster *postgresv1alpha1.PostgresCluster) string {
	if cluster.Spec.TLS != nil && cluster.Spec.TLS.CertSecretName != "" {
		return cluster.Spec.TLS.CertSecretName
	}
	return cluster.Name + "-tls"
}

// buildCertificate 는 cert-manager Certificate CR 을 반환한다 (Phase 2).
//
// SAN 은 shard 별 headless Service DNS + cluster-wide client Service DNS 를
// 모두 포함 — Phase 3 의 reconciler 가 server cert 를 모든 shard pod 의 STS
// volume mount 로 사용 시 hostname verify 실패 회피.
//
// duration / renewBefore / privateKey rotation 은 cert-manager default
// (90d / 15d / Always) 사용 — 명시 필요 시 spec.tls 에 후속 field 추가.
func buildCertificate(cluster *postgresv1alpha1.PostgresCluster) *unstructured.Unstructured {
	if cluster.Spec.TLS == nil || !cluster.Spec.TLS.Enabled || cluster.Spec.TLS.IssuerRef == nil {
		return nil
	}
	issuer := cluster.Spec.TLS.IssuerRef
	kind := issuer.Kind
	if kind == "" {
		kind = "Issuer"
	}

	// SAN: cluster.Name 외에 모든 shard ordinal 의 headless service DNS 포함.
	// unstructured.SetNestedField 는 []any 만 deep copy 가능 ([]string 에서 panic).
	dnsNames := []any{cluster.Name}
	for ord := int32(0); ord < cluster.Spec.Shards.InitialCount; ord++ {
		svc := ShardServiceName(cluster.Name, ord)
		dnsNames = append(dnsNames,
			svc,
			fmt.Sprintf("%s.%s", svc, cluster.Namespace),
			fmt.Sprintf("%s.%s.svc", svc, cluster.Namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", svc, cluster.Namespace),
		)
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(CertificateGVK)
	cert.SetName(cluster.Name + "-tls")
	cert.SetNamespace(cluster.Namespace)
	cert.SetLabels(map[string]string{
		"app.kubernetes.io/name":       "postgrescluster",
		"app.kubernetes.io/instance":   cluster.Name,
		"app.kubernetes.io/managed-by": "keiailab-postgres-operator",
		"postgres.keiailab.io/role":    "server-tls",
	})

	spec := map[string]any{
		"secretName": TLSCertSecretName(cluster),
		"commonName": cluster.Name,
		"dnsNames":   dnsNames,
		"issuerRef": map[string]any{
			"name": issuer.Name,
			"kind": kind,
			// group default = cert-manager.io (cert-manager Issuer/ClusterIssuer 만 지원).
			"group": "cert-manager.io",
		},
		"usages": []any{"server auth", "client auth"},
		"privateKey": map[string]any{
			"algorithm":      "ECDSA",
			"size":           int64(256),
			"rotationPolicy": "Always",
		},
	}
	if err := unstructured.SetNestedField(cert.Object, spec, "spec"); err != nil {
		// programming error — spec 은 단순 map. recover 불필요.
		return nil
	}
	return cert
}

// tlsEnabled 는 cluster 가 TLS Phase 3 mount 를 활성한 상태인지 반환한다.
// IssuerRef 명시 + Enabled=true 둘 다 충족시만 true.
func tlsEnabled(cluster *postgresv1alpha1.PostgresCluster) bool {
	return cluster.Spec.TLS != nil &&
		cluster.Spec.TLS.Enabled &&
		cluster.Spec.TLS.IssuerRef != nil &&
		cluster.Spec.TLS.IssuerRef.Name != ""
}

// tlsVolumes 는 TLS 활성 시 server cert Secret 을 mount 하는 Volume 을 반환한다.
// cert-manager 가 발급한 Secret (tls.crt + tls.key + ca.crt) 을 그대로 mount —
// postgres 의 ssl_cert_file/ssl_key_file 가 본 경로 참조 (Phase 3b 별 cycle).
//
// defaultMode 0o400: postgres user (999) read-only. group/other 차단으로
// PostgreSQL 의 키 파일 권한 검사 통과.
func tlsVolumes(cluster *postgresv1alpha1.PostgresCluster) []corev1.Volume {
	if !tlsEnabled(cluster) {
		return nil
	}
	mode := int32(0o400)
	return []corev1.Volume{{
		Name: "tls-server",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  TLSCertSecretName(cluster),
				DefaultMode: &mode,
			},
		},
	}}
}

// tlsVolumeMounts 는 tls-server Volume 의 mount path 를 반환한다.
func tlsVolumeMounts(cluster *postgresv1alpha1.PostgresCluster) []corev1.VolumeMount {
	if !tlsEnabled(cluster) {
		return nil
	}
	return []corev1.VolumeMount{{
		Name:      "tls-server",
		MountPath: pgTLSMountPath,
		ReadOnly:  true,
	}}
}
