/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 멤버 재구축 엔진이다(설계 2026-09-25 §3.5, ADR-0031). 되감기·재시드처럼 멤버 데이터를
// 버리는 일은 hold 와 readyToUse 스냅숏 뒤에, 예산 안에서만 일어난다. PVC 는 지우지 않는다.
//
//	requestRebuild ── MEMBER_HOLD 에 즉시(예산·쿨다운·동시성 무관) · 항목 Holding
//	Holding      ── 전파 대기 → 쿨다운·샤드당 1 → 재검증 → 끝 위치 → 파드 삭제
//	                 → 새 파드가 hold 로 멈춤(init 비0 ∧ postgres 돈 적 없음) · 떴으면 다시 삭제
//	Snapshotting ── VolumeSnapshot <pod>-rebuild-<token> → readyToUse(직접 읽음) → 재검증 → 예산
//	Releasing    ── hold 해제(Rogue·Stale 은 RESEED_REQUESTS 와 한 CM 쓰기) → 전파 대기 → 파드 재삭제
//	Verifying    ── 복제 그래프 연결 ∧ (재시드면 applied == token) → RESEED 제거 · unfence · 원장
//
// 상태 = CR 애노테이션 member-rebuild(JSON v1) — 원장이 CR 에 있어 재기동한 오퍼레이터도 같은 예산을
// 잰다. 앞으로 가는 쓰기는 CR 먼저(원장이 해제를 먼저 센다), 끝내는 쓰기는 CM 먼저다.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
)

const (
	// memberRebuildAnnotation 은 재구축 상태·원장(JSON v1)을 담는 CR 애노테이션이다.
	memberRebuildAnnotation = "postgres.keiailab.io/member-rebuild"
	rebuildStateVersion     = 1

	// runtimePropagationWait 는 런타임 CM 쓰기 뒤 파드를 지우기 전 기다리는 시간이다 — 새 컨테이너가 새 값을 읽게.
	runtimePropagationWait = statusPollInterval
	// snapshotReadyTimeout 은 스냅숏이 readyToUse 가 되기까지의 상한이다.
	snapshotReadyTimeout = staleStandbyReseedTimeout
	// rebuildBudgetWindow 는 재구축 예산 창이다 — 창 안 해제 수 ≤ replicas.
	rebuildBudgetWindow = time.Hour
	// memberSnapshotRetention 은 비 data-bearing 재구축 스냅숏 보존 기간이다(pvc-snapshot-ops 주간 창).
	memberSnapshotRetention = 7 * 24 * time.Hour
	// snapshotsPerMember 는 비 data-bearing 스냅숏 상한의 멤버당 수다 — 상한 = 2 × members.
	snapshotsPerMember = 2
	// availabilityExceptions 는 샤드당 창 안에 예산 밖으로 허가하는 해제 수다 — streaming standby 0 일 때.
	availabilityExceptions = 1
	// rebuildExecTimeout 은 재구축 exec 1회의 상한이다 — 무응답 exec 가 reconcile 을 붙잡지 않는다.
	rebuildExecTimeout = statusPollInterval

	// 재구축 스냅숏 라벨 — GC·입장 정책(VAP)이 읽는다. 클러스터 라벨은 백업·풀러와 같은 키다.
	memberSnapshotLabel = "postgres.keiailab.io/member-snapshot"
	dataBearingLabel    = "postgres.keiailab.io/data-bearing"
	rebuildReasonLabel  = "postgres.keiailab.io/rebuild-reason"
	clusterLabelKey     = backupJobClusterLabelKey
	labelTrue           = "true"
	labelFalse          = "false"

	// memberSnapshotInfix 는 스냅숏 이름 <pod>-rebuild-<token> 의 가운데다.
	memberSnapshotInfix = "-rebuild-"
	// rebuildTokenBase 는 토큰(요청 시각 unix 초)의 진법이다 — 짧고 DNS 이름에 쓸 수 있다.
	rebuildTokenBase = 36

	// endPositionQuery 는 멤버의 WAL 끝이다 — standby 는 수신·재생 중 큰 것, primary 는 현재 쓰기 위치.
	endPositionQuery = "SELECT pg_is_in_recovery(), CASE WHEN pg_is_in_recovery() " +
		"THEN GREATEST(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) ELSE pg_current_wal_lsn() END"

	memberHeldReason               = "MemberHeld"
	memberSnapshotReadyReason      = "MemberSnapshotReady"
	memberRejoinReleasedReason     = "MemberRejoinReleased"
	memberReseedRequestedReason    = "MemberReseedRequested"
	memberRebuildCompletedReason   = "MemberRebuildCompleted"
	rebuildCancelledReason         = "RebuildCancelled"
	rebuildDiscardsNewerDataReason = "RebuildDiscardsNewerData"
)

// volumeSnapshotGVK 는 CSI 스냅숏 종류다 — 오퍼레이터 스킴에 없어 unstructured 로 다룬다(캐시 밖 live 읽기라
// CRD·권한이 없어도 informer 가 매니저를 세우지 않는다).
var volumeSnapshotGVK = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}

// imagePullReasons 는 이미지를 못 받아 기다리는 컨테이너 사유다 — 그런 멤버는 끊긴(Stale) 것이 아니다.
var imagePullReasons = []string{"ErrImagePull", "ImagePullBackOff"}

// rebuildReason 은 재구축을 부른 사유다.
type rebuildReason string

const (
	rebuildDemoted       rebuildReason = "Demoted"       // failover 로 밀려난 옛 primary
	rebuildFencedPrimary rebuildReason = "FencedPrimary" // fence 된 PVC 로 primary 를 보고하는 멤버
	rebuildRogue         rebuildReason = "Rogue"         // 승격 마커 없이 primary 를 보고하는 멤버
	rebuildStale         rebuildReason = "Stale"         // 복제 그래프에서 오래 끊긴 standby
)

// reseeds 는 PGDATA 를 새로 받는 사유인지다 — 아니면 hold 해제 뒤 되감기로 합류한다.
func (reason rebuildReason) reseeds() bool {
	return reason == rebuildRogue || reason == rebuildStale
}

// oldPrimary 는 옛 primary 사유인지다 — 가용성 예외의 대상이다.
func (reason rebuildReason) oldPrimary() bool {
	return reason == rebuildDemoted || reason == rebuildFencedPrimary
}

// rebuildPhase 는 재구축 단계다.
type rebuildPhase string

const (
	rebuildHolding      rebuildPhase = "Holding"
	rebuildSnapshotting rebuildPhase = "Snapshotting"
	rebuildReleasing    rebuildPhase = "Releasing"
	rebuildVerifying    rebuildPhase = "Verifying"
)

