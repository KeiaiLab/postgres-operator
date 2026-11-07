/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 제자리 switchover 다(설계 2026-09-25 §3.16, ADR-0032). 파드를 지우면 인스턴스 매니저가 postgres 를
// 곧바로 SIGKILL 해 스트리밍 안 된 꼬리를 잃는다 — 옛 primary P 는 fast shutdown 으로 멈추고, 같은 컨테이너가
// standby 로 다시 뜬 뒤에야 승격한다. 파드 Delete 0.
//
//	Preparing ── P 실제 primary · 재구축 0 · 대상 t = streaming 중 수신 최대 → 상태 기록 → CHECKPOINT(공백 밖)
//	Stopping  ── 상태(stopAt·oldRestarts) 먼저 → exec P: conninfo=t · standby.signal · 마커 rm · L0 · pg_ctl stop
//	             → P 재기동 ∧ in recovery 전엔 승격 0 · switchoverRestartWait 넘으면 중단(보류한 failover 가 잇는다)
//	Promoting ── 승자 = t → 다른 standby → P (옛 primary 끝 이상) → promoteAndPublish(switchover, hold 0) → 상태 삭제
//
// 상태 = CR 애노테이션 switchover-state(JSON v1) — 재기동한 오퍼레이터가 이어 간다. 진행 중 샤드의 자동 failover 는
// 실행만 보류된다(창은 누적 — §3.3). 어느 단계든 switchoverBudget 을 넘으면 중단한다.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
)

const (
	// switchoverStateAnnotation 은 진행 중 switchover(JSON v1)를 담는 CR 애노테이션이다.
	switchoverStateAnnotation = "postgres.keiailab.io/switchover-state"
	switchoverStateVersion    = 1

	// switchoverRestartWait 는 정지 뒤 옛 primary 가 standby 로 돌아오기까지의 상한이다.
	switchoverRestartWait = statusStaleThresh
	// promotionExecBudget 은 CHECKPOINT·정지 exec 1회의 상한이다 — 승격 스크립트 대기의 두 배.
	promotionExecBudget = 2 * pgPromoteWait
	// switchoverBudget 은 switchover 한 번의 상한이다 — 넘으면 중단하고 보류한 failover 가 돈다.
	switchoverBudget = switchoverRestartWait + promotionExecBudget + 2*statusPollInterval

	// checkpointQuery 는 정지 전에 dirty 버퍼를 비운다 — 공백 안의 종료 체크포인트를 짧게.
	checkpointQuery = "CHECKPOINT"
	// standbySignalFile 은 postgres 를 standby 로 띄우는 PGDATA 파일이다.
	standbySignalFile = "standby.signal"
	// stopLSNPrefix·stopRequestedMarker 는 정지 exec 출력의 줄이다 — L0 와 pg_ctl 신호 전송.
	stopLSNPrefix       = "L0="
	stopRequestedMarker = "STOP_REQUESTED"

	switchoverStartedReason      = "SwitchoverStarted"
	switchoverCompletedReason    = "SwitchoverCompleted"
	switchoverAbortedReason      = "SwitchoverAborted"
	switchoverTargetBehindReason = "SwitchoverTargetBehind"
)

// switchoverPhase 는 switchover 단계다.
type switchoverPhase string

const (
	switchoverPreparing switchoverPhase = "Preparing"
	switchoverStopping  switchoverPhase = "Stopping"
	switchoverPromoting switchoverPhase = "Promoting"
)

// switchoverReason 은 switchover 를 연 쪽이다.
type switchoverReason string

const (
	switchoverRequested switchoverReason = "Requested" // switchover-target 애노테이션
	switchoverRollout   switchoverReason = "Rollout"   // 단계화 롤아웃
)

// switchoverAbort 는 중단 사유다(SwitchoverAborted 메시지).
type switchoverAbort string

const (
	abortPrimaryNotConfirmed switchoverAbort = "PrimaryNotConfirmed" // Preparing 에 P 가 실제 primary 가 아니다
	abortTargetBehind        switchoverAbort = "TargetBehind"        // 요청 대상이 streaming 중 수신 최대가 아니다
	abortStopFailed          switchoverAbort = "StopFailed"          // 정지 전 실패 — P 를 primary 로 되돌렸다
	abortOldPrimaryNotBack   switchoverAbort = "OldPrimaryNotBack"   // switchoverRestartWait 안에 standby 로 안 돌아왔다
	abortBudgetExceeded      switchoverAbort = "BudgetExceeded"      // switchoverBudget 을 넘었다
)

