/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
Copyright 2026 keiailab.
*/

// Admission round-trip — webhook_suite_test 의 envtest 통합. mongodb /
// valkey-operator 와 동일 패턴 (cross-cut UX, ADR-0016).

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

var _ = Describe("PostgresCluster webhook admission round-trip", func() {
	It("rejects unsupported postgresVersion via real apiserver", func() {
		c := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rt-badver", Namespace: "default"},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "99",
				ShardingMode:    postgresv1alpha1.ShardingModeNone,
				Shards: postgresv1alpha1.ShardsSpec{
					InitialCount: 1,
					Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
					Replicas:     1,
				},
			},
		}
		err := k8sClient.Create(ctx, c)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		// CRD enum (+kubebuilder:validation:Enum=17;18) 가 *먼저* 거부 — webhook
		// 의 'supported matrix' 도달 안 함. design 정합 (CRD 우선, webhook 은
		// 표현 불가 영역만).
		Expect(err.Error()).To(ContainSubstring("postgresVersion"))
	})

	It("rejects shards.storage.size below 1Gi", func() {
		c := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rt-smallstorage", Namespace: "default"},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				ShardingMode:    postgresv1alpha1.ShardingModeNone,
				Shards: postgresv1alpha1.ShardsSpec{
					InitialCount: 1,
					Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("512Mi")},
					Replicas:     1,
				},
			},
		}
		err := k8sClient.Create(ctx, c)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("storage.size"))
	})

	It("accepts valid PostgresCluster — admission round-trip 통과", func() {
		c := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rt-happy", Namespace: "default"},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				PostgresVersion: "18",
				ShardingMode:    postgresv1alpha1.ShardingModeNone,
				Shards: postgresv1alpha1.ShardsSpec{
					InitialCount: 1,
					Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
					Replicas:     1,
				},
			},
		}
		err := k8sClient.Create(ctx, c)
		Expect(err).NotTo(HaveOccurred(), "valid spec 은 admission 통과")
		Expect(k8sClient.Delete(ctx, c)).To(Succeed())
	})
})