// rebuildOutcome 은 member_rebuilds_total 의 결과다 — 해제(파괴 허가)·취소·포기마다 하나.
type rebuildOutcome string

const (
	outcomeReleased     rebuildOutcome = "released"              // 분기점을 넘지 않은 멤버
	outcomeDataBearing  rebuildOutcome = "data_bearing"          // 분기점을 넘은 WAL 이 스냅숏에만 남았다
	outcomeUnproven     rebuildOutcome = "data_bearing_unproven" // 끝 위치나 분기점을 모른다
	outcomeAvailability rebuildOutcome = "availability"          // 예산 밖 — streaming standby 0
	outcomeCancelled    rebuildOutcome = "cancelled"             // 재검증이 필요 없다고 봤다
	outcomeAbandoned    rebuildOutcome = "abandoned"             // Stale 인데 스냅숏을 못 떴다
)

var (
	rebuildReasons  = []rebuildReason{rebuildDemoted, rebuildFencedPrimary, rebuildRogue, rebuildStale}
	rebuildOutcomes = []rebuildOutcome{
		outcomeReleased, outcomeDataBearing, outcomeUnproven, outcomeAvailability, outcomeCancelled, outcomeAbandoned,
	}
)

// rebuildAdmission 은 해제를 허가한 근거다.
type rebuildAdmission string

const (
	admitBudget       rebuildAdmission = "budget"
	admitAvailability rebuildAdmission = "availability"
)

// snapshotState 는 VolumeSnapshot 을 읽은 결과다.
type snapshotState string

const (
	snapPending snapshotState = "pending"
	snapReady   snapshotState = "ready"
	snapFailed  snapshotState = "failed"
)

// memberRebuild 는 진행 중인 재구축 하나다.
type memberRebuild struct {
	Pod          string        `json:"pod"`
	Shard        string        `json:"shard"`
	Reason       rebuildReason `json:"reason"`
	Phase        rebuildPhase  `json:"phase"`
	Token        string        `json:"token"`                  // 스냅숏 이름·재시드 토큰 — 스냅숏 시도마다 새로
	Snapshot     string        `json:"snapshot,omitempty"`     // 만든 VolumeSnapshot
	Since        time.Time     `json:"since"`                  // 요청(hold) 시각
	PhaseSince   time.Time     `json:"phaseSince"`             // 단계 진입 또는 그 단계의 CM 쓰기 시각
	Attempt      int           `json:"attempt"`                // 스냅숏 시도 — 실패마다 +1
	EndLSN       string        `json:"endLSN,omitempty"`       // 멤버가 가진 WAL 위치(정확, 또는 분기점을 넘는 하한)
	PromotionLSN string        `json:"promotionLSN,omitempty"` // 멤버를 멈출 때의 샤드 분기점
	DataBearing  bool          `json:"dataBearing"`            // lead > 0 ∨ 모름
	PodUID       string        `json:"podUID,omitempty"`       // 엔진이 지운 파드
	StoppedAt    time.Time     `json:"stoppedAt,omitzero"`     // 멤버를 멈춘(지운, 또는 없음을 본) 시각
	ReadyAt      time.Time     `json:"readyAt,omitzero"`       // 스냅숏 readyToUse 를 처음 본 시각
	Blocked      string        `json:"blocked,omitempty"`      // MemberRebuildBlocked 사유
}

// started 는 항목이 멤버를 멈춘 뒤인지다 — 샤드당 1 이 센다.
func (e memberRebuild) started() bool {
	return e.Phase != rebuildHolding || !e.StoppedAt.IsZero()
}

// rebuildEntry 는 원장 한 줄이다 — 예산은 Released, 쿨다운은 Ended 를 읽는다.
type rebuildEntry struct {
	Pod       string           `json:"pod"`
	Shard     string           `json:"shard"`
	Token     string           `json:"token"`
	Reason    rebuildReason    `json:"reason"`
	Outcome   rebuildOutcome   `json:"outcome"`
	Admission rebuildAdmission `json:"admission,omitempty"`
	Released  time.Time        `json:"released,omitzero"`
	Ended     time.Time        `json:"ended,omitzero"`
}

// rebuildState 는 CR 정본이다 — {version:1, active:{<pod>:…}, ledger:[…]}.
type rebuildState struct {
	Version int                      `json:"version"`
	Active  map[string]memberRebuild `json:"active"`
	Ledger  []rebuildEntry           `json:"ledger"`
}

// loadRebuildState 는 CR 정본을 읽는다 — 없으면 빈 v1, 깨졌으면 오류(덮어써서 원장을 잃지 않는다).
func loadRebuildState(cluster *postgresv1alpha1.PostgresCluster) (rebuildState, error) {
	state := rebuildState{Version: rebuildStateVersion, Active: map[string]memberRebuild{}}
	raw := cluster.Annotations[memberRebuildAnnotation]
	if raw == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return rebuildState{}, fmt.Errorf("decode %s: %w", memberRebuildAnnotation, err)
	}
	if state.Version != rebuildStateVersion {
		return rebuildState{}, fmt.Errorf("%s version %d unsupported", memberRebuildAnnotation, state.Version)
	}
	if state.Active == nil {
		state.Active = map[string]memberRebuild{}
	}
	return state, nil
}

// saveRebuildState 는 정본을 낙관적 잠금 병합 패치로 쓴다 — 이 reconcile 의 status 는 두고 메타데이터만 받는다.
func (r *PostgresClusterReconciler) saveRebuildState(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	state rebuildState,
) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	target := cluster.DeepCopy()
	before := target.DeepCopy()
	if target.Annotations == nil {
		target.Annotations = map[string]string{}
	}
	target.Annotations[memberRebuildAnnotation] = string(raw)
	if err := r.Patch(ctx, target, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("save %s: %w", memberRebuildAnnotation, err)
	}
	cluster.Annotations = target.Annotations
	cluster.ResourceVersion = target.ResourceVersion
	return nil
}

// rebuildBudget 은 1h 창 해제 상한이다 — replicas, 최소 1.
func rebuildBudget(cluster *postgresv1alpha1.PostgresCluster) int {
	return max(1, int(cluster.Spec.Shards.Replicas))
}

// memberSnapshotCap 은 비 data-bearing 스냅숏 상한이다 — 2 × members(members = 1 + replicas).
func memberSnapshotCap(cluster *postgresv1alpha1.PostgresCluster) int {
	return snapshotsPerMember * (1 + int(cluster.Spec.Shards.Replicas))
}