// switchoverProgress 는 한 걸음의 결과다 — Reconcile 이 끝·requeue 를 정한다.
type switchoverProgress int

const (
	switchoverIdle     switchoverProgress = iota // 진행 중인 switchover 가 없다
	switchoverWaiting                            // 진행 중 — pgPromotePoll 로 다시 본다
	switchoverPromoted                           // 이 reconcile 이 승격했다(또는 채택을 확인했다) — 끝낸다(§3.3)
)

// switchoverState 는 진행 중 switchover 다 — CR 애노테이션 JSON v1.
type switchoverState struct {
	Version     int              `json:"version"`
	Shard       string           `json:"shard"`
	OldPod      string           `json:"oldPod,omitempty"` // 멈출 옛 primary(CM primary)
	OldUID      string           `json:"oldUID,omitempty"` // 그 파드 — 바뀌었으면 재생성으로 돌아왔다
	OldRestarts int32            `json:"oldRestarts"`      // 정지 직전 postgres 컨테이너 재시작 수
	Target      string           `json:"target,omitempty"` // 대상 t — 비면 Preparing 이 고른다
	Reason      switchoverReason `json:"reason"`
	Phase       switchoverPhase  `json:"phase"`
	Since       time.Time        `json:"since"`           // 연 시각 — switchoverBudget 기준
	StopAt      time.Time        `json:"stopAt,omitzero"` // 쓰기 공백 시작(CHECKPOINT 뒤)
	L0          string           `json:"l0,omitempty"`    // 정지 직전 pg_current_wal_lsn
}

// switchoverRun 은 한 걸음의 입력이다.
type switchoverRun struct {
	cluster *postgresv1alpha1.PostgresCluster
	shard   postgresv1alpha1.ShardStatus
	ref     shardRef
	state   switchoverState
	now     time.Time
	started time.Time // 걸음을 시작한 벽시계
}

// at 은 now 에 이 걸음이 exec 에 쓴 시간을 더한 시각이다 — 공백 시계는 CHECKPOINT 뒤에 시작하고 승격 뒤에 끝난다.
func (s *switchoverRun) at() time.Time {
	return s.now.Add(time.Since(s.started))
}

// loadSwitchoverState 는 진행 중 switchover 다 — 없으면 found=false, 깨졌거나 모르는 판이면 오류.
func loadSwitchoverState(cluster *postgresv1alpha1.PostgresCluster) (switchoverState, bool, error) {
	raw := cluster.Annotations[switchoverStateAnnotation]
	if raw == "" {
		return switchoverState{}, false, nil
	}
	var state switchoverState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return switchoverState{}, false, fmt.Errorf("decode %s: %w", switchoverStateAnnotation, err)
	}
	if state.Version != switchoverStateVersion {
		return switchoverState{}, false, fmt.Errorf("%s version %d unsupported", switchoverStateAnnotation, state.Version)
	}
	return state, true, nil
}

// saveSwitchoverState 는 상태를 쓴다.
func (r *PostgresClusterReconciler) saveSwitchoverState(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	state switchoverState,
) error {
	state.Version = switchoverStateVersion
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return r.patchSwitchover(ctx, cluster, func(annotations map[string]string) {
		annotations[switchoverStateAnnotation] = string(raw)
	})
}

// clearSwitchover 는 상태를 지운다 — 애노테이션 요청이면 switchover-target 도 같은 패치로(다시 열리지 않게).
func (r *PostgresClusterReconciler) clearSwitchover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	state switchoverState,
) error {
	return r.patchSwitchover(ctx, cluster, func(annotations map[string]string) {
		delete(annotations, switchoverStateAnnotation)
		if state.Reason == switchoverRequested {
			delete(annotations, AnnotationSwitchoverTarget)
		}
	})
}

// patchSwitchover 는 CR 애노테이션을 낙관적 잠금 병합 패치로 고친다 — 이 reconcile 의 status 는 두고 메타데이터만 받는다.
func (r *PostgresClusterReconciler) patchSwitchover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	edit func(map[string]string),
) error {
	target := cluster.DeepCopy()
	before := target.DeepCopy()
	if target.Annotations == nil {
		target.Annotations = map[string]string{}
	}
	edit(target.Annotations)
	if err := r.Patch(ctx, target, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("save %s: %w", switchoverStateAnnotation, err)
	}
	cluster.Annotations = target.Annotations
	cluster.ResourceVersion = target.ResourceVersion
	return nil
}

