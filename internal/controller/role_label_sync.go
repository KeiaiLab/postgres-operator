/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// InstanceRoleLabelKey는 각 PG Pod의 *현재* primary/replica 역할을 반영하는 live
// label이다. internal/controller/failover/pvc_fence_runbook.go의
// PVCFenceMountedPod.InstanceRole 설계 및 docs/runbooks/pvc-fence.md §5.2가
// 이미 참조해 온 키다.
//
// RwRoutingRoleLabelKey는 라이브 postgres-prod 클러스터에서 실측 확인된
// (2026-07-11, kubectl get svc/pod 직접 조회) rw Service selector가 실제로
// 소비하는 키다 — `kubectl get svc postgres-prod-shard-0-rw -o
// jsonpath='{.spec.selector}'`가 `postgres.keiailab.io/role: primary`를
// 반환했고, 죽은 shard-0-0 Pod에도 동일 키로 `role=primary`가 붙어 있었다.
//
// 본 repo의 Go 코드 및 git 전체 히스토리(`git log -S 'postgres.keiailab.io/role"'
// --all`)를 조사했으나 이 키를 primary/replica 값으로 사용하는 Service
// builder나 label patch 코드는 발견되지 않았다 — 유일한 등장은 tls.go의 무관한
// `"server-tls"` 값이다. 즉 그 rw Service와 `role` label은 이 repo *밖*
// (운영/GitOps 레이어)에서 관리되고 있고, 그 레이어 역시 failover 시 라벨을
// 갱신하지 않는다는 것이 사고의 실체다. repo 안에서 그 외부 메커니즘의 정확한
// 위치를 확정할 수 없으므로, 두 키를 함께 동기화하는 것이 안전한 선택이다
// (instance-role만 갱신하면 실측된 실제 소비자인 role 키가 계속 stale로
// 남는다).
//
// #226 (2026-07-11 INC): instance-role 키 자체도 오래 전부터 fencing 결정
// 로직 + 운영 runbook에서 참조돼 왔고 한때 e2e 테스트도 이 label로 primary를
// selector했지만, 실제로 Pod에 patch하는 코드는 존재한 적이 없었다 — commit
// 65d5819가 그 gap("PG Pod 에 부착되지 않는 label")을 발견해 e2e를 CR-status
// 기반으로 우회시켰을 뿐, 근본 원인(아무도 patch하지 않음)은 미해결로 남아
// 있었다. 그 결과 failover가 CR status/lease 수준에서는 완료돼도, 죽은
// 노드의 구 primary Pod가 stuck Terminating 상태로 label을 영원히 들고 있는
// 동안 새 primary는 label을 받지 못해 rw Service의 endpoints가 0개가 되는
// 사고로 이어졌다. 본 파일이 그 gap을 채운다.
const (
	InstanceRoleLabelKey     = "postgres.keiailab.io/instance-role"
	RwRoutingRoleLabelKey    = "postgres.keiailab.io/role"
	InstanceRoleLabelPrimary = "primary"
	InstanceRoleLabelReplica = "replica"
)

// primaryRoleLabelKeys는 reconcilePrimaryRoleLabels가 각 Pod에 동기화하는 전체
// label key 목록이다 — 현재 알려진 소비자(내부 문서화된 instance-role +
// 라이브 실측된 rw Service의 role) 둘 다.
var primaryRoleLabelKeys = []string{InstanceRoleLabelKey, RwRoutingRoleLabelKey}

// shardAuthority 는 샤드 하나의 rw 권위다 — 런타임 CM 의 primary 와 그것이 실제 primary 인지
// (§3.2). rw 라벨·-primary Service·PrimaryEndpointConsistent 가 같은 값을 쓴다.
//
//	CM PRIMARY_ENDPOINT ─▶ primary ─ checkPrimary ─┬▶ confirmed → rw 라벨 · -primary Service
//	                                               └▶ 그 밖     → rw 라벨 0(쓰기 fail-closed) · Warning
type shardAuthority struct {
	shard       string       // "shard-0" · "t1"
	members     []string     // rw 라벨을 맞출 멤버 파드
	endpoint    string       // CM PRIMARY_ENDPOINT — 빈 문자열이면 모름(CM 없음)
	primary     string       // endpoint 의 파드
	check       primaryCheck // primary 가 실제 primary 인가
	initialized bool         // CM CLUSTER_INITIALIZED — 첫 primary 전의 비확인은 이견이 아니다
	rivals      []string     // primary 밖에서 신선·unfenced Role=Primary 를 보고하는 멤버
	observed    string       // aggregate 가 고른 primary
}

// writable 은 rw 라벨·-primary Service 를 받을 파드다 — 실제 primary 로 확인된 CM 파드, 아니면 빈 문자열.
func (a *shardAuthority) writable() string {
	if a.check != primaryConfirmed {
		return ""
	}
	return a.primary
}

