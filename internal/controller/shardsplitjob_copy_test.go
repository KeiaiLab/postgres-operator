/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ShardSplitJob InitialCopy phase 의 *복사 Job 결선* 을 envtest 로 검증한다:
// reconcileInitialCopy 가 각 target 의 복사 Job 을 올바른 DSN/vindex/ranges env 로 생성하고,
// Job 완료를 집계해 done 을 보고하며, Job 실패를 failure 로 보고하는지.

var _ = Describe("ShardSplitJob InitialCopy 복사 Job 결선", func() {
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

	It("각 target 의 복사 Job 을 올바른 env 로 생성하고 완료를 집계한다", func() {
		clusterName := fmt.Sprintf("rsdcopy-%d", GinkgoRandomSeed())
		keyspace := "default"

		// vindex 출처가 되는 ShardRange.
		sr := &postgresv1alpha1.ShardRange{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: ns},
			Spec: postgresv1alpha1.ShardRangeSpec{
				Cluster:         clusterName,
				Keyspace:        keyspace,
				Vindex:          postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
				ReferenceTables: []string{"country"},
				Ranges: []postgresv1alpha1.ShardRangeEntry{
					{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())

		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-ssj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster:  clusterName,
				Keyspace: keyspace,
				Sources:  []string{"shard-0"},
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "t0"}}},
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x80000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed()) // UID 채우기.

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		// 1) 첫 호출: Job 2개 생성, 아직 미완료(done=false).
		done, failure, err := r.reconcileInitialCopy(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())

		// 2) 각 target Job 이 올바른 env 로 생성됐는지.
		for _, shardID := range []string{"t0", "t1"} {
			var job batchv1.Job
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, shardID, false)}, &job)).
				To(Succeed(), "target %s 복사 Job 이 생성돼야 함", shardID)
			c := job.Spec.Template.Spec.Containers[0]
			Expect(envOf(c, "PGROUTER_RESHARD_TARGET_SHARD")).To(Equal(shardID))
			Expect(envOf(c, "PGROUTER_VINDEX_COLUMN")).To(Equal("id"))
			Expect(envOf(c, "PGROUTER_VINDEX_FUNCTION")).To(Equal("murmur3"))
			Expect(envOf(c, "PGROUTER_REFERENCE_TABLES")).To(Equal("country"))
			Expect(envOf(c, "PGROUTER_RANGES")).To(And(ContainSubstring("t0:0x00000000:0x7fffffff"), ContainSubstring("t1:0x80000000:0xffffffff")))
			Expect(envOf(c, "PGROUTER_SOURCE_DSN")).To(ContainSubstring(ShardServiceName(clusterName, 0)))
			Expect(envOf(c, "PGROUTER_TARGET_DSN")).To(ContainSubstring(TargetShardServiceName(clusterName, shardID)))
			Expect(envOf(c, "PGROUTER_SOURCE_DSN")).To(ContainSubstring("user=postgres")) // trust, no password
			Expect(strings.Contains(envOf(c, "PGROUTER_SOURCE_DSN"), "password=")).To(BeFalse())
			Expect(job.OwnerReferences).To(HaveLen(1))
			Expect(job.OwnerReferences[0].Name).To(Equal(ssj.Name))
		}

		// 3) 멱등성: 재호출이 중복 생성 없이 여전히 done=false.
		done, _, err = r.reconcileInitialCopy(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())

		// 4) 두 Job 을 성공으로 표기 → done=true.
		for _, shardID := range []string{"t0", "t1"} {
			var job batchv1.Job
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, shardID, false)}, &job)).To(Succeed())
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())
		}
		done, failure, err = r.reconcileInitialCopy(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeTrue())
	})

	It("복사 Job 이 실패하면 failure 를 보고한다", func() {
		clusterName := fmt.Sprintf("rsdfail-%d", GinkgoRandomSeed())
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
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t0"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, _, err := r.reconcileInitialCopy(ctx, ssj) // Job 생성.
		Expect(err).NotTo(HaveOccurred())

		var job batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t0", false)}, &job)).To(Succeed())
		now := metav1.Now()
		job.Status.StartTime = &now // apiserver: finished job 은 startTime 필수.
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}, // 1.36: Failed 선행 필수.
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
		}
		Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())

		_, failure, err := r.reconcileInitialCopy(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(ContainSubstring("failed"))
	})

	It("Cleanup 이 각 target 의 삭제(delete-only) Job 을 source 대상으로 생성한다", func() {
		clusterName := fmt.Sprintf("rsdclean-%d", GinkgoRandomSeed())
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
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x80000000", Hi: "0xffffffff", Shard: "t1"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ssj), ssj)).To(Succeed())

		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		done, failure, err := r.reconcileCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(failure).To(BeEmpty())
		Expect(done).To(BeFalse())

		var job batchv1.Job
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: reshardJobName(clusterName, "t1", true)}, &job)).
			To(Succeed(), "delete Job 이 생성돼야 함")
		c := job.Spec.Template.Spec.Containers[0]
		Expect(envOf(c, "PGROUTER_RESHARD_DELETE_ONLY")).To(Equal("1"))
		Expect(envOf(c, "PGROUTER_RESHARD_TARGET_SHARD")).To(Equal("t1"))
		Expect(envOf(c, "PGROUTER_SOURCE_DSN")).To(ContainSubstring(ShardServiceName(clusterName, 0)))
		Expect(envOf(c, "PGROUTER_TARGET_DSN")).To(BeEmpty()) // 삭제는 source 만.

		job.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())
		done, _, err = r.reconcileCleanup(ctx, ssj)
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeTrue())
	})
})
