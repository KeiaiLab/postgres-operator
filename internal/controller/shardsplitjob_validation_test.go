/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ShardSplitJob.Spec.Targets[].ShardID 의 CRD pattern validation
// (ADR-0027, P2 prerequisite) 을 *apiserver 실강제* 로 검증하는 통합 테스트다.
//
// reconciler 가 shardID 를 K8s 자원명(`<cluster>-rsd-<shardID>`, names.go)에 직접
// 박으므로 ShardID 는 DNS-1123 label-safe 여야 한다. 형제 필드(Keyspace /
// ShardRangeEntry.Shard)의 패턴은 언더스코어를 허용해 DNS 에 무효이므로 ShardID 는
// 더 엄격한 패턴(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)을 쓴다. DNS-무효 값이 apiserver
// 까지 도달하면 자원 생성 실패/혼동(#220-class)을 낳으므로, admission 단계에서
// 거부됨을 실측한다.

// validShardSplitJob 는 ShardID 만 바꿔 검증에 쓰는 baseline 유효 객체다.
func validShardSplitJob(name, shardID string) *postgresv1alpha1.ShardSplitJob {
	return &postgresv1alpha1.ShardSplitJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: postgresv1alpha1.ShardSplitJobSpec{
			Cluster:  "pg",
			Keyspace: "ks",
			Sources:  []string{"shard-0"},
			Targets: []postgresv1alpha1.ShardSplitTarget{{
				ShardID: shardID,
				Ranges: []postgresv1alpha1.ShardRangeEntry{{
					Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0",
				}},
			}},
		},
	}
}

var _ = Describe("ShardSplitJob ShardID apiserver 검증 (ADR-0027 P2 prerequisite)", func() {
	ctx := context.Background()

	DescribeTable("apiserver 가 DNS-무효 ShardID 를 거부한다",
		func(shardID string) {
			ssj := validShardSplitJob("invalid-shardid", shardID)
			Expect(k8sClient.Create(ctx, ssj)).NotTo(Succeed(),
				"apiserver 가 DNS-무효 ShardID %q 를 수락하면 안 됨", shardID)
		},
		Entry("언더스코어 (형제 패턴은 허용 — DNS 무효)", "bad_id"),
		Entry("대문자", "ShardA"),
		Entry("후행 하이픈", "shard-"),
		Entry("선행 하이픈", "-shard"),
		Entry("선행 숫자", "0shard"),
	)

	It("apiserver 가 유효 DNS-safe ShardID 를 수락한다", func() {
		ssj := validShardSplitJob("valid-shardid", "shard-0a")
		Expect(k8sClient.Create(ctx, ssj)).To(Succeed())
		// 본 객체를 watch 하는 reconciler 가 suite 에 없어 finalizer 없이 즉시 삭제 가능.
		Expect(k8sClient.Delete(ctx, ssj)).To(Succeed())
	})
})