// disagreement 는 rw 권위가 흔들리는 이유다 — 빈 문자열이면 CM 파드가 실제 primary 이고 이견이 없다.
func (a *shardAuthority) disagreement() string {
	var why []string
	switch {
	case a.endpoint == "":
		why = append(why, "runtime primary unknown")
	case a.check != primaryConfirmed && a.initialized:
		why = append(why, fmt.Sprintf("runtime primary %s %s", a.primary, a.check))
	}
	if len(a.rivals) > 0 {
		why = append(why, strings.Join(a.rivals, " ")+" also primary")
	}
	if a.primary != "" && a.observed != "" && a.observed != a.primary && !slices.Contains(a.rivals, a.observed) {
		why = append(why, "observed primary "+a.observed)
	}
	return strings.Join(why, ", ")
}

// primaryAuthorities 는 샤드마다 rw 권위를 정한다. 복제 클러스터는 런타임 CM 이 없어 관측된
// Ready primary 가 권위다(무변).
func (r *PostgresClusterReconciler) primaryAuthorities(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) []shardAuthority {
	if standaloneReplicaEnabled(cluster) {
		return observedAuthorities(shards)
	}

	// fence 를 못 읽으면(nil) 어떤 CM 파드도 확인하지 않는다 — unfenced 를 증명할 수 없다.
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		log.FromContext(ctx).Info("fence state unknown; runtime primaries left unconfirmed", "error", err)
	}
	now := time.Now()
	out := make([]shardAuthority, 0, len(shards))
	for i := range shards {
		out = append(out, r.runtimeAuthority(ctx, cluster, &shards[i], fenced, now))
	}
	return out
}

// runtimeAuthority 는 런타임 CM 샤드 하나의 rw 권위다 — CM 이 없으면 권위도 없다(rw 라벨 0).
func (r *PostgresClusterReconciler) runtimeAuthority(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ss *postgresv1alpha1.ShardStatus,
	fenced map[string]bool,
	now time.Time,
) shardAuthority {
	ref := shardRefFor(cluster, ss)
	a := shardAuthority{shard: ref.name, check: primaryUnknown}
	if ss.Primary != nil {
		a.observed = ss.Primary.Pod
	}
	logger := log.FromContext(ctx).WithValues("shard", ref.name)
	pods, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		logger.Info("shard members unknown; rw labels left as they are", "error", err)
		return a
	}
	a.members = make([]string, 0, len(pods))
	for i := range pods {
		a.members = append(a.members, pods[i].Name)
	}

	rt, found, err := r.readRuntime(ctx, cluster, ref.name)
	if err != nil {
		logger.Info("runtime ConfigMap unreadable; rw labels withheld", "error", err)
	}
	if !found {
		return a
	}
	a.endpoint = rt.PrimaryEndpoint
	a.primary = endpointPod(rt.PrimaryEndpoint)
	a.initialized = rt.ClusterInitialized
	a.rivals = rivalPrimaries(pods, a.primary, fenced, now)
	if fenced != nil {
		a.check = r.checkPrimary(ctx, podNamed(pods, a.primary), fenced, now)
	}
	return a
}

// observedAuthorities 는 복제 클러스터의 권위다 — 관측된 Ready primary(런타임 CM 없음).
func observedAuthorities(shards []postgresv1alpha1.ShardStatus) []shardAuthority {
	var out []shardAuthority
	for i := range shards {
		p := shards[i].Primary
		if p == nil || p.Pod == "" || !p.Ready {
			continue
		}
		out = append(out, shardAuthority{shard: shards[i].Name, endpoint: p.Endpoint, primary: p.Pod, check: primaryConfirmed})
	}
	return out
}

// rivalPrimaries 는 primary 밖에서 신선·unfenced Role=Primary 를 보고하는 멤버다(이름순).
func rivalPrimaries(pods []corev1.Pod, primary string, fenced map[string]bool, now time.Time) []string {
	var out []string
	for i := range pods {
		if pods[i].Name == primary || !claimsPrimary(&pods[i], fenced, now) {
			continue
		}
		out = append(out, pods[i].Name)
	}
	slices.Sort(out)
	return out
}

// podNamed 는 이름이 name 인 멤버다(없으면 nil).
func podNamed(pods []corev1.Pod, name string) *corev1.Pod {
	for i := range pods {
		if pods[i].Name == name {
			return &pods[i]
		}
	}
	return nil
}

