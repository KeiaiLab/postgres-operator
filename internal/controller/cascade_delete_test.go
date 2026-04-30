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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 ADR 0008 (Finalizer 회피 정책)의 회귀 차단 envtest다.
//
// 검증 대상:
//   1. 모든 하위 자원(STS, Deployment, Service, ConfigMap)에 controllerutil.
//      SetControllerReference로 OwnerReference + Controller=true 설정.
//   2. PostgresCluster CR과 모든 하위 자원에 *finalizer 부재* (ADR 0008 §결정).
//   3. PGC 삭제 시 *즉시 etcd에서 사라짐* (finalizer wait 없음).
//
// 한계: envtest는 K8s GC controller를 시뮬레이트하지 않으므로 *실제 cascade
// 삭제*는 e2e(kind, P0-4 phase 2)에서만 검증 가능. 본 envtest는 *전제 조건*만
// 검증한다. 향후 누군가 finalizer를 추가하면 본 test가 즉시 PR을 fail시킨다.

var _ = Describe("Cascade Delete [ADR 0008]", func() {
	const (
		clusterName = "cascade-test"
		namespace   = "default"
		timeout     = 30 * time.Second
		interval    = 250 * time.Millisecond
	)

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
		})
	})

	It("attaches ControllerReference to all subresources and uses no finalizer", func() {
		By("creating a development-mode PostgresCluster")
		cr := &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
			Spec: postgresv1alpha1.PostgresClusterSpec{
				Deployment: postgresv1alpha1.DeploymentDevelopment,
				Version:    postgresv1alpha1.VersionSpec{Postgres: "17", Citus: "13.0"},
				Coordinator: postgresv1alpha1.CoordinatorSpec{
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				},
				Workers: []postgresv1alpha1.WorkerPoolSpec{{
					Name:    "pool-a",
					Members: 1,
					Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				}},
				Routers: postgresv1alpha1.RouterSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		By("waiting for all 5 subresource kinds (STS×2, Deployment, Service, ConfigMap)")
		coordSTS := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: CoordinatorStatefulSetName(clusterName),
			}, coordSTS)
		}, timeout, interval).Should(Succeed())

		workerSTS := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: WorkerStatefulSetName(clusterName, "pool-a"),
			}, workerSTS)
		}, timeout, interval).Should(Succeed())

		routerDep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: RouterDeploymentName(clusterName),
			}, routerDep)
		}, timeout, interval).Should(Succeed())

		coordSvc := &corev1.Service{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: CoordinatorServiceName(clusterName),
			}, coordSvc)
		}, timeout, interval).Should(Succeed())

		coordCM := &corev1.ConfigMap{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: CoordinatorConfigMapName(clusterName),
			}, coordCM)
		}, timeout, interval).Should(Succeed())

		By("capturing PostgresCluster UID")
		created := &postgresv1alpha1.PostgresCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: namespace, Name: clusterName,
		}, created)).To(Succeed())
		crUID := created.UID
		Expect(crUID).NotTo(BeEmpty(), "PostgresCluster UID must be set after Create")

		By("verifying all subresources have ControllerReference matching PGC UID + no finalizer")
		subresources := []client.Object{coordSTS, workerSTS, routerDep, coordSvc, coordCM}
		for _, obj := range subresources {
			label := obj.GetObjectKind().GroupVersionKind().Kind + "/" + obj.GetName()

			refs := obj.GetOwnerReferences()
			Expect(refs).To(HaveLen(1),
				"%s: must have exactly 1 OwnerReference (ADR 0008)", label)
			Expect(refs[0].UID).To(Equal(crUID),
				"%s: OwnerReference.UID must match PostgresCluster UID", label)
			Expect(refs[0].Controller).NotTo(BeNil(), "%s: Controller pointer required", label)
			Expect(*refs[0].Controller).To(BeTrue(),
				"%s: Controller=true required for K8s GC cascade (ADR 0008)", label)

			// ADR 0008 §결정: 하위 자원에 finalizer 부재.
			Expect(obj.GetFinalizers()).To(BeEmpty(),
				"%s: must have no finalizers (ADR 0008 — Finalizer 회피)", label)
		}

		By("verifying PostgresCluster itself has no finalizer (ADR 0008)")
		Expect(created.GetFinalizers()).To(BeEmpty(),
			"PostgresCluster must have no finalizers — Finalizer 도입은 ADR 0008 위반 + RFC 필요")

		By("deleting PostgresCluster — must be removed immediately (no finalizer wait)")
		Expect(k8sClient.Delete(ctx, created)).To(Succeed())
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: clusterName,
			}, &postgresv1alpha1.PostgresCluster{})
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(),
			"PostgresCluster must be deleted immediately — finalizer wait 없음 (ADR 0008)")

		// envtest는 GC controller를 시뮬레이트하지 않으므로 실제 cascade 삭제는
		// e2e(kind, P0-4 phase 2)에서 별도 검증. 본 envtest는 *전제 조건*만 검증.
	})
})