// rebuildToken 은 요청 시각·시도의 토큰이다(예: vhk1sg-0) — 스냅숏 이름과 재시드 파일이 같이 쓴다.
func rebuildToken(since time.Time, attempt int) string {
	return strconv.FormatInt(since.Unix(), rebuildTokenBase) + "-" + strconv.Itoa(attempt)
}

// memberSnapshotName 은 재구축 스냅숏 이름이다.
func memberSnapshotName(pod, token string) string {
	return pod + memberSnapshotInfix + token
}

// requestRebuild 는 멤버 재구축을 요청한다 — MEMBER_HOLD 에 즉시 넣고(예산·쿨다운·동시성 무관) 항목을
// 남긴다. 이미 진행 중이면 그대로 둔다. Stale 은 파드 나이 ≥ staleStandbyReseedTimeout 이고 이미지를
// 못 받는 중이 아닐 때만 받는다.
func (r *PostgresClusterReconciler) requestRebuild(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, pod string,
	reason rebuildReason,
	now time.Time,
) error {
	state, err := loadRebuildState(cluster)
	if err != nil {
		return err
	}
	if _, active := state.Active[pod]; active {
		return nil
	}
	if reason == rebuildStale {
		eligible, err := r.staleEligible(ctx, cluster, pod, now)
		if err != nil || !eligible {
			return err
		}
	}

	// hold 가 먼저다 — 항목 쓰기가 실패해도 멤버는 멈춘 채 남는다.
	e := memberRebuild{
		Pod: pod, Shard: shard, Reason: reason, Phase: rebuildHolding,
		Token: rebuildToken(now, 0), Since: now, PhaseSince: now,
	}
	if _, err := r.syncMemberRuntime(ctx, cluster, e.Shard, e.Pod, runtimeFor(e)); err != nil {
		return fmt.Errorf("hold member %q: %w", pod, err)
	}
	state.Active[pod] = e
	if err := r.saveRebuildState(ctx, cluster, state); err != nil {
		return err
	}
	commonsevents.Emitf(r.Recorder, cluster, memberHeldReason, "shard %s: %s held for rebuild (%s)", shard, pod, reason)
	return nil
}

// staleEligible 은 Stale 요청을 받을지다 — 막 뜬 파드·이미지를 못 받는 파드는 끊긴 것이 아니다.
func (r *PostgresClusterReconciler) staleEligible(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
	now time.Time,
) (bool, error) {
	pod, err := r.memberPod(ctx, cluster, name)
	if err != nil || pod == nil {
		return false, err
	}
	if now.Sub(pod.CreationTimestamp.Time) < staleStandbyReseedTimeout {
		return false, nil
	}
	for _, st := range slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses) {
		if st.State.Waiting != nil && slices.Contains(imagePullReasons, st.State.Waiting.Reason) {
			return false, nil
		}
	}
	return true, nil
}

// rebuildPass 는 한 reconcile 의 엔진 입력과 바뀐 상태다 — 상태는 pass 끝에 한 번 저장한다.
type rebuildPass struct {
	cluster *postgresv1alpha1.PostgresCluster
	shards  []postgresv1alpha1.ShardStatus
	graphs  map[string]replicationGraph
	state   rebuildState
	now     time.Time
	dirty   bool
}

// advanceRebuilds 는 진행 중인 재구축을 한 걸음씩 옮기고 condition·게이지를 싣는다.
func (r *PostgresClusterReconciler) advanceRebuilds(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	graphs map[string]replicationGraph,
	now time.Time,
) {
	logger := log.FromContext(ctx)
	initRebuildSeries(cluster, shards)
	state, err := loadRebuildState(cluster)
	if err != nil {
		logger.Error(err, "member rebuild state unreadable; rebuilds paused")
		return
	}

	p := &rebuildPass{cluster: cluster, shards: shards, graphs: graphs, state: state, now: now}
	p.prune()
	for _, e := range p.queue() {
		if err := r.stepRebuild(ctx, p, e); err != nil {
			logger.Info("member rebuild step failed", "pod", e.Pod, "phase", e.Phase, "error", err)
		}
	}
	if p.dirty {
		if err := r.saveRebuildState(ctx, cluster, p.state); err != nil {
			logger.Info("member rebuild state not saved", "error", err)
		}
	}
	setRebuildCondition(cluster, p.state)
	r.observeRebuilds(ctx, p)
}

// stepRebuild 는 항목 하나를 그 단계에서 한 걸음 옮긴다.
func (r *PostgresClusterReconciler) stepRebuild(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	switch e.Phase {
	case rebuildHolding:
		return r.stepHolding(ctx, p, e)
	case rebuildSnapshotting:
		return r.stepSnapshot(ctx, p, e)
	case rebuildReleasing:
		return r.stepReleasing(ctx, p, e)
	case rebuildVerifying:
		return r.stepVerifying(ctx, p, e)
	}
	return fmt.Errorf("unknown rebuild phase %q", e.Phase)
}

// stepHolding 은 hold 를 맞춘 뒤 멤버를 멈추고, 새 파드가 hold 로 멈춘 것을 보면 Snapshotting 으로 간다.
func (r *PostgresClusterReconciler) stepHolding(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	wrote, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, runtimeFor(e))
	if err != nil {
		return err
	}
	if wrote {
		// hold 를 다시 썼다 — 전파를 처음부터 기다린다.
		e.PhaseSince = p.now
		p.put(e)
		return nil
	}
	if e.StoppedAt.IsZero() {
		return r.stopMember(ctx, p, e)
	}
	return r.awaitHeld(ctx, p, e)
}

// stopMember 는 전파·쿨다운·샤드당 1 을 기다린 뒤 재검증하고, 끝 위치를 읽고, 파드를 지운다.
func (r *PostgresClusterReconciler) stopMember(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if p.now.Sub(e.PhaseSince) < runtimePropagationWait || !p.mayStart(e) {
		return nil
	}
	// 스냅숏을 못 뜨는 Stale 은 멈추지 않고 끝낸다 — 끊긴 standby 를 헛되이 내리지 않는다.
	if e.Reason == rebuildStale && r.MemberSnapshotClass == "" {
		return r.abandon(ctx, p, e, ReasonSnapshotClassUnset)
	}
	needless, err := r.rebuildNeedless(ctx, p, e)
	if err != nil {
		return err
	}
	if needless {
		return r.cancel(ctx, p, e)
	}

	pod, err := r.memberPod(ctx, p.cluster, e.Pod)
	if err != nil {
		return err
	}
	e.PromotionLSN = forkLSN(p.cluster, e.Shard)
	if pod != nil {
		e.PodUID = string(pod.UID)
		e.EndLSN = r.endPosition(ctx, p, e, pod)
		if err := r.deleteMember(ctx, p.cluster, pod, p.now); err != nil {
			return err
		}
	}
	e.DataBearing = dataBearing(e.EndLSN, e.PromotionLSN)
	e.StoppedAt = p.now
	p.put(e)
	return nil
}