// switchoverActive 는 shard 에 switchoverBudget 안의 switchover 가 있는지다 — 참이면 자동 failover 는 실행만
// 보류한다(창은 누적, §3.3). 상태를 못 읽으면 거짓이다(failover 를 막지 않는다).
func switchoverActive(cluster *postgresv1alpha1.PostgresCluster, shard string, now time.Time) bool {
	state, found, err := loadSwitchoverState(cluster)
	if err != nil || !found {
		return false
	}
	return state.Shard == shard && now.Sub(state.Since) <= switchoverBudget
}

// startSwitchover 는 shard 의 switchover 를 연다(Preparing) — 진행 중인 것이 있으면 그대로 둔다(한 번에 하나).
// target 이 비면 Preparing 이 streaming 중 수신 최대 standby 를 고른다.
func (r *PostgresClusterReconciler) startSwitchover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, target string,
	reason switchoverReason,
) error {
	_, found, err := loadSwitchoverState(cluster)
	if err != nil || found {
		return err
	}
	state := switchoverState{Shard: shard, Target: target, Reason: reason, Phase: switchoverPreparing, Since: time.Now().UTC()}
	if err := r.saveSwitchoverState(ctx, cluster, state); err != nil {
		return err
	}
	commonsevents.Emitf(r.Recorder, cluster, switchoverStartedReason, "shard %s: switchover started (%s, target %q)", shard, reason, target)
	return nil
}

// advanceSwitchover 는 진행 중 switchover 를 한 걸음 옮긴다(§3.16) — Reconcile 이 failover 판정 뒤에 부른다.
func (r *PostgresClusterReconciler) advanceSwitchover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	now time.Time,
) (switchoverProgress, error) {
	initSwitchoverSeries(cluster, shards)
	state, found, err := loadSwitchoverState(cluster)
	if err != nil || !found {
		return switchoverIdle, err
	}
	s := &switchoverRun{cluster: cluster, state: state, now: now, started: time.Now()}
	if i := slices.IndexFunc(shards, func(ss postgresv1alpha1.ShardStatus) bool { return ss.Name == state.Shard }); i >= 0 {
		s.shard, s.ref = shards[i], shardRefFor(cluster, &shards[i])
	}
	if now.Sub(state.Since) > switchoverBudget {
		return switchoverIdle, r.expireSwitchover(ctx, s)
	}

	// 샤드·exec 가 없으면 걸음이 없다 — 예산이 끝낸다.
	if s.ref.name == "" || r.PromotionPodExecutor == nil {
		return switchoverWaiting, errors.New("switchover shard or pod executor unavailable")
	}
	switch state.Phase {
	case switchoverPreparing:
		return r.prepareSwitchover(ctx, s)
	case switchoverStopping:
		return r.awaitOldPrimary(ctx, s)
	case switchoverPromoting:
		return r.resumePromoting(ctx, s)
	}
	return switchoverWaiting, fmt.Errorf("unknown switchover phase %q", state.Phase)
}

