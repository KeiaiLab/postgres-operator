/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 자동 failover 의 펜스·수신 끊기·승자·재지정이다(ADR-0036). 한 reconcile 안의 걸음이고 걸음마다
// 멱등이라 상태를 남기지 않는다 — 재기동한 오퍼레이터는 처음부터 다시 한다.
//
//	F1 fenceOldPrimary   옛 primary: standby.signal · 마커 rm → fast → immediate → SIGKILL → 확인
//	F2 cutSurvivors      생존 standby 전부(동시): primary_conninfo '' + reload → walreceiver 0 행
//	F3 failoverWinner    끊긴 생존자 중 끝 위치 최대(switchoverWinner) — 바뀌면 #220 가드·readiness 를 다시 본다
//	F4 promoteAndPublish 기존(failover_promoter.go)
//	F5 retargetSurvivors 나머지 생존자 → 승자, 재지정 기록(그래프가 다시 안 한다)

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
)

// fenceOutcome 은 옛 primary 펜스 결과다.
type fenceOutcome string

const (
	fenceStopped     fenceOutcome = "Stopped"     // pg_ctl fast·immediate 로 멈췄다
	fenceKilled      fenceOutcome = "Killed"      // SIGKILL 로 멈췄다
	fenceDown        fenceOutcome = "Down"        // postgres 가 이미 안 돈다(파드 없음 포함)
	fenceStandby     fenceOutcome = "Standby"     // 이미 standby 로 돈다(앞선 펜스가 아닌 standby.signal) — 정지 없음
	fenceStuck       fenceOutcome = "Stuck"       // SIGKILL 뒤에도 남았다
	fenceUnreachable fenceOutcome = "Unreachable" // exec 못 함·실패·시한 초과 — 결과 모름
)

const (
	// fenceFastWait·fenceImmediateWait 는 pg_ctl stop 단계마다의 상한이다.
	fenceFastWait      = statusPollInterval
	fenceImmediateWait = statusPollInterval
	// fenceExecBudget 은 펜스 exec 1회의 상한이다 — 두 정지 단계 + SIGKILL·확인 한 박자(15 s).
	fenceExecBudget = fenceFastWait + fenceImmediateWait + statusPollInterval
	// fenceKillPolls 는 SIGKILL 뒤 확인 횟수다(pgPromotePoll 간격) — 마지막 박자는 exec 여유로 남긴다.
	fenceKillPolls = int(statusPollInterval/pgPromotePoll) - 1

	// cutWait 는 reload 뒤 walreceiver 가 내려가기를 기다리는 상한이다.
	cutWait = replicationProbeTimeout
	// cutBudget 은 멤버 하나 끊기 exec 의 상한이다 — cutWait + 질의·reload 한 박자.
	cutBudget = cutWait + statusPollInterval
	// cutPolls 는 reload 뒤 walreceiver 확인 횟수다(pgPromotePoll 간격).
	cutPolls = int(cutWait / pgPromotePoll)

	// fenceResultPrefix 는 펜스 exec 출력의 결과 줄이다.
	fenceResultPrefix = "FENCE_RESULT="
	// fencePendingMarker 는 펜스가 쓴 standby.signal 이 아직 정지로 증명되지 않았다는 PGDATA 표식이다 — 다시 돈
	// 펜스가 자기 표식을 standby 로 오인하지 않는다.
	fencePendingMarker = ".keiailab-fence-pending"
	// cutResultPrefix 는 끊기 exec 출력의 결과 줄이다 — cut(끊음) · primary(in recovery 아님).
	cutResultPrefix = "CUT_RESULT="
	cutResultCut    = "cut"
	cutResultPrim   = "primary"
	// walReceiverQuery 는 standby 의 walreceiver 행 수다 — 0 이면 더 받지 않는다.
	walReceiverQuery = "SELECT count(*) FROM pg_stat_wal_receiver"

	primaryFencedReason         = "PrimaryFenced"
	primaryFenceUnprovenReason  = "PrimaryFenceUnproven"
	standbyCutFailedReason      = "StandbyCutFailed"
	failoverWinnerChangedReason = "FailoverWinnerChanged"
	failoverWinnerUnavailable   = "FailoverWinnerUnavailable"
)

