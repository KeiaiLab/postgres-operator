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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// 본 파일은 PostgresCluster CR로부터 K8s 하위 자원(StatefulSet, Service,
// ConfigMap, Deployment)의 desired state를 생성하는 순수 함수들의 모음이다.
//
// 설계 원칙:
//   - 모든 함수는 입력에서 출력으로 결정적(stateless, side-effect 0).
//   - controllerutil.SetControllerReference 호출은 reconciler가 담당. 본 함수는
//     ObjectMeta까지만 채운다.
//   - 컨테이너 이미지 lookup은 internal/version/matrix.go의 결과만 사용한다.
//     본 파일에 imageRef:tag 하드코딩 금지.
//   - PostgreSQL 컨테이너 환경 변수, 볼륨 마운트, postgresql.conf의 세부
//     defaulting은 P1-M1 후속 작업에서 보강한다. 현재는 PG가 부팅 가능한 최소
//     스펙만 보장한다.

const (
	// pgContainerName은 PG 컨테이너의 식별자다. status 보고에서 동일 값을 참조.
	pgContainerName = "postgres"

	// pgPort는 PostgreSQL의 표준 포트다.
	pgPort int32 = 5432

	// pgDataMountPath는 PVC가 마운트되는 위치다.
	pgDataMountPath = "/var/lib/postgresql/data"

	// pgConfigMountPath는 ConfigMap이 마운트되는 위치다.
	pgConfigMountPath = "/etc/postgres-operator/conf"

	// postgresUserUID는 PostgreSQL 표준 postgres user의 UID/GID다.
	// ADR 0006에 의해 동결된 데이터플레인 Pod의 runAsUser/runAsGroup/fsGroup 기본값.
	postgresUserUID int64 = 70
)

// ptrBool/ptrInt64는 외부 의존 없이 inline pointer를 만드는 헬퍼다.
// (K8s API의 *bool/*int64 필드용. k8s.io/utils/ptr import 회피로 SDK 의존 최소화.)
func ptrBool(b bool) *bool    { return &b }
func ptrInt64(i int64) *int64 { return &i }

// dataplanePodSecurityContext는 데이터플레인 Pod(PG StatefulSet, Router Deployment)
// 의 PodSecurityContext 기본값을 반환한다. ADR 0006 §결정에 의해 동결.
//
// 구성:
//   - runAsNonRoot=true (root 거부)
//   - runAsUser/Group/FSGroup=70 (PG postgres user)
//   - seccompProfile=RuntimeDefault (커널 syscall 화이트리스트)
//
// 사용자 override는 향후 PostgresCluster.Spec.SecurityContext 필드 + webhook에서
// 처리한다(ADR 0006 §트레이드오프). 현 시점은 *opt-out 강제* — 운영자가 잊으면
// root 가능 상태로 떨어지지 않도록 default를 항상 강제한다.
func dataplanePodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptrBool(true),
		RunAsUser:    ptrInt64(postgresUserUID),
		RunAsGroup:   ptrInt64(postgresUserUID),
		FSGroup:      ptrInt64(postgresUserUID),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// dataplaneContainerSecurityContext는 데이터플레인 Container의 SecurityContext
// 기본값을 반환한다. ADR 0006 §결정.
//
// 구성:
//   - allowPrivilegeEscalation=false (suid/setuid 비활성)
//   - readOnlyRootFilesystem=true (컨테이너 내 임의 바이너리 작성 차단 — 공급망 공격 완화)
//   - capabilities.drop=[ALL] (모든 Linux capability 제거)
//
// readOnlyRootFilesystem 동반: PG가 /tmp, /run, /var/run/postgresql에 socket/lock
// 작성하므로 emptyDir mount 3개 추가(dataplaneEphemeralVolumeMounts/Volumes).
func dataplaneContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// dataplaneEphemeralVolumeMounts는 readOnlyRootFilesystem=true 동반에 필요한
// 쓰기 가능 mount point들을 반환한다(/tmp, /run, /var/run/postgresql).
func dataplaneEphemeralVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "ephemeral-tmp", MountPath: "/tmp"},
		{Name: "ephemeral-run", MountPath: "/run"},
		{Name: "ephemeral-pg-run", MountPath: "/var/run/postgresql"},
	}
}

// dataplaneEphemeralVolumes는 dataplaneEphemeralVolumeMounts와 짝이 되는
// emptyDir Volume 정의를 반환한다.
func dataplaneEphemeralVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "ephemeral-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-pg-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}

