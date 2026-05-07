/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"
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
		"",
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
		"",
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
		"POSTGRES_MEMBER_COUNT":  "1",
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
	for _, name := range []string{"POD_NAME", "POD_NAMESPACE", "POD_UID"} {
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

func TestBuildPGStatefulSet_HasBootstrapAndServiceAccount(t *testing.T) {
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
		"",
	)
	pod := &sts.Spec.Template.Spec
	if pod.ServiceAccountName != "demo-instance" {
		t.Errorf("ServiceAccountName = %q, want demo-instance", pod.ServiceAccountName)
	}
	if got := len(pod.InitContainers); got != 1 {
		t.Fatalf("init containers = %d, want 1", got)
	}
	init := pod.InitContainers[0]
	if init.Name != bootstrapContainerName {
		t.Errorf("init container name = %q, want bootstrap", init.Name)
	}
	if len(init.Command) == 0 || init.Command[0] != "sh" {
		t.Errorf("init command should be sh -c, got %v", init.Command)
	}
}

func TestBuildBootstrapContainer_OrdinalZero_RunsInitdb(t *testing.T) {
	t.Parallel()

	c := buildBootstrapContainer("img:18", "18", 0, "", 2)
	if c.Name != bootstrapContainerName {
		t.Errorf("Name = %q, want bootstrap", c.Name)
	}
	if len(c.Args) != 1 {
		t.Fatalf("Args length = %d, want 1", len(c.Args))
	}
	script := c.Args[0]
	if !strings.Contains(script, "initdb") {
		t.Error("script must contain initdb")
	}
	// POD_ORDINAL 은 downward API 로 주입된 POD_NAME 의 마지막 `-` 뒤를 추출.
	// StatefulSet 명명 규약 (`<sts>-<ordinal>`) 에 의존.
	if !strings.Contains(script, `POD_ORDINAL="${POD_NAME##*-}"`) {
		t.Error(`script must contain POD_ORDINAL extraction: POD_ORDINAL="${POD_NAME##*-}"`)
	}
	if !strings.Contains(script, restartPrimaryAsStandbyMarker) {
		t.Errorf("script must contain restart marker %q", restartPrimaryAsStandbyMarker)
	}
	// 연산자 반전 가드: bash 분기 조건의 `||` 연산자가 `&&` 등으로 뒤집히면
	// ENV 기반 검증만으로는 잡히지 않으므로 리터럴 substring 으로 고정한다.
	// POD_ORDINAL 키잉 (SHARD_ORDINAL 아님) — 같은 shard 의 pod 별 분기 보장.
	const wantBranchOperator = `[ "$POD_ORDINAL" = "0" ] || [ -z "$PRIMARY_ENDPOINT" ]`
	if !strings.Contains(script, wantBranchOperator) {
		t.Errorf("script must contain branch operator %q (operator inversion guard)", wantBranchOperator)
	}
	envByName := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		envByName[e.Name] = e
	}
	if got := envByName["SHARD_ORDINAL"].Value; got != "0" {
		t.Errorf("SHARD_ORDINAL env = %q, want 0", got)
	}
	if got := envByName["PRIMARY_ENDPOINT"].Value; got != "" {
		t.Errorf("PRIMARY_ENDPOINT env = %q, want empty", got)
	}
	if got := envByName["POSTGRES_MEMBER_COUNT"].Value; got != "2" {
		t.Errorf("POSTGRES_MEMBER_COUNT env = %q, want 2", got)
	}
	// POD_NAME 은 downward API (metadata.name) — Value 는 비어 있고 ValueFrom 만 설정.
	pn, ok := envByName["POD_NAME"]
	if !ok {
		t.Fatal("POD_NAME env missing — downward API 미주입")
	}
	if pn.ValueFrom == nil || pn.ValueFrom.FieldRef == nil ||
		pn.ValueFrom.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("POD_NAME must use ValueFrom.FieldRef{FieldPath: \"metadata.name\"}, got %+v", pn.ValueFrom)
	}
}