// awaitHeld 는 지운 파드가 사라지고 새 파드가 hold 로 멈추기(또는 파드가 없기)를 기다린다 — 새 파드의
// postgres 가 돈 적 있으면 hold 가 반영되기 전이라 다시 지운다(스냅숏 0).
func (r *PostgresClusterReconciler) awaitHeld(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	pod, err := r.memberPod(ctx, p.cluster, e.Pod)
	if err != nil {
		return err
	}
	switch {
	case pod == nil:
		// 새 파드가 없다 — 볼륨을 쓰는 postgres 가 없다.
	case string(pod.UID) == e.PodUID || pod.DeletionTimestamp != nil:
		// 지운 파드가 아직 있다 — 죽은 노드에 걸렸으면 force delete.
		return r.deleteMember(ctx, p.cluster, pod, p.now)
	case postgresHasRun(pod):
		e.PodUID = string(pod.UID)
		e.StoppedAt = p.now
		p.put(e)
		return r.deleteMember(ctx, p.cluster, pod, p.now)
	case !bootstrapHeld(pod):
		return nil
	}
	e.Phase = rebuildSnapshotting
	e.PhaseSince = p.now
	p.put(e)
	return nil
}

// bootstrapHeld 는 bootstrap init 이 비0 으로 끝났는지다(지금 또는 직전).
func bootstrapHeld(pod *corev1.Pod) bool {
	for _, st := range pod.Status.InitContainerStatuses {
		if st.Name != bootstrapContainerName {
			continue
		}
		return failedExit(st.State.Terminated) || failedExit(st.LastTerminationState.Terminated)
	}
	return false
}

func failedExit(t *corev1.ContainerStateTerminated) bool {
	return t != nil && t.ExitCode != 0
}

// postgresHasRun 은 postgres 컨테이너가 이 파드에서 돈 적 있는지다.
func postgresHasRun(pod *corev1.Pod) bool {
	for _, st := range pod.Status.ContainerStatuses {
		if st.Name != pgContainerName {
			continue
		}
		return st.State.Running != nil || st.State.Terminated != nil || st.RestartCount > 0 ||
			st.LastTerminationState.Running != nil || st.LastTerminationState.Terminated != nil
	}
	return false
}

// stepSnapshot 은 스냅숏을 만들고 readyToUse 를 기다린다 — 준비되면 재검증·예산 뒤 해제를 허가한다.
func (r *PostgresClusterReconciler) stepSnapshot(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if _, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, runtimeFor(e)); err != nil {
		return err
	}
	if e.Snapshot == "" {
		return r.takeSnapshot(ctx, p, e)
	}
	state, err := r.memberSnapshotStatus(ctx, p.cluster, e.Snapshot)
	if err != nil {
		return err
	}
	switch {
	case state == snapFailed:
		return r.failSnapshot(ctx, p, e, ReasonSnapshotFailed)
	case state == snapPending && p.now.Sub(e.PhaseSince) > snapshotReadyTimeout:
		return r.failSnapshot(ctx, p, e, ReasonSnapshotTimeout)
	case state == snapPending:
		return nil
	}

	// readyToUse — 해제 허가는 방금 직접 읽은 이 값만 믿는다(I3).
	if e.ReadyAt.IsZero() {
		e.ReadyAt = p.now
		p.put(e)
		commonsevents.Emitf(r.Recorder, p.cluster, memberSnapshotReadyReason,
			"shard %s: %s snapshot %s ready (data-bearing=%t)", e.Shard, e.Pod, e.Snapshot, e.DataBearing)
	}
	needless, err := r.rebuildNeedless(ctx, p, e)
	if err != nil {
		return err
	}
	if needless {
		return r.cancel(ctx, p, e)
	}
	r.admit(p, e)
	return nil
}