// prepareSwitchover 는 Preparing 이다(§3.16-1) — P 가 실제 primary 가 아니면 곧바로 중단한다(보류할 failover 가
// 있다). 재구축 중이거나 streaming standby 를 모르면 기다린다. 대상을 고르면 적고 CHECKPOINT 한 뒤 멈춘다.
func (r *PostgresClusterReconciler) prepareSwitchover(ctx context.Context, s *switchoverRun) (switchoverProgress, error) {
	pods, err := r.shardMembers(ctx, s.cluster, s.ref.member)
	if err != nil {
		return switchoverWaiting, err
	}
	rt, found, err := r.readRuntime(ctx, s.cluster, s.state.Shard)
	if err != nil || !found {
		return switchoverWaiting, err
	}
	fenced, err := r.fencedPVCs(ctx, s.cluster.Namespace)
	if err != nil {
		return switchoverWaiting, err
	}
	old := podNamed(pods, endpointPod(rt.PrimaryEndpoint))
	if check := r.checkPrimary(ctx, old, fenced, s.now); check != primaryConfirmed {
		return switchoverIdle, r.abortSwitchover(ctx, s, abortPrimaryNotConfirmed, string(check))
	}
	if shardRebuilding(s) {
		return switchoverWaiting, nil
	}

	target, behind, err := r.switchoverTarget(ctx, s, old, pods)
	if behind {
		return switchoverIdle, r.abortSwitchover(ctx, s, abortTargetBehind, "")
	}
	if err != nil || target == nil {
		return switchoverWaiting, err
	}

	// 상태 기록 → CHECKPOINT(공백 밖) → Stopping(stopAt·oldRestarts) 먼저 → 정지 exec.
	if old.Name != s.state.OldPod || string(old.UID) != s.state.OldUID || target.Name != s.state.Target {
		s.state.OldPod, s.state.OldUID, s.state.Target = old.Name, string(old.UID), target.Name
		if err := r.saveSwitchoverState(ctx, s.cluster, s.state); err != nil {
			return switchoverWaiting, err
		}
	}
	if err := r.checkpointMember(ctx, old); err != nil {
		return switchoverWaiting, err
	}
	s.state.Phase, s.state.StopAt, s.state.OldRestarts = switchoverStopping, s.at(), pgRestarts(old)
	if err := r.saveSwitchoverState(ctx, s.cluster, s.state); err != nil {
		return switchoverWaiting, err
	}
	return r.stopOldPrimary(ctx, s, old, target)
}

// shardRebuilding 은 샤드에 진행 중인 재구축이 있는지다 — 상태를 못 읽으면 참(기다린다).
func shardRebuilding(s *switchoverRun) bool {
	state, err := loadRebuildState(s.cluster)
	if err != nil {
		return true
	}
	for _, e := range state.Active {
		if e.Shard == s.state.Shard {
			return true
		}
	}
	return false
}

// switchoverTarget 은 대상이다 — P 의 pg_stat_replication 에서 streaming 인 멤버 중 수신 위치 최대(동률 → 낮은
// 서수). 요청 대상은 그 최대여야 하고 아니면 behind 다. 모르면 nil 이다(기다린다).
func (r *PostgresClusterReconciler) switchoverTarget(
	ctx context.Context,
	s *switchoverRun,
	old *corev1.Pod,
	pods []corev1.Pod,
) (*corev1.Pod, bool, error) {
	rows, err := r.replicationRows(ctx, old)
	if err != nil {
		return nil, false, err
	}
	positions := r.receivePositions(ctx, s.cluster, s.shard)
	streaming := func(pod string) bool {
		_, known := positions[pod]
		return pod != old.Name && rows[pod] == walsenderStreaming && known
	}

	var best *corev1.Pod
	for i := range pods {
		pod := &pods[i]
		if !streaming(pod.Name) {
			continue
		}
		if best == nil || positions[pod.Name] > positions[best.Name] ||
			(positions[pod.Name] == positions[best.Name] && podOrdinal(pod.Name) < podOrdinal(best.Name)) {
			best = pod
		}
	}
	if best == nil || s.state.Reason != switchoverRequested {
		return best, false, nil
	}

	// 요청 대상은 수신 최대여야 한다 — 뒤처진 대상을 멈춘 primary 뒤에 두지 않는다.
	requested := podNamed(pods, s.state.Target)
	if requested != nil && streaming(requested.Name) && positions[requested.Name] >= positions[best.Name] {
		return requested, false, nil
	}
	commonsevents.EmitWarningf(r.Recorder, s.cluster, switchoverTargetBehindReason,
		"shard %s: %s is not the streaming standby furthest ahead (%s at %s); switchover not started",
		s.state.Shard, s.state.Target, best.Name, positionLSN(positions[best.Name]))
	return nil, true, nil
}

// checkpointMember 는 정지 전 CHECKPOINT 다.
func (r *PostgresClusterReconciler) checkpointMember(ctx context.Context, pod *corev1.Pod) error {
	execCtx, cancel := context.WithTimeout(ctx, promotionExecBudget)
	defer cancel()
	_, err := r.PromotionPodExecutor.Exec(execCtx, memberTarget(pod), []string{
		"psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-Atqc", checkpointQuery,
	})
	return err
}

