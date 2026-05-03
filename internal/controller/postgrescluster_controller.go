/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller 는 keiailab/postgres-operator 의 reconciler 들을 보유한다.
//
// 본 파일은 RFC 0001 (PostgresCluster CRD v2) schema 위에서 동작하는 PostgresCluster
// reconciler 본체다 (F01b). desired state 생성은 다음 흐름을 따른다:
//
//  1. PostgresCluster CR fetch + matrix lookup (PostgresVersion / FeatureGates).
//  2. spec.shards.initialCount 만큼 shard 자원 3종 (ConfigMap, Headless Service,
//     StatefulSet) 을 ordinal 0..N-1 로 upsert. 각 STS 의 replicas 는 1 (primary) +
//     spec.shards.replicas (async).
//  3. shardingMode=native && spec.router.enabled 일 때만 router 자원 3종
//     (ConfigMap, ClusterIP Service, Deployment) upsert.
//  4. status.shards / status.router / phase / conditions 갱신.
//
// status.phase 전환 규칙 (RFC 0001 §3.4):
//   - 모든 shard primary ready && (router 부재 || router ready) → Ready
//   - 그 외 → Provisioning
package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// PostgresClusterReconciler 는 PostgresCluster CR 을 reconcile 한다.
type PostgresClusterReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Plugins *plugin.Registry

	// FeatureGates 는 PG18 같은 격리 채널 활성화 결정에 사용된다.
	// nil 이면 빈 맵으로 취급 (기본 비활성).
	FeatureGates map[string]bool
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 PostgresCluster CR 변화에 반응한다.
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

	pgVersion := cluster.Spec.PostgresVersion
	if pgVersion == "" {
		pgVersion = "18"
	}

	combo, ok := lookupCombo(pgVersion, r.FeatureGates)
	if !ok {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonVersionRejected,
			fmt.Sprintf("PG=%q is not in supported matrix (or feature gate missing)", pgVersion))
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseDegraded
		cluster.Status.ObservedGeneration = cluster.Generation
		if err := r.Status().Update(ctx, &cluster); err != nil {
			logger.Error(err, "Failed to update status with version rejection")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 0. instance manager 가 사용할 RBAC (ServiceAccount + Role + RoleBinding) upsert.
	// shard StatefulSet 보다 먼저 — Pod 가 SA reference 를 사용하므로 fail-fast 회피.
	if err := r.upsert(ctx, &cluster, buildInstanceServiceAccount(&cluster)); err != nil {
		return r.handleUpsertErr(ctx, err, "instance ServiceAccount", logger)
	}
	if err := r.upsert(ctx, &cluster, buildInstanceRole(&cluster)); err != nil {
		return r.handleUpsertErr(ctx, err, "instance Role", logger)
	}
	if err := r.upsert(ctx, &cluster, buildInstanceRoleBinding(&cluster)); err != nil {
		return r.handleUpsertErr(ctx, err, "instance RoleBinding", logger)
	}

	// 1. shard 자원 3종 upsert (ordinal 0..InitialCount-1)
	shardCount := cluster.Spec.Shards.InitialCount
	members := int32(1) + cluster.Spec.Shards.Replicas
	shardStatuses := make([]postgresv1alpha1.ShardStatus, 0, shardCount)
	allShardPrimaryReady := true

	for ord := range shardCount {
		cmName := ShardConfigMapName(cluster.Name, ord)
		svcName := ShardServiceName(cluster.Name, ord)
		stsName := ShardStatefulSetName(cluster.Name, ord)

		if err := r.upsert(ctx, &cluster, buildConfigMap(&cluster, cmName, "shard", ord, r.Plugins)); err != nil {
			return r.handleUpsertErr(ctx, err, "shard ConfigMap", logger)
		}
		if err := r.upsert(ctx, &cluster, buildHeadlessService(&cluster, svcName, "shard", ord)); err != nil {
			return r.handleUpsertErr(ctx, err, "shard Service", logger)
		}
		desiredSTS := buildPGStatefulSet(
			&cluster, stsName, svcName,
			ord,
			combo.Image, cmName, combo.PostgresMajor,
			members,
			cluster.Spec.Shards.Storage, cluster.Spec.Shards.Resources,
		)
		if err := r.upsert(ctx, &cluster, desiredSTS); err != nil {
			return r.handleUpsertErr(ctx, err, "shard StatefulSet", logger)
		}

		// observed STS 를 다시 조회하여 readyReplicas 기반 status 산출.
		// 방금 Create 한 STS 는 cache propagation 지연으로 NotFound 가 잠깐 보일
		// 수 있다 — 그 경우 readiness=false 로 단순화 (다음 reconcile 에 실제
		// status 가 관측된다).
		var observed appsv1.StatefulSet
		primaryReady := false
		if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: stsName}, &observed); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to re-read shard StatefulSet for status", "name", stsName)
				return ctrl.Result{}, err
			}
		} else {
			primaryReady = observed.Status.ReadyReplicas >= 1
		}
		if !primaryReady {
			allShardPrimaryReady = false
		}
		shardStatuses = append(shardStatuses, postgresv1alpha1.ShardStatus{
			Name:    fmt.Sprintf("shard-%d", ord),
			Ordinal: ord,
			Primary: &postgresv1alpha1.ShardEndpoint{
				Pod:      fmt.Sprintf("%s-0", stsName),
				Endpoint: fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:%d", stsName, svcName, cluster.Namespace, pgPort),
				Ready:    primaryReady,
			},
		})
	}

	// 2. router 자원 3종 — shardingMode=native && Router.Enabled 일 때만.
	routerActive := cluster.Spec.ShardingMode == postgresv1alpha1.ShardingModeNative &&
		cluster.Spec.Router != nil && cluster.Spec.Router.Enabled
	var routerStatus *postgresv1alpha1.ClusterRouterStatus

	if routerActive {
		cmName := RouterConfigMapName(cluster.Name)
		svcName := RouterServiceName(cluster.Name)
		depName := RouterDeploymentName(cluster.Name)

		if err := r.upsert(ctx, &cluster, buildConfigMap(&cluster, cmName, "router", -1, r.Plugins)); err != nil {
			return r.handleUpsertErr(ctx, err, "router ConfigMap", logger)
		}
		if err := r.upsert(ctx, &cluster, buildClientService(&cluster, svcName, "router")); err != nil {
			return r.handleUpsertErr(ctx, err, "router Service", logger)
		}
		// router 이미지: P12-T2 까지 PG 베이스 이미지 placeholder.
		desiredDep := buildRouterDeployment(
			&cluster, depName, cmName, combo.Image,
			cluster.Spec.Router.Replicas,
			cluster.Spec.Router.Resources,
		)
		if err := r.upsert(ctx, &cluster, desiredDep); err != nil {
			return r.handleUpsertErr(ctx, err, "router Deployment", logger)
		}

		// router Deployment 도 cache propagation 지연을 graceful 처리.
		var observed appsv1.Deployment
		var observedReady int32
		if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: depName}, &observed); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to re-read router Deployment for status", "name", depName)
				return ctrl.Result{}, err
			}
		} else {
			observedReady = observed.Status.ReadyReplicas
		}
		routerStatus = &postgresv1alpha1.ClusterRouterStatus{
			Replicas:      cluster.Spec.Router.Replicas,
			ReadyReplicas: observedReady,
			Endpoint:      fmt.Sprintf("%s.%s.svc.cluster.local:%d", svcName, cluster.Namespace, pgPort),
		}
	}

	// 3. status 종합.
	cluster.Status.Shards = shardStatuses
	cluster.Status.Router = routerStatus
	cluster.Status.ObservedGeneration = cluster.Generation
	applyClusterConditions(&cluster, shardCount, allShardPrimaryReady, routerActive, routerStatus)

	if err := r.Status().Update(ctx, &cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to update PostgresCluster status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// applyClusterConditions 는 reconcile 산출물 (shard 준비 상태, router 활성/준비
// 상태) 를 RFC 0001 §3.4 Condition 카탈로그 + ClusterPhase 로 변환하여 cluster
// 객체에 직접 기록한다.
func applyClusterConditions(
	cluster *postgresv1alpha1.PostgresCluster,
	shardCount int32,
	allShardPrimaryReady, routerActive bool,
	routerStatus *postgresv1alpha1.ClusterRouterStatus,
) {
	conds := &cluster.Status.Conditions

	if allShardPrimaryReady && shardCount > 0 {
		setCondition(conds, ConditionShardsReady, metav1.ConditionTrue, ReasonAvailable,
			fmt.Sprintf("%d/%d shard primary ready", shardCount, shardCount))
	} else {
		setCondition(conds, ConditionShardsReady, metav1.ConditionFalse, ReasonProgressing,
			"waiting for shard primary readiness")
	}

	routerReady := !routerActive ||
		(routerStatus != nil && routerStatus.Replicas > 0 &&
			routerStatus.ReadyReplicas == cluster.Spec.Router.Replicas)
	switch {
	case !routerActive:
		setCondition(conds, ConditionRouterReady, metav1.ConditionTrue, ReasonNotApplicable,
			"router disabled (shardingMode=none or router.enabled=false)")
	case routerReady:
		setCondition(conds, ConditionRouterReady, metav1.ConditionTrue, ReasonAvailable,
			fmt.Sprintf("%d/%d router replicas ready", routerStatus.ReadyReplicas, routerStatus.Replicas))
	default:
		setCondition(conds, ConditionRouterReady, metav1.ConditionFalse, ReasonProgressing,
			"waiting for router readiness")
	}

	clusterReady := allShardPrimaryReady && shardCount > 0 && routerReady
	if clusterReady {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
		setCondition(conds, ConditionReady, metav1.ConditionTrue, ReasonAvailable, "all subsystems ready")
		setCondition(conds, ConditionProgressing, metav1.ConditionFalse, ReasonAvailable, "reconcile reached steady state")
	} else {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseProvisioning
		setCondition(conds, ConditionReady, metav1.ConditionFalse, ReasonProgressing, "reconcile in progress")
		setCondition(conds, ConditionProgressing, metav1.ConditionTrue, ReasonReconciling, "creating or waiting for subresources")
	}
}

// upsert 는 owner reference 부착 후 CreateOrUpdate 로 desired 객체를 적용한다.
// desired 는 ObjectMeta + Spec 이 채워진 새 객체이며, 기존 객체가 있으면 Spec 만
// 덮어쓰고 ResourceVersion / Status 는 보존된다.
func (r *PostgresClusterReconciler) upsert(ctx context.Context, owner *postgresv1alpha1.PostgresCluster, desired client.Object) error {
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference: %w", err)
	}
	// CreateOrUpdate 는 desired 의 포인터에 기존 객체 metadata 를 채워넣은 뒤
	// mutator 안에서 spec 을 덮어쓴다. mutator 진입 시점에 desired.Spec 이
	// 의도한 값이므로, 기존 객체를 새로 fetch 한 뒤 다시 그 spec 을 desired 의
	// spec 으로 교체하는 패턴을 사용한다.
	desiredCopy := desired.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, desired, func() error {
		// CreateOrUpdate 가 fetch 후 mutator 안에서 desired 객체에 기존 metadata
		// (ResourceVersion 등) 를 채워준다. 우리는 desiredCopy 의 spec 을 기준으로
		// 강제 동기화한다 — 기존 spec 변경분 (사용자/admission 의 mutation) 은
		// 무시되며, desired 가 단일 진실이다.
		copySpec(desired, desiredCopy)
		// SetControllerReference 는 기존 객체에 이미 owner ref 가 있을 수 있으므로
		// 멱등성 위해 재호출.
		return controllerutil.SetControllerReference(owner, desired, r.Scheme)
	})
	return err
}

