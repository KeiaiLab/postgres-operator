/*
Copyright 2026 Keiailab.
*/

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNS   = "ns"
	size10Gi = "10Gi"
	size20Gi = "20Gi"
)

func resizeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	if err := storagev1.AddToScheme(s); err != nil {
		t.Fatalf("storagev1: %v", err)
	}
	return s
}

func boundPVCFor(name, scName string) *corev1.PersistentVolumeClaim {
	q := resource.MustParse(size10Gi)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &scName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: q},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
}

func scAllow(name string, allow bool) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: name},
		Provisioner:          "test",
		AllowVolumeExpansion: &allow,
	}
}

func ctxBg() context.Context { return context.Background() }

func sizeOf(t *testing.T, c client.Client, name string) string {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	_ = c.Get(ctxBg(), types.NamespacedName{Namespace: testNS, Name: name}, pvc)
	q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	return q.String()
}

func TestExpandDataPVCs_grows_when_SC_allows(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(resizeScheme(t)).WithObjects(
		scAllow("gp3", true),
		boundPVCFor("data-pg-shard-0-0", "gp3"),
		boundPVCFor("data-pg-shard-0-1", "gp3"),
	).Build()

	if err := expandDataPVCs(ctxBg(), c, testNS,
		[]string{"pg-shard-0"}, resource.MustParse(size20Gi)); err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, n := range []string{"data-pg-shard-0-0", "data-pg-shard-0-1"} {
		if got := sizeOf(t, c, n); got != size20Gi {
			t.Errorf("%s: %s want %s", n, got, size20Gi)
		}
	}
}

func TestExpandDataPVCs_skips_non_matching_PVCs(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(resizeScheme(t)).WithObjects(
		scAllow("gp3", true),
		boundPVCFor("data-pg-shard-0-0", "gp3"),
		boundPVCFor("data-other-cluster-0", "gp3"), // 다른 cluster
	).Build()
	if err := expandDataPVCs(ctxBg(), c, testNS,
		[]string{"pg-shard-0"}, resource.MustParse(size20Gi)); err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := sizeOf(t, c, "data-pg-shard-0-0"); got != size20Gi {
		t.Errorf("expected %s, got %s", size20Gi, got)
	}
	if got := sizeOf(t, c, "data-other-cluster-0"); got != size10Gi {
		t.Errorf("other cluster PVC 변경되면 안됨: got %s", got)
	}
}

func TestExpandDataPVCs_no_expansion_when_SC_disallows(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(resizeScheme(t)).WithObjects(
		scAllow("standard", false),
		boundPVCFor("data-pg-shard-0-0", "standard"),
	).Build()
	_ = expandDataPVCs(ctxBg(), c, testNS,
		[]string{"pg-shard-0"}, resource.MustParse(size20Gi))
	if got := sizeOf(t, c, "data-pg-shard-0-0"); got != size10Gi {
		t.Errorf("disallow expansion → unchanged, got %s", got)
	}
}

func TestExpandDataPVCs_zero_desired_noop(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(resizeScheme(t)).WithObjects(
		scAllow("gp3", true),
		boundPVCFor("data-pg-shard-0-0", "gp3"),
	).Build()
	_ = expandDataPVCs(ctxBg(), c, testNS,
		[]string{"pg-shard-0"}, resource.Quantity{})
	if got := sizeOf(t, c, "data-pg-shard-0-0"); got != size10Gi {
		t.Errorf("zero desired → unchanged, got %s", got)
	}
}

func TestExpandDataPVCs_multi_shard(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(resizeScheme(t)).WithObjects(
		scAllow("gp3", true),
		boundPVCFor("data-pg-shard-0-0", "gp3"),
		boundPVCFor("data-pg-shard-1-0", "gp3"),
		boundPVCFor("data-pg-shard-2-0", "gp3"),
	).Build()
	_ = expandDataPVCs(ctxBg(), c, testNS,
		[]string{"pg-shard-0", "pg-shard-1", "pg-shard-2"},
		resource.MustParse(size20Gi))
	for _, n := range []string{"data-pg-shard-0-0", "data-pg-shard-1-0", "data-pg-shard-2-0"} {
		if got := sizeOf(t, c, n); got != size20Gi {
			t.Errorf("%s: %s want %s (multi-shard)", n, got, size20Gi)
		}
	}
}
