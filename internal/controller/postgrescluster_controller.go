/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonspvc "github.com/keiailab/operator-commons/pkg/pvc"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	statusPollInterval = 5 * time.Second

	// failoverDebounceThreshold 는 자동 failover 를 발동하기 전 primary 실패가
	// 지속되어야 하는 최소 시간이다. statusPollInterval(5s) 의 ~1.5배로, 실패가
	// 최소 2회의 reconcile 관측에 걸쳐 지속되어야 promotion 이 실행된다 — sub-second
	// status flicker 는 걸러지고, 진짜 실패는 ~10s 내 promote 된다.
	failoverDebounceThreshold = 8 * time.Second

	// AnnotationHibernation 은 CloudNativePG 의 선언형 하이버네이션 스위치와
	// 같은 annotation 이다. cnpg.io/hibernation=on 이면 database Pod 를 0개로
	// 줄이고 PVC 소유권은 재수화를 위해 보존한다.
	AnnotationHibernation = "cnpg.io/hibernation"
)

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
	Recorder events.EventRecorder

	// PromotionPodExecutor 는 controller-layer failover promotion 이 replica Pod
	// 안의 postgres container 로 실행할 pods/exec 경로다. nil 이면 SetupWithManager
	// 가 manager rest.Config 기반 production executor 를 주입한다.
	PromotionPodExecutor BackupSidecarExecutor

	// failoverPending 은 자동 failover 를 debounce 한다 — primary 실패가
	// failoverDebounceThreshold 동안 *지속* 되어야 executeClusterPromotion 이 실행된다.
	// 이는 standby join 중 일시적 Primary==nil 같은 single-reconcile status flicker 가
	// 가짜 promotion 을 유발(→ fenceNonTargetMembers 로 건강한 멤버를 fence)하는 것을
	// 막는다 (#220 라이브 드릴 RCA). namespace/name 키. in-memory 로 충분하다:
	// operator 는 단일 leader-elected replica 이고, 재시작은 window 를 보수적으로
	// 재시작한다.
	failoverPending   map[string]time.Time
	failoverPendingMu sync.Mutex
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=imagecatalogs;clusterimagecatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresusers,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;patch;update;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 PostgresCluster CR 변화에 반응한다.
//
//nolint:gocyclo // 33 cyclomatic — 단일 reconcile 의 step-by-step 직관성 우위. helper 분해는 별 cycle.
func (r *PostgresClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (rresult ctrl.Result, rerr error) {
	logger := log.FromContext(ctx).WithValues("postgrescluster", req.NamespacedName)

	// SLO observability — reconcile latency Histogram.
	MetricReconcileTotal.WithLabelValues(req.Namespace, req.Name).Inc()
	timer := prometheus.NewTimer(prometheus.ObserverFunc(func(v float64) {
		result := "success"
		if rerr != nil {
			result = "error"
		}
		MetricReconcileLatency.WithLabelValues(req.Namespace, req.Name, result).Observe(v)
	}))
	defer timer.ObserveDuration()

	var cluster postgresv1alpha1.PostgresCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			DeleteMetricsFor(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch PostgresCluster")
		return ctrl.Result{}, err
	}

	pgVersion := imageMajorFromSpec(&cluster)

	combo, ok := lookupCombo(pgVersion, r.FeatureGates)
	if !ok {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonVersionRejected,
			fmt.Sprintf("PG=%q is not in supported matrix (or feature gate missing)", pgVersion))
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseDegraded
		cluster.Status.ObservedGeneration = cluster.Generation
		// RFC-0017 §3.4: version rejection 운영 가시 Event.
		if r.Recorder != nil {
			r.Recorder.Eventf(&cluster, nil, corev1.EventTypeWarning, ReasonVersionRejected, ReasonVersionRejected,
				"PG=%q is not in supported matrix (or feature gate missing)", pgVersion)
		}
		if err := r.Status().Update(ctx, &cluster); err != nil {
			logger.Error(err, "Failed to update status with version rejection")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	resolvedImage, err := r.resolvePostgresImage(ctx, &cluster, combo)
	if err != nil {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonImageCatalogRejected, err.Error())
		setCondition(&cluster.Status.Conditions, ConditionProgressing, metav1.ConditionFalse, ReasonImageCatalogRejected,
			"image catalog reference rejected before creating database pods")
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseDegraded
		cluster.Status.ObservedGeneration = cluster.Generation
		if r.Recorder != nil {
			r.Recorder.Eventf(&cluster, nil, corev1.EventTypeWarning, ReasonImageCatalogRejected, ReasonImageCatalogRejected, "%v", err)
		}
		if statusErr := r.Status().Update(ctx, &cluster); statusErr != nil {
			logger.Error(statusErr, "Failed to update status with image catalog rejection")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}
	replicaBootstrap, err := replicaBootstrapConfigForCluster(&cluster)
	if err != nil {
		setCondition(&cluster.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonReplicaClusterRejected, err.Error())
		setCondition(&cluster.Status.Conditions, ConditionProgressing, metav1.ConditionFalse, ReasonReplicaClusterRejected,
			"replica cluster reference rejected before creating database pods")
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseDegraded
		cluster.Status.ObservedGeneration = cluster.Generation
		if r.Recorder != nil {
			r.Recorder.Eventf(&cluster, nil, corev1.EventTypeWarning, ReasonReplicaClusterRejected, ReasonReplicaClusterRejected, "%v", err)
		}
		if statusErr := r.Status().Update(ctx, &cluster); statusErr != nil {
			logger.Error(statusErr, "Failed to update status with replica cluster rejection")
			return ctrl.Result{}, statusErr
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
	hibernating := hibernationRequested(&cluster)
	desiredMembers := members
	if hibernating {
		desiredMembers = 0
	}
	shardStatuses := make([]postgresv1alpha1.ShardStatus, 0, shardCount)
	allShardPrimaryReady := true

	for ord := range shardCount {
		cmName := ShardConfigMapName(cluster.Name, ord)
		svcName := ShardServiceName(cluster.Name, ord)
		stsName := ShardStatefulSetName(cluster.Name, ord)

		cm := buildConfigMap(&cluster, cmName, "shard", ord, r.Plugins)
		configHash := postgresConfigHash(cm.Data)
		if err := r.upsert(ctx, &cluster, cm); err != nil {
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
		if replicaBootstrap != nil {
			primaryEndpoint = replicaBootstrap.Endpoint
		} else if !hibernating && int(ord) < len(cluster.Status.Shards) {
			if p := cluster.Status.Shards[ord].Primary; p != nil {
				primaryEndpoint = p.Endpoint
			}
		}
		desiredSTS := buildPGStatefulSet(
			&cluster, stsName, svcName,
			ord,
			resolvedImage.Image, cmName, resolvedImage.PostgresMajor,
			desiredMembers,
			cluster.Spec.Shards.Storage, cluster.Spec.Shards.Resources,
			primaryEndpoint,
			configHash,
			"", // reshardTargetID: ordinal shard (격리 label 미사용)
		)
		if err := r.upsert(ctx, &cluster, desiredSTS); err != nil {
			return r.handleUpsertErr(ctx, &cluster, err, "shard StatefulSet", logger)
		}

		// shard PDB (PR #31): members>=2 시 자동 생성.
		if !hibernating && shouldAutoCreatePDB(members) {
			pdb := BuildShardPDB(&cluster, ord, members)
			if err := r.upsert(ctx, &cluster, pdb); err != nil {
				return r.handleUpsertErr(ctx, &cluster, err, "shard PDB", logger)
			}
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
		if hibernating {
			shardStatuses = append(shardStatuses, postgresv1alpha1.ShardStatus{
				Name:    fmt.Sprintf("shard-%d", ord),
				Ordinal: ord,
			})
			continue
		}
		// RFC 0006 R2 — Pod annotation 기반 live aggregation. 우선 시도 후
		// 결과가 비면 STS readyReplicas 기반 fallback (annotation 부재 시).
		shardStat := aggregateShardStatus(ctx, r.Client, &cluster, ord, svcName)
		if shardStat.Primary == nil || shardStat.Primary.Pod == "" {
			// fallback — STS-time 근사값 (annotation 미수집 / Pod 부팅 전 일시).
			// Ready 는 fallbackPrimaryReady 로 산출한다: STS readyReplicas proxy
			// 는 HA shard 에서 standby readiness 까지 합산하므로, primary 가 죽고
			// standby 만 Ready 인 상황을 Ready=true 로 마스킹해 DetectPrimaryFailure
			// 를 ReasonNone 으로 만들어 자동 failover 를 영영 막았다 (live RCA
			// 2026-06-04 pg-ha-drill cordon chaos). Ready replica 가 관측되면
			// primary 부재 = outage 로 보고 Ready=false 를 강제한다.
			// #220: preserve the LAST-KNOWN primary (previous reconcile) rather than
			// hardcoding ordinal-0. After a failover the primary is ord-1; hardcoding
			// ord-0 makes the StatefulSet PRIMARY_ENDPOINT flicker back to ord-0 during
			// a transient status lapse, so a reseeded former primary boots with env=self
			// and initdb's itself into a rogue primary (oscillation). On first bootstrap
			// no previous primary exists → falls through to ord-0 (#218/#219 unchanged).
			fbPod := fmt.Sprintf("%s-0", stsName)
			fbEndpoint := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:%d", stsName, svcName, cluster.Namespace, pgPort)
			for k := range cluster.Status.Shards {
				prev := &cluster.Status.Shards[k]
				if prev.Ordinal == ord && prev.Primary != nil && prev.Primary.Pod != "" {
					fbPod = prev.Primary.Pod
					if prev.Primary.Endpoint != "" {
						fbEndpoint = prev.Primary.Endpoint
					}
					break
				}
			}
			shardStat.Primary = &postgresv1alpha1.ShardEndpoint{
				Pod:      fbPod,
				Endpoint: fbEndpoint,
				Ready:    fallbackPrimaryReady(primaryReady, shardStat.Replicas),
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
		routerReplicas := cluster.Spec.Router.Replicas
		if hibernating {
			routerReplicas = 0
		}
		desiredDep := buildRouterDeployment(
			&cluster, depName, cmName, resolvedImage.Image,
			routerReplicas,
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
			Replicas:      routerReplicas,
			ReadyReplicas: observedReady,
			Endpoint:      fmt.Sprintf("%s.%s.svc.cluster.local:%d", svcName, cluster.Namespace, pgPort),
		}
	}

	// 2.5. PVC online expansion (PR #33): Spec.Shards.Storage.Size 증가 시
	// 기존 PVC 직접 patch.
	stsNamesForResize := make([]string, 0, shardCount)
	for ord := range shardCount {
		stsNamesForResize = append(stsNamesForResize, ShardStatefulSetName(cluster.Name, ord))
	}
	if err := commonspvc.ExpandDataPVCs(ctx, r.Client, cluster.Namespace, stsNamesForResize, cluster.Spec.Shards.Storage.Size); err != nil {
		logger.Error(err, "PVC resize failed (best-effort, reconcile 계속)")
	}

	// 3. status 종합.
	prevPhase := cluster.Status.Phase
	cluster.Status.Shards = shardStatuses
	cluster.Status.Router = routerStatus
	managedRolesStatus, err := r.managedRolesStatus(ctx, &cluster)
	if err != nil {
		logger.Error(err, "Failed to aggregate managed role status")
		return ctrl.Result{}, err
	}
	cluster.Status.ManagedRolesStatus = managedRolesStatus
	cluster.Status.ObservedGeneration = cluster.Generation
	// Switchover: annotation-triggered planned primary change (Sprint S5).
	if !hibernating && allShardPrimaryReady {
		if err := r.handleSwitchover(ctx, &cluster, shardStatuses); err != nil {
			logger.Error(err, "Switchover failed")
			if r.Recorder != nil {
				r.Recorder.Eventf(&cluster, nil, corev1.EventTypeWarning, "SwitchoverFailed", "SwitchoverFailed", "%v", err)
			}
		}
	}

	failoverShardName, failoverDecision := clusterFailoverDecision(shardStatuses)
	// 가짜 promotion 차단 (#220 라이브 드릴 RCA): 실패가 debounce window 동안 지속될
	// 때만 promote. 일시적 status flicker 는 fenceNonTargetMembers 를 통해 건강한 멤버를
	// fence 할 수 있으므로 instantaneous 트리거 금지.
	failureDetected := failoverDecision.Failed && failoverDecision.PromotionCandidate != nil
	clusterWasReady := prevPhase == postgresv1alpha1.ClusterPhaseReady
	if r.shouldPromoteAfterDebounce(cluster.Namespace+"/"+cluster.Name, failureDetected, clusterWasReady, time.Now()) {
		// #220 failback guard: never re-promote a fenced candidate (a known-failed
		// primary) while an unfenced member is serving — otherwise the promoter's
		// unfenceTargetPVC defeats the fence and a returned old primary re-takes on
		// a stale timeline, rewinding away post-failover writes.
		skipFenced, ferr := r.shouldSkipFencedCandidate(ctx, cluster.Namespace, failoverDecision.PromotionCandidate.Pod)
		if ferr != nil {
			logger.Error(ferr, "fenced-candidate check failed; proceeding with promotion",
				"shard", failoverShardName, "candidate", failoverDecision.PromotionCandidate.Pod)
		}
		if skipFenced {
			logger.Info("skipping promotion: candidate PVC is fenced (known-failed primary) while an unfenced member is serving (#220 failback guard)",
				"shard", failoverShardName, "candidate", failoverDecision.PromotionCandidate.Pod)
		} else if err := r.executeClusterPromotion(ctx, &cluster, failoverShardName, failoverDecision); err != nil {
			logger.Error(err, "Failed to execute failover promotion",
				"shard", failoverShardName, "pod", failoverDecision.PromotionCandidate.Pod)
			if r.Recorder != nil {
				r.Recorder.Eventf(&cluster, nil, corev1.EventTypeWarning, "FailoverPromotionFailed", "FailoverPromotionFailed",
					"shard=%q pod=%q: %v", failoverShardName, failoverDecision.PromotionCandidate.Pod, err)
			}
			failoverDecision.Message = fmt.Sprintf("%s; promotion execution failed: %v", failoverDecision.Message, err)
		}
	}
	// #205: re-seed any standby that failed to rejoin (not-ready too long with a
	// ready primary, e.g. stuck in startup recovery after a primary restart).
	// Best-effort — log and continue.
	if !hibernating {
		if err := r.reconcileStaleReplicas(ctx, &cluster, shardStatuses, time.Now()); err != nil {
			logger.Error(err, "stale standby re-seed failed (best-effort)")
		}
		// #220 clean-rejoin: reseed any rogue old primary (booted empty from a stale
		// env after failback) into a clean standby of the real promoted primary.
		if err := r.reconcileRoguePrimaries(ctx, &cluster, shardStatuses); err != nil {
			logger.Error(err, "rogue primary re-seed failed (best-effort)")
		}
	}
	applyClusterConditions(&cluster, shardCount, allShardPrimaryReady, routerActive, routerStatus, hibernating,
		prevPhase == postgresv1alpha1.ClusterPhaseReady, failoverDecision)

	// Config hot-reload: if cluster is Ready and primary Pods are running with
	// a stale configHash, signal PostgreSQL to reload without restarting.
	if !hibernating && allShardPrimaryReady {
		for _, ss := range shardStatuses {
			if ss.Primary != nil && ss.Primary.Ready && ss.Primary.Pod != "" {
				if err := r.reloadPostgresConfig(ctx, cluster.Namespace, ss.Primary.Pod); err != nil {
					logger.V(1).Info("pg_reload_conf best-effort failed", "pod", ss.Primary.Pod, "err", err)
				}
			}
		}
	}

	// RFC-0017 §3.4: Phase 가 *최초 Ready 도달* 시점에만 Event 발행 (idempotent —
	// 매 reconcile noise 회피). prevPhase 비교로 transition 감지.
	if r.Recorder != nil && cluster.Status.Phase == postgresv1alpha1.ClusterPhaseReady && prevPhase != postgresv1alpha1.ClusterPhaseReady {
		r.Recorder.Eventf(&cluster, nil, corev1.EventTypeNormal, "ClusterReady", "ClusterReady",
			"PostgresCluster %d/%d shards primary ready, router=%v", shardCount, shardCount, routerActive)
	}

	if err := r.Status().Update(ctx, &cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to update PostgresCluster status")
		return ctrl.Result{}, err
	}
	ObservePostgresClusterMetrics(&cluster)

	return ctrl.Result{RequeueAfter: statusPollInterval}, nil
}

func hibernationRequested(cluster *postgresv1alpha1.PostgresCluster) bool {
	if cluster == nil || cluster.Annotations == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cluster.Annotations[AnnotationHibernation]), "on")
}

// applyClusterConditions 는 reconcile 산출물 (shard 준비 상태, router 활성/준비
// 상태) 를 RFC 0001 §3.4 Condition 카탈로그 + ClusterPhase 로 변환하여 cluster
// 객체에 직접 기록한다.
func applyClusterConditions(
	cluster *postgresv1alpha1.PostgresCluster,
	shardCount int32,
	allShardPrimaryReady, routerActive bool,
	routerStatus *postgresv1alpha1.ClusterRouterStatus,
	hibernating bool,
	wasReady bool,
	failoverDecision failover.Decision,
) {
	conds := &cluster.Status.Conditions
	if hibernating {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseHibernated
		setCondition(conds, ConditionHibernation, metav1.ConditionTrue, ReasonHibernated,
			"Cluster has been hibernated")
		setCondition(conds, ConditionShardsReady, metav1.ConditionFalse, ReasonHibernated,
			"database pods intentionally stopped; PVCs retained")
		if routerActive {
			setCondition(conds, ConditionRouterReady, metav1.ConditionFalse, ReasonHibernated,
				"router replicas intentionally scaled to zero during hibernation")
		} else {
			setCondition(conds, ConditionRouterReady, metav1.ConditionTrue, ReasonNotApplicable,
				"router disabled (shardingMode=none or router.enabled=false)")
		}
		setCondition(conds, ConditionFailoverReady, metav1.ConditionFalse, ReasonHibernated,
			"failover suspended while cluster is hibernated")
		setCondition(conds, ConditionReady, metav1.ConditionFalse, ReasonHibernated,
			"cluster hibernated; database pods intentionally stopped")
		setCondition(conds, ConditionProgressing, metav1.ConditionFalse, ReasonHibernated,
			"hibernate steady state reached")
		return
	}
	setCondition(conds, ConditionHibernation, metav1.ConditionFalse, ReasonNotHibernated,
		"Cluster is not hibernated")

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

	failoverDegraded := wasReady && failoverDecision.Failed
	if failoverDegraded {
		message := failoverDecision.Message
		if failoverDecision.PromotionCandidate != nil {
			message = fmt.Sprintf("%s; promotion candidate=%s", message, failoverDecision.PromotionCandidate.Pod)
		}
		setCondition(conds, ConditionFailoverReady, metav1.ConditionFalse, string(failoverDecision.Reason), message)
	} else {
		setCondition(conds, ConditionFailoverReady, metav1.ConditionTrue, ReasonAvailable,
			"no failover action required")
	}

	clusterReady := allShardPrimaryReady && shardCount > 0 && routerReady
	if failoverDegraded {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseDegraded
		setCondition(conds, ConditionReady, metav1.ConditionFalse, string(failoverDecision.Reason), failoverDecision.Message)
		setCondition(conds, ConditionProgressing, metav1.ConditionFalse, ReasonAvailable, "primary failure detected after Ready")
	} else if clusterReady {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
		setCondition(conds, ConditionReady, metav1.ConditionTrue, ReasonAvailable, "all subsystems ready")
		setCondition(conds, ConditionProgressing, metav1.ConditionFalse, ReasonAvailable, "reconcile reached steady state")
	} else {
		cluster.Status.Phase = postgresv1alpha1.ClusterPhaseProvisioning
		setCondition(conds, ConditionReady, metav1.ConditionFalse, ReasonProgressing, "reconcile in progress")
		setCondition(conds, ConditionProgressing, metav1.ConditionTrue, ReasonReconciling, "creating or waiting for subresources")
	}
}

// shouldPromoteAfterDebounce 는 자동 failover 를 sustained-failure window 뒤로
// gate 한다. primary 실패가 failoverDebounceThreshold 동안 *연속* 관측되어야 true 를
// 반환한다 — single-reconcile status flicker(가짜 promotion 의 근원, fenceNonTargetMembers
// 와 결합 시 건강한 멤버 fence)를 걸러낸다. failed=false 는 window 를 clear 한다.
// in-memory map 외에는 순수 함수라 gate 로직을 라이브 클러스터 없이 unit test 가능하다.
// reconcile 은 statusPollInterval(5s) 주기 requeue + Owns(STS)/Pod watch 로 window
// 동안 재평가된다.
//
// canStart 은 window *시작* 만 gate 한다(클러스터가 실패 최초 관측 시점에 Ready 였어야
// 함 — 기존 prevPhase==Ready 가드 미러). 일단 시작되면, 실패가 phase 를 Ready 밖으로
// 떨어뜨린 뒤에도 후속 reconcile 이 window 를 *이어간다*. canStart 을 매 reconcile
// 평가하면 failure 가 첫 reconcile 직후 phase 를 떨어뜨려 window 가 영영 누적되지
// 못한다(라이브 드릴 RCA).
func (r *PostgresClusterReconciler) shouldPromoteAfterDebounce(key string, failed, canStart bool, now time.Time) bool {
	r.failoverPendingMu.Lock()
	defer r.failoverPendingMu.Unlock()
	if !failed {
		delete(r.failoverPending, key)
		return false
	}
	if r.failoverPending == nil {
		r.failoverPending = map[string]time.Time{}
	}
	first, ok := r.failoverPending[key]
	if !ok {
		if !canStart {
			return false
		}
		r.failoverPending[key] = now
		return false
	}
	return now.Sub(first) >= failoverDebounceThreshold
}

func clusterFailoverDecision(shards []postgresv1alpha1.ShardStatus) (string, failover.Decision) {
	for _, shard := range shards {
		decision := failover.DetectPrimaryFailure(shard)
		if decision.Failed {
			return shard.Name, decision
		}
	}
	return "", failover.Decision{Reason: failover.ReasonNone}
}

// fallbackPrimaryReady 는 어떤 Pod 도 primary role 을 보고하지 않을 때 합성하는
// fallback primary endpoint 의 Ready 값을 결정한다. STS readyReplicas proxy 는
// genuine early boot (아직 Ready replica 0) 구간에만 신뢰한다. Ready replica 가
// 하나라도 관측되면 — primary 가 보고되지 않는데 standby 는 살아있는 — 실제
// primary outage 이므로 Ready=false 를 반환해 DetectPrimaryFailure 가 자동
// failover 를 발동하게 한다 (live RCA 2026-06-04 pg-ha-drill: STS readyReplicas
// 가 standby 를 합산해 primary outage 를 가렸다). 부팅 중 standby 가 primary
// annotation 보다 먼저 Ready 가 되는 false-positive 는 reconcile 의
// prevPhase==Ready 게이트가 걸러낸다 (steady state 도달 전엔 promote 안 함).
func fallbackPrimaryReady(stsReadyProxy bool, replicas []postgresv1alpha1.ShardEndpoint) bool {
	return stsReadyProxy && !hasReadyReplica(replicas)
}

// hasReadyReplica 는 replica 목록에 Ready=true 가 하나라도 있는지 반환한다.
func hasReadyReplica(replicas []postgresv1alpha1.ShardEndpoint) bool {
	for i := range replicas {
		if replicas[i].Ready {
			return true
		}
	}
	return false
}

func (r *PostgresClusterReconciler) managedRolesStatus(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
) (*postgresv1alpha1.ManagedRolesStatus, error) {
	var users postgresv1alpha1.PostgresUserList
	if err := r.List(ctx, &users, client.InNamespace(cluster.Namespace)); err != nil {
		return nil, err
	}
	status := managedRolesStatusForUsers(cluster, users.Items)
	return &status, nil
}

func managedRolesStatusForUsers(
	cluster *postgresv1alpha1.PostgresCluster,
	users []postgresv1alpha1.PostgresUser,
) postgresv1alpha1.ManagedRolesStatus {
	const (
		roleStatusReserved              = "reserved"
		roleStatusReconciled            = "reconciled"
		roleStatusPendingReconciliation = "pending-reconciliation"
	)

	status := postgresv1alpha1.ManagedRolesStatus{
		ByStatus: map[string][]string{
			roleStatusReserved: {"postgres", "streaming_replica"},
		},
		CannotReconcile: map[string][]string{},
		PasswordStatus:  map[string]postgresv1alpha1.ManagedRolePasswordStatus{},
	}

	for _, user := range users {
		if user.Namespace != cluster.Namespace || user.Spec.Cluster.Name != cluster.Name || user.Spec.Name == "" {
			continue
		}
		roleName := user.Spec.Name
		bucket := roleStatusPendingReconciliation
		if user.Status.Applied && user.Status.ObservedGeneration == user.Generation {
			bucket = roleStatusReconciled
		}
		status.ByStatus[bucket] = append(status.ByStatus[bucket], roleName)

		if !user.Status.Applied && user.Status.Message != "" {
			status.CannotReconcile[roleName] = []string{user.Status.Message}
		}
		if user.Status.PasswordSecretResourceVersion != "" {
			status.PasswordStatus[roleName] = postgresv1alpha1.ManagedRolePasswordStatus{
				SecretResourceVersion: user.Status.PasswordSecretResourceVersion,
				ObservedGeneration:    user.Status.ObservedGeneration,
			}
		}
	}

	for state := range status.ByStatus {
		sort.Strings(status.ByStatus[state])
	}
	if len(status.CannotReconcile) == 0 {
		status.CannotReconcile = nil
	}
	if len(status.PasswordStatus) == 0 {
		status.PasswordStatus = nil
	}
	return status
}

func (r *PostgresClusterReconciler) postgresClustersForUser(
	_ context.Context,
	obj client.Object,
) []reconcile.Request {
	user, ok := obj.(*postgresv1alpha1.PostgresUser)
	if !ok || user.Spec.Cluster.Name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: user.Namespace,
			Name:      user.Spec.Cluster.Name,
		},
	}}
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
	case *policyv1.PodDisruptionBudget:
		// PR #31 + fix: PDB upsert 지원. Spec 전체 복사 (Selector + MinAvailable).
		s := src.(*policyv1.PodDisruptionBudget)
		d.Spec = s.Spec
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
		log.FromContext(context.TODO()).Error(
			fmt.Errorf("copySpec: unsupported type %T", dst),
			"BUG: unknown object type in copySpec — skipping spec copy",
		)
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
		r.Recorder.Eventf(cluster, nil, corev1.EventTypeWarning, "UpsertFailed", "UpsertFailed",
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
		// events API 마이그레이션 완료 (RFC-0023 Phase 2, 2026-05-11).
		r.Recorder = mgr.GetEventRecorder("postgrescluster-controller")
	}
	if r.PromotionPodExecutor == nil {
		executor, err := NewKubernetesBackupSidecarExecutor(mgr.GetConfig())
		if err != nil {
			return err
		}
		r.PromotionPodExecutor = executor
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.PostgresCluster{}).
		Watches(&postgresv1alpha1.PostgresUser{},
			handler.EnqueueRequestsFromMapFunc(r.postgresClustersForUser),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&postgresv1alpha1.ImageCatalog{},
			handler.EnqueueRequestsFromMapFunc(r.postgresClustersForImageCatalog),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&postgresv1alpha1.ClusterImageCatalog{},
			handler.EnqueueRequestsFromMapFunc(r.postgresClustersForClusterImageCatalog),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("postgrescluster").
		Complete(r)
}

// reloadPostgresConfig executes SELECT pg_reload_conf() on the ready primary Pod
// to apply postgresql.conf/pg_hba.conf changes without restarting the Pod.
// This enables hot-reload for parameters that support SIGHUP-level changes.
func (r *PostgresClusterReconciler) reloadPostgresConfig(ctx context.Context, namespace, podName string) error {
	if r.PromotionPodExecutor == nil {
		return nil
	}
	target := BackupSidecarTarget{
		Namespace: namespace,
		Pod:       podName,
		Container: "postgres",
	}
	out, err := r.PromotionPodExecutor.Exec(ctx, target, []string{
		"psql", "-U", "postgres", "-d", "postgres", "-tAc", "SELECT pg_reload_conf()",
	})
	if err != nil {
		return fmt.Errorf("pg_reload_conf on %s: %w (output: %s)", podName, err, string(out))
	}
	return nil
}