// takeSnapshot 은 멤버 PVC 의 VolumeSnapshot 을 만든다 — 실패한 시도 뒤면 reseedCooldown 을 기다린다.
func (r *PostgresClusterReconciler) takeSnapshot(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if e.Attempt > 0 && p.now.Sub(e.PhaseSince) < reseedCooldown {
		return nil
	}
	if r.MemberSnapshotClass == "" {
		return r.blockSnapshot(ctx, p, e, ReasonSnapshotClassUnset)
	}
	name := memberSnapshotName(e.Pod, e.Token)
	err := r.createMemberSnapshot(ctx, p.cluster, e, name)
	if meta.IsNoMatchError(err) {
		return r.failSnapshot(ctx, p, e, ReasonSnapshotUnavailable)
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	e.Snapshot = name
	e.PhaseSince = p.now
	e.Blocked = ""
	p.put(e)
	return nil
}

// blockSnapshot 은 스냅숏을 만들 수 없는 설정이다 — Stale 은 해제·종료, 그 밖은 hold 를 둔 채 기다린다.
func (r *PostgresClusterReconciler) blockSnapshot(ctx context.Context, p *rebuildPass, e memberRebuild, reason string) error {
	if e.Reason == rebuildStale {
		return r.abandon(ctx, p, e, reason)
	}
	r.block(p, e, reason)
	return nil
}

// failSnapshot 은 이번 시도를 버린다 — Stale 은 해제·종료, 그 밖은 새 토큰으로 reseedCooldown 뒤 다시.
func (r *PostgresClusterReconciler) failSnapshot(ctx context.Context, p *rebuildPass, e memberRebuild, reason string) error {
	if e.Reason == rebuildStale {
		return r.abandon(ctx, p, e, reason)
	}
	e.Attempt++
	e.Token = rebuildToken(e.Since, e.Attempt)
	e.Snapshot = ""
	e.ReadyAt = time.Time{}
	e.PhaseSince = p.now
	r.block(p, e, reason)
	return nil
}

// admit 는 해제를 허가한다 — 1h 창 해제 수가 예산 안이거나 가용성 예외일 때만. 원장이 먼저다: 이 pass 가
// CR 에 해제를 적고 다음 걸음(Releasing)이 CM 을 쓴다.
func (r *PostgresClusterReconciler) admit(p *rebuildPass, e memberRebuild) {
	admission := admitBudget
	if p.released() >= rebuildBudget(p.cluster) {
		if !p.availabilityException(e) {
			r.block(p, e, ReasonBudgetExhausted)
			return
		}
		admission = admitAvailability
	}
	outcome := releaseOutcome(e, admission)
	p.state.Ledger = append(p.state.Ledger, rebuildEntry{
		Pod: e.Pod, Shard: e.Shard, Token: e.Token, Reason: e.Reason,
		Outcome: outcome, Admission: admission, Released: p.now,
	})
	e.Phase = rebuildReleasing
	e.PhaseSince = p.now
	e.Blocked = ""
	p.put(e)
	MetricMemberRebuilds.WithLabelValues(p.cluster.Namespace, p.cluster.Name, e.Shard, string(e.Reason), string(outcome)).Inc()
	r.announceRelease(p, e, outcome)
}

// releaseOutcome 은 해제의 지표 결과다 — 분기점을 넘은 양성 증거가 가용성 예외보다 먼저다(경보를 가리지 않게).
func releaseOutcome(e memberRebuild, admission rebuildAdmission) rebuildOutcome {
	switch {
	case leads(e.EndLSN, e.PromotionLSN):
		return outcomeDataBearing
	case admission == admitAvailability:
		return outcomeAvailability
	case e.DataBearing:
		return outcomeUnproven
	}
	return outcomeReleased
}

// announceRelease 는 해제를 이벤트로 남긴다 — 분기점을 넘은 데이터를 버리면 Warning 이 따로 뜬다.
func (r *PostgresClusterReconciler) announceRelease(p *rebuildPass, e memberRebuild, outcome rebuildOutcome) {
	if outcome == outcomeDataBearing {
		commonsevents.EmitWarningf(r.Recorder, p.cluster, rebuildDiscardsNewerDataReason,
			"shard %s: %s WAL end %s is past promotion point %s; that WAL stays only in snapshot %s",
			e.Shard, e.Pod, e.EndLSN, e.PromotionLSN, e.Snapshot)
	}
	reason, action := memberRejoinReleasedReason, "released to rejoin"
	if e.Reason.reseeds() {
		reason, action = memberReseedRequestedReason, "released for reseed "+e.Token
	}
	commonsevents.Emitf(r.Recorder, p.cluster, reason, "shard %s: %s (%s) %s after snapshot %s (%s)",
		e.Shard, e.Pod, e.Reason, action, e.Snapshot, outcome)
}

// stepReleasing 은 해제를 CM 에 쓰고(hold 해제 · 재시드 토큰을 한 번에) 전파 뒤 파드를 다시 지운다.
func (r *PostgresClusterReconciler) stepReleasing(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	wrote, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, runtimeFor(e))
	if err != nil {
		return err
	}
	if wrote {
		e.PhaseSince = p.now
		p.put(e)
		return nil
	}
	if p.now.Sub(e.PhaseSince) < runtimePropagationWait {
		return nil
	}
	pod, err := r.memberPod(ctx, p.cluster, e.Pod)
	if err != nil {
		return err
	}
	if pod != nil {
		if err := r.deleteMember(ctx, p.cluster, pod, p.now); err != nil {
			return err
		}
	}
	e.Phase = rebuildVerifying
	e.PhaseSince = p.now
	p.put(e)
	return nil
}

// stepVerifying 은 멤버가 CM primary 의 복제 그래프에 붙고 재시드면 그 토큰까지 적용된 뒤 끝낸다.
func (r *PostgresClusterReconciler) stepVerifying(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if _, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, runtimeFor(e)); err != nil {
		return err
	}
	pod, err := r.memberPod(ctx, p.cluster, e.Pod)
	if err != nil {
		return err
	}
	if pod != nil && pod.DeletionTimestamp != nil {
		// 재삭제가 죽은 노드에 걸렸다 — 기존 force delete.
		return r.deleteMember(ctx, p.cluster, pod, p.now)
	}
	if pod == nil || !p.graphs[e.Shard].linked[e.Pod] || !r.tokenApplied(ctx, pod, e) {
		if p.now.Sub(e.PhaseSince) > staleStandbyReseedTimeout {
			r.block(p, e, ReasonNotRejoined)
		}
		return nil
	}
	return r.finish(ctx, p, e)
}

// finish 는 재구축을 끝낸다 — CM 정리(재시드 토큰) → fence 해제 → 원장 → 항목 삭제.
func (r *PostgresClusterReconciler) finish(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if _, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, memberRuntime{}); err != nil {
		return err
	}
	if err := unfencePVC(ctx, r.Client, p.cluster.Namespace, e.Pod); err != nil {
		return err
	}
	p.end(e, outcomeReleased)
	commonsevents.Emitf(r.Recorder, p.cluster, memberRebuildCompletedReason,
		"shard %s: %s rebuilt (%s) and replicating", e.Shard, e.Pod, e.Reason)
	return nil
}

// cancel 은 재검증이 필요 없다고 본 재구축을 파괴 없이 끝낸다.
func (r *PostgresClusterReconciler) cancel(ctx context.Context, p *rebuildPass, e memberRebuild) error {
	if err := r.drop(ctx, p, e, outcomeCancelled); err != nil {
		return err
	}
	commonsevents.Emitf(r.Recorder, p.cluster, rebuildCancelledReason,
		"shard %s: %s rebuild (%s) cancelled — runtime primary or already replicating; hold released", e.Shard, e.Pod, e.Reason)
	return nil
}

// abandon 은 스냅숏을 못 뜬 Stale 재구축을 파괴 없이 끝낸다.
func (r *PostgresClusterReconciler) abandon(ctx context.Context, p *rebuildPass, e memberRebuild, reason string) error {
	if err := r.drop(ctx, p, e, outcomeAbandoned); err != nil {
		return err
	}
	commonsevents.EmitWarningf(r.Recorder, p.cluster, ConditionMemberRebuildBlocked,
		"shard %s: %s rebuild (%s) abandoned without a snapshot (%s); hold released", e.Shard, e.Pod, e.Reason, reason)
	return nil
}

// drop 은 파괴 없이 끝낸다 — CM 정리가 먼저, 원장·지표가 뒤다.
func (r *PostgresClusterReconciler) drop(ctx context.Context, p *rebuildPass, e memberRebuild, outcome rebuildOutcome) error {
	if _, err := r.syncMemberRuntime(ctx, p.cluster, e.Shard, e.Pod, memberRuntime{}); err != nil {
		return err
	}
	p.end(e, outcome)
	MetricMemberRebuilds.WithLabelValues(p.cluster.Namespace, p.cluster.Name, e.Shard, string(e.Reason), string(outcome)).Inc()
	return nil
}

// block 은 항목을 막힌 사유와 함께 둔다 — 사유가 바뀔 때만 Warning 을 낸다.
func (r *PostgresClusterReconciler) block(p *rebuildPass, e memberRebuild, reason string) {
	changed := p.state.Active[e.Pod].Blocked != reason
	e.Blocked = reason
	p.put(e)
	if !changed {
		return
	}
	commonsevents.EmitWarningf(r.Recorder, p.cluster, ConditionMemberRebuildBlocked,
		"shard %s: %s rebuild (%s) blocked: %s", e.Shard, e.Pod, e.Reason, reason)
}

