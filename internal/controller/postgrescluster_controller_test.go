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
	"strings"
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

// 본 파일은 P1-M1 DoD("e2e 1개 통과")의 직접 증거다. envtest에 dev 샘플 CR을
// 적용하고 reconciler가 desired state를 K8s에 반영하는지 종단 간 검증한다.
//
// 검증 대상:
//   1. coordinator/worker/router StatefulSet+Deployment+Service+ConfigMap 생성
//   2. 각 자원의 controller owner reference가 PostgresCluster를 가리킴
//   3. ConfigMap.Data["postgresql.conf"]에 shared_preload_libraries='citus' 포함
//      (P13 SDK의 Plugin Registry 결과가 reconciler에 의해 ConfigMap에 반영됨을
//      end-to-end로 확인 — Issue #3194 회귀 차단의 두 번째 방어선)
//   4. status.channel = "stable" (PG17+Citus13.0 매트릭스 lookup 결과)
//   5. ObservedGeneration이 spec generation과 일치

var _ = Describe("PostgresCluster reconciler [P1-M1]", func() {
	const (
		clusterName = "quickstart-it"
		namespace   = "default"
		timeout     = 30 * time.Second
		interval    = 250 * time.Millisecond
	)

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		// 다음 spec과 격리. PostgresCluster를 지우면 owner reference로 인해
		// 모든 하위 자원이 K8s GC로 삭제된다(envtest는 GC controller를
		// 시뮬레이트하지 않으므로 envtest 종료 시 정리됨).
		_ = k8sClient.Delete(ctx, &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
		})
	})

	It("creates coordinator/worker/router subresources from a dev-mode CR", func() {
		By("applying a development-mode PostgresCluster")
		cr := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				Deployment: postgresv1alpha1.DeploymentDevelopment,
				Version:    postgresv1alpha1.VersionSpec{Postgres: "17", Citus: "13.0"},
				Coordinator: postgresv1alpha1.CoordinatorSpec{
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
				},
				Workers: []postgresv1alpha1.WorkerPoolSpec{{
					Name:    "pool-a",
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
				}},
				Routers: postgresv1alpha1.RouterSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		By("waiting for coordinator StatefulSet")
		coordSTS := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      CoordinatorStatefulSetName(clusterName),
			}, coordSTS)
		}, timeout, interval).Should(Succeed())

		Expect(coordSTS.OwnerReferences).To(HaveLen(1))
		Expect(coordSTS.OwnerReferences[0].Name).To(Equal(clusterName))
		Expect(*coordSTS.OwnerReferences[0].Controller).To(BeTrue())
		Expect(*coordSTS.Spec.Replicas).To(Equal(int32(1)))

		By("waiting for worker StatefulSet")
		workerSTS := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      WorkerStatefulSetName(clusterName, "pool-a"),
			}, workerSTS)
		}, timeout, interval).Should(Succeed())

		Expect(workerSTS.OwnerReferences[0].Name).To(Equal(clusterName))

		By("waiting for router Deployment")
		routerDep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      RouterDeploymentName(clusterName),
			}, routerDep)
		}, timeout, interval).Should(Succeed())

		// ADR 0003 §강제: 라우터는 PVC 보유 금지. Deployment가 VolumeClaimTemplate을
		// 가질 수 없으므로 자연 강제되지만, 추가로 PodSpec.Volumes에 PVC 마운트가
		// 없는지 검증한다.
		for _, v := range routerDep.Spec.Template.Spec.Volumes {
			Expect(v.PersistentVolumeClaim).To(BeNil(),
				"router Pod must not mount a PersistentVolumeClaim (ADR 0003)")
		}

		By("verifying coordinator ConfigMap contains shared_preload_libraries=citus")
		// P13 Plugin SDK 결과가 reconciler를 통해 K8s에 반영되었는지 end-to-end 검증.
		coordCM := &corev1.ConfigMap{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      CoordinatorConfigMapName(clusterName),
			}, coordCM)
		}, timeout, interval).Should(Succeed())

		conf, ok := coordCM.Data["postgresql.conf"]
		Expect(ok).To(BeTrue(), "coordinator ConfigMap must have postgresql.conf key")
		Expect(conf).To(ContainSubstring("shared_preload_libraries = 'citus'"),
			"reconciler must serialize Plugin Registry result into postgresql.conf")

		By("verifying worker headless Service exists")
		workerSvc := &corev1.Service{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      WorkerServiceName(clusterName, "pool-a"),
			}, workerSvc)
		}, timeout, interval).Should(Succeed())
		Expect(workerSvc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

		By("verifying router client Service is ClusterIP")
		routerSvc := &corev1.Service{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      RouterServiceName(clusterName),
			}, routerSvc)
		}, timeout, interval).Should(Succeed())
		Expect(routerSvc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		Expect(routerSvc.Spec.ClusterIP).NotTo(Equal(corev1.ClusterIPNone))

		By("verifying status.channel == beta for PG17+Citus13.0 (ADR 0010)")
		// 0.2.0-alpha 이후 Citus 조합은 Beta 채널로 강등됨 (license 격리).
		// vanilla PG18은 Stable. 본 테스트는 Citus opt-in 경로 검증.
		Eventually(func() string {
			updated := &postgresv1alpha1.PostgresCluster{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: clusterName,
			}, updated); err != nil {
				return ""
			}
			return updated.Status.Channel
		}, timeout, interval).Should(Equal("beta"))

		By("[P11-T1] verifying Citus desired topology is surfaced in Workers[].DistNode")
		// RFC 0002 §10 Status 반영. NullExecutor 사용 중이므로 SQL은 호출되지
		// 않지만 desired state는 Status에 표면화되어야 함.
		Eventually(func() *postgresv1alpha1.DistNodeRef {
			updated := &postgresv1alpha1.PostgresCluster{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: clusterName,
			}, updated); err != nil {
				return nil
			}
			if len(updated.Status.Topology.Workers) == 0 {
				return nil
			}
			return updated.Status.Topology.Workers[0].DistNode
		}, timeout, interval).ShouldNot(BeNil())

		// 정확한 형식 검증.
		updated := &postgresv1alpha1.PostgresCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: clusterName,
		}, updated)).To(Succeed())

		Expect(updated.Status.Topology.Workers).To(HaveLen(1))
		dn := updated.Status.Topology.Workers[0].DistNode
		Expect(dn).NotTo(BeNil())
		Expect(dn.GroupID).To(Equal(int32(1)), "first worker pool must have groupid=1 (RFC 0002 §2)")
		Expect(dn.NodePort).To(Equal(int32(5432)))
		Expect(dn.ShouldHaveShards).To(BeTrue(), "worker default ShouldHaveShards must be true (RFC 0002 §3)")
		Expect(dn.NodeName).To(ContainSubstring("worker-pool-a-0"),
			"NodeName must follow Pod DNS pattern (RFC 0002 §1)")
		Expect(dn.NodeName).To(ContainSubstring(".svc.cluster.local"))

		By("verifying ObservedGeneration tracks spec generation")
		Eventually(func() int64 {
			updated := &postgresv1alpha1.PostgresCluster{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: clusterName,
			}, updated); err != nil {
				return -1
			}
			return updated.Status.ObservedGeneration
		}, timeout, interval).Should(BeNumerically(">=", 1))
	})

	It("rejects unsupported version via reconciler status (defense-in-depth)", func() {
		// webhook이 동일 검사를 하지만 본 envtest에는 webhook이 미부착이므로
		// reconciler 측 방어가 동작함을 추가로 검증한다(defense-in-depth).
		By("applying a CR with version not in matrix")
		cr := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				Deployment: postgresv1alpha1.DeploymentDevelopment,
				Version:    postgresv1alpha1.VersionSpec{Postgres: "17", Citus: "99.99"},
				Coordinator: postgresv1alpha1.CoordinatorSpec{
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
				},
				Workers: []postgresv1alpha1.WorkerPoolSpec{{
					Name:    "pool-a",
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
				}},
				Routers: postgresv1alpha1.RouterSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		By("expecting Ready=False with Reason=VersionRejected")
		Eventually(func() string {
			updated := &postgresv1alpha1.PostgresCluster{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: clusterName,
			}, updated); err != nil {
				return ""
			}
			for _, c := range updated.Status.Conditions {
				if c.Type == ConditionReady {
					if c.Status == metav1.ConditionFalse && strings.Contains(c.Message, "supported matrix") {
						return c.Reason
					}
				}
			}
			return ""
		}, timeout, interval).Should(Equal(ReasonVersionRejected))
	})
})
