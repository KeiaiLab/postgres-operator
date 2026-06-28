/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ShardSplitJob Cutover write-block 을 envtest 로 검증한다: Cutover phase 가
// ShardRange 에 write-block 을 켜고(라우터가 쓰기 거부), RoutingUpdate 가 ranges 를 flip 하며
// write-block 을 끄는지(쓰기 재개).

var _ = Describe("ShardSplitJob Cutover write-block", func() {
	ctx := context.Background()
	const ns = "default"

	envOf := func(c corev1.Container, key string) string {
		for _, e := range c.Env {
			if e.Name == key {
				return e.Value
			}
		}
		return ""
	}

	It("Cutover 가 write-block 을 켜고 RoutingUpdate 가 ranges flip 과 함께 끈다", func() {
		clusterName := fmt.Sprintf("rsdwb-%d", GinkgoRandomSeed())
		keyspace := "default"

		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Vindex:   postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())

		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"},
				AllowForwardOnly: false,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "t0"}}},
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x80000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseCutover
		Expect(k8sClient.Status().Update(ctx, ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ssj)}

		// Reconcile 1: Cutover → write-block ON, phase→RoutingUpdate.
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got postgresv1alpha1.ShardRange
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeTrue(), "Cutover 가 write-block 을 켜야 함")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		Expect(ssj.Status.Phase).To(Equal(postgresv1alpha1.ShardSplitPhaseRoutingUpdate))

		// Reconcile 2: RoutingUpdate → ranges flip + write-block OFF, phase→Cleanup.
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeFalse(), "RoutingUpdate 가 write-block 을 꺼야 함")
		Expect(got.Spec.Ranges).To(HaveLen(2)) // t0/t1 로 flip.
		shards := []string{got.Spec.Ranges[0].Shard, got.Spec.Ranges[1].Shard}
		Expect(shards).To(ConsistOf("t0", "t1"))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		Expect(ssj.Status.Phase).To(Equal(postgresv1alpha1.ShardSplitPhaseCleanup))
	})

	It("online 모드 CDCCatchup: cdc-setup Job → write-block → cdc-finalize Job 순서", func() {
		clusterName := fmt.Sprintf("rsdcdc-%d", GinkgoRandomSeed())
		keyspace := "default"
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex: postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"}, Online: true,
				CDCMaxLag: 16 << 20,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		// 1) reconcileCDC: cdc-setup Job 생성, 미완료(write-block 아직 안 켜짐).
		done, failure, err := r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		var setup batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-setup")}, &setup)).To(Succeed())
		Expect(envOf(setup.Spec.Template.Spec.Containers[0], "PGROUTER_RESHARD_MODE")).To(Equal("cdc-setup"))
		Expect(envOf(setup.Spec.Template.Spec.Containers[0], "PGROUTER_CDC_MAX_LAG")).To(Equal("16777216"))
		var got postgresv1alpha1.ShardRange
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeFalse(), "cdc-setup 완료 전엔 write-block 미설정")

		// 2) cdc-setup 성공 → reconcileCDC 가 write-block 켜고 cdc-finalize Job 생성.
		setup.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, &setup)).To(Succeed())
		done, _, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeTrue(), "cdc-setup 후 write-block 켜짐")
		var fin batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-finalize")}, &fin)).To(Succeed())
		Expect(envOf(fin.Spec.Template.Spec.Containers[0], "PGROUTER_RESHARD_MODE")).To(Equal("cdc-finalize"))

		// 3) cdc-finalize 성공 → done.
		fin.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, &fin)).To(Succeed())
		done, _, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeTrue())
	})

	It("forward-only cutover 는 write-block 을 켜지 않는다(비가역 거부)", func() {
		clusterName := fmt.Sprintf("rsdwbfo-%d", GinkgoRandomSeed())
		keyspace := "default"
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex: postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"},
				AllowForwardOnly: true,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t0"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseCutover
		Expect(k8sClient.Status().Update(ctx, ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ssj)})
		Expect(err).NotTo(HaveOccurred())

		var got postgresv1alpha1.ShardRange
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeFalse(), "forward-only 는 write-block 미설정")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		Expect(ssj.Status.Phase).To(Equal(postgresv1alpha1.ShardSplitPhaseFailed))
	})
})
