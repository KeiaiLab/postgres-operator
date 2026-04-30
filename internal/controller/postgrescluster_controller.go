/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller는 keiailab/postgres-operator의 reconciler들을 보유한다.
//
// 본 파일은 Pillar P1-T2의 본체다. PostgresCluster CR을 관찰하여 다음 desired
// state를 K8s에 반영한다.
//
//  1. coordinator: StatefulSet + headless Service + ConfigMap
//  2. 각 worker pool: StatefulSet + headless Service + ConfigMap
//  3. routers: stub Deployment + ClusterIP Service + ConfigMap (PVC 부재,
//     ADR 0003 무상태 강제). cmd/router 본체는 P12-T2에서 교체됨.
//
// 모든 하위 자원에 controllerutil.SetControllerReference를 설정하여 K8s GC가
// PostgresCluster 삭제 시 cascade delete 하게 한다.
//
// 본 reconciler는 internal/plugin/<concrete>/ 하위 패키지를 직접 import 하지
// 않는다. 모든 ExtensionPlugin/BackupPlugin/... 호출은 r.Plugins(*plugin.Registry)
// 를 통해서만 이뤄진다(ADR 0005 §강제 메커니즘).
package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/citus"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// PostgresClusterReconciler는 PostgresCluster CR을 reconcile한다.
type PostgresClusterReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Plugins *plugin.Registry

	// FeatureGates는 PG18 같은 격리 채널 활성화 결정에 사용된다.
	// nil이면 빈 맵으로 취급(기본 비활성).
	FeatureGates map[string]bool

	// CitusExec는 pg_dist_node 동기화 SQL을 실 PG에 적용하는 실행기다(RFC 0002).
	// nil이면 NullExecutor를 사용 — Pillar P11-M0(spike)의 기본값으로, desired
	// state는 Status.Topology에 표면화되지만 실 SQL은 호출되지 않는다.
	// Pillar P11-M1에서 LibPQExecutor가 cmd/main.go에서 주입된다.
	CitusExec citus.SQLExecutor
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile은 PostgresCluster CR 변화에 반응한다.
//
//nolint:gocyclo // P1-T2 본체. 자원 종류별 분기로 인해 복잡도 자연 누적.
func (r *PostgresClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("postgrescluster", req.NamespacedName)

	var cluster postgresv1alpha1.PostgresCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch PostgresCluster")
		return ctrl.Result{}, err
	}

	// 1) 버전 매트릭스 검증. matrix.go의 IsSupported 통과해야만 reconcile 진행.
	combo, ok := lookupCombo(cluster.Spec.Version, r.FeatureGates)
	if !ok {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonVersionRejected,
			fmt.Sprintf("PG=%q Citus=%q is not in supported matrix (or feature gate missing)", cluster.Spec.Version.Postgres, cluster.Spec.Version.Citus))
		cluster.Status.ObservedGeneration = cluster.Generation
		if err := r.Status().Update(ctx, &cluster); err != nil {
			logger.Error(err, "Failed to update status with version rejection")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	cluster.Status.Channel = string(combo.Channel)

	// 2) 진행 중 신호. 본 reconcile이 끝까지 가면 ConditionReady가 True로 갱신.
	setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionUnknown, ReasonReconciling,
		"Reconciliation in progress")

	// 3) coordinator 자원 생성/갱신.
	if err := r.reconcileCoordinator(ctx, &cluster, combo.Image); err != nil {
		logger.Error(err, "Failed to reconcile coordinator")
		return ctrl.Result{}, err
	}

	// 4) 각 worker pool.
	for i := range cluster.Spec.Workers {
		if err := r.reconcileWorkerPool(ctx, &cluster, &cluster.Spec.Workers[i], combo.Image); err != nil {
			logger.Error(err, "Failed to reconcile worker pool", "pool", cluster.Spec.Workers[i].Name)
			return ctrl.Result{}, err
		}
	}

	// 5) routers (stub Deployment).
	if err := r.reconcileRouters(ctx, &cluster, combo.Image); err != nil {
		logger.Error(err, "Failed to reconcile routers")
		return ctrl.Result{}, err
	}

	// 6) Status 갱신: 토폴로지 readyReplicas + Conditions.
	if err := r.refreshStatus(ctx, &cluster); err != nil {
		logger.Error(err, "Failed to refresh status")
		return ctrl.Result{}, err
	}

	cluster.Status.ObservedGeneration = cluster.Generation
	if err := r.Status().Update(ctx, &cluster); err != nil {
		// 충돌은 정상 상황. 다음 reconcile에서 재시도.
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to update PostgresCluster status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileCoordinator는 coordinator의 ConfigMap → Service → StatefulSet 순으로
// upsert 한다.
func (r *PostgresClusterReconciler) reconcileCoordinator(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, image string) error {
	cmName := CoordinatorConfigMapName(cluster.Name)
	svcName := CoordinatorServiceName(cluster.Name)
	stsName := CoordinatorStatefulSetName(cluster.Name)

	cm := buildConfigMap(cluster, cmName, "coordinator", "", r.Plugins)
	if err := r.upsert(ctx, cluster, cm, func(have, want client.Object) {
		have.(*corev1.ConfigMap).Data = want.(*corev1.ConfigMap).Data
	}); err != nil {
		return fmt.Errorf("coordinator ConfigMap: %w", err)
	}

	svc := buildHeadlessService(cluster, svcName, "coordinator", "")
	if err := r.upsert(ctx, cluster, svc, func(have, want client.Object) {
		// ClusterIP는 immutable. Spec.Selector/Ports만 갱신.
		h := have.(*corev1.Service)
		w := want.(*corev1.Service)
		h.Spec.Selector = w.Spec.Selector
		h.Spec.Ports = w.Spec.Ports
	}); err != nil {
		return fmt.Errorf("coordinator Service: %w", err)
	}

	sts := buildPGStatefulSet(cluster, stsName, svcName, "coordinator", "", image, cmName,
		cluster.Spec.Coordinator.Members, cluster.Spec.Coordinator.Storage, cluster.Spec.Coordinator.Resources)
	if err := r.upsert(ctx, cluster, sts, func(have, want client.Object) {
		// VolumeClaimTemplates와 ServiceName은 immutable. Replicas/Template만 갱신.
		h := have.(*appsv1.StatefulSet)
		w := want.(*appsv1.StatefulSet)
		h.Spec.Replicas = w.Spec.Replicas
		h.Spec.Template = w.Spec.Template
	}); err != nil {
		return fmt.Errorf("coordinator StatefulSet: %w", err)
	}
	return nil
}

// reconcileWorkerPool은 단일 worker pool 자원 3종을 upsert 한다.
func (r *PostgresClusterReconciler) reconcileWorkerPool(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, pool *postgresv1alpha1.WorkerPoolSpec, image string) error {
	cmName := WorkerConfigMapName(cluster.Name, pool.Name)
	svcName := WorkerServiceName(cluster.Name, pool.Name)
	stsName := WorkerStatefulSetName(cluster.Name, pool.Name)

	cm := buildConfigMap(cluster, cmName, "worker", pool.Name, r.Plugins)
	if err := r.upsert(ctx, cluster, cm, func(have, want client.Object) {
		have.(*corev1.ConfigMap).Data = want.(*corev1.ConfigMap).Data
	}); err != nil {
		return fmt.Errorf("worker ConfigMap %s: %w", pool.Name, err)
	}

	svc := buildHeadlessService(cluster, svcName, "worker", pool.Name)
	if err := r.upsert(ctx, cluster, svc, func(have, want client.Object) {
		h := have.(*corev1.Service)
		w := want.(*corev1.Service)
		h.Spec.Selector = w.Spec.Selector
		h.Spec.Ports = w.Spec.Ports
	}); err != nil {
		return fmt.Errorf("worker Service %s: %w", pool.Name, err)
	}

	sts := buildPGStatefulSet(cluster, stsName, svcName, "worker", pool.Name, image, cmName,
		pool.Members, pool.Storage, pool.Resources)
	if err := r.upsert(ctx, cluster, sts, func(have, want client.Object) {
		h := have.(*appsv1.StatefulSet)
		w := want.(*appsv1.StatefulSet)
		h.Spec.Replicas = w.Spec.Replicas
		h.Spec.Template = w.Spec.Template
	}); err != nil {
		return fmt.Errorf("worker StatefulSet %s: %w", pool.Name, err)
	}
	return nil
}

// reconcileRouters는 라우터 ConfigMap + Deployment + 클라이언트 Service를 upsert.
// PVC는 절대 생성하지 않는다(ADR 0003 §강제). 본 함수의 image는 P12-T2 시점에
// cmd/router 바이너리 이미지로 교체된다.
func (r *PostgresClusterReconciler) reconcileRouters(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, image string) error {
	cmName := RouterConfigMapName(cluster.Name)
	svcName := RouterServiceName(cluster.Name)
	depName := RouterDeploymentName(cluster.Name)

	cm := buildConfigMap(cluster, cmName, "router", "", r.Plugins)
	if err := r.upsert(ctx, cluster, cm, func(have, want client.Object) {
		have.(*corev1.ConfigMap).Data = want.(*corev1.ConfigMap).Data
	}); err != nil {
		return fmt.Errorf("router ConfigMap: %w", err)
	}

	svc := buildClientService(cluster, svcName, "router")
	if err := r.upsert(ctx, cluster, svc, func(have, want client.Object) {
		h := have.(*corev1.Service)
		w := want.(*corev1.Service)
		h.Spec.Selector = w.Spec.Selector
		h.Spec.Ports = w.Spec.Ports
	}); err != nil {
		return fmt.Errorf("router Service: %w", err)
	}

	dep := buildRouterDeployment(cluster, depName, cmName, image, cluster.Spec.Routers.Replicas, cluster.Spec.Routers.Resources)
	if err := r.upsert(ctx, cluster, dep, func(have, want client.Object) {
		h := have.(*appsv1.Deployment)
		w := want.(*appsv1.Deployment)
		h.Spec.Replicas = w.Spec.Replicas
		h.Spec.Template = w.Spec.Template
	}); err != nil {
		return fmt.Errorf("router Deployment: %w", err)
	}
	return nil
}

// upsert는 owner reference 설정 + Get → 없으면 Create → 있으면 mutate→Update
// 의 표준 패턴이다. mutate는 immutable 필드를 회피해야 한다(호출자 책임).
func (r *PostgresClusterReconciler) upsert(ctx context.Context, owner *postgresv1alpha1.PostgresCluster, want client.Object, mutate func(have, want client.Object)) error {
	if err := controllerutil.SetControllerReference(owner, want, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference: %w", err)
	}

	have := want.DeepCopyObject().(client.Object)
	key := types.NamespacedName{Namespace: want.GetNamespace(), Name: want.GetName()}
	err := r.Get(ctx, key, have)
	switch {
	case apierrors.IsNotFound(err):
		return r.Create(ctx, want)
	case err != nil:
		return fmt.Errorf("get existing: %w", err)
	}

	// 존재하면 owner reference 보존 + mutate 적용 후 Update.
	// mutate는 immutable 필드를 건드리지 않도록 호출자가 보장한다.
	mutate(have, want)
	if err := controllerutil.SetControllerReference(owner, have, r.Scheme); err != nil {
		return fmt.Errorf("re-set controller reference: %w", err)
	}
	return r.Update(ctx, have)
}

// refreshStatus는 하위 자원의 ready 상태를 읽어 Conditions와 Topology를
// 갱신한다.
func (r *PostgresClusterReconciler) refreshStatus(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) error {
	allReady := true

	// Coordinator
	coordSTS := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      CoordinatorStatefulSetName(cluster.Name),
	}, coordSTS); err != nil {
		return err
	}
	coordReady := coordSTS.Status.ReadyReplicas == cluster.Spec.Coordinator.Members
	if coordReady {
		setCondition(&cluster.Status.Conditions, ConditionCoordinatorReady, metav1.ConditionTrue, ReasonAvailable,
			fmt.Sprintf("%d/%d members ready", coordSTS.Status.ReadyReplicas, cluster.Spec.Coordinator.Members))
	} else {
		setCondition(&cluster.Status.Conditions, ConditionCoordinatorReady, metav1.ConditionFalse, ReasonProgressing,
			fmt.Sprintf("%d/%d members ready", coordSTS.Status.ReadyReplicas, cluster.Spec.Coordinator.Members))
		allReady = false
	}

	// Workers
	workerStatuses := make([]postgresv1alpha1.WorkerPoolStatus, 0, len(cluster.Spec.Workers))
	allWorkersReady := true
	for _, pool := range cluster.Spec.Workers {
		sts := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      WorkerStatefulSetName(cluster.Name, pool.Name),
		}, sts); err != nil {
			return err
		}
		ready := sts.Status.ReadyReplicas == pool.Members
		if !ready {
			allWorkersReady = false
		}
		workerStatuses = append(workerStatuses, postgresv1alpha1.WorkerPoolStatus{
			Name: pool.Name,
		})
		_ = ready // 추가 NodeStatus 필드(Primary/Replicas/LeaseHolder)는 P2에서 채움
	}
	if allWorkersReady {
		setCondition(&cluster.Status.Conditions, ConditionWorkersReady, metav1.ConditionTrue, ReasonAvailable,
			fmt.Sprintf("%d/%d pools ready", len(cluster.Spec.Workers), len(cluster.Spec.Workers)))
	} else {
		setCondition(&cluster.Status.Conditions, ConditionWorkersReady, metav1.ConditionFalse, ReasonProgressing,
			"some worker pools have unready members")
		allReady = false
	}

	// Routers
	routerDep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      RouterDeploymentName(cluster.Name),
	}, routerDep); err != nil {
		return err
	}
	routersReady := routerDep.Status.ReadyReplicas == cluster.Spec.Routers.Replicas
	if routersReady {
		setCondition(&cluster.Status.Conditions, ConditionRoutersReady, metav1.ConditionTrue, ReasonAvailable,
			fmt.Sprintf("%d/%d routers ready", routerDep.Status.ReadyReplicas, cluster.Spec.Routers.Replicas))
	} else {
		setCondition(&cluster.Status.Conditions, ConditionRoutersReady, metav1.ConditionFalse, ReasonProgressing,
			fmt.Sprintf("%d/%d routers ready", routerDep.Status.ReadyReplicas, cluster.Spec.Routers.Replicas))
		allReady = false
	}

	// P11-T1 spike — DesiredNodes 계산 결과를 Workers[].DistNode로 표면화.
	// 실 SQL 적용은 NullExecutor(M0 기본) 또는 LibPQExecutor(M1)에 위임.
	// 현재 reconcile에서는 SQL 호출까지 가지만 NullExecutor가 no-op이므로 안전.
	desiredNodes := citus.DesiredNodes(cluster)
	for _, node := range desiredNodes {
		if node.Role != "worker" {
			continue
		}
		// 같은 pool의 첫 번째 멤버(Index=0) 정보만 Status에 노출 — 추가 멤버는
		// 같은 group에 속하므로 P2 election 후 primary 추적으로 갱신된다.
		if node.Index != 0 {
			continue
		}
		for i := range workerStatuses {
			if workerStatuses[i].Name == node.Pool {
				workerStatuses[i].DistNode = &postgresv1alpha1.DistNodeRef{
					GroupID:          node.Group,
					NodeName:         node.Name,
					NodePort:         node.Port,
					ShouldHaveShards: node.ShouldHaveShards,
				}
				break
			}
		}
	}

	// SQL Apply — NullExecutor(spike 기본) 또는 LibPQExecutor(M1).
	// Apply 호출은 모든 자원 ready일 때만 의미가 있으나, NullExecutor는 항상
	// 안전하므로 조건 분기 없이 호출한다.
	exec := r.CitusExec
	if exec == nil {
		exec = citus.NullExecutor{}
	}
	// 현재 pg_dist_node 조회는 P11-M1(LibPQExecutor)에서 추가됨. M0 spike에서는
	// 빈 슬라이스를 current로 가정 → 모든 desired가 add로 계산됨.
	actions := citus.ComputeActions(nil, desiredNodes)
	// P0-6 phase 2b — ctx에 ClusterID 주입. LibPQExecutor.DSNFunc가
	// citus.ClusterFromContext로 추출하여 cluster별 DSN을 lookup 가능.
	ctxWithCluster := citus.WithCluster(ctx, cluster.Namespace, cluster.Name)
	if err := exec.Apply(ctxWithCluster, actions); err != nil {
		setCondition(&cluster.Status.Conditions, ConditionMetadataInSync, metav1.ConditionFalse, ReasonProgressing,
			fmt.Sprintf("Citus SQL apply failed: %v", err))
	} else {
		// NullExecutor 또는 성공한 LibPQExecutor 모두 여기.
		// NullExecutor 사용 중인지 사용자에게 분명한 신호:
		if _, isNull := exec.(citus.NullExecutor); isNull {
			setCondition(&cluster.Status.Conditions, ConditionMetadataInSync, metav1.ConditionUnknown, ReasonNotApplicable,
				"Pillar P11-M0 spike: desired Citus topology surfaced in Status; SQL execution requires LibPQExecutor (P11-M1)")
		} else if allReady {
			setCondition(&cluster.Status.Conditions, ConditionMetadataInSync, metav1.ConditionTrue, ReasonAvailable,
				fmt.Sprintf("%d Citus actions applied successfully", len(actions)))
		} else {
			setCondition(&cluster.Status.Conditions, ConditionMetadataInSync, metav1.ConditionFalse, ReasonProgressing,
				"Subresources not yet ready; metadata sync deferred")
		}
	}

	// Topology — DistNode는 위에서 채움. Primary/lease는 P2에서 보강.
	cluster.Status.Topology = postgresv1alpha1.TopologyStatus{
		Coordinator: postgresv1alpha1.NodeStatus{},
		Workers:     workerStatuses,
		Routers: postgresv1alpha1.RouterPoolStatus{
			ReadyReplicas: routerDep.Status.ReadyReplicas,
		},
	}

	// Ready 종합 — coord/workers/routers 모두 ready인 경우만.
	if allReady {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionTrue, ReasonAvailable,
			"All subresources are ready")
	} else {
		// 이미 위에서 Reconciling/Progressing 신호를 충분히 줬다.
		// Ready는 명시적으로 False로 두어 사용자에게 분명히 신호.
		if cond := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReady); cond == nil || cond.Status != metav1.ConditionFalse {
			setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonProgressing,
				"Subresources are not all ready yet")
		}
	}
	return nil
}

// SetupWithManager는 본 reconciler를 controller-runtime Manager에 등록한다.
func (r *PostgresClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.PostgresCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("postgrescluster").
		Complete(r)
}
