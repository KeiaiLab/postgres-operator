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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

const statusPollInterval = 5 * time.Second

// PostgresClusterReconciler 는 PostgresCluster CR 을 reconcile 한다.
type PostgresClusterReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Plugins *plugin.Registry

	// FeatureGates 는 PG18 같은 격리 채널 활성화 결정에 사용된다.
	// nil 이면 빈 맵으로 취급 (기본 비활성).
	FeatureGates map[string]bool

	// Recorder 는 K8s Event 발행 (kubectl describe 의 Events 표시) 용. RFC-0017
	// §3.4. SetupWithManager 가 자동 주입 — cmd/main.go 측에서는 명시 setting
	// 불필요. nil 이면 Eventf 호출이 panic — Setup 호출 보장 의무.
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 PostgresCluster CR 변화에 반응한다.
//
//nolint:gocyclo // 33 cyclomatic — 단일 reconcile 의 step-by-step 직관성 우위. helper 분해는 별 cycle.
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
		// RFC-0017 §3.4: version rejection 운영 가시 Event.
		if r.Recorder != nil {
			r.Recorder.Eventf(&cluster, corev1.EventTypeWarning, ReasonVersionRejected,
				"PG=%q is not in supported matrix (or feature gate missing)", pgVersion)
		}
		if err := r.Status().Update(ctx, &cluster); err != nil {
			logger.Error(err, "Failed to update status with version rejection")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 0. instance manager 가 사용할 RBAC (ServiceAccount + Role + RoleBinding) upsert.
	// shard StatefulSet 보다 먼저 — Pod 가 SA reference 를 사용하므로 fail-fast 회피.
	if name, err := r.reconcileInstanceRBAC(ctx, &cluster); err != nil {
		return r.handleUpsertErr(ctx, &cluster, err, name, logger)
	}

	// 0.5. (Pillar P7 §7) TLS reconcile — Certificate CR upsert.
	if err := r.reconcileTLS(ctx, &cluster); err != nil {
		return r.handleUpsertErr(ctx, &cluster, err, "tls Certificate", logger)
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
			return r.handleUpsertErr(ctx, &cluster, err, "shard ConfigMap", logger)
		}
		if err := r.upsert(ctx, &cluster, buildHeadlessService(&cluster, svcName, "shard", ord)); err != nil {
			return r.handleUpsertErr(ctx, &cluster, err, "shard Service", logger)
		}
		// primaryEndpoint 결정: 이전 reconcile 에서 관측된 primary 가 존재하면
		// 그 endpoint 를 init container 로 전달 → ord!=0 의 첫 부팅 시 pg_basebackup
		// path 를 활성화한다. 없으면 빈 값 — bootstrap script 가 ord==0 또는 endpoint
		// 부재일 때 자동으로 initdb path 로 fallback.
		primaryEndpoint := ""
		if int(ord) < len(cluster.Status.Shards) {
			if p := cluster.Status.Shards[ord].Primary; p != nil {
				primaryEndpoint = p.Endpoint
			}
		}
		desiredSTS := buildPGStatefulSet(
			&cluster, stsName, svcName,
			ord,
			combo.Image, cmName, combo.PostgresMajor,
			members,
			cluster.Spec.Shards.Storage, cluster.Spec.Shards.Resources,
			primaryEndpoint,
		)
		if err := r.upsert(ctx, &cluster, desiredSTS); err != nil {
			return r.handleUpsertErr(ctx, &cluster, err, "shard StatefulSet", logger)
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
		// RFC 0006 R2 — Pod annotation 기반 live aggregation. 우선 시도 후
		// 결과가 비면 STS readyReplicas 기반 fallback (annotation 부재 시).
		shardStat := aggregateShardStatus(ctx, r.Client, &cluster, ord, svcName)
		if shardStat.Primary == nil || shardStat.Primary.Pod == "" {
			// fallback — STS-time 근사값 (annotation 미수집 / Pod 부팅 전 일시).
			shardStat.Primary = &postgresv1alpha1.ShardEndpoint{
				Pod:      fmt.Sprintf("%s-0", stsName),
				Endpoint: fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:%d", stsName, svcName, cluster.Namespace, pgPort),
				Ready:    primaryReady,
			}
		}
		shardStatuses = append(shardStatuses, shardStat)
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
			return r.handleUpsertErr(ctx, &cluster, err, "router ConfigMap", logger)
		}
		if err := r.upsert(ctx, &cluster, buildClientService(&cluster, svcName, "router")); err != nil {
			return r.handleUpsertErr(ctx, &cluster, err, "router Service", logger)
		}
		// router 이미지: P12-T2 까지 PG 베이스 이미지 placeholder.
		desiredDep := buildRouterDeployment(
			&cluster, depName, cmName, combo.Image,
			cluster.Spec.Router.Replicas,
			cluster.Spec.Router.Resources,
		)
		if err := r.upsert(ctx, &cluster, desiredDep); err != nil {
			return r.handleUpsertErr(ctx, &cluster, err, "router Deployment", logger)
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
	prevPhase := cluster.Status.Phase
	cluster.Status.Shards = shardStatuses
	cluster.Status.Router = routerStatus
	cluster.Status.ObservedGeneration = cluster.Generation
	applyClusterConditions(&cluster, shardCount, allShardPrimaryReady, routerActive, routerStatus)

	// RFC-0017 §3.4: Phase 가 *최초 Ready 도달* 시점에만 Event 발행 (idempotent —
	// 매 reconcile noise 회피). prevPhase 비교로 transition 감지.
	if r.Recorder != nil && cluster.Status.Phase == postgresv1alpha1.ClusterPhaseReady && prevPhase != postgresv1alpha1.ClusterPhaseReady {
		r.Recorder.Eventf(&cluster, corev1.EventTypeNormal, "ClusterReady",
			"PostgresCluster %d/%d shards primary ready, router=%v", shardCount, shardCount, routerActive)
	}

	if err := r.Status().Update(ctx, &cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to update PostgresCluster status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: statusPollInterval}, nil
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

// reconcileTLS 는 Pillar P7 §7 의 cert-manager Certificate CR upsert 를 처리한다.
// TLS 미활성 시 (cluster.Spec.TLS == nil 또는 enabled=false 또는 IssuerRef 누락)
// no-op 으로 즉시 반환 — Phase 3a 의 STS volume mount + Phase 3b 의 ssl=on 도
// tlsEnabled() 동일 결정값을 공유하므로 일관 동작.
//
// 본 helper 가 Reconcile 의 cyclomatic complexity 분리 (gocyclo < 30 baseline 정합)
// + 후속 Phase (cert renewal observability, Issuer auto self-signed, mTLS client
// auth) 의 단일 진입점.
// reconcileInstanceRBAC 는 instance manager (postgres pod) 가 사용할 SA + Role +
// RoleBinding 3종을 단일 진입점에서 upsert. 첫 실패 자원 이름을 반환하여 caller
// 가 handleUpsertErr 로 condition 메시지 표기 — Reconcile 의 cyclomatic
// complexity 절감 (gocyclo < 30 baseline).
func (r *PostgresClusterReconciler) reconcileInstanceRBAC(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
) (string, error) {
	specs := []struct {
		name string
		obj  client.Object
	}{
		{"instance ServiceAccount", buildInstanceServiceAccount(cluster)},
		{"instance Role", buildInstanceRole(cluster)},
		{"instance RoleBinding", buildInstanceRoleBinding(cluster)},
	}
	for _, s := range specs {
		if err := r.upsert(ctx, cluster, s.obj); err != nil {
			return s.name, err
		}
	}
	return "", nil
}

func (r *PostgresClusterReconciler) reconcileTLS(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
) error {
	cert := buildCertificate(cluster)
	if cert == nil {
		return nil
	}
	return r.upsert(ctx, cluster, cert)
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
	case *unstructured.Unstructured:
		// cert-manager Certificate CR (Phase 2) — unstructured.Unstructured 로 emit.
		// 전체 spec map 을 desired 로 덮어쓰기 (DeepCopy 후 fetch 된 metadata 보존).
		s := src.(*unstructured.Unstructured)
		if spec, found, err := unstructured.NestedMap(s.Object, "spec"); err == nil && found {
			_ = unstructured.SetNestedField(d.Object, spec, "spec")
		}
		d.SetLabels(s.GetLabels())
	default:
		panic(fmt.Sprintf("copySpec: unsupported type %T", dst))
	}
}

// handleUpsertErr 는 upsert 실패를 일관된 형태로 처리한다 (conflict → requeue).
//
// RFC-0017 §3.4: conflict 가 *아닌* 실패에 대해서만 K8s Event(Warning) 를
// 발행한다 — conflict 는 정상 requeue 동작이므로 운영 noise 회피. Recorder 가
// nil 이면 Eventf 호출을 skip (테스트 환경 보호).
func (r *PostgresClusterReconciler) handleUpsertErr(
	_ context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	err error, what string,
	logger logSink,
) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	logger.Error(err, "upsert failed", "resource", what)
	if r.Recorder != nil && cluster != nil {
		r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "UpsertFailed",
			"resource=%q: %v", what, err)
	}
	return ctrl.Result{}, err
}

// logSink 는 controller-runtime logger 의 좁은 인터페이스다 — handleUpsertErr 는
// Error 만 사용하므로 의존을 최소화한다.
type logSink interface {
	Error(err error, msg string, keysAndValues ...any)
}

// SetupWithManager 는 본 reconciler 를 controller-runtime Manager 에 등록한다.
func (r *PostgresClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// RFC-0017 §3.4: EventRecorder 자동 주입. 이름 "postgrescluster-controller" 는
	// kubectl describe 의 Events Source.Component 에 표시된다.
	if r.Recorder == nil {
		//lint:ignore SA1019 client-go record.EventRecorder 유지. events.k8s.io 전환은
		// Recorder field migration 과 함께 별 cycle (mongodb ADR-0022 동일 패턴).
		r.Recorder = mgr.GetEventRecorderFor("postgrescluster-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.PostgresCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("postgrescluster").
		Complete(r)
}