// fenceScriptResults 는 스크립트가 낼 수 있는 결과다 — 그 밖은 Unreachable(증명 없음).
var fenceScriptResults = []fenceOutcome{fenceStopped, fenceKilled, fenceDown, fenceStandby, fenceStuck}

// prepareFailover 는 F1–F3 이다 — 승격할 멤버와 끊긴 생존자를 돌려준다. 후보가 이미 primary 면(앞선 승격 뒤 발행
// 실패) 후보가 승자이고 F3 를 건너뛴다. 후보가 아닌 생존자가 primary 면 오류다 — 둘째 primary 를 만들지 않는다.
// 후보가 샤드 멤버에 없거나 승자를 못 정하면 끊기를 그대로 두고 오류다 — CM primary 는
// 방금 멈춘 파드거나(Unreachable 이면) 아직 쓰기를 받는 옛 primary 라 되돌리면 S3 분기가 다시 생긴다. 디바운스 창은
// 남아 다음 reconcile 이 곧바로 F1 부터 다시 한다.
func (r *PostgresClusterReconciler) prepareFailover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, oldPrimary string,
	candidate *postgresv1alpha1.ShardEndpoint,
) (*postgresv1alpha1.ShardEndpoint, []corev1.Pod, error) {
	if oldPrimary != "" && oldPrimary != candidate.Pod {
		r.fenceOldPrimary(ctx, cluster, shard, oldPrimary)
	}

	ref := shardRefNamed(cluster, shard)
	members, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		return nil, nil, err
	}
	// 옛 primary 가 standby 로 다시 떠 후보면 끊기·재기에 든다 — 빼면 뒤처진 standby 가 이긴다.
	exclude := oldPrimary
	if oldPrimary == candidate.Pod {
		exclude = ""
	}
	cut, primaries := r.cutSurvivors(ctx, cluster, shard, members, exclude)

	// 멤버가 아닌 후보는 끊지도 재지도 못했다 — 승격하면 끊긴 생존자보다 뒤처졌을 수 있다.
	var winner *postgresv1alpha1.ShardEndpoint
	err = fmt.Errorf("shard %s: candidate %s is not a shard member", shard, candidate.Pod)
	if slices.ContainsFunc(members, func(p corev1.Pod) bool { return p.Name == candidate.Pod }) {
		winner, err = r.pickWinner(ctx, cluster, shard, cut, primaries, candidate, ref.service)
	}
	if err != nil {
		commonsevents.EmitWarningf(r.Recorder, cluster, failoverWinnerUnavailable,
			"shard %s: %d survivors left cut, no promotion: %v", shard, len(cut), err)
		return nil, nil, err
	}
	return winner, cut, nil
}

// pickWinner 는 primary 로 답한 생존자를 먼저 본다 — 후보뿐이면 후보가 승자(F3 생략), 다른 멤버면 둘째 primary
// 라 오류다. 없으면 F3 다.
func (r *PostgresClusterReconciler) pickWinner(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	cut []corev1.Pod,
	primaries []string,
	candidate *postgresv1alpha1.ShardEndpoint,
	service string,
) (*postgresv1alpha1.ShardEndpoint, error) {
	if slices.ContainsFunc(primaries, func(p string) bool { return p != candidate.Pod }) {
		return nil, fmt.Errorf("shard %s: survivors %v answered primary, candidate %s", shard, primaries, candidate.Pod)
	}
	if len(primaries) > 0 {
		return candidate, nil
	}
	return r.failoverWinner(ctx, cluster, shard, cut, candidate, service)
}