// stopOldPrimary 는 정지 exec 한 번이다(§3.16-2). STOP_REQUESTED 가 없으면 정지 전 실패일 수 있다 — P 가 아직
// 실제 primary 면 되돌리고 중단한다. L0 는 알면 적는다.
func (r *PostgresClusterReconciler) stopOldPrimary(
	ctx context.Context,
	s *switchoverRun,
	old, target *corev1.Pod,
) (switchoverProgress, error) {
	execCtx, cancel := context.WithTimeout(ctx, promotionExecBudget)
	defer cancel()
	conninfo := standbyConninfo(memberEndpoint(target, s.ref.service), old.Name)
	out, execErr := r.PromotionPodExecutor.Exec(execCtx, memberTarget(old), stopInPlaceCommand(conninfo))
	l0, requested := parseStopOutput(out)
	if !requested {
		if inRecovery, _, err := r.memberLSN(ctx, old); err == nil && !inRecovery {
			return r.undoStop(ctx, s, old)
		}
		return switchoverWaiting, execErr
	}
	if l0 == "" {
		return switchoverWaiting, nil
	}
	s.state.L0 = l0
	return switchoverWaiting, r.saveSwitchoverState(ctx, s.cluster, s.state)
}

// awaitOldPrimary 는 Stopping 이다(§3.16-4) — P 가 제자리 재기동해 in recovery 면 승자를 고른다. 한 걸음 뒤에도
// 실제 primary 면 정지가 안 된 것이라 되돌리고 중단한다. switchoverRestartWait 안에 안 돌아오면 중단한다 — 보류한
// failover 가 누적된 창으로 잇는다.
func (r *PostgresClusterReconciler) awaitOldPrimary(ctx context.Context, s *switchoverRun) (switchoverProgress, error) {
	old, err := r.memberPod(ctx, s.cluster, s.state.OldPod)
	if err != nil {
		return switchoverWaiting, err
	}
	if old != nil {
		inRecovery, end, err := r.memberLSN(ctx, old)
		if err == nil && !inRecovery && s.now.Sub(s.state.StopAt) >= pgPromotePoll {
			return r.undoStop(ctx, s, old)
		}
		if err == nil && inRecovery && restartedSince(old, s.state) {
			return r.promoteSwitchover(ctx, s, end)
		}
	}

	// 내려가는 중이거나 다시 뜨는 중이다.
	if s.now.Sub(s.state.StopAt) <= switchoverRestartWait {
		return switchoverWaiting, nil
	}
	return switchoverIdle, r.abortSwitchover(ctx, s, abortOldPrimaryNotBack, "")
}

// undoStop 은 정지 전 실패를 되돌리고 중단한다(§3.16-3) — 되돌리기가 실패하면 상태를 두고 다음 걸음이 다시 한다.
func (r *PostgresClusterReconciler) undoStop(ctx context.Context, s *switchoverRun, old *corev1.Pod) (switchoverProgress, error) {
	if err := r.restoreOldPrimary(ctx, s, old); err != nil {
		return switchoverWaiting, err
	}
	return switchoverIdle, r.abortSwitchover(ctx, s, abortStopFailed, "")
}

// restoreOldPrimary 는 P 의 PGDATA 를 primary 로 되돌린다 — standby.signal rm · 승격 마커 = CR epoch.
func (r *PostgresClusterReconciler) restoreOldPrimary(ctx context.Context, s *switchoverRun, old *corev1.Pod) error {
	canon, err := loadRuntimeState(s.cluster)
	if err != nil {
		return err
	}
	execCtx, cancel := context.WithTimeout(ctx, promotionExecBudget)
	defer cancel()
	_, err = r.PromotionPodExecutor.Exec(execCtx, memberTarget(old), restorePrimaryCommand(canon.Shards[s.state.Shard].Epoch))
	return err
}

