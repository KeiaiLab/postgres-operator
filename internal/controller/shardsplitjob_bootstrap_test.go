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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ShardSplitJob Bootstrap phase 의 *실 자원 생성* (ADR-0027 P2) 을
// envtest(실 apiserver) 로 검증한다. reconcileBootstrapTargets 가 각 target 에 대해
// 격리 식별의 ConfigMap + headless Service + StatefulSet 을 만들고, 그것들이 ordinal
// shard 와 격리됨(#220-class 차단)을 실측한다.

var _ = Describe("ShardSplitJob Bootstrap target 생성 (ADR-0027 P2)", func() {
	ctx := context.Background()

	It("Bootstrap 이 각 target 의 격리 ConfigMap/Service/StatefulSet 을 생성한다", func() {
		const ns = "default"
		clusterName := fmt.Sprintf("bsboot-%d", GinkgoRandomSeed())

		cluster := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: ns},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				ShardingMode:    postgresv1alpha1.ShardingModeNone,
				Shards: postgresv1alpha1.ShardsSpec{
					InitialCount: 1,
					Replicas:     0,
					Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		// owner ref 용 UID 채우기.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())

		// PostgresClusterReconciler (suite 등록) 가 source shard-0 STS 를 생성할 때까지
		// 대기 — reconcileBootstrapTargets 는 그 STS 의 image 를 도출한다.
		srcName := ShardStatefulSetName(clusterName, 0)
		Eventually(func(g Gomega) {
			var src appsv1.StatefulSet
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: srcName}, &src)).To(Succeed())
			g.Expect(containerImage(&src, pgContainerName)).NotTo(BeEmpty())
		}, envtestTimeout, envtestInterval).Should(Succeed())

		// Bootstrap 실행.
		ssj := &postgresv1alpha1.ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: "bsj", Namespace: ns},
			Spec: postgresv1alpha1.ShardSplitJobSpec{
				Cluster:  clusterName,
				Keyspace: "ks",
				Sources:  []string{"shard-0"},
				Targets: []postgresv1alpha1.ShardSplitTarget{
					{ShardID: "shard-0a", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0a"}}},
					{ShardID: "shard-0b", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-0b"}}},
				},
			},
		}
		r := &ShardSplitJobReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		Expect(r.reconcileBootstrapTargets(ctx, ssj)).To(Succeed())

		// 각 target 의 STS/CM/Svc 가 격리 식별로 생성됐는지 검증.
		for _, shardID := range []string{"shard-0a", "shard-0b"} {
			var tsts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: TargetShardStatefulSetName(clusterName, shardID)}, &tsts)).
				To(Succeed(), "target %s STS 가 생성돼야 함", shardID)
			// #220-class 격리: ordinal shard label 부재 + reshard-target label.
			Expect(tsts.Spec.Template.Labels).NotTo(HaveKey("postgres.keiailab.io/shard"))
			Expect(tsts.Spec.Template.Labels[ReshardTargetLabelKey]).To(Equal(shardID))
			// source image 도출 정합 (빈 PRIMARY_ENDPOINT → pod-0 initdb fresh primary).
			Expect(tsts.Spec.Template.Spec.Containers[0].Image).NotTo(BeEmpty())
			Expect(*tsts.Spec.Replicas).To(Equal(int32(1)))
			// owner = cluster (영구 shard 승격 대비).
			Expect(tsts.OwnerReferences).To(HaveLen(1))
			Expect(tsts.OwnerReferences[0].Name).To(Equal(clusterName))

			var tcm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: TargetShardConfigMapName(clusterName, shardID)}, &tcm)).
				To(Succeed(), "target %s ConfigMap 이 생성돼야 함", shardID)

			var tsvc corev1.Service
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: TargetShardServiceName(clusterName, shardID)}, &tsvc)).
				To(Succeed(), "target %s Service 가 생성돼야 함", shardID)
			Expect(tsvc.Spec.ClusterIP).To(Equal("None"))
		}

		// 멱등성: 재실행이 에러 없이 통과.
		Expect(r.reconcileBootstrapTargets(ctx, ssj)).To(Succeed())
	})
})