// reportAuthorities 는 rw 권위가 흔들린 샤드를 condition PrimaryEndpointConsistent 와 Warning
// PrimaryEndpointDisagrees 로 보인다 — CM 은 쓰지 않는다(aggregate 불일치만으로는 발행 0, §3.2).
func (r *PostgresClusterReconciler) reportAuthorities(
	cluster *postgresv1alpha1.PostgresCluster,
	authorities []shardAuthority,
) {
	conds := &cluster.Status.Conditions
	if standaloneReplicaEnabled(cluster) || hibernationRequested(cluster) || restoreInProgress(cluster) {
		setCondition(conds, ConditionPrimaryEndpointConsistent, metav1.ConditionTrue, ReasonNotApplicable,
			"no runtime primary: replica cluster or database pods stopped", cluster.Generation)
		return
	}

	var shaken []string
	for i := range authorities {
		why := authorities[i].disagreement()
		if why == "" {
			continue
		}
		shaken = append(shaken, authorities[i].shard+": "+why)
		commonsevents.EmitWarningf(r.Recorder, cluster, ReasonPrimaryEndpointDisagrees,
			"shard %s: %s", authorities[i].shard, why)
	}
	if len(shaken) == 0 {
		setCondition(conds, ConditionPrimaryEndpointConsistent, metav1.ConditionTrue, ReasonPrimaryEndpointAgrees,
			"every runtime primary is the actual primary", cluster.Generation)
		return
	}
	setCondition(conds, ConditionPrimaryEndpointConsistent, metav1.ConditionFalse, ReasonPrimaryEndpointDisagrees,
		strings.Join(shaken, "; "), cluster.Generation)
}

// reconcilePrimaryRoleLabels는 샤드마다 rw 라벨(primaryRoleLabelKeys 전체)을 런타임 CM 파드에만
// 단다 — 그 파드가 실제 primary 로 확인될 때만(§3.2). 아니면 rw 라벨 0 — 쓰기는 fail-closed 다.
// aggregate 가 고른 primary 는 따르지 않는다: primary 가 둘이면 목록 순서대로 요동한다(DS-2).
// 멤버를 먼저 replica 로 내리고 권위 파드를 마지막에 올린다 — rw 라벨이 둘인 순간이 없다.
//
// #226: standby cleanup/재시딩(reconcileStaleReplicas/reconcileRoguePrimaries)
// 보다 *먼저*, 그리고 *독립적으로* 호출되어야 한다 — 그래야 죽은 노드의 pod
// delete가 영원히 hang해도(kubelet 무응답) rw 트래픽 라우팅은 항상 런타임 CM이
// 가리키는 실제 primary로 수렴한다. 강등 대상 Pod가 이미
// Terminating이어도 label patch 자체는 성공한다 — Kubernetes는 실제 삭제
// (finalizer 해소 + etcd 제거) 완료 전까지 metadata patch를 허용하므로, 이
// 함수는 재시딩 delete의 진행 상태와 무관하게 항상 label을 correct 상태로
// 되돌릴 수 있다.
//
// 반환값 primaryLabelsSynced는 실제 primary 로 확인된 *모든* CM 파드가
// primaryRoleLabelKeys 전체를 실제로 보유하게 됐는지를 보고한다 — F3
// (ConditionPrimaryRoleSynced)가 이 값으로 "primary 는 확인됐는데 라벨/라우팅은
// 아직 수렴하지 않음"이라는 모순을 노출한다. 확인된 primary가 없는 shard는
// 집계에서 제외한다(sync할 대상이 없다 — 그 불일치는 PrimaryEndpointConsistent 가 보인다).
//
// best-effort: 개별 Pod patch 실패(NotFound 포함)는 첫 에러만 반환하고 계속
// 진행한다 — 한 shard/pod의 실패가 다른 shard의 label 수렴을 막지 않는다.
func (r *PostgresClusterReconciler) reconcilePrimaryRoleLabels(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	authorities []shardAuthority,
) (primaryLabelsSynced bool, err error) {
	primaryLabelsSynced = true
	var firstErr error
	keep := func(labelErr error, role, pod string) {
		if labelErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("label %s pod %q: %w", role, pod, labelErr)
		}
	}

	for i := range authorities {
		primary := authorities[i].writable()
		for _, pod := range authorities[i].members {
			if pod == primary {
				continue
			}
			keep(r.setPrimaryRoleLabels(ctx, cluster.Namespace, pod, InstanceRoleLabelReplica), InstanceRoleLabelReplica, pod)
		}
		if primary == "" {
			continue
		}
		labelErr := r.setPrimaryRoleLabels(ctx, cluster.Namespace, primary, InstanceRoleLabelPrimary)
		if labelErr != nil {
			primaryLabelsSynced = false
		}
		keep(labelErr, InstanceRoleLabelPrimary, primary)
	}
	return primaryLabelsSynced, firstErr
}

// setPrimaryRoleLabels patches every key in primaryRoleLabelKeys on the named
// Pod to role in a single Get+Patch — no-op (zero API calls beyond the Get) if
// all keys already carry the desired value. An already fully-deleted Pod has
// nothing left to label, which is treated as success.
func (r *PostgresClusterReconciler) setPrimaryRoleLabels(
	ctx context.Context,
	namespace, podName, role string,
) error {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	needsPatch := false
	for _, key := range primaryRoleLabelKeys {
		if pod.Labels[key] != role {
			needsPatch = true
			break
		}
	}
	if !needsPatch {
		return nil
	}
	before := pod.DeepCopy()
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	for _, key := range primaryRoleLabelKeys {
		pod.Labels[key] = role
	}
	return r.Patch(ctx, &pod, client.MergeFrom(before))
}
