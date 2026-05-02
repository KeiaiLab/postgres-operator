/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 envtest 는 ADR 0008 (finalizer-avoidance-policy, archived as v0.x) 의 회귀
// 차단용이다. PostgresCluster 가 생성하는 모든 자식 자원 (StatefulSet, Service,
// ConfigMap, Deployment) 이 부모 PostgresCluster 를 가리키는 OwnerReference 를
// 부착하는지 검증한다.
//
// envtest 에는 K8s garbage collector 가 없으므로 실제 cascade 삭제 동작을 직접
// 관측할 수 없다 — 대신 OwnerReference 의 정확한 부착이 cascade GC 의 *유일한
// 전제 조건* 이므로 그 부착 자체를 검증한다 (Controller=true, BlockOwnerDeletion=
// true, UID 일치).

var _ = Describe("PostgresClusterReconciler — cascade delete (ADR 0008)", func() {
	It("attaches OwnerReference to every shard subresource for native GC", func() {
		ctx := context.Background()
		namespace := fmt.Sprintf("cascade-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		})).To(Succeed())

		cluster := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: namespace},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				ShardingMode:    postgresv1alpha1.ShardingModeNative,
				Shards: postgresv1alpha1.ShardsSpec{
					InitialCount: 1,
					Replicas:     0,
					Storage: postgresv1alpha1.StorageSpec{
						Size: resource.MustParse("1Gi"),
					},
				},
				Router: &postgresv1alpha1.RouterSpec{
					Enabled:  true,
					Replicas: 1,
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		By("waiting until all child resources are reconciled into existence")
		Eventually(func(g Gomega) {
			for _, name := range []string{
				ShardStatefulSetName("owner", 0),
				ShardServiceName("owner", 0),
				ShardConfigMapName("owner", 0),
				RouterDeploymentName("owner"),
				RouterServiceName("owner"),
				RouterConfigMapName("owner"),
			} {
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &corev1.ConfigMap{})
				if err == nil {
					continue
				}
			}
			// presence verified per-type below
			var got postgresv1alpha1.PostgresCluster
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "owner"}, &got)).To(Succeed())
			g.Expect(got.Status.Shards).To(HaveLen(1))
		}, envtestTimeout, envtestInterval).Should(Succeed())

		// owner UID 를 확보 (OwnerReference UID 일치 검증의 기준값).
		var owner postgresv1alpha1.PostgresCluster
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "owner"}, &owner)).To(Succeed())
		ownerUID := owner.UID

		By("verifying OwnerReference on shard StatefulSet")
		var sts appsv1.StatefulSet
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: ShardStatefulSetName("owner", 0),
			}, &sts)).To(Succeed())
			assertControllerOwnerRef(g, sts.OwnerReferences, ownerUID)
		}, envtestTimeout, envtestInterval).Should(Succeed())

		By("verifying OwnerReference on shard headless Service")
		var svc corev1.Service
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: ShardServiceName("owner", 0),
		}, &svc)).To(Succeed())
		assertControllerOwnerRef(Default, svc.OwnerReferences, ownerUID)

		By("verifying OwnerReference on shard ConfigMap")
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: ShardConfigMapName("owner", 0),
		}, &cm)).To(Succeed())
		assertControllerOwnerRef(Default, cm.OwnerReferences, ownerUID)

		By("verifying OwnerReference on router Deployment")
		var dep appsv1.Deployment
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: RouterDeploymentName("owner"),
			}, &dep)).To(Succeed())
			assertControllerOwnerRef(g, dep.OwnerReferences, ownerUID)
		}, envtestTimeout, envtestInterval).Should(Succeed())

		By("verifying OwnerReference on router Service")
		var rsvc corev1.Service
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: RouterServiceName("owner"),
		}, &rsvc)).To(Succeed())
		assertControllerOwnerRef(Default, rsvc.OwnerReferences, ownerUID)

		By("verifying OwnerReference on router ConfigMap")
		var rcm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: RouterConfigMapName("owner"),
		}, &rcm)).To(Succeed())
		assertControllerOwnerRef(Default, rcm.OwnerReferences, ownerUID)
	})
})

// assertControllerOwnerRef 는 OwnerReferences 에 PostgresCluster 를 가리키는
// controller=true + blockOwnerDeletion=true + UID 일치 항목이 정확히 1 개 있는지
// 검증한다 — K8s GC 의 cascade 삭제는 본 메타데이터를 단일 진실로 사용한다.
// (UID 만으로 owner 동일성이 결정되므로 Name 은 별도 비교하지 않는다.)
func assertControllerOwnerRef(g Gomega, refs []metav1.OwnerReference, ownerUID types.UID) {
	count := 0
	for _, r := range refs {
		if r.Kind != "PostgresCluster" {
			continue
		}
		count++
		g.Expect(r.UID).To(Equal(ownerUID))
		g.Expect(r.Controller).NotTo(BeNil())
		g.Expect(*r.Controller).To(BeTrue(), "Controller=true is required for cascade GC")
		g.Expect(r.BlockOwnerDeletion).NotTo(BeNil())
		g.Expect(*r.BlockOwnerDeletion).To(BeTrue())
	}
	g.Expect(count).To(Equal(1), "exactly one PostgresCluster owner reference expected")
}
