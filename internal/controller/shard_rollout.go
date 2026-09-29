/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 샤드 단계화 롤아웃이다(설계 2026-09-25 §3.16, ADR-0033). StatefulSet 은 OnDelete 라 템플릿이
// 바뀌어도 STS 컨트롤러가 멤버를 재시작하지 않는다 — 멤버를 지우는 주체는 오퍼레이터 하나다.
//
//	옛 replica 하나 삭제(streaming 아닌 것 → 서수 높은 것) ─ STS 가 새 revision 으로 다시 만든다
//	  … 대상 밖 replica 가 전부 Ready ∧ streaming 일 때만 다음 삭제
//	옛 replica 0 ∧ primary 가 옛 ─▶ startSwitchover(Rollout) ─ 옛 primary 는 제자리 standby 가 된다
//	  … 옛 revision replica 가 된 옛 primary 를 같은 규칙으로 교체
//	전 파드 해시 = updateRevision ∧ updatedReplicas = replicas ─▶ ShardRolloutCompleted
//
// 선결이 memberConvergeBudget 넘게 안 서면 RolloutBlocked(+ Warning 한 번), 서면 RolloutClear 다. streaming 은
// 복제 그래프(pg_stat_replication)다 — readyz 가 아니다. switchover 는 재구축 예산을 쓰지 않는다.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	// memberConvergeBudget 은 롤아웃 다음 단계의 선결을 기다리는 상한이다 — 넘으면 RolloutBlocked(부록 A).
	// 옛 primary switchover 가 primary 를 못 바꿨을 때 다시 여는 간격이기도 하다.
	memberConvergeBudget = staleStandbyReseedTimeout

	// rolloutProbeTimeout 은 primary 설정 exec 1회의 상한이다.
	rolloutProbeTimeout = statusPollInterval

	// walKeepSizeSetting 은 첫 단계 전에 primary 와 렌더를 맞춰 보는 설정이다. 기본 단위는 MB 다.
	walKeepSizeSetting = "wal_keep_size"
	walKeepSizeUnit    = 1 << 20

	// connectionSlotsFull 은 연결 슬롯이 없을 때 postgres FATAL 의 일부다 — 그런 exec 는 모름이다.
	connectionSlotsFull = "too many clients"

	shardRolloutStepReason      = "ShardRolloutStep"
	shardRolloutCompletedReason = "ShardRolloutCompleted"
	shardRolloutBlockedReason   = "ShardRolloutBlocked"
)

// rolloutStep 은 롤아웃 단계다.
type rolloutStep string

const (
	rolloutReplica rolloutStep = "replica" // 옛 replica 삭제
	rolloutPrimary rolloutStep = "primary" // 옛 primary 의 switchover(멤버가 하나면 삭제)
)

// rolloutOutcome 은 rollout_steps_total 의 결과다.
type rolloutOutcome string

const (
	rolloutStarted rolloutOutcome = "started" // 삭제 · switchover 시작을 실행했다
	rolloutStalled rolloutOutcome = "blocked" // 선결이 memberConvergeBudget 넘게 안 섰다
)

var (
	rolloutSteps    = []rolloutStep{rolloutReplica, rolloutPrimary}
	rolloutOutcomes = []rolloutOutcome{rolloutStarted, rolloutStalled}
)

// hotStandbySettings 는 standby 값이 primary 값 이상이어야 복구가 도는 설정이다(PG hot standby) — 렌더가
// primary 보다 작으면 새 revision standby 가 멈춘다(I24).
var hotStandbySettings = []string{
	"max_connections", "max_worker_processes", "max_wal_senders", "max_prepared_transactions", "max_locks_per_transaction",
}

