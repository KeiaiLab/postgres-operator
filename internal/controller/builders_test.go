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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ADR 0006 (Security Defaults Rationale)의 회귀 차단용 단위 테스트다.
// 데이터플레인 Pod의 SecurityContext가 *기본값으로 항상 적용*되는지 검증한다.
//
// 회귀 시 PR이 fail해야 한다 — 운영자가 잊으면 root 가능 상태로 돌아가는 것을
// 방지하는 것이 본 ADR의 핵심.

func TestDataplanePodSecurityContext_ADR0006(t *testing.T) {
	t.Parallel()

	sc := dataplanePodSecurityContext()
	if sc == nil {
		t.Fatal("dataplanePodSecurityContext() returned nil — ADR 0006 위반")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("RunAsNonRoot: want true, got %v", sc.RunAsNonRoot)
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != postgresUserUID {
		t.Errorf("RunAsUser: want %d (postgresUserUID), got %v", postgresUserUID, sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != postgresUserUID {
		t.Errorf("RunAsGroup: want %d, got %v", postgresUserUID, sc.RunAsGroup)
	}
	if sc.FSGroup == nil || *sc.FSGroup != postgresUserUID {
		t.Errorf("FSGroup: want %d, got %v", postgresUserUID, sc.FSGroup)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("SeccompProfile.Type: want RuntimeDefault, got %v", sc.SeccompProfile)
	}
}

func TestDataplaneContainerSecurityContext_ADR0006(t *testing.T) {
	t.Parallel()

	sc := dataplaneContainerSecurityContext()
	if sc == nil {
		t.Fatal("dataplaneContainerSecurityContext() returned nil — ADR 0006 위반")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation: want false, got %v", sc.AllowPrivilegeEscalation)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("ReadOnlyRootFilesystem: want true, got %v", sc.ReadOnlyRootFilesystem)
	}
	if sc.Capabilities == nil {
		t.Fatal("Capabilities: want non-nil with Drop=[ALL]")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop: want [ALL], got %v", sc.Capabilities.Drop)
	}
}

func TestBuildPGStatefulSet_AppliesSecurityContextAndEphemeralMounts(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}
	sts := buildPGStatefulSet(
		cluster,
		"test-shard-0", "test-shard-0-headless",
		0,
		"example.com/postgres:18", "test-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		corev1.ResourceRequirements{},
	)

	assertDataplaneSecurityContext(t, &sts.Spec.Template.Spec, "PG StatefulSet")
}

func TestBuildPGStatefulSet_InjectsInstanceEnv(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
	}
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-3", "demo-shard-3-headless",
		3,
		"example.com/postgres:18", "demo-shard-3-config", "18",
		1,
		postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		corev1.ResourceRequirements{},
	)

	if got, want := len(sts.Spec.Template.Spec.Containers), 1; got != want {
		t.Fatalf("containers count = %d, want %d", got, want)
	}
	envByName := map[string]corev1.EnvVar{}
	for _, e := range sts.Spec.Template.Spec.Containers[0].Env {
		envByName[e.Name] = e
	}
	// supervise.Config 필수 env (instance 가 envOrDie 로 강제).
	expectedValues := map[string]string{
		"POSTGRES_CLUSTER":       "demo",
		"POSTGRES_ROLE":          "shard",
		"POSTGRES_SHARD_ORDINAL": "3",
		"POSTGRES_BIN_DIR":       "/usr/lib/postgresql/18/bin",
		"POSTGRES_DATA_DIR":      "/var/lib/postgresql/data/pgdata",
		"POSTGRES_CONFIG_FILE":   "/etc/postgres-operator/conf/postgresql.conf",
		"POSTGRES_HBA_FILE":      "/etc/postgres-operator/conf/pg_hba.conf",
		"POSTGRES_LOCAL_DSN":     "host=/var/run/postgresql user=postgres dbname=postgres",
	}
	for name, want := range expectedValues {
		got, ok := envByName[name]
		if !ok {
			t.Errorf("env %s missing", name)
			continue
		}
		if got.Value != want {
			t.Errorf("env %s = %q, want %q", name, got.Value, want)
		}
	}
	// downward API — POD_NAME / POD_NAMESPACE 는 ValueFrom.FieldRef 만 검증.
	for _, name := range []string{"POD_NAME", "POD_NAMESPACE"} {
		e, ok := envByName[name]
		if !ok {
			t.Errorf("env %s missing", name)
			continue
		}
		if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil {
			t.Errorf("env %s should use ValueFrom.FieldRef (downward API)", name)
		}
	}
}

func TestBuildConfigMap_IncludesPGHBA(t *testing.T) {
	t.Parallel()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
	}
	cm := buildConfigMap(cluster, "demo-cm", "shard", 0, nil)
	if _, ok := cm.Data["postgresql.conf"]; !ok {
		t.Error("ConfigMap missing postgresql.conf")
	}
	if _, ok := cm.Data["pg_hba.conf"]; !ok {
		t.Error("ConfigMap missing pg_hba.conf")
	}
}

