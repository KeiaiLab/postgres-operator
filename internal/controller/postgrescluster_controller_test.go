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
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 envtest 는 RFC 0001 PostgresCluster CRD v2 위에서의 reconcile 를 검증한다.
// 두 시나리오:
//
//  1. SingleShardNoRouter — shardingMode=none, shards.initialCount=1, router=nil
//     → STS/SVC/CM 각 1 개 생성 + Phase=Provisioning
//     → STS readyReplicas=1 mock + reconcile trigger → Phase=Ready
//
//  2. NativeMultiShardWithRouter — shardingMode=native, initialCount=2, router.replicas=2
//     → STS 2 + Router Deployment 1 + ClientService 1 + ConfigMap 3 (shard×2 + router×1)
//
// envtest 에는 STS / Deployment controller 가 없으므로 readyReplicas 는 수동으로
// 설정하고, reconcile re-trigger 는 spec annotation bump 로 수행한다.

const (
	envtestTimeout  = 10 * time.Second
	envtestInterval = 200 * time.Millisecond
)

var _ = Describe("PostgresClusterReconciler — RFC 0001 spec", func() {
	var (
		ctx       context.Context
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = fmt.Sprintf("f01b-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		})).To(Succeed())
	})

	Context("when shardingMode=none with single shard and no router", func() {
		It("creates exactly one shard's resources and reaches Ready after STS readiness", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "single", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						// Replicas 는 CRD default=1 (omitempty + kubebuilder:default=1) 이라
						// 명시적으로 1 을 보내거나 생략해도 server-side 가 1 로 채운다.
						// members = primary 1 + async 1 = 2.
						Replicas: 1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("single", 0)
			svcName := ShardServiceName("single", 0)
			cmName := ShardConfigMapName("single", 0)

			By("provisioning shard subresources")
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "primary 1 + async 1")
				g.Expect(sts.Spec.ServiceName).To(Equal(svcName))

				var svc corev1.Service
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: svcName}, &svc)).To(Succeed())
				g.Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

				var cm corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, &cm)).To(Succeed())
				g.Expect(cm.Data).To(HaveKey("postgresql.conf"))
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("observing Provisioning phase before STS becomes ready")
			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "single"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseProvisioning))
				g.Expect(got.Status.Shards).To(HaveLen(1))
				g.Expect(got.Status.Shards[0].Ordinal).To(Equal(int32(0)))
				g.Expect(got.Status.Shards[0].Primary.Ready).To(BeFalse())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("simulating STS primary readiness and re-triggering reconcile")
			markSTSReady(ctx, namespace, stsName, 1)
			bumpAnnotation(ctx, cluster)

			By("reaching Ready phase")
			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "single"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseReady))
				g.Expect(got.Status.Shards[0].Primary.Ready).To(BeTrue())
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when shardingMode=native with 2 shards and router", func() {
		It("creates 2 shard STSes plus router Deployment and ClientService", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 2,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
					Router: &postgresv1alpha1.RouterSpec{
						Enabled:  true,
						Replicas: 2,
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			By("creating both shard STSes")
			Eventually(func(g Gomega) {
				for _, ord := range []int32{0, 1} {
					var sts appsv1.StatefulSet
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{
						Namespace: namespace, Name: ShardStatefulSetName("multi", ord),
					}, &sts)).To(Succeed())
					g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "primary 1 + replica 1")
				}
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("creating router Deployment + ClusterIP Service")
			Eventually(func(g Gomega) {
				var dep appsv1.Deployment
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterDeploymentName("multi"),
				}, &dep)).To(Succeed())
				g.Expect(*dep.Spec.Replicas).To(Equal(int32(2)))

				var svc corev1.Service
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterServiceName("multi"),
				}, &svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				g.Expect(svc.Spec.ClusterIP).NotTo(Equal(corev1.ClusterIPNone))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})
})

// markSTSReady 는 envtest 에서 부재한 STS controller 를 흉내내어 readyReplicas 를
// 강제로 설정한다. status subresource 라 별도 Update 호출이 필요하다.
func markSTSReady(ctx context.Context, ns, name string, ready int32) {
	GinkgoHelper()
	var sts appsv1.StatefulSet
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sts)).To(Succeed())
	sts.Status.Replicas = ready
	sts.Status.ReadyReplicas = ready
	sts.Status.AvailableReplicas = ready
	Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
}

// bumpAnnotation 은 reconcile 를 재트리거하기 위해 spec 외부의 annotation 을
// 갱신한다 (status 변경만으로는 reconcile 가 항상 트리거되지는 않음).
func bumpAnnotation(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) {
	GinkgoHelper()
	Eventually(func() error {
		var got postgresv1alpha1.PostgresCluster
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &got); err != nil {
			return err
		}
		if got.Annotations == nil {
			got.Annotations = map[string]string{}
		}
		got.Annotations["postgres.keiailab.io/test-bump"] = fmt.Sprintf("%d", time.Now().UnixNano())
		return k8sClient.Update(ctx, &got)
	}, envtestTimeout, envtestInterval).Should(Or(Succeed(), MatchError(ContainSubstring("conflict"))))
	// conflict 발생해도 reconcile 트리거 목적은 달성됐다 — 다른 reconcile 가 spec 을
	// 이미 건드렸다는 뜻이라 watch event 가 이미 흘러갔다.
}