// settingUnits 는 PG 메모리 단위의 바이트 배수다(1024 진법).
var settingUnits = map[string]int64{"B": 1, "kB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}

// settingPattern 은 렌더한 설정 값이다 — 정수와 단위(예: 5GB · 5120 · 64).
var settingPattern = regexp.MustCompile(`^([0-9]+)\s*([A-Za-z]*)$`)

// rolloutSettingsQuery 는 primary 의 롤아웃 설정이다(이름|현재값 — pg_settings 기본 단위).
var rolloutSettingsQuery = func() string {
	names := make([]string, 0, 1+len(hotStandbySettings))
	for _, name := range append([]string{walKeepSizeSetting}, hotStandbySettings...) {
		names = append(names, sqlLiteral(name))
	}
	return "SELECT name, setting FROM pg_settings WHERE name IN (" + strings.Join(names, ", ") + ")"
}()

// rolloutMark 는 샤드 롤아웃의 기억이다(메모리 — 오퍼레이터가 다시 뜨면 예산 시계를 새로 잰다).
type rolloutMark struct {
	revision string      // 롤아웃 중인 updateRevision
	at       time.Time   // 마지막 단계(없으면 롤아웃을 처음 본) 시각 — memberConvergeBudget 기준
	step     rolloutStep // 마지막 단계 — 비면 이 revision 에서 아직 단계 전
	member   string      // 마지막 단계에서 지운(또는 switchover 한) 멤버
	uid      types.UID   // 그 파드 — 캐시에 아직 보이면 다시 만들어지기 전이다
	blocked  string      // RolloutBlocked 사유 — 비었다 찰 때 Warning 한 번
	detail   string      // 막힌 까닭
}

// rolloutHold 는 다음 단계를 막는 선결이다 — reason 은 RolloutBlocked 사유다.
type rolloutHold struct {
	reason string
	detail string
}

// rolloutPlan 은 한 걸음의 결정이다 — hold 가 차면 기다린다.
type rolloutPlan struct {
	step   rolloutStep
	member *corev1.Pod
	hold   rolloutHold
}

// held 는 plan 을 막는다.
func (p rolloutPlan) held(reason, detail string) rolloutPlan {
	p.hold = rolloutHold{reason: reason, detail: detail}
	return p
}

// advanceRollouts 는 샤드마다 단계화 롤아웃을 한 걸음 옮기고 RolloutBlocked 를 싣는다 — 그래프·재구축·switchover
// 다음에 부른다.
func (r *PostgresClusterReconciler) advanceRollouts(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	graphs map[string]replicationGraph,
	now time.Time,
) {
	logger := log.FromContext(ctx)
	initRolloutSeries(cluster, shards)
	for i := range shards {
		ref := shardRefFor(cluster, &shards[i])
		sts, err := r.shardStatefulSet(ctx, cluster, ref.sts)
		if err != nil {
			logger.Info("shard rollout skipped", "shard", ref.name, "error", err)
			continue
		}
		pods, err := r.shardMembers(ctx, cluster, ref.member)
		if err != nil {
			logger.Info("shard rollout skipped", "shard", ref.name, "error", err)
			continue
		}
		if err := r.advanceRollout(ctx, cluster, shards[i], sts, pods, graphs[shards[i].Name], now); err != nil {
			logger.Info("shard rollout step failed", "shard", ref.name, "error", err)
		}
	}
	r.setRolloutCondition(cluster, shards)
}

// advanceRollout 은 샤드 하나를 한 걸음 옮긴다(§3.16) — 끝났으면 한 번 알리고, 선결이 서면 다음 단계를
// 하고, 안 서면 예산을 잰다.
func (r *PostgresClusterReconciler) advanceRollout(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard postgresv1alpha1.ShardStatus,
	sts *appsv1.StatefulSet,
	pods []corev1.Pod,
	g replicationGraph,
	now time.Time,
) error {
	key := cluster.Namespace + "/" + cluster.Name + "/" + shard.Name
	if !rolloutManaged(sts) {
		r.forgetRollout(key)
		return nil
	}
	pending := MetricRolloutPendingMembers.WithLabelValues(cluster.Namespace, cluster.Name, shard.Name)
	pending.Set(float64(pendingMembers(sts, pods)))
	if rolloutComplete(sts, pods) {
		r.completeRollout(cluster, shard.Name, key, sts)
		return nil
	}

	mark := r.markRollout(key, sts.Status.UpdateRevision, now)
	plan := r.planRollout(ctx, cluster, shard, sts, pods, g, mark, now)
	if plan.hold.reason != "" {
		r.holdRollout(cluster, shard.Name, key, plan, now)
		return nil
	}
	return r.stepRollout(ctx, cluster, shard.Name, key, sts, plan, now)
}

// planRollout 은 다음 단계와 그 선결이다(§3.16) — 막히면 hold 에 사유를 싣는다.
func (r *PostgresClusterReconciler) planRollout(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard postgresv1alpha1.ShardStatus,
	sts *appsv1.StatefulSet,
	pods []corev1.Pod,
	g replicationGraph,
	mark rolloutMark,
	now time.Time,
) rolloutPlan {
	rt, found, err := r.readRuntime(ctx, cluster, shard.Name)
	if err != nil || !found {
		return rolloutPlan{step: rolloutReplica}.held(ReasonMemberNotConverged, "runtime ConfigMap unreadable")
	}
	cmPrimary := endpointPod(rt.PrimaryEndpoint)
	plan := rolloutPlan{step: pendingStep(sts, pods, cmPrimary)}

	// 재구축·switchover 가 도는 샤드의 멤버는 건드리지 않는다.
	if detail := shardBusy(cluster, shard.Name, now); detail != "" {
		return plan.held(ReasonRebuildActive, detail)
	}

	// 그래프가 전부를 알고 CM primary 가 실제 primary 여야 한다 — 모르면 연결 슬롯부터 가른다.
	primary := podNamed(pods, cmPrimary)
	if g.root != rt.PrimaryEndpoint || !g.known || primary == nil {
		return plan.held(r.unknownGraph(ctx, cmPrimary, primary))
	}

	plan, hold := nextStep(cluster, sts, pods, g, primary, mark, now)
	if hold.reason != "" {
		return plan.held(hold.reason, hold.detail)
	}

	// primary 설정 — 첫 단계 전 wal_keep_size = 렌더 · hot standby 키 감소 0.
	logger := log.FromContext(ctx).WithValues("shard", shard.Name)
	running, err := r.primarySettings(ctx, primary)
	if err != nil {
		logger.V(1).Info("primary settings unknown", "pod", primary.Name, "error", err)
		return plan.held(settingsHold(primary.Name, err))
	}
	rendered, err := r.renderedSettings(ctx, sts)
	if err != nil {
		logger.Info("rendered settings unreadable", "error", err)
		return plan.held(ReasonPrimaryConfigPending, "rendered settings unreadable")
	}
	if want, ok := rendered[walKeepSizeSetting]; ok && mark.step == "" && running[walKeepSizeSetting] != want {
		return plan.held(ReasonPrimaryConfigPending, fmt.Sprintf("%s %s is %d bytes, rendered %d",
			primary.Name, walKeepSizeSetting, running[walKeepSizeSetting], want))
	}
	if keys := hotStandbyDecrease(rendered, running); len(keys) > 0 {
		return plan.held(ReasonParameterDecrease, "rendered below "+primary.Name+": "+strings.Join(keys, ","))
	}
	return plan
}

// nextStep 은 다음 단계와 그 멤버 선결이다 — 옛 replica 가 있으면 하나(대상 밖 replica 가 Ready ∧ streaming),
// 없고 primary 가 옛이면 primary(standby 전원이 streaming — 수렴).
func nextStep(
	cluster *postgresv1alpha1.PostgresCluster,
	sts *appsv1.StatefulSet,
	pods []corev1.Pod,
	g replicationGraph,
	primary *corev1.Pod,
	mark rolloutMark,
	now time.Time,
) (rolloutPlan, rolloutHold) {
	// 지운 파드가 캐시에 아직 보이면 기다린다 — 그 멤버가 다시 만들어지기 전에 다른 멤버를 지우지 않는다.
	if pod := podNamed(pods, mark.member); pod != nil && mark.uid != "" && pod.UID == mark.uid && mark.step == rolloutReplica {
		return rolloutPlan{step: rolloutReplica, member: pod}, rolloutHold{ReasonMemberNotConverged, mark.member + " not recreated yet"}
	}
	if replica, ok := pickReplica(outdatedMembers(sts, expectedMembers(sts, pods)), g); ok {
		return rolloutPlan{step: rolloutReplica, member: &replica}, laggingHold(sts, pods, g, primary.Name, replica.Name)
	}

	plan := rolloutPlan{step: rolloutPrimary, member: primary}
	if primary.Labels[appsv1.StatefulSetRevisionLabel] == sts.Status.UpdateRevision {
		return plan, rolloutHold{ReasonMemberNotConverged, "waiting for the StatefulSet to recreate members"}
	}
	if hold := laggingHold(sts, pods, g, primary.Name); hold.reason != "" {
		return plan, hold
	}
	if !converged(g, stsMembers(sts)) {
		return plan, rolloutHold{ReasonMemberNotConverged, "not every standby streams"}
	}
	if mark.step == rolloutPrimary && now.Sub(mark.at) < memberConvergeBudget {
		return plan, rolloutHold{ReasonMemberNotConverged, "switchover from " + primary.Name + " has not replaced the primary"}
	}
	if _, found, err := loadSwitchoverState(cluster); err != nil || found {
		return plan, rolloutHold{ReasonRebuildActive, "another switchover in progress"}
	}
	return plan, rolloutHold{}
}

// stepRollout 은 단계를 실행한다 — replica(와 standby 가 없는 primary)는 UID 조건부 삭제(STS 가 새 revision 으로
// 다시 만든다), primary 는 switchover(Rollout) 를 연다.
func (r *PostgresClusterReconciler) stepRollout(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, key string,
	sts *appsv1.StatefulSet,
	plan rolloutPlan,
	now time.Time,
) error {
	member, verb := plan.member, "replacing"
	if plan.step == rolloutPrimary && stsMembers(sts) > 1 {
		if err := r.startSwitchover(ctx, cluster, shard, "", switchoverRollout); err != nil {
			return err
		}
		verb = "switching over from"
	} else if err := r.Delete(ctx, member, client.Preconditions{UID: &member.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete outdated member %q: %w", member.Name, err)
	}

	r.recordStep(key, rolloutMark{
		revision: sts.Status.UpdateRevision, at: now, step: plan.step, member: member.Name, uid: member.UID,
	})
	MetricRolloutSteps.WithLabelValues(cluster.Namespace, cluster.Name, shard, string(plan.step), string(rolloutStarted)).Inc()
	commonsevents.Emitf(r.Recorder, cluster, shardRolloutStepReason, "shard %s: %s %s %s for revision %s",
		shard, verb, plan.step, member.Name, sts.Status.UpdateRevision)
	return nil
}

// holdRollout 은 선결이 안 선 걸음이다 — memberConvergeBudget 을 넘으면 막힘으로 적고, 막히기 시작할 때만
// Warning 과 blocked 를 센다.
func (r *PostgresClusterReconciler) holdRollout(
	cluster *postgresv1alpha1.PostgresCluster,
	shard, key string,
	plan rolloutPlan,
	now time.Time,
) {
	r.rolloutMu.Lock()
	mark := r.lastStepAt[key]
	over := now.Sub(mark.at) > memberConvergeBudget
	first := over && mark.blocked == ""
	if over {
		mark.blocked, mark.detail = plan.hold.reason, plan.hold.detail
		r.lastStepAt[key] = mark
	}
	r.rolloutMu.Unlock()
	if !first {
		return
	}
	MetricRolloutSteps.WithLabelValues(cluster.Namespace, cluster.Name, shard, string(plan.step), string(rolloutStalled)).Inc()
	commonsevents.EmitWarningf(r.Recorder, cluster, shardRolloutBlockedReason, "shard %s: %s step blocked over %s: %s (%s)",
		shard, plan.step, memberConvergeBudget, plan.hold.reason, plan.hold.detail)
}

// completeRollout 은 끝난 롤아웃을 한 번 알리고 기억을 지운다 — 이 프로세스가 진행을 본 revision 만 알린다.
func (r *PostgresClusterReconciler) completeRollout(
	cluster *postgresv1alpha1.PostgresCluster,
	shard, key string,
	sts *appsv1.StatefulSet,
) {
	r.rolloutMu.Lock()
	mark, tracked := r.lastStepAt[key]
	delete(r.lastStepAt, key)
	r.rolloutMu.Unlock()
	if !tracked || mark.revision != sts.Status.UpdateRevision {
		return
	}
	commonsevents.Emitf(r.Recorder, cluster, shardRolloutCompletedReason, "shard %s: all %d members on revision %s",
		shard, stsMembers(sts), sts.Status.UpdateRevision)
}

// markRollout 은 샤드 롤아웃의 기억이다 — 없거나 revision 이 바뀌었으면 지금부터 잰다.
func (r *PostgresClusterReconciler) markRollout(key, revision string, now time.Time) rolloutMark {
	r.rolloutMu.Lock()
	defer r.rolloutMu.Unlock()
	if r.lastStepAt == nil {
		r.lastStepAt = map[string]rolloutMark{}
	}
	mark, ok := r.lastStepAt[key]
	if !ok || mark.revision != revision {
		mark = rolloutMark{revision: revision, at: now}
		r.lastStepAt[key] = mark
	}
	return mark
}

// recordStep 은 실행한 단계를 적는다 — 막힘이 풀린다.
func (r *PostgresClusterReconciler) recordStep(key string, mark rolloutMark) {
	r.rolloutMu.Lock()
	defer r.rolloutMu.Unlock()
	r.lastStepAt[key] = mark
}

// forgetRollout 은 샤드 롤아웃의 기억을 지운다.
func (r *PostgresClusterReconciler) forgetRollout(key string) {
	r.rolloutMu.Lock()
	defer r.rolloutMu.Unlock()
	delete(r.lastStepAt, key)
}

// setRolloutCondition 은 막힌 샤드를 RolloutBlocked 로 보인다(KSM → O1 경보).
func (r *PostgresClusterReconciler) setRolloutCondition(
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) {
	var blocked []string
	reason := ""
	r.rolloutMu.Lock()
	for i := range shards {
		mark := r.lastStepAt[cluster.Namespace+"/"+cluster.Name+"/"+shards[i].Name]
		if mark.blocked == "" {
			continue
		}
		if reason == "" {
			reason = mark.blocked
		}
		blocked = append(blocked, fmt.Sprintf("%s=%s (%s)", shards[i].Name, mark.blocked, mark.detail))
	}
	r.rolloutMu.Unlock()
	if len(blocked) == 0 {
		setCondition(&cluster.Status.Conditions, ConditionRolloutBlocked, metav1.ConditionFalse,
			ReasonRolloutClear, "no shard rollout is blocked", cluster.Generation)
		return
	}
	setCondition(&cluster.Status.Conditions, ConditionRolloutBlocked, metav1.ConditionTrue,
		reason, "shard rollout blocked: "+strings.Join(blocked, "; "), cluster.Generation)
}

// shardBusy 는 샤드에 재구축이나 switchover 가 도는지다(까닭, 아니면 빈 문자열) — 재구축 상태를 못 읽으면
// 도는 것으로 본다(멤버를 안 지운다).
func shardBusy(cluster *postgresv1alpha1.PostgresCluster, shard string, now time.Time) string {
	state, err := loadRebuildState(cluster)
	if err != nil {
		return "member rebuild state unreadable"
	}
	for _, pod := range slices.Sorted(maps.Keys(state.Active)) {
		if state.Active[pod].Shard == shard {
			return "member rebuild of " + pod + " in progress"
		}
	}
	if switchoverActive(cluster, shard, now) {
		return "switchover in progress"
	}
	return ""
}

// unknownGraph 는 그래프를 모를 때의 사유다 — CM primary 가 연결 슬롯이 없어 exec 를 못 받으면 ConnectionSlots.
func (r *PostgresClusterReconciler) unknownGraph(ctx context.Context, name string, pod *corev1.Pod) (string, string) {
	if pod == nil {
		return ReasonMemberNotConverged, "primary " + name + " absent"
	}
	if _, err := r.primarySettings(ctx, pod); err != nil && strings.Contains(err.Error(), connectionSlotsFull) {
		return settingsHold(pod.Name, err)
	}
	return ReasonMemberNotConverged, "replication graph of " + name + " unknown"
}

// settingsHold 는 primary 설정을 못 읽은 사유다 — 연결 슬롯이 없으면 ConnectionSlots(모름).
func settingsHold(pod string, err error) (string, string) {
	if strings.Contains(err.Error(), connectionSlotsFull) {
		return ReasonConnectionSlots, pod + " has no free connection slot"
	}
	return ReasonPrimaryConfigPending, "settings of " + pod + " unknown"
}

// pendingStep 은 남은 단계의 종류다 — primary 밖에 옛 멤버가 있으면 replica 다.
func pendingStep(sts *appsv1.StatefulSet, pods []corev1.Pod, primary string) rolloutStep {
	for _, pod := range outdatedMembers(sts, expectedMembers(sts, pods)) {
		if pod.Name != primary {
			return rolloutReplica
		}
	}
	return rolloutPrimary
}

// laggingHold 는 skip 밖 멤버 중 Ready ∧ streaming 이 아닌 첫 멤버(서수 순)의 사유다 — 이미지를 못 받으면
// ImagePull. 모두 서면 빈 hold 다.
func laggingHold(sts *appsv1.StatefulSet, pods []corev1.Pod, g replicationGraph, skip ...string) rolloutHold {
	for _, name := range memberNames(sts) {
		if slices.Contains(skip, name) {
			continue
		}
		pod := podNamed(pods, name)
		if pod != nil && !kubernetesPodNotReady(pod) && g.streaming[name] {
			continue
		}
		if pod != nil && imagePulling(pod) {
			return rolloutHold{ReasonImagePull, name + " cannot pull its image"}
		}
		return rolloutHold{ReasonMemberNotConverged, name + " is not Ready and streaming"}
	}
	return rolloutHold{}
}

// primarySettings 는 primary 의 현재 설정이다 — wal_keep_size 는 바이트, 나머지는 정수.
func (r *PostgresClusterReconciler) primarySettings(ctx context.Context, pod *corev1.Pod) (map[string]int64, error) {
	if r.PromotionPodExecutor == nil {
		return nil, errors.New("pod executor is not configured")
	}
	// 죽은 노드의 파드에 exec 하면 시한까지 붙잡힌다.
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("%s is not running", pod.Name)
	}
	probeCtx, cancel := context.WithTimeout(ctx, rolloutProbeTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(probeCtx, memberTarget(pod),
		[]string{"psql", "-U", "postgres", "-d", "postgres", "-Atqc", rolloutSettingsQuery})
	if err != nil {
		return nil, err
	}

	running := map[string]int64{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, raw, ok := strings.Cut(strings.TrimSpace(line), psqlFieldSeparator)
		value, err := strconv.ParseInt(raw, 10, 64)
		if !ok || err != nil {
			return nil, fmt.Errorf("pg_settings row %q unreadable", line)
		}
		if name == walKeepSizeSetting {
			value *= walKeepSizeUnit
		}
		running[name] = value
	}
	return running, nil
}

// renderedSettings 는 멤버가 마운트하는 postgresql.conf(STS 템플릿의 pgConfigMountPath 볼륨)에서 렌더한 롤아웃
// 설정이다 — wal_keep_size 는 바이트, 나머지는 정수. 렌더하지 않은 키는 없다.
func (r *PostgresClusterReconciler) renderedSettings(ctx context.Context, sts *appsv1.StatefulSet) (map[string]int64, error) {
	name := configMapMounted(sts)
	if name == "" {
		return nil, errors.New("config volume absent")
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: name}, &cm); err != nil {
		return nil, err
	}

	conf := confSettings(cm.Data[path.Base(pgConfigFile)])
	rendered := map[string]int64{}
	if raw, ok := conf[walKeepSizeSetting]; ok {
		size, err := settingBytes(raw, walKeepSizeUnit)
		if err != nil {
			return nil, err
		}
		rendered[walKeepSizeSetting] = size
	}
	for _, key := range hotStandbySettings {
		raw, ok := conf[key]
		if !ok {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("rendered %s %q unreadable", key, raw)
		}
		rendered[key] = value
	}
	return rendered, nil
}

// configMapMounted 는 postgres 컨테이너가 pgConfigMountPath 에 마운트하는 ConfigMap 이름이다.
func configMapMounted(sts *appsv1.StatefulSet) string {
	spec := sts.Spec.Template.Spec
	volume := ""
	for _, c := range spec.Containers {
		if c.Name != pgContainerName {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.MountPath == pgConfigMountPath {
				volume = m.Name
			}
		}
	}
	for _, v := range spec.Volumes {
		if v.Name == volume && v.ConfigMap != nil {
			return v.ConfigMap.Name
		}
	}
	return ""
}

// confSettings 는 postgresql.conf 의 key = value 줄이다 — 작은따옴표는 벗기고, 주석 줄은 건너뛰고, 뒤 줄이 이긴다.
func confSettings(conf string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(conf, "\n") {
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.HasPrefix(key, "#") {
			continue
		}
		value = strings.TrimSpace(value)
		if quoted, found := strings.CutPrefix(value, "'"); found {
			value, _, _ = strings.Cut(quoted, "'")
		} else if i := strings.IndexAny(value, " \t#"); i >= 0 {
			value = value[:i]
		}
		out[key] = value
	}
	return out
}

// settingBytes 는 메모리 설정 값을 바이트로 읽는다 — 단위가 없으면 unit(GUC 기본 단위)이다(예: 5GB · 5120).
func settingBytes(raw string, unit int64) (int64, error) {
	m := settingPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0, fmt.Errorf("setting %q unreadable", raw)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	if m[2] == "" {
		return n * unit, nil
	}
	scale, ok := settingUnits[m[2]]
	if !ok {
		return 0, fmt.Errorf("setting %q has unknown unit", raw)
	}
	return n * scale, nil
}

// hotStandbyDecrease 는 렌더 값이 primary 현재값보다 작은 hot standby 필수 키다(I24) — 렌더하지 않은 키는 보지 않는다.
func hotStandbyDecrease(rendered, running map[string]int64) []string {
	var out []string
	for _, key := range hotStandbySettings {
		want, ok := rendered[key]
		have, known := running[key]
		if ok && known && want < have {
			out = append(out, key)
		}
	}
	return out
}

// converged 는 standby 전원(members−1)이 streaming 인지다 — 그래프가 전부를 알 때만.
func converged(g replicationGraph, members int32) bool {
	return g.root != "" && g.known && len(g.streaming) == int(members)-1
}

// pickReplica 는 먼저 교체할 옛 replica 다 — streaming 아닌 것이 먼저, 같으면 서수가 높은 것(§3.16). primary(그래프
// 뿌리)와 지워지는 중인 파드는 고르지 않는다.
func pickReplica(outdated []corev1.Pod, g replicationGraph) (corev1.Pod, bool) {
	root := endpointPod(g.root)
	var best *corev1.Pod
	for i := range outdated {
		pod := &outdated[i]
		if pod.Name == root || pod.DeletionTimestamp != nil {
			continue
		}
		if best == nil || replacedBefore(pod, best, g) {
			best = pod
		}
	}
	if best == nil {
		return corev1.Pod{}, false
	}
	return *best, true
}

// replacedBefore 는 a 를 b 보다 먼저 교체할지다.
func replacedBefore(a, b *corev1.Pod, g replicationGraph) bool {
	if g.streaming[a.Name] != g.streaming[b.Name] {
		return !g.streaming[a.Name]
	}
	return podOrdinal(a.Name) > podOrdinal(b.Name)
}

// outdatedMembers 는 StatefulSet updateRevision 이 아닌 파드다.
func outdatedMembers(sts *appsv1.StatefulSet, pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for i := range pods {
		if pods[i].Labels[appsv1.StatefulSetRevisionLabel] != sts.Status.UpdateRevision {
			out = append(out, pods[i])
		}
	}
	return out
}

// expectedMembers 는 STS 서수 안(0..replicas−1)의 파드다 — 줄어드는 서수는 STS 가 지운다.
func expectedMembers(sts *appsv1.StatefulSet, pods []corev1.Pod) []corev1.Pod {
	names := memberNames(sts)
	var out []corev1.Pod
	for i := range pods {
		if slices.Contains(names, pods[i].Name) {
			out = append(out, pods[i])
		}
	}
	return out
}

// pendingMembers 는 updateRevision 에 아직 없는 멤버 수다.
func pendingMembers(sts *appsv1.StatefulSet, pods []corev1.Pod) int {
	names := memberNames(sts)
	pending := len(names)
	for _, name := range names {
		pod := podNamed(pods, name)
		if pod != nil && pod.DeletionTimestamp == nil && pod.Labels[appsv1.StatefulSetRevisionLabel] == sts.Status.UpdateRevision {
			pending--
		}
	}
	return pending
}

// rolloutComplete 는 롤아웃이 끝났는지다 — 전 파드 해시 = updateRevision ∧ updatedReplicas = replicas.
// OnDelete 에서는 currentRevision 이 안 움직일 수 있어 보지 않는다(위험 20).
func rolloutComplete(sts *appsv1.StatefulSet, pods []corev1.Pod) bool {
	return sts.Status.UpdatedReplicas == stsMembers(sts) && pendingMembers(sts, pods) == 0 &&
		len(outdatedMembers(sts, pods)) == 0
}

// rolloutManaged 는 오퍼레이터가 이 STS 의 멤버를 교체할 차례인지다 — OnDelete 이고 상태가 현재 spec 을 봤다.
// RollingUpdate 는 STS 컨트롤러 몫이다.
func rolloutManaged(sts *appsv1.StatefulSet) bool {
	return sts != nil && sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType &&
		sts.Status.ObservedGeneration >= sts.Generation && sts.Status.UpdateRevision != ""
}

// memberNames 는 STS 서수의 파드 이름이다.
func memberNames(sts *appsv1.StatefulSet) []string {
	members := stsMembers(sts)
	names := make([]string, 0, members)
	for i := range members {
		names = append(names, fmt.Sprintf("%s-%d", sts.Name, i))
	}
	return names
}

// stsMembers 는 STS 멤버 수다(비면 1 — k8s 기본값).
func stsMembers(sts *appsv1.StatefulSet) int32 {
	if sts.Spec.Replicas == nil {
		return 1
	}
	return *sts.Spec.Replicas
}

// imagePulling 은 멤버 컨테이너가 이미지를 못 받아 기다리는지다.
func imagePulling(pod *corev1.Pod) bool {
	for _, st := range slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses) {
		if st.State.Waiting != nil && slices.Contains(imagePullReasons, st.State.Waiting.Reason) {
			return true
		}
	}
	return false
}

// shardStatefulSet 은 샤드 StatefulSet 이다 — 없으면 nil.
func (r *PostgresClusterReconciler) shardStatefulSet(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
) (*appsv1.StatefulSet, error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, &sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get StatefulSet %q: %w", name, err)
	}
	return &sts, nil
}

// initRolloutSeries 는 샤드마다 롤아웃 시계열을 0 으로 만든다 — 첫 단계 전에도 보이게.
func initRolloutSeries(cluster *postgresv1alpha1.PostgresCluster, shards []postgresv1alpha1.ShardStatus) {
	for i := range shards {
		for _, step := range rolloutSteps {
			for _, outcome := range rolloutOutcomes {
				MetricRolloutSteps.WithLabelValues(cluster.Namespace, cluster.Name, shards[i].Name,
					string(step), string(outcome)).Add(0)
			}
		}
		MetricRolloutPendingMembers.WithLabelValues(cluster.Namespace, cluster.Name, shards[i].Name).Add(0)
	}
}