// rebuildNeedless 는 재검증이다 — 대상이 CM primary 이거나, in recovery 로 CM primary 의 복제 그래프에
// 붙어 있으면 재구축할 까닭이 없다. 그래프·exec 를 모르면 필요 없다고 보지 않는다.
func (r *PostgresClusterReconciler) rebuildNeedless(ctx context.Context, p *rebuildPass, e memberRebuild) (bool, error) {
	rt, found, err := r.readRuntime(ctx, p.cluster, e.Shard)
	if err != nil {
		return false, err
	}
	if found && endpointPod(rt.PrimaryEndpoint) == e.Pod {
		return true, nil
	}
	if !p.graphs[e.Shard].linked[e.Pod] {
		return false, nil
	}
	pod, err := r.memberPod(ctx, p.cluster, e.Pod)
	if err != nil || pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false, err
	}
	inRecovery, err := r.inRecovery(ctx, pod)
	return err == nil && inRecovery, nil
}

// memberRuntime 은 멤버 하나의 런타임 CM 목표다 — 영값은 hold·재시드 토큰 모두 없음이다.
type memberRuntime struct {
	held  bool   // MEMBER_HOLD 에 있다
	token string // RESEED_REQUESTS 값 — 빈 문자열은 항목 없음
}

// runtimeFor 는 단계가 요구하는 목표다 — 해제 전엔 hold, 해제 뒤엔 재시드 사유만 토큰.
func runtimeFor(e memberRebuild) memberRuntime {
	switch e.Phase {
	case rebuildHolding, rebuildSnapshotting:
		return memberRuntime{held: true}
	}
	if e.Reason.reseeds() {
		return memberRuntime{token: e.Token}
	}
	return memberRuntime{}
}

// syncMemberRuntime 은 멤버의 hold·재시드 토큰을 목표에 맞춘다 — 두 키를 한 번에 쓰고, 이미 맞으면
// 쓰지 않는다(참 = 썼다). 목록 키라 낙관적 잠금으로 쓴다.
func (r *PostgresClusterReconciler) syncMemberRuntime(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, pod string,
	want memberRuntime,
) (bool, error) {
	cm, found, err := r.runtimeConfigMap(ctx, cluster, shard)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("runtime ConfigMap of shard %s is absent", shard)
	}
	rt := runtimeFromData(cm.Data)
	hold := slices.DeleteFunc(slices.Clone(rt.Hold), func(member string) bool { return member == pod })
	if want.held {
		hold = append(hold, pod)
	}
	reseed := maps.Clone(rt.Reseed)
	delete(reseed, pod)
	if want.token != "" {
		reseed[pod] = want.token
	}
	data := map[string]string{runtimeKeyMemberHold: formatHold(hold), runtimeKeyReseedRequests: formatReseed(reseed)}
	if data[runtimeKeyMemberHold] == cm.Data[runtimeKeyMemberHold] &&
		data[runtimeKeyReseedRequests] == cm.Data[runtimeKeyReseedRequests] {
		return false, nil
	}
	return true, r.patchRuntimeLocked(ctx, cm, data)
}

// memberPod 는 이름으로 멤버 파드를 읽는다 — 없으면 nil.
func (r *PostgresClusterReconciler) memberPod(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get member pod %q: %w", name, err)
	}
	return &pod, nil
}

// deleteMember 는 멤버 파드를 지운다 — 이미 Terminating 이면 죽은 노드에 걸렸을 때만 force delete(#226).
func (r *PostgresClusterReconciler) deleteMember(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pod *corev1.Pod,
	now time.Time,
) error {
	if pod.DeletionTimestamp == nil {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete member pod %q: %w", pod.Name, err)
		}
		return nil
	}
	if r.stuckTerminatingOnDeadNode(ctx, cluster, pod, now) && r.podAbsentOrNotReady(ctx, cluster.Namespace, pod.Name) {
		return r.forceDeleteStuckPod(ctx, pod)
	}
	return nil
}

// forkLSN 은 샤드 정본의 promotionLSN(새 타임라인 분기점)이다 — 모르면 빈 문자열.
func forkLSN(cluster *postgresv1alpha1.PostgresCluster, shard string) string {
	state, err := loadRuntimeState(cluster)
	if err != nil {
		return ""
	}
	return state.Shards[shard].PromotionLSN
}

// endPosition 은 멤버가 가진 WAL 위치다. standby exec 는 정확하다(쓰기를 못 받는다). primary exec 와 상태
// 애노테이션(5 s 낡은 수신 위치)은 하한이라 분기점을 넘을 때만 쓴다 — 넘지 않으면 모름(보존)이다.
func (r *PostgresClusterReconciler) endPosition(ctx context.Context, p *rebuildPass, e memberRebuild, pod *corev1.Pod) string {
	inRecovery, lsn, err := r.walEnd(ctx, pod)
	if err == nil && (inRecovery || leads(lsn, e.PromotionLSN)) {
		return lsn
	}
	for i := range p.shards {
		if p.shards[i].Name != e.Shard {
			continue
		}
		position, known := r.receivePositions(ctx, p.cluster, p.shards[i])[pod.Name]
		if known && leads(positionLSN(position), e.PromotionLSN) {
			return positionLSN(position)
		}
	}
	return ""
}

// walEnd 는 도는 멤버에 끝 위치를 묻는다(in recovery, LSN).
func (r *PostgresClusterReconciler) walEnd(ctx context.Context, pod *corev1.Pod) (bool, string, error) {
	if r.PromotionPodExecutor == nil {
		return false, "", errors.New("pod executor is not configured")
	}
	// 죽은 노드의 파드에 exec 하면 시한까지 붙잡힌다.
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false, "", fmt.Errorf("%s is not running", pod.Name)
	}
	execCtx, cancel := context.WithTimeout(ctx, rebuildExecTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(execCtx, BackupSidecarTarget{
		Namespace: pod.Namespace,
		Pod:       pod.Name,
		Container: pgContainerName,
	}, []string{"psql", "-U", "postgres", "-d", "postgres", "-Atqc", endPositionQuery})
	if err != nil {
		return false, "", err
	}
	recovery, lsn, ok := strings.Cut(strings.TrimSpace(string(out)), psqlFieldSeparator)
	if !ok || (recovery != pgBoolTrue && recovery != pgBoolFalse) {
		return false, "", fmt.Errorf("end position answered %q", out)
	}
	if _, err := parseLSN(lsn); err != nil {
		return false, "", err
	}
	return recovery == pgBoolTrue, lsn, nil
}