// NOTE: 본 테스트는 buildBootstrapContainer 를 shardOrdinal=1 로 호출하지만,
// 실제 런타임 분기는 *POD_ORDINAL* (downward API 로 Pod 마다 다른 값) 으로
// 결정된다. 단위 테스트는 downward API 를 시뮬레이트할 수 없으므로 *script
// wiring + env injection* 만 검증하고, 분기 결과는 e2e 에서 검증한다.
func TestBuildBootstrapContainer_NonZero_RunsBasebackup(t *testing.T) {
	t.Parallel()

	c := buildBootstrapContainer("img:18", "18", 1, "primary.svc:5432", 2)
	if c.Name != bootstrapContainerName {
		t.Errorf("Name = %q, want bootstrap", c.Name)
	}
	script := c.Args[0]
	for _, want := range []string{"pg_basebackup", "standby.signal", "primary_conninfo", "postgresql.auto.conf"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if !strings.Contains(script, `POD_ORDINAL="${POD_NAME##*-}"`) {
		t.Error(`script must contain POD_ORDINAL extraction: POD_ORDINAL="${POD_NAME##*-}"`)
	}
	const wantBranchOperator = `[ "$POD_ORDINAL" = "0" ] || [ -z "$PRIMARY_ENDPOINT" ]`
	if !strings.Contains(script, wantBranchOperator) {
		t.Errorf("script must contain branch operator %q (operator inversion guard)", wantBranchOperator)
	}
	envByName := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		envByName[e.Name] = e
	}
	if got := envByName["SHARD_ORDINAL"].Value; got != "1" {
		t.Errorf("SHARD_ORDINAL env = %q, want 1", got)
	}
	if got := envByName["PRIMARY_ENDPOINT"].Value; got != "primary.svc:5432" {
		t.Errorf("PRIMARY_ENDPOINT env = %q, want primary.svc:5432", got)
	}
	if got := envByName["POSTGRES_MEMBER_COUNT"].Value; got != "2" {
		t.Errorf("POSTGRES_MEMBER_COUNT env = %q, want 2", got)
	}
	pn, ok := envByName["POD_NAME"]
	if !ok {
		t.Fatal("POD_NAME env missing — downward API 미주입")
	}
	if pn.ValueFrom == nil || pn.ValueFrom.FieldRef == nil ||
		pn.ValueFrom.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("POD_NAME must use ValueFrom.FieldRef{FieldPath: \"metadata.name\"}, got %+v", pn.ValueFrom)
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

// TestBuildPGStatefulSet_ReadinessProbe_FastInitialDelay 는 readinessProbe 가
// 빠른 Pod Ready 전환을 위해 initialDelaySeconds=5 + periodSeconds=3 으로
// 설정되어 있는지 검증한다. instance manager 의 waitSupReady 가 postgres unix
// socket race 를 코드 레벨에서 처리하므로 probe 가 race 회피를 중복할 필요 없다.
// LivenessProbe 는 보수적 (initialDelaySeconds=60, PeriodSeconds=30) 유지.
func TestBuildPGStatefulSet_ReadinessProbe_FastInitialDelay(t *testing.T) {
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
		"",
	)
	if got := len(sts.Spec.Template.Spec.Containers); got != 1 {
		t.Fatalf("containers count = %d, want 1", got)
	}
	rp := sts.Spec.Template.Spec.Containers[0].ReadinessProbe
	if rp == nil {
		t.Fatal("ReadinessProbe is nil")
	}
	if got := rp.InitialDelaySeconds; got != 5 {
		t.Errorf("ReadinessProbe.InitialDelaySeconds = %d, want 5", got)
	}
	if got := rp.PeriodSeconds; got != 3 {
		t.Errorf("ReadinessProbe.PeriodSeconds = %d, want 3", got)
	}
	// LivenessProbe 는 보수적 유지 — false positive 가 Pod restart 유발하므로.
	lp := sts.Spec.Template.Spec.Containers[0].LivenessProbe
	if lp == nil {
		t.Fatal("LivenessProbe is nil")
	}
	if got := lp.InitialDelaySeconds; got != 60 {
		t.Errorf("LivenessProbe.InitialDelaySeconds = %d, want 60 (보수적 유지)", got)
	}
	if got := lp.PeriodSeconds; got != 30 {
		t.Errorf("LivenessProbe.PeriodSeconds = %d, want 30 (보수적 유지)", got)
	}
}

// TestBuildPGStatefulSet_DefaultResources_BurstableQoS 는 사용자 spec.shards.resources
// 가 비어있을 때 기본 requests (CPU 100m, Memory 256Mi) 가 적용되어 Burstable QoS
// 가 보장되는지 검증한다. BestEffort 는 kube-scheduler eviction 1순위 — production 위험.
func TestBuildPGStatefulSet_DefaultResources_BurstableQoS(t *testing.T) {
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
		corev1.ResourceRequirements{}, // empty — default 적용 기대
		"",
	)
	if got := len(sts.Spec.Template.Spec.Containers); got != 1 {
		t.Fatalf("containers count = %d, want 1", got)
	}
	res := sts.Spec.Template.Spec.Containers[0].Resources
	cpu, ok := res.Requests[corev1.ResourceCPU]
	if !ok {
		t.Fatal("Resources.Requests[CPU] missing — Burstable QoS 보장 실패")
	}
	if want := resource.MustParse("100m"); cpu.Cmp(want) != 0 {
		t.Errorf("Resources.Requests[CPU] = %s, want 100m", cpu.String())
	}
	mem, ok := res.Requests[corev1.ResourceMemory]
	if !ok {
		t.Fatal("Resources.Requests[Memory] missing — Burstable QoS 보장 실패")
	}
	if want := resource.MustParse("256Mi"); mem.Cmp(want) != 0 {
		t.Errorf("Resources.Requests[Memory] = %s, want 256Mi", mem.String())
	}
	// Limits 는 미설정 (Burstable). 사용자가 명시 시만 limit 적용.
	if len(res.Limits) != 0 {
		t.Errorf("Resources.Limits = %v, want empty (Burstable)", res.Limits)
	}

	// 사용자 명시 시 default override 안 되는지 확인.
	customRes := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		},
	}
	sts2 := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		customRes,
		"",
	)
	gotCPU := sts2.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if want := resource.MustParse("500m"); gotCPU.Cmp(want) != 0 {
		t.Errorf("user-specified resources overridden: CPU = %s, want 500m", gotCPU.String())
	}
}