// promoteSwitchover 는 승자를 고르고 승격한다(§3.16-5·6). 다른 멤버가 이미 실제 primary 면 승격하지 않는다 —
// 채택이 잇고 예산이 끝낸다.
func (r *PostgresClusterReconciler) promoteSwitchover(ctx context.Context, s *switchoverRun, oldEnd uint64) (switchoverProgress, error) {
	pods, err := r.shardMembers(ctx, s.cluster, s.ref.member)
	if err != nil {
		return switchoverWaiting, err
	}
	received := map[string]uint64{}
	for i := range pods {
		if pods[i].Name == s.state.OldPod {
			continue
		}
		inRecovery, lsn, err := r.memberLSN(ctx, &pods[i])
		if err != nil {
			continue
		}
		if !inRecovery {
			return switchoverWaiting, fmt.Errorf("%s already runs as a primary", pods[i].Name)
		}
		received[pods[i].Name] = lsn
	}

	// 승자는 옛 primary 의 끝(L0 이상)을 가져야 한다 — ack 된 커밋은 전부 그 안에 있다.
	need := oldEnd
	if l0, err := parseLSN(s.state.L0); err == nil {
		need = max(need, l0)
	}
	winner := switchoverWinner(s.state.Target, s.state.OldPod, need, received)
	pod := podNamed(pods, winner)
	if pod == nil {
		return switchoverWaiting, fmt.Errorf("switchover winner %s is gone", winner)
	}
	if winner != s.state.Target {
		commonsevents.EmitWarningf(r.Recorder, s.cluster, switchoverTargetBehindReason,
			"shard %s: %s is behind the end %s of %s; promoting %s",
			s.state.Shard, s.state.Target, formatLSN(need), s.state.OldPod, winner)
	}

	s.state.Phase = switchoverPromoting
	if err := r.saveSwitchoverState(ctx, s.cluster, s.state); err != nil {
		return switchoverWaiting, err
	}
	target := failover.PromotionTarget{ShardName: s.state.Shard, Pod: winner, Endpoint: memberEndpoint(pod, s.ref.service)}
	if err := r.promoteAndPublish(ctx, s.cluster, s.state.Shard, target, s.state.OldPod, promotionSwitchover); err != nil {
		return switchoverWaiting, err
	}

	// 승격한 reconcile 은 끝낸다 — 발행 확인이 늦으면 다음 걸음(Promoting)이 잇는다.
	primary, published := r.switchoverPublished(ctx, s)
	if !published {
		return switchoverPromoted, fmt.Errorf("promotion of %s not confirmed yet", winner)
	}
	return switchoverPromoted, r.completeSwitchover(ctx, s, primary)
}

// resumePromoting 은 Promoting 이다 — 발행됐으면 끝낸다(채택이 이 reconcile 앞에서 맞췄을 수 있다). 아니면
// P 가 in recovery 일 때만 끝을 다시 재고 승자를 다시 고른다.
func (r *PostgresClusterReconciler) resumePromoting(ctx context.Context, s *switchoverRun) (switchoverProgress, error) {
	if primary, published := r.switchoverPublished(ctx, s); published {
		return switchoverPromoted, r.completeSwitchover(ctx, s, primary)
	}
	old, err := r.memberPod(ctx, s.cluster, s.state.OldPod)
	if err != nil || old == nil {
		return switchoverWaiting, err
	}
	inRecovery, end, err := r.memberLSN(ctx, old)
	if err != nil || !inRecovery {
		return switchoverWaiting, err
	}
	return r.promoteSwitchover(ctx, s, end)
}

// switchoverPublished 는 승격이 발행됐는지다 — CM epoch 가 정본 epoch 이고 CM primary 가 실제 primary 다.
func (r *PostgresClusterReconciler) switchoverPublished(ctx context.Context, s *switchoverRun) (string, bool) {
	canon, err := loadRuntimeState(s.cluster)
	if err != nil {
		return "", false
	}
	rt, found, err := r.readRuntime(ctx, s.cluster, s.state.Shard)
	if err != nil || !found || rt.PromotionEpoch != canon.Shards[s.state.Shard].Epoch {
		return "", false
	}
	primary := endpointPod(rt.PrimaryEndpoint)
	pod, err := r.memberPod(ctx, s.cluster, primary)
	if err != nil {
		return "", false
	}
	fenced, err := r.fencedPVCs(ctx, s.cluster.Namespace)
	if err != nil {
		return "", false
	}
	return primary, r.checkPrimary(ctx, pod, fenced, s.now) == primaryConfirmed
}

// completeSwitchover 는 상태를 지우고 쓰기 공백(stopAt → 승격)을 남긴다.
func (r *PostgresClusterReconciler) completeSwitchover(ctx context.Context, s *switchoverRun, primary string) error {
	if err := r.clearSwitchover(ctx, s.cluster, s.state); err != nil {
		return err
	}
	gap := s.at().Sub(s.state.StopAt)
	MetricSwitchoverDuration.WithLabelValues(s.cluster.Namespace, s.cluster.Name, s.state.Shard).Set(gap.Seconds())
	commonsevents.Emitf(r.Recorder, s.cluster, switchoverCompletedReason, "shard %s: %s is primary in place of %s; write gap %s",
		s.state.Shard, primary, s.state.OldPod, gap.Round(time.Millisecond))
	return nil
}

