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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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
	failJob := func(job *batchv1.Job) {
		now := metav1.Now()
		job.Status.StartTime = &now
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
		}
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

	It("Promote phase 가 target STS 와 live Pod 에 shard-id 를 adopt label 로 붙인다", func() {
		clusterName := fmt.Sprintf("rsdprom-%d", GinkgoRandomSeed())
		keyspace := "default"
		targetShard := "t1"
		Expect(k8sClient.Create(ctx, &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Vindex:   postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: targetShard}},
			},
		})).To(Succeed())
		cluster := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: ns},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				Shards: postgresv1alpha1.ShardsSpec{
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				},
			},
		}
		sts := buildTargetShardStatefulSet(
			cluster, targetShard, "postgres:18", "18",
			postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")}, corev1.ResourceRequirements{},
			TargetShardConfigMapName(clusterName, targetShard), "cfg",
		)
		Expect(k8sClient.Create(ctx, sts)).To(Succeed())
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TargetShardStatefulSetName(clusterName, targetShard) + "-0",
				Namespace: ns,
				Labels:    ReshardTargetSelectorLabels(clusterName, targetShard),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: pgContainerName, Image: "postgres:18"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())

		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Sources:  []string{"shard-0"},
				Targets: []postgresv1alpha1.ShardSplitTarget{{
					ShardID: targetShard,
					Ranges:  []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: targetShard}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhasePromote
		Expect(k8sClient.Status().Update(ctx, ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ssj)})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(ctrl.Result{}))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		Expect(ssj.Status.Phase).To(Equal(postgresv1alpha1.ShardSplitPhaseCompleted))

		var gotSTS appsv1.StatefulSet
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), &gotSTS)).To(Succeed())
		Expect(gotSTS.Labels[ReshardTargetLabelKey]).To(Equal(targetShard))
		Expect(gotSTS.Labels[ShardIDLabelKey]).To(Equal(targetShard))
		Expect(gotSTS.Spec.Template.Labels[ReshardTargetLabelKey]).To(Equal(targetShard))
		Expect(gotSTS.Spec.Template.Labels[ShardIDLabelKey]).To(Equal(targetShard))
		Expect(gotSTS.Spec.Selector.MatchLabels).NotTo(HaveKey(ShardIDLabelKey))

		var gotPod corev1.Pod
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &gotPod)).To(Succeed())
		Expect(gotPod.Labels[ReshardTargetLabelKey]).To(Equal(targetShard))
		Expect(gotPod.Labels[ShardIDLabelKey]).To(Equal(targetShard))
	})

	It("Promote phase 가 ShardRange source active 중에는 target adopt 를 보류한다", func() {
		clusterName := fmt.Sprintf("rsdpromgate-%d", GinkgoRandomSeed())
		keyspace := "default"
		targetShard := "t1"
		Expect(k8sClient.Create(ctx, &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Vindex:   postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		})).To(Succeed())

		cluster := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: ns},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				Shards: postgresv1alpha1.ShardsSpec{
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				},
			},
		}
		sts := buildTargetShardStatefulSet(
			cluster, targetShard, "postgres:18", "18",
			postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")}, corev1.ResourceRequirements{},
			TargetShardConfigMapName(clusterName, targetShard), "cfg",
		)
		Expect(k8sClient.Create(ctx, sts)).To(Succeed())
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TargetShardStatefulSetName(clusterName, targetShard) + "-0",
				Namespace: ns,
				Labels:    ReshardTargetSelectorLabels(clusterName, targetShard),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: pgContainerName, Image: "postgres:18"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())

		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Sources:  []string{"shard-0"},
				Targets: []postgresv1alpha1.ShardSplitTarget{{
					ShardID: targetShard,
					Ranges:  []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: targetShard}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhasePromote
		Expect(k8sClient.Status().Update(ctx, ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ssj)})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).NotTo(BeZero())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		Expect(ssj.Status.Phase).To(Equal(postgresv1alpha1.ShardSplitPhasePromote))

		var gotSTS appsv1.StatefulSet
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), &gotSTS)).To(Succeed())
		Expect(gotSTS.Labels).NotTo(HaveKey(ShardIDLabelKey))
		Expect(gotSTS.Spec.Template.Labels).NotTo(HaveKey(ShardIDLabelKey))

		var gotPod corev1.Pod
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &gotPod)).To(Succeed())
		Expect(gotPod.Labels).NotTo(HaveKey(ShardIDLabelKey))
	})

	It("online 모드 CDCCatchup: cdc-setup Job → write-block → cdc-finalize Job 순서", func() {
		clusterName := fmt.Sprintf("rsdcdc-%d", GinkgoRandomSeed())
		keyspace := "default"
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex: postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeRange, Column: "id"},
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
		Expect(envOf(setup.Spec.Template.Spec.Containers[0], "PGROUTER_VINDEX_TYPE")).To(Equal("range"))
		Expect(envOf(setup.Spec.Template.Spec.Containers[0], "PGROUTER_VINDEX_COLUMN")).To(Equal("id"))
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

	It("online 모드 CDC Job 실패를 phase별 failure 로 보고한다", func() {
		clusterName := fmt.Sprintf("rsdcdcfail-%d", GinkgoRandomSeed())
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
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		done, failure, err := r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		var setup batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-setup")}, &setup)).To(Succeed())
		failJob(&setup)
		Expect(k8sClient.Status().Update(ctx, &setup)).To(Succeed())
		done, failure, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())
		Expect(failure).To(ContainSubstring("cdc-setup"))

		clusterName = fmt.Sprintf("rsdcdcfinfail-%d", GinkgoRandomSeed())
		sr = &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex: postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		ssj = &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"}, Online: true,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())

		done, failure, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-setup")}, &setup)).To(Succeed())
		setup.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, &setup)).To(Succeed())
		done, failure, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		var fin batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-finalize")}, &fin)).To(Succeed())
		failJob(&fin)
		Expect(k8sClient.Status().Update(ctx, &fin)).To(Succeed())
		done, failure, err = r.reconcileCDC(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())
		Expect(failure).To(ContainSubstring("cdc-finalize"))
	})

	It("online abort cleanup: cdc-abort Job 성공 후 write-block 을 해제하고 멱등이다", func() {
		clusterName := fmt.Sprintf("rsdabort-%d", GinkgoRandomSeed())
		keyspace := "default"
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex:       postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				WriteBlocked: true,
				Ranges:       []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"}, Online: true,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		done, failure, err := r.reconcileModeJobs(ctx, ssj, "cdc-setup")
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())

		done, failure, err = r.reconcileAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		var abort batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-abort")}, &abort)).To(Succeed())
		Expect(envOf(abort.Spec.Template.Spec.Containers[0], "PGROUTER_RESHARD_MODE")).To(Equal("cdc-abort"))
		var got postgresv1alpha1.ShardRange
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeTrue(), "abort cleanup Job 완료 전에는 write-block 을 유지한다")

		abort.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, &abort)).To(Succeed())
		done, failure, err = r.reconcileAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeTrue())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeFalse())

		done, failure, err = r.reconcileAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeTrue())
	})

	It("online abort cleanup: cdc-abort Job 실패를 manual cleanup 필요 상태로 보고한다", func() {
		clusterName := fmt.Sprintf("rsdabortfail-%d", GinkgoRandomSeed())
		keyspace := "default"
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster: clusterName, Keyspace: keyspace,
				Vindex:       postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				WriteBlocked: true,
				Ranges:       []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster: clusterName, Keyspace: keyspace, Sources: []string{"shard-0"}, Online: true,
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		done, failure, err := r.reconcileModeJobs(ctx, ssj, "cdc-setup")
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		done, failure, err = r.reconcileAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())
		var abort batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", "cdc-abort")}, &abort)).To(Succeed())
		failJob(&abort)
		Expect(k8sClient.Status().Update(ctx, &abort)).To(Succeed())

		done, failure, err = r.reconcileAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())
		Expect(failure).To(ContainSubstring("cdc-abort"))
		var got postgresv1alpha1.ShardRange
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), &got)).To(Succeed())
		Expect(got.Spec.WriteBlocked).To(BeTrue(), "cleanup 실패 시 write-block 해제 여부를 성공으로 오인하면 안 된다")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
		Expect(k8sClient.Status().Update(ctx, ssj)).To(Succeed())
		_, err = r.reconcileTerminalAbortCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())
		cond := apimeta.FindStatusCondition(ssj.Status.Conditions, shardSplitConditionAbortCleanup)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(shardSplitReasonCleanupFailed))
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