// shardRefNamed 는 이름의 샤드다 — ordinal 샤드는 서수를 이름에서 찾는다(기록된 status 에 기대지 않는다).
func shardRefNamed(cluster *postgresv1alpha1.PostgresCluster, shard string) shardRef {
	for ord := int32(0); ord < cluster.Spec.Shards.InitialCount; ord++ {
		if ShardIDForOrdinal(ord) == shard {
			return ordinalShardRef(cluster, ord)
		}
	}
	return namedShardRef(cluster, shard)
}

// fenceOldPrimary 는 F1 이다 — 결과를 지표·이벤트로 남긴다. 어느 결과든 끊기(F2)로 간다.
func (r *PostgresClusterReconciler) fenceOldPrimary(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, pod string,
) fenceOutcome {
	outcome := r.fenceExec(ctx, cluster, pod)
	MetricFailoverFence.WithLabelValues(cluster.Namespace, cluster.Name, shard, string(outcome)).Inc()
	log.FromContext(ctx).Info("old primary fenced", "shard", shard, "pod", pod, "outcome", outcome)

	if outcome == fenceStuck || outcome == fenceUnreachable {
		commonsevents.EmitWarningf(r.Recorder, cluster, primaryFenceUnprovenReason,
			"shard %s: old primary %s fence %s; survivors are cut before promotion", shard, pod, outcome)
		return outcome
	}
	commonsevents.Emitf(r.Recorder, cluster, primaryFencedReason, "shard %s: old primary %s %s", shard, pod, outcome)
	return outcome
}

// fenceExec 는 펜스 exec 한 번이다. 죽은 노드의 파드에 exec 하면 시한까지 붙잡혀 exec 하지 않는다.
func (r *PostgresClusterReconciler) fenceExec(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
) fenceOutcome {
	pod, err := r.memberPod(ctx, cluster, name)
	if err != nil {
		return fenceUnreachable
	}
	if pod == nil {
		return fenceDown
	}
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return fenceUnreachable
	}

	execCtx, cancel := context.WithTimeout(ctx, fenceExecBudget)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(execCtx, memberTarget(pod), fenceCommand())
	if err != nil {
		return fenceUnreachable
	}
	return parseFenceOutput(out)
}

// parseFenceOutput 은 펜스 exec 의 결과 줄이다 — 없거나 모르는 값은 Unreachable.
func parseFenceOutput(out []byte) fenceOutcome {
	for line := range strings.SplitSeq(string(out), "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), fenceResultPrefix)
		if ok && slices.Contains(fenceScriptResults, fenceOutcome(value)) {
			return fenceOutcome(value)
		}
	}
	return fenceUnreachable
}