// abortSwitchover 는 상태를 지우고 SwitchoverAborted 를 남긴다 — 보류한 failover 가 다음 판정에 돈다.
func (r *PostgresClusterReconciler) abortSwitchover(ctx context.Context, s *switchoverRun, why switchoverAbort, detail string) error {
	if err := r.clearSwitchover(ctx, s.cluster, s.state); err != nil {
		return err
	}
	commonsevents.EmitWarningf(r.Recorder, s.cluster, switchoverAbortedReason, "shard %s: switchover from %s to %s aborted: %s %s",
		s.state.Shard, s.state.OldPod, s.state.Target, why, detail)
	return nil
}

// expireSwitchover 는 switchoverBudget 을 넘은 switchover 를 끝낸다 — 멈추던 P 가 아직 실제 primary 면 먼저
// 되돌린다. 되돌리기 실패는 기록만 한다(예산 뒤엔 failover 를 막지 않는다).
func (r *PostgresClusterReconciler) expireSwitchover(ctx context.Context, s *switchoverRun) error {
	if old := r.standingOldPrimary(ctx, s); old != nil {
		if err := r.restoreOldPrimary(ctx, s, old); err != nil {
			log.FromContext(ctx).Info("old primary not restored at switchover expiry", "pod", old.Name, "error", err)
		}
	}
	return r.abortSwitchover(ctx, s, abortBudgetExceeded, "")
}

// standingOldPrimary 는 Stopping 의 P 가 아직 실제 primary(pg_is_in_recovery=f)면 그 파드다 — 정지가 안 됐다.
func (r *PostgresClusterReconciler) standingOldPrimary(ctx context.Context, s *switchoverRun) *corev1.Pod {
	if s.state.Phase != switchoverStopping {
		return nil
	}
	old, err := r.memberPod(ctx, s.cluster, s.state.OldPod)
	if err != nil || old == nil {
		return nil
	}
	inRecovery, _, err := r.memberLSN(ctx, old)
	if err != nil || inRecovery {
		return nil
	}
	return old
}

// memberLSN 은 도는 멤버의 WAL 끝이다(in recovery, 절대 바이트) — standby 는 수신·재생 중 큰 것(제자리 재기동한
// 옛 primary 는 재생 끝), primary 는 현재 위치.
func (r *PostgresClusterReconciler) memberLSN(ctx context.Context, pod *corev1.Pod) (bool, uint64, error) {
	inRecovery, end, err := r.walEnd(ctx, pod)
	if err != nil {
		return false, 0, err
	}
	lsn, err := parseLSN(end)
	return inRecovery, lsn, err
}

// switchoverWinner 는 승격할 멤버다(§3.16-5) — need 이상을 받은 요청 대상, 아니면 그런 다른 standby 중 수신 최대
// (동률 → 낮은 서수), 아니면 옛 primary(데이터를 전부 가졌다).
func switchoverWinner(target, old string, need uint64, received map[string]uint64) string {
	if lsn, ok := received[target]; ok && lsn >= need {
		return target
	}
	winner := old
	byOrdinal := func(a, b string) int { return cmp.Compare(podOrdinal(a), podOrdinal(b)) }
	for _, pod := range slices.SortedFunc(maps.Keys(received), byOrdinal) {
		if received[pod] < need {
			continue
		}
		if winner == old || received[pod] > received[winner] {
			winner = pod
		}
	}
	return winner
}

// restartedSince 는 P 의 postgres 컨테이너가 정지 뒤 다시 떴는지다 — 재시작 수가 올랐거나 파드가 바뀌었다.
func restartedSince(pod *corev1.Pod, state switchoverState) bool {
	return string(pod.UID) != state.OldUID || pgRestarts(pod) > state.OldRestarts
}

// pgRestarts 는 postgres 컨테이너 재시작 수다.
func pgRestarts(pod *corev1.Pod) int32 {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == pgContainerName {
			return pod.Status.ContainerStatuses[i].RestartCount
		}
	}
	return 0
}