// copySpec 은 src 의 Spec 필드를 dst 로 복사한다 (현재 지원: ConfigMap/Service/
// StatefulSet/Deployment). 다른 타입이 들어오면 panic — F01b 에서 호출 가능한
// 타입은 4 종 뿐이므로 명시적으로 타입을 좁혀 잘못된 사용을 빠르게 발견한다.
func copySpec(dst, src client.Object) {
	switch d := dst.(type) {
	case *corev1.ConfigMap:
		s := src.(*corev1.ConfigMap)
		d.Data = s.Data
		d.BinaryData = s.BinaryData
		d.Labels = s.Labels
	case *corev1.Service:
		s := src.(*corev1.Service)
		// ClusterIP 는 immutable 이므로 기존 값 보존 (CreateOrUpdate 가 이미 채워둠).
		// Selector, Ports, Type 만 desired 로 동기화.
		d.Spec.Selector = s.Spec.Selector
		d.Spec.Ports = s.Spec.Ports
		d.Spec.Type = s.Spec.Type
		d.Labels = s.Labels
	case *appsv1.StatefulSet:
		s := src.(*appsv1.StatefulSet)
		d.Spec.Replicas = s.Spec.Replicas
		d.Spec.Template = s.Spec.Template
		d.Spec.ServiceName = s.Spec.ServiceName
		// Selector / VolumeClaimTemplates 는 immutable — Create 시점에만 채워짐.
		if d.Spec.Selector == nil {
			d.Spec.Selector = s.Spec.Selector
		}
		if len(d.Spec.VolumeClaimTemplates) == 0 {
			d.Spec.VolumeClaimTemplates = s.Spec.VolumeClaimTemplates
		}
		d.Labels = s.Labels
	case *appsv1.Deployment:
		s := src.(*appsv1.Deployment)
		d.Spec.Replicas = s.Spec.Replicas
		d.Spec.Template = s.Spec.Template
		if d.Spec.Selector == nil {
			d.Spec.Selector = s.Spec.Selector
		}
		d.Labels = s.Labels
	case *corev1.ServiceAccount:
		s := src.(*corev1.ServiceAccount)
		// ServiceAccount 는 spec 이 거의 비어 있음 — Labels 만 동기화.
		d.Labels = s.Labels
	case *rbacv1.Role:
		s := src.(*rbacv1.Role)
		d.Rules = s.Rules
		d.Labels = s.Labels
	case *rbacv1.RoleBinding:
		s := src.(*rbacv1.RoleBinding)
		// RoleRef 는 immutable — 기존 객체 RoleRef 그대로. Subjects 만 desired.
		d.Subjects = s.Subjects
		if d.RoleRef.Kind == "" {
			d.RoleRef = s.RoleRef
		}
		d.Labels = s.Labels
	default:
		panic(fmt.Sprintf("copySpec: unsupported type %T", dst))
	}
}

// handleUpsertErr 는 upsert 실패를 일관된 형태로 처리한다 (conflict → requeue).
func (r *PostgresClusterReconciler) handleUpsertErr(_ context.Context, err error, what string, logger logSink) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	logger.Error(err, "upsert failed", "resource", what)
	return ctrl.Result{}, err
}

// logSink 는 controller-runtime logger 의 좁은 인터페이스다 — handleUpsertErr 는
// Error 만 사용하므로 의존을 최소화한다.
type logSink interface {
	Error(err error, msg string, keysAndValues ...any)
}

// SetupWithManager 는 본 reconciler 를 controller-runtime Manager 에 등록한다.
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