// fenceCommand 는 옛 primary 를 멈추는 exec 다(POSIX sh — pg 이미지에 ps·pgrep·awk 가 없다). 생존 판정은 psql 이
// 아니라 /proc 이다: 얼어 있는 postgres 는 psql 을 시한까지 붙잡아 SIGKILL 단에 못 간다. 순서가 계약이다:
//
//	(standby.signal 이 이미 있고 돈다 → Standby) → 표식(pending · standby.signal · 승격 마커 rm)
//	  → (안 돌면 Down) → fast → immediate → SIGKILL → 비좀비 0 확인 → pending rm
//
// 표식이 먼저라 매니저가 깨어나 컨테이너가 init 없이 다시 떠도 Follower 다. fast shutdown 은 연결된 standby 에
// 종료 체크포인트까지 보낸다 — 끊기(F2)보다 앞서는 이유다. 좀비(Z)는 멈춘 것이다: 얼어 있는 매니저가 죽은 자식을
// 거두지 않는다. pending 표식은 앞선 펜스가 Stuck 으로 남긴 standby.signal 을 Standby 로 오인하지 않게 한다.
// PROC_DIR 은 시험이 가짜 /proc 을 넣는 자리다.
func fenceCommand() []string {
	seconds := func(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
	script := `set -u
BIN="${POSTGRES_BIN_DIR:-/usr/lib/postgresql/18/bin}"
DATA="${POSTGRES_DATA_DIR:-/var/lib/postgresql/data/pgdata}"
PROC="${PROC_DIR:-/proc}"

alive() {
  { read -r comm < "$1/comm"; } 2>/dev/null || return 1
  [ "$comm" = postgres ] || return 1
  { read -r stat < "$1/stat"; } 2>/dev/null || return 1
  rest="${stat##*) }"
  [ "${rest%% *}" != Z ]
}

live() {
  pm=
  if { read -r pm < "$DATA/postmaster.pid"; } 2>/dev/null && [ -n "$pm" ] && alive "$PROC/$pm"; then
    echo "$pm"
  fi
  for d in "$PROC"/[0-9]*; do
    [ "${d##*/}" = "${pm:-}" ] && continue
    alive "$d" && echo "${d##*/}"
  done
}

result() {
  case "$1" in ` + string(fenceStopped) + `|` + string(fenceKilled) + `|` + string(fenceDown) + `) rm -f "$DATA/` + fencePendingMarker + `" 2>/dev/null ;; esac
  echo "` + fenceResultPrefix + `$1"
  exit 0
}

if [ -e "$DATA/` + standbySignalFile + `" ] && [ ! -e "$DATA/` + fencePendingMarker + `" ] && [ -n "$(live)" ]; then
  result ` + string(fenceStandby) + `
fi
touch "$DATA/` + fencePendingMarker + `" "$DATA/` + standbySignalFile + `" 2>/dev/null || true
rm -f "$DATA/` + promotedPrimaryMarker + `" 2>/dev/null || true
[ -z "$(live)" ] && result ` + string(fenceDown) + `

"$BIN/pg_ctl" stop -D "$DATA" -m fast -t ` + seconds(fenceFastWait) + ` >/dev/null 2>&1 || true
[ -z "$(live)" ] && result ` + string(fenceStopped) + `
"$BIN/pg_ctl" stop -D "$DATA" -m immediate -t ` + seconds(fenceImmediateWait) + ` >/dev/null 2>&1 || true
[ -z "$(live)" ] && result ` + string(fenceStopped) + `

for p in $(live); do
  kill -9 "$p" 2>/dev/null || true
done
i=0
while [ "$i" -lt ` + strconv.Itoa(fenceKillPolls) + ` ]; do
  [ -z "$(live)" ] && result ` + string(fenceKilled) + `
  i=$((i + 1))
  sleep ` + seconds(pgPromotePoll) + `
done
[ -z "$(live)" ] && result ` + string(fenceKilled) + `
result ` + string(fenceStuck) + `
`
	return []string{"sh", "-c", script}
}

// cutSurvivors 는 F2 다 — exclude(옛 primary)를 뺀 Running ∧ in recovery 멤버의 수신을 동시에 끊는다. 끊긴 멤버와
// primary 로 답한 멤버를 돌려준다. 실패한 멤버는 승자 후보에서 빠진다.
func (r *PostgresClusterReconciler) cutSurvivors(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	members []corev1.Pod,
	exclude string,
) ([]corev1.Pod, []string) {
	pods := slices.DeleteFunc(slices.Clone(members), func(p corev1.Pod) bool {
		return p.Name == exclude || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning
	})

	// 멤버마다 동시에 — 끊는 동안 다른 멤버가 옛 primary 에서 더 받지 않게.
	standby := make([]bool, len(pods))
	errs := make([]error, len(pods))
	var wg sync.WaitGroup
	for i := range pods {
		wg.Go(func() { standby[i], errs[i] = r.cutMember(ctx, &pods[i]) })
	}
	wg.Wait()

	var cut []corev1.Pod
	var primaries []string
	for i := range pods {
		if !standby[i] {
			primaries = append(primaries, pods[i].Name)
			continue
		}
		if errs[i] != nil {
			commonsevents.EmitWarningf(r.Recorder, cluster, standbyCutFailedReason,
				"shard %s: %s intake not cut, excluded from the failover winner: %v", shard, pods[i].Name, errs[i])
			continue
		}
		cut = append(cut, pods[i])
	}
	return cut, primaries
}

// cutMember 는 standby 하나의 수신을 exec 한 번으로 끊는다 — standby 가 아니면 (false, nil). in recovery 를 모르거나
// walreceiver 가 cutWait 안에 안 내려가면 끊기 실패다.
func (r *PostgresClusterReconciler) cutMember(ctx context.Context, pod *corev1.Pod) (bool, error) {
	cutCtx, cancel := context.WithTimeout(ctx, cutBudget)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(cutCtx, memberTarget(pod), cutCommand())
	if err != nil {
		return true, err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case cutResultPrefix + cutResultCut:
			return true, nil
		case cutResultPrefix + cutResultPrim:
			return false, nil
		}
	}
	return true, fmt.Errorf("cut answered %q", out)
}