// podOrdinal 은 StatefulSet 파드 이름 끝의 서수다(예: demo-shard-0-2 → 2) — 없으면 -1.
func podOrdinal(pod string) int {
	ordinal, err := strconv.Atoi(pod[strings.LastIndex(pod, "-")+1:])
	if err != nil {
		return -1
	}
	return ordinal
}

// memberTarget 은 멤버 postgres 컨테이너의 exec 자리다.
func memberTarget(pod *corev1.Pod) BackupSidecarTarget {
	return BackupSidecarTarget{Namespace: pod.Namespace, Pod: pod.Name, Container: pgContainerName}
}

// initSwitchoverSeries 는 샤드마다 switchover_duration_seconds 시계열을 만든다 — 첫 switchover 전에도 보이게.
func initSwitchoverSeries(cluster *postgresv1alpha1.PostgresCluster, shards []postgresv1alpha1.ShardStatus) {
	for i := range shards {
		MetricSwitchoverDuration.WithLabelValues(cluster.Namespace, cluster.Name, shards[i].Name).Add(0)
	}
}

// parseStopOutput 은 정지 exec 출력의 L0 와 STOP_REQUESTED(pg_ctl 이 신호를 보냈다)다.
func parseStopOutput(out []byte) (string, bool) {
	l0, requested := "", false
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == stopRequestedMarker {
			requested = true
		}
		if lsn, ok := strings.CutPrefix(line, stopLSNPrefix); ok && lsnPattern.MatchString(lsn) {
			l0 = lsn
		}
	}
	return l0, requested
}

// stopInPlaceCommand 는 옛 primary 를 제자리 standby 로 멈추는 exec 한 번이다(§3.16-2). 순서가 계약이다:
//
//	primary_conninfo = conninfo(t 를 따른다) → standby.signal → 승격 마커 rm → L0 → pg_ctl stop -m fast -W
//
// fast shutdown 은 클라이언트를 끊고 종료 체크포인트까지 연결된 스탠바이의 flush 확인을 받은 뒤 끝난다 — ack 된
// 커밋은 전부 스탠바이에 있다. postgres 가 끝나면 인스턴스 매니저가 끝나고 컨테이너가 init 없이 다시 떠
// standby.signal 로 Follower 가 된다. -W 는 기다리지 않는다(컨테이너와 함께 exec 가 끊기기 전에 답한다).
func stopInPlaceCommand(conninfo string) []string {
	script := `set -eu
BIN="${POSTGRES_BIN_DIR:-/usr/lib/postgresql/18/bin}"
DATA="${POSTGRES_DATA_DIR:-/var/lib/postgresql/data/pgdata}"
DSN="${POSTGRES_LOCAL_DSN:-host=/var/run/postgresql user=postgres dbname=postgres}"
"$BIN/psql" "$DSN" -v ON_ERROR_STOP=1 -Atqc "$1"
touch "$DATA/` + standbySignalFile + `"
rm -f "$DATA/` + promotedPrimaryMarker + `"
L0="$("$BIN/psql" "$DSN" -v ON_ERROR_STOP=1 -Atqc "SELECT pg_current_wal_lsn()")"
echo "` + stopLSNPrefix + `$L0"
"$BIN/pg_ctl" stop -D "$DATA" -m fast -W
echo "` + stopRequestedMarker + `"
`
	return []string{"sh", "-ec", script, "sh", "ALTER SYSTEM SET primary_conninfo = " + sqlLiteral(conninfo)}
}

// restorePrimaryCommand 는 정지 전 실패를 되돌리는 exec 다(§3.16-3) — standby.signal 을 지우고 승격 마커를 epoch 로
// 쓴다(rename — 반쯤 쓴 숫자 없음).
func restorePrimaryCommand(epoch int64) []string {
	script := `set -eu
DATA="${POSTGRES_DATA_DIR:-/var/lib/postgresql/data/pgdata}"
rm -f "$DATA/` + standbySignalFile + `"
printf '%s\n' "$1" > "$DATA/` + promotedPrimaryMarker + `.tmp"
mv "$DATA/` + promotedPrimaryMarker + `.tmp" "$DATA/` + promotedPrimaryMarker + `"
`
	return []string{"sh", "-ec", script, "sh", strconv.FormatInt(epoch, 10)}
}