// formatLSN 은 절대 바이트 위치를 LSN 표기로 쓴다.
func formatLSN(position uint64) string {
	return fmt.Sprintf("%X/%X", position>>32, position&0xFFFFFFFF)
}

// positionLSN 은 수신 위치(바이트, int64)를 LSN 표기로 쓴다 — 음수는 위치가 아니라 "" 다.
func positionLSN(position int64) string {
	if position < 0 {
		return ""
	}
	return formatLSN(uint64(position))
}

// walLead 는 끝 위치 − 분기점(바이트)이다 — 어느 쪽이든 모르면 known=false.
func walLead(end, fork string) (int64, bool) {
	e, err := parseLSN(end)
	if err != nil {
		return 0, false
	}
	f, err := parseLSN(fork)
	if err != nil {
		return 0, false
	}

	// 차이가 int64 를 넘으면 부호가 wrap 된다 — 모름으로 둔다.
	sign, gap := int64(1), e-f
	if e < f {
		sign, gap = -1, f-e
	}
	if gap > math.MaxInt64 {
		return 0, false
	}
	return sign * int64(gap), true
}

// leads 는 위치가 분기점을 넘는다는 양성 증거인지다.
func leads(end, fork string) bool {
	lead, known := walLead(end, fork)
	return known && lead > 0
}

// dataBearing 은 멤버가 분기점보다 앞선 WAL 을 가졌을 수 있는지다 — lead > 0 ∨ 모름.
func dataBearing(end, fork string) bool {
	lead, known := walLead(end, fork)
	return !known || lead > 0
}

// tokenApplied 는 재시드 항목이면 멤버가 그 토큰의 basebackup 을 끝냈는지다(PVC 루트의 applied 파일).
func (r *PostgresClusterReconciler) tokenApplied(ctx context.Context, pod *corev1.Pod, e memberRebuild) bool {
	if !e.Reason.reseeds() {
		return true
	}
	if r.PromotionPodExecutor == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	execCtx, cancel := context.WithTimeout(ctx, rebuildExecTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(execCtx, BackupSidecarTarget{
		Namespace: pod.Namespace,
		Pod:       pod.Name,
		Container: pgContainerName,
	}, []string{"sh", "-c", `if [ -f "$1" ]; then cat "$1"; fi`, "sh", pgDataMountPath + "/" + reseedAppliedMarker})
	return err == nil && strings.TrimSpace(string(out)) == e.Token
}

// createMemberSnapshot 은 멤버 데이터 PVC 의 VolumeSnapshot 을 만든다 — 소유 참조를 달지 않는다(클러스터를
// 지워도 스냅숏은 남는다).
func (r *PostgresClusterReconciler) createMemberSnapshot(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	e memberRebuild,
	name string,
) error {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	snap.SetNamespace(cluster.Namespace)
	snap.SetName(name)
	snap.SetLabels(map[string]string{
		memberSnapshotLabel: labelTrue,
		clusterLabelKey:     cluster.Name,
		ShardIDLabelKey:     e.Shard,
		rebuildReasonLabel:  string(e.Reason),
		dataBearingLabel:    strconv.FormatBool(e.DataBearing),
	})
	snap.Object["spec"] = map[string]any{
		"volumeSnapshotClassName": r.MemberSnapshotClass,
		"source":                  map[string]any{"persistentVolumeClaimName": fencing.PVCName(e.Pod)},
	}
	return r.Create(ctx, snap)
}

// memberSnapshotStatus 는 스냅숏 상태를 직접(캐시 밖) 읽는다 — status.error 나 사라짐은 실패다.
func (r *PostgresClusterReconciler) memberSnapshotStatus(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
) (snapshotState, error) {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, snap); err != nil {
		if apierrors.IsNotFound(err) {
			return snapFailed, nil
		}
		return snapPending, fmt.Errorf("get snapshot %q: %w", name, err)
	}
	if ready, _, _ := unstructured.NestedBool(snap.Object, "status", "readyToUse"); ready {
		return snapReady, nil
	}
	if _, failed, _ := unstructured.NestedMap(snap.Object, "status", "error"); failed {
		return snapFailed, nil
	}
	return snapPending, nil
}