// cutCommand 는 수신 끊기 exec 다: in recovery 확인 → primary_conninfo ” + reload → walreceiver 0 행까지 대기.
// 확인마다 한 박자 먼저 잔다 — reload(SIGHUP)가 walreceiver 에 닿기 전의 행을 끊김으로 오인하지 않는다.
// ALTER SYSTEM 은 트랜잭션 블록 안에서 못 돌아 -c 를 나눈다(retargetCommand 와 같다).
func cutCommand() []string {
	script := `set -eu
BIN="${POSTGRES_BIN_DIR:-/usr/lib/postgresql/18/bin}"
DSN="${POSTGRES_LOCAL_DSN:-host=/var/run/postgresql user=postgres dbname=postgres}"
q() { "$BIN/psql" "$DSN" -v ON_ERROR_STOP=1 -Atq "$@"; }

REC="$(q -c "` + recoveryQuery + `")"
if [ "$REC" != ` + pgBoolTrue + ` ]; then
  echo "` + cutResultPrefix + cutResultPrim + `"
  exit 0
fi
q -c "ALTER SYSTEM SET primary_conninfo = ''" -c "SELECT pg_reload_conf()" >/dev/null
i=0
while [ "$i" -lt ` + strconv.Itoa(cutPolls) + ` ]; do
  sleep ` + strconv.FormatInt(int64(pgPromotePoll/time.Second), 10) + `
  N="$(q -c "` + walReceiverQuery + `")" || N=""
  if [ "$N" = 0 ]; then
    echo "` + cutResultPrefix + cutResultCut + `"
    exit 0
  fi
  i=$((i + 1))
done
echo "walreceiver still up after ` + strconv.FormatInt(int64(cutWait/time.Second), 10) + `s" >&2
exit 1
`
	return []string{"sh", "-c", script}
}

// failoverWinner 는 F3 다 — 끊긴 생존자마다 끝 위치를 재고 최대(동률 → 판정 후보 → 낮은 서수)를 고른다. 승자가
// 바뀌면 #220 가드와 exec readiness 를 다시 본다. 잰 멤버가 없거나 승자가 막히면 오류다.
func (r *PostgresClusterReconciler) failoverWinner(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	cut []corev1.Pod,
	candidate *postgresv1alpha1.ShardEndpoint,
	service string,
) (*postgresv1alpha1.ShardEndpoint, error) {
	received := r.cutPositions(ctx, cut)
	for pod := range r.graphDetached(cluster, cut, time.Now()) {
		delete(received, pod)
	}
	if len(received) == 0 {
		return nil, fmt.Errorf("shard %s: no cut survivor has a measurable WAL end", shard)
	}
	need := slices.Max(slices.Collect(maps.Values(received)))
	winner := switchoverWinner(candidate.Pod, "", need, received)
	if winner == candidate.Pod {
		return candidate, nil
	}

	// 사유를 가른다 — 후보가 뒤처졌는지, 후보를 못 끊거나 못 쟀는지.
	format := "shard %s: %s received %s, ahead of candidate %s"
	if _, measured := received[candidate.Pod]; !measured {
		format = "shard %s: %s received %s, candidate %s cut failed or unmeasured"
	}
	commonsevents.Emitf(r.Recorder, cluster, failoverWinnerChangedReason, format, shard, winner, formatLSN(need), candidate.Pod)
	skip, err := r.shouldSkipFencedCandidate(ctx, cluster, winner)
	if err != nil {
		return nil, fmt.Errorf("failover winner %q #220 failback guard unknown: %w", winner, err)
	}
	if skip {
		return nil, fmt.Errorf("failover winner %q PVC is fenced while an unfenced member serves (#220 failback guard)", winner)
	}
	if err := r.promotionCandidateReadyForExec(ctx, cluster.Namespace, winner); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(cut, func(p corev1.Pod) bool { return p.Name == winner })
	return &postgresv1alpha1.ShardEndpoint{Pod: winner, Endpoint: memberEndpoint(&cut[i], service), Ready: true}, nil
}