// renderSharedPreloadLibraries는 등록된 모든 ExtensionPlugin을 우선순위 순으로
// 직렬화하여 shared_preload_libraries 값을 만든다.
//
// 우선순위는 Registry.Extensions()가 SharedPreloadOrder 오름차순으로 정렬해
// 반환하는 것으로 보장된다(ADR 0005, registry.go:Extensions). 본 함수는 그 결과를
// 콤마로 join 할 뿐이며, 별도 정렬 로직을 두지 않는다.
//
// 비어 있으면 빈 문자열 반환(예: 단위 테스트에서 Registry에 아무것도 등록 안 한
// 경우). 그 경우 ConfigMap에서 shared_preload_libraries 라인을 생략한다.
func renderSharedPreloadLibraries(reg *plugin.Registry) string {
	if reg == nil {
		return ""
	}
	exts := reg.Extensions()
	names := make([]string, 0, len(exts))
	for _, e := range exts {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// renderPostgresConf는 postgresql.conf의 본문을 생성한다.
// 현재는 최소집합. P10-T2/T3에서 extension별 옵션을 합성하도록 확장된다.
func renderPostgresConf(reg *plugin.Registry) string {
	var sb strings.Builder
	sb.WriteString("# Generated by keiailab-postgres-operator. Do not edit by hand.\n")
	sb.WriteString("listen_addresses = '*'\n")
	if spl := renderSharedPreloadLibraries(reg); spl != "" {
		fmt.Fprintf(&sb, "shared_preload_libraries = '%s'\n", spl)
	}
	return sb.String()
}

// buildConfigMap은 coordinator/worker/router 모두에서 동일 패턴으로 사용된다.
// 호출자가 name·role·pool을 정해 넘긴다.
func buildConfigMap(cluster *postgresv1alpha1.PostgresCluster, name, role, pool string, reg *plugin.Registry) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, role, pool),
		},
		Data: map[string]string{
			"postgresql.conf": renderPostgresConf(reg),
		},
	}
}

// buildHeadlessService는 StatefulSet과 짝이 되는 ClusterIP=None Service를 만든다.
// 안정적 Pod DNS 제공이 목적이다.
func buildHeadlessService(cluster *postgresv1alpha1.PostgresCluster, name, role, pool string) *corev1.Service {
	labels := SelectorLabels(cluster.Name, role, pool)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
			Ports: []corev1.ServicePort{{
				Name:       "postgres",
				Port:       pgPort,
				TargetPort: intstr.FromInt32(pgPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildClientService는 라우터의 진입점 Service(ClusterIP)다.
func buildClientService(cluster *postgresv1alpha1.PostgresCluster, name, role string) *corev1.Service {
	labels := SelectorLabels(cluster.Name, role, "")
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "postgres",
				Port:       pgPort,
				TargetPort: intstr.FromInt32(pgPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildPGStatefulSet은 coordinator 또는 worker pool의 StatefulSet desired state를
// 만든다. role="coordinator" 또는 "worker", pool은 worker일 때만 의미.
func buildPGStatefulSet(
	cluster *postgresv1alpha1.PostgresCluster,
	name, serviceName, role, pool, image, configMapName string,
	members int32,
	storage postgresv1alpha1.StorageSpec,
	resources corev1.ResourceRequirements,
) *appsv1.StatefulSet {
	labels := SelectorLabels(cluster.Name, role, pool)

	pvcAccessModes := storage.AccessModes
	if len(pvcAccessModes) == 0 {
		pvcAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: pvcAccessModes,
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storage.Size,
			},
		},
		StorageClassName: storage.StorageClassName,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: serviceName,
			Replicas:    &members,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: dataplanePodSecurityContext(),
					Containers: []corev1.Container{{
						Name:            pgContainerName,
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Ports: []corev1.ContainerPort{{
							Name:          "postgres",
							ContainerPort: pgPort,
							Protocol:      corev1.ProtocolTCP,
						}},
						VolumeMounts: append([]corev1.VolumeMount{
							{Name: "data", MountPath: pgDataMountPath},
							{Name: "config", MountPath: pgConfigMountPath, ReadOnly: true},
						}, dataplaneEphemeralVolumeMounts()...),
					}},
					Volumes: append([]corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							},
						},
					}}, dataplaneEphemeralVolumes()...),
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "data",
					Labels: labels,
				},
				Spec: pvcSpec,
			}},
		},
	}
}

// buildRouterDeployment는 stateless QueryRouter의 Deployment를 만든다.
// ADR 0003 §강제 메커니즘에 의해 PVC를 절대 마운트하지 않는다(StatefulSet 사용
// 금지). 본 함수는 P12-T2 시점에 cmd/router 바이너리 이미지로 교체된다. 현재는
// PG 베이스 이미지를 그대로 사용하는 placeholder.
func buildRouterDeployment(
	cluster *postgresv1alpha1.PostgresCluster,
	name, configMapName, image string,
	replicas int32,
	resources corev1.ResourceRequirements,
) *appsv1.Deployment {
	labels := SelectorLabels(cluster.Name, "router", "")

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: dataplanePodSecurityContext(),
					Containers: []corev1.Container{{
						Name:            "router",
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Ports: []corev1.ContainerPort{{
							Name:          "postgres",
							ContainerPort: pgPort,
							Protocol:      corev1.ProtocolTCP,
						}},
						VolumeMounts: append([]corev1.VolumeMount{
							{Name: "config", MountPath: pgConfigMountPath, ReadOnly: true},
						}, dataplaneEphemeralVolumeMounts()...),
					}},
					Volumes: append([]corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							},
						},
					}}, dataplaneEphemeralVolumes()...),
				},
			},
		},
	}
}