func TestBuildPGStatefulSet_HasInitdbAndServiceAccount(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
	}
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		corev1.ResourceRequirements{},
	)
	pod := &sts.Spec.Template.Spec
	if pod.ServiceAccountName != "demo-instance" {
		t.Errorf("ServiceAccountName = %q, want demo-instance", pod.ServiceAccountName)
	}
	if got := len(pod.InitContainers); got != 1 {
		t.Fatalf("init containers = %d, want 1", got)
	}
	init := pod.InitContainers[0]
	if init.Name != "initdb" {
		t.Errorf("init container name = %q, want initdb", init.Name)
	}
	if len(init.Command) == 0 || init.Command[0] != "sh" {
		t.Errorf("init command should be sh -c, got %v", init.Command)
	}
}

func TestBuildInstanceRole_HasLeaseAndPVCVerbs(t *testing.T) {
	t.Parallel()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
	}
	role := buildInstanceRole(cluster)
	type ruleKey struct{ group, resource string }
	got := map[ruleKey][]string{}
	for _, r := range role.Rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				got[ruleKey{g, res}] = r.Verbs
			}
		}
	}
	leaseVerbs, ok := got[ruleKey{"coordination.k8s.io", "leases"}]
	if !ok {
		t.Fatal("Role missing coordination.k8s.io/leases rule")
	}
	required := map[string]bool{"get": false, "create": false, "update": false}
	for _, v := range leaseVerbs {
		if _, ok := required[v]; ok {
			required[v] = true
		}
	}
	for v, ok := range required {
		if !ok {
			t.Errorf("leases rule missing verb %q", v)
		}
	}
	pvcVerbs, ok := got[ruleKey{"", "persistentvolumeclaims"}]
	if !ok {
		t.Fatal("Role missing core/persistentvolumeclaims rule")
	}
	hasPatch := false
	for _, v := range pvcVerbs {
		if v == "patch" {
			hasPatch = true
		}
	}
	if !hasPatch {
		t.Error("pvc rule must include patch (fencing.MarkFenced 사용)")
	}
}

func TestBuildRouterDeployment_AppliesSecurityContextAndEphemeralMounts(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}
	dep := buildRouterDeployment(
		cluster,
		"test-router", "test-cm", "example.com/router:placeholder",
		2,
		corev1.ResourceRequirements{},
	)
	// buildRouterDeployment의 반환 타입이 *appsv1.Deployment임이 컴파일 시점에
	// 보장되므로(시그니처 변경 시 본 호출 자체가 fail), 별도 type assertion 불요.

	assertDataplaneSecurityContext(t, &dep.Spec.Template.Spec, "Router Deployment")
}

// assertDataplaneSecurityContext는 PG StatefulSet과 Router Deployment 모두에서
// 동일한 검증을 수행한다. PodSecurityContext + Container.SecurityContext +
// emptyDir mount 3개(/tmp, /run, /var/run/postgresql) 모두 존재해야 한다.
func assertDataplaneSecurityContext(t *testing.T, podSpec *corev1.PodSpec, label string) {
	t.Helper()

	// 1. PodSecurityContext 적용
	if podSpec.SecurityContext == nil {
		t.Fatalf("[%s] PodSpec.SecurityContext is nil — ADR 0006 위반", label)
	}
	if podSpec.SecurityContext.RunAsNonRoot == nil || !*podSpec.SecurityContext.RunAsNonRoot {
		t.Errorf("[%s] PodSecurityContext.RunAsNonRoot: want true", label)
	}

	// 2. Container.SecurityContext 적용 (첫 컨테이너)
	if len(podSpec.Containers) < 1 {
		t.Fatalf("[%s] PodSpec.Containers empty", label)
	}
	cnt := podSpec.Containers[0]
	if cnt.SecurityContext == nil {
		t.Fatalf("[%s] Container.SecurityContext is nil — ADR 0006 위반", label)
	}
	if cnt.SecurityContext.ReadOnlyRootFilesystem == nil || !*cnt.SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("[%s] Container.ReadOnlyRootFilesystem: want true", label)
	}

	// 3. emptyDir mount 3개 (readOnlyRootFilesystem 동반)
	wantMounts := map[string]string{
		"ephemeral-tmp":    "/tmp",
		"ephemeral-run":    "/run",
		"ephemeral-pg-run": "/var/run/postgresql",
	}
	mountsByName := make(map[string]string, len(cnt.VolumeMounts))
	for _, vm := range cnt.VolumeMounts {
		mountsByName[vm.Name] = vm.MountPath
	}
	for name, want := range wantMounts {
		if got, ok := mountsByName[name]; !ok {
			t.Errorf("[%s] VolumeMount %q missing — readOnlyRootFs 동반 emptyDir 부재", label, name)
		} else if got != want {
			t.Errorf("[%s] VolumeMount %q: want path %q, got %q", label, name, want, got)
		}
	}

	// 4. 짝이 되는 emptyDir Volume 정의 존재
	volsByName := make(map[string]*corev1.EmptyDirVolumeSource, len(podSpec.Volumes))
	for i := range podSpec.Volumes {
		v := &podSpec.Volumes[i]
		if v.EmptyDir != nil {
			volsByName[v.Name] = v.EmptyDir
		}
	}
	for name := range wantMounts {
		if _, ok := volsByName[name]; !ok {
			t.Errorf("[%s] Volume %q (emptyDir) missing", label, name)
		}
	}
}