// cutPositions 는 끊긴 멤버마다 끝 위치(수신·재생 중 큰 것)다 — 못 잰 멤버는 뺀다.
func (r *PostgresClusterReconciler) cutPositions(ctx context.Context, cut []corev1.Pod) map[string]uint64 {
	lsn := make([]uint64, len(cut))
	known := make([]bool, len(cut))
	var wg sync.WaitGroup
	for i := range cut {
		wg.Go(func() {
			inRecovery, end, err := r.memberLSN(ctx, &cut[i])
			known[i] = err == nil && inRecovery
			lsn[i] = end
		})
	}
	wg.Wait()

	received := map[string]uint64{}
	for i := range cut {
		if !known[i] {
			log.FromContext(ctx).Info("cut survivor WAL end unknown", "pod", cut[i].Name)
			continue
		}
		received[cut[i].Name] = lsn[i]
	}
	return received
}

// graphDetached 는 그래프가 memberRetargetAfter 넘게 끊겼다고 본 생존자다 — 연결 기록이 있는 생존자가 있을
// 때만(후보 필터 linkedCandidateFilter 와 같은 규칙, §3.3). 오래 끊긴 standby 의 끝 위치는 다른 타임라인일 수
// 있어 승자를 다투지 않는다(끊고 재지정은 한다).
func (r *PostgresClusterReconciler) graphDetached(
	cluster *postgresv1alpha1.PostgresCluster,
	cut []corev1.Pod,
	now time.Time,
) map[string]bool {
	r.graphMu.Lock()
	records := maps.Clone(r.memberAbsentSince[cluster.Namespace+"/"+cluster.Name])
	r.graphMu.Unlock()

	detached := map[string]bool{}
	linked := false
	for i := range cut {
		since, recorded := records[cut[i].Name]
		switch {
		case !recorded:
		case absentLong(since, now):
			detached[cut[i].Name] = true
		default:
			linked = true
		}
	}
	if !linked {
		return nil
	}
	return detached
}

// retargetSurvivors 는 F5 다 — 승자를 뺀 끊긴 멤버를 승자로 재지정하고 기록한다. 분기점 = 승자 끝 ≥ 각 생존자
// 끝이라 새 타임라인을 따른다. 실패는 그래프의 retargetDetached 가 잇는다.
func (r *PostgresClusterReconciler) retargetSurvivors(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	cut []corev1.Pod,
	winner failover.PromotionTarget,
) {
	key := cluster.Namespace + "/" + cluster.Name
	logger := log.FromContext(ctx).WithValues("shard", shard)
	for i := range cut {
		pod := &cut[i]
		if pod.Name == winner.Pod {
			continue
		}
		err := r.retargetStandby(ctx, pod, winner.Endpoint)
		r.recordRetarget(key, pod.Name, winner.Endpoint, err == nil, time.Now())
		if err != nil {
			logger.Info("survivor retarget failed", "pod", pod.Name, "error", err)
			continue
		}
		commonsevents.Emitf(r.Recorder, cluster, memberRetargetedReason,
			"shard %s: %s primary_conninfo now follows %s", shard, pod.Name, primaryEndpointHost(winner.Endpoint))
	}
}