// gcMemberSnapshots 는 오래됐거나 상한을 넘은 비 data-bearing 재구축 스냅숏을 지운다. 라벨(member-snapshot ·
// cluster)과 이름 접두 <cluster>- 로 이 클러스터 것만 본다 — 진행 중 항목의 스냅숏과 data-bearing(라벨이
// false 가 아닌 것 전부)은 남긴다. 게이지 member_snapshots 를 싣는다.
func (r *PostgresClusterReconciler) gcMemberSnapshots(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	now time.Time,
) {
	logger := log.FromContext(ctx)
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(volumeSnapshotGVK.GroupVersion().WithKind(volumeSnapshotGVK.Kind + "List"))
	if err := r.List(ctx, list, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{memberSnapshotLabel: labelTrue, clusterLabelKey: cluster.Name}); err != nil {
		if !meta.IsNoMatchError(err) {
			logger.V(1).Info("member snapshots unreadable; GC skipped", "error", err)
		}
		return
	}
	state, err := loadRebuildState(cluster)
	if err != nil {
		return
	}
	active := map[string]bool{}
	for _, e := range state.Active {
		active[e.Snapshot] = true
	}

	prefix := cluster.Name + "-"
	var spare []unstructured.Unstructured
	bearing := 0
	for _, snap := range list.Items {
		if !strings.HasPrefix(snap.GetName(), prefix) {
			continue
		}
		if snap.GetLabels()[dataBearingLabel] != labelFalse {
			bearing++
			continue
		}
		spare = append(spare, snap)
	}

	// 새것부터 — 상한 안의 새 스냅숏만 남긴다.
	slices.SortFunc(spare, func(a, b unstructured.Unstructured) int {
		if c := b.GetCreationTimestamp().Compare(a.GetCreationTimestamp().Time); c != 0 {
			return c
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	capacity, kept := memberSnapshotCap(cluster), 0
	for i := range spare {
		snap := &spare[i]
		expired := i >= capacity || now.Sub(snap.GetCreationTimestamp().Time) > memberSnapshotRetention
		if !expired || active[snap.GetName()] || snap.GetDeletionTimestamp() != nil {
			kept++
			continue
		}
		if err := r.Delete(ctx, snap); err != nil && !apierrors.IsNotFound(err) {
			logger.Info("member snapshot GC failed", "snapshot", snap.GetName(), "error", err)
			kept++
		}
	}
	MetricMemberSnapshots.WithLabelValues(cluster.Namespace, cluster.Name, labelTrue).Set(float64(bearing))
	MetricMemberSnapshots.WithLabelValues(cluster.Namespace, cluster.Name, labelFalse).Set(float64(kept))
}

// initRebuildSeries 는 샤드마다 재구축 카운터 시계열을 0 으로 만든다 — 첫 증분 전에 시계열이 없으면
// increase() 가 그 증분을 놓친다(O1 PostgresRebuildDiscardedNewerData, 설계 §3.9).
func initRebuildSeries(cluster *postgresv1alpha1.PostgresCluster, shards []postgresv1alpha1.ShardStatus) {
	for i := range shards {
		for _, reason := range rebuildReasons {
			for _, outcome := range rebuildOutcomes {
				MetricMemberRebuilds.WithLabelValues(cluster.Namespace, cluster.Name, shards[i].Name,
					string(reason), string(outcome)).Add(0)
			}
		}
	}
}

// observeRebuilds 는 남은 예산과 샤드별 hold 멤버 수(런타임 CM 기준 — 실제로 멈추는 목록)를 싣는다.
func (r *PostgresClusterReconciler) observeRebuilds(ctx context.Context, p *rebuildPass) {
	ns, name := p.cluster.Namespace, p.cluster.Name
	MetricMemberRebuildBudget.WithLabelValues(ns, name).Set(float64(max(0, rebuildBudget(p.cluster)-p.released())))
	for i := range p.shards {
		rt, found, err := r.readRuntime(ctx, p.cluster, p.shards[i].Name)
		if err != nil || !found {
			continue
		}
		MetricMembersHeld.WithLabelValues(ns, name, p.shards[i].Name).Set(float64(len(rt.Hold)))
	}
}

// setRebuildCondition 은 막힌 항목을 MemberRebuildBlocked 로 보인다(KSM → O1 경보).
func setRebuildCondition(cluster *postgresv1alpha1.PostgresCluster, state rebuildState) {
	var blocked []string
	reason := ""
	for _, pod := range slices.Sorted(maps.Keys(state.Active)) {
		e := state.Active[pod]
		if e.Blocked == "" {
			continue
		}
		if reason == "" {
			reason = e.Blocked
		}
		blocked = append(blocked, pod+"="+e.Blocked)
	}
	if len(blocked) == 0 {
		setCondition(&cluster.Status.Conditions, ConditionMemberRebuildBlocked, metav1.ConditionFalse,
			ReasonRebuildClear, "no member rebuild is blocked", cluster.Generation)
		return
	}
	setCondition(&cluster.Status.Conditions, ConditionMemberRebuildBlocked, metav1.ConditionTrue,
		reason, "member rebuild blocked: "+strings.Join(blocked, ","), cluster.Generation)
}

// put 은 항목을 바꾼다 — 같은 값이면 저장할 것이 없다.
func (p *rebuildPass) put(e memberRebuild) {
	if old, ok := p.state.Active[e.Pod]; ok && old == e {
		return
	}
	p.state.Active[e.Pod] = e
	p.dirty = true
}

// end 는 항목을 원장에 끝으로 적고 지운다 — 해제된 항목은 그 줄에 끝을, 아니면 새 줄을 쓴다.
func (p *rebuildPass) end(e memberRebuild, outcome rebuildOutcome) {
	delete(p.state.Active, e.Pod)
	p.dirty = true
	for i := range p.state.Ledger {
		if p.state.Ledger[i].Pod == e.Pod && p.state.Ledger[i].Token == e.Token {
			p.state.Ledger[i].Ended = p.now
			return
		}
	}
	p.state.Ledger = append(p.state.Ledger, rebuildEntry{
		Pod: e.Pod, Shard: e.Shard, Token: e.Token, Reason: e.Reason, Outcome: outcome, Ended: p.now,
	})
}

// queue 는 항목을 요청 순(같으면 이름 순)으로 준다 — 샤드당 1 이 먼저 온 요청을 고른다.
func (p *rebuildPass) queue() []memberRebuild {
	out := slices.Collect(maps.Values(p.state.Active))
	slices.SortFunc(out, func(a, b memberRebuild) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return strings.Compare(a.Pod, b.Pod)
	})
	return out
}

// prune 은 창을 벗어난 원장 줄을 버린다 — 진행 중 항목의 줄은 남긴다.
func (p *rebuildPass) prune() {
	active := map[string]bool{}
	for _, e := range p.state.Active {
		active[e.Token] = true
	}
	before := len(p.state.Ledger)
	p.state.Ledger = slices.DeleteFunc(p.state.Ledger, func(x rebuildEntry) bool {
		last := x.Released
		if x.Ended.After(last) {
			last = x.Ended
		}
		return !active[x.Token] && p.now.Sub(last) >= rebuildBudgetWindow
	})
	if len(p.state.Ledger) != before {
		p.dirty = true
	}
}

// mayStart 는 항목이 멤버를 멈춰도 되는지다 — 멤버 쿨다운(마지막 끝 + reseedCooldown) · 샤드당 1.
func (p *rebuildPass) mayStart(e memberRebuild) bool {
	for _, x := range p.state.Ledger {
		if x.Pod == e.Pod && !x.Ended.IsZero() && p.now.Sub(x.Ended) < reseedCooldown {
			return false
		}
	}
	for _, other := range p.state.Active {
		if other.Pod != e.Pod && other.Shard == e.Shard && other.started() {
			return false
		}
	}
	return true
}

// within 은 시각이 예산 창 안인지다.
func (p *rebuildPass) within(at time.Time) bool {
	return !at.IsZero() && p.now.Sub(at) < rebuildBudgetWindow
}

// released 는 창 안 예산 해제 수다 — 가용성 예외는 예산 밖이라 세지 않는다.
func (p *rebuildPass) released() int {
	n := 0
	for _, x := range p.state.Ledger {
		if x.Admission == admitBudget && p.within(x.Released) {
			n++
		}
	}
	return n
}

// availabilityException 은 예산 밖 해제를 허가할지다 — 옛 primary 이고, 샤드 그래프가 streaming standby 0 을
// 알고, 창 안 그 샤드의 예외가 availabilityExceptions 보다 적을 때.
func (p *rebuildPass) availabilityException(e memberRebuild) bool {
	g := p.graphs[e.Shard]
	if !e.Reason.oldPrimary() || g.root == "" || !g.known {
		return false
	}
	for pod := range g.linked {
		if pod != e.Pod {
			return false
		}
	}
	used := 0
	for _, x := range p.state.Ledger {
		if x.Shard == e.Shard && x.Admission == admitAvailability && p.within(x.Released) {
			used++
		}
	}
	return used < availabilityExceptions
}
