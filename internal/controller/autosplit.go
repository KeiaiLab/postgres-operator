// Package controller — autosplit.go 는 AutoSplit(자동 shard 확장) 결정·오케스트레이션
// 루프다. spec.autoSplit 이 활성이고 shardingMode=native 일 때, per-shard 관측치를
// 트리거 임계값(sizeThresholdGB / cpuPercent / p99LatencyMs)과 비교하고, 임계 초과가
// durationMinutes 동안 *지속*되면 그 shard 의 키 범위를 중점에서 둘로 나누는
// ShardSplitJob 을 자동 생성한다(requireApproval 이면 승인 annotation 대기).
//
// 관측 파이프라인: ① size = instance manager 가 primary 에서 pg_database_size 를
// statusapi.Status.SizeBytes 로 보고 → aggregate_status 가 ShardStatus.SizeBytes 로 집계
// → statusShardObserver 가 읽음. ② cpu = cpuAugmentingObserver 가 shard primary Pod 의
// metrics.k8s.io PodMetrics(사용량) ÷ Pod CPU request × 100 을 계산(autosplit_cpu.go).
// metrics-server 부재 시 CPU 관측 0(graceful — AND 조건상 CPU 트리거 미발동, 오탐 없음).
// ③ P99 latency 는 아직 미결선(라우터 per-shard 지연 히스토그램 필요) → 0. latency 만
// 활성화 시 AutoSplitEligible condition 이 MetricsSourceMissing 으로 사유를 노출한다.
package controller

import (
	"context"
	"fmt"
	"hash/crc32"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

const (
	// AnnotationAutoSplit 는 이 ShardSplitJob 이 AutoSplit 루프가 자동 생성했음을 표식한다.
	AnnotationAutoSplit = "postgres.keiailab.io/autosplit"
	// AnnotationAutoSplitApproval 은 승인 요구 여부를 나타낸다: "required"(수동 승인 대기)
	// 또는 "auto"(즉시 실행 허용). requireApproval spec 을 반영한다.
	AnnotationAutoSplitApproval = "postgres.keiailab.io/autosplit-approval"
	// AnnotationAutoSplitApproved 는 운영자가 승인 시 "true" 로 설정한다(수동). 이 값이
	// 있어야 approval=required 인 job 이 Pending 을 벗어난다.
	AnnotationAutoSplitApproved = "postgres.keiailab.io/autosplit-approved"
	// autoSplitApprovalRequired 는 AnnotationAutoSplitApproval 의 수동 승인 대기 값이다.
	autoSplitApprovalRequired = "required"

	// bytesPerGB 는 sizeThresholdGB(GB) → bytes 환산 상수(10진 GB, disk 표기 관례).
	bytesPerGB = int64(1_000_000_000)

	// autoSplitEligibleReasonEligible 등은 AutoSplitEligible condition 의 reason 이다.
	autoSplitReasonEligible    = "SplitCandidate"
	autoSplitReasonNone        = "NoCandidate"
	autoSplitReasonDisabled    = "Disabled"
	autoSplitReasonInProgress  = "SplitInProgress"
	autoSplitReasonUnsupported = "UnsupportedVindex"
	autoSplitReasonNoMetrics   = "MetricsSourceMissing"
)

// ShardObservation 은 한 shard 의 AutoSplit 트리거 관측치다.
type ShardObservation struct {
	// ShardID 는 ShardStatus.Name / ShardRangeEntry.Shard 와 일치하는 shard 식별자.
	ShardID string
	// SizeBytes 는 shard 데이터베이스 크기(bytes). 0 = 미관측.
	SizeBytes int64
	// CPUPercent 는 평균 CPU 사용률(%, 사용량/request). 0 = 미관측(metrics-server 부재
	// 또는 request 미설정). cpuAugmentingObserver 가 채운다.
	CPUPercent int32
	// P99LatencyMs 는 P99 지연(ms). 0 = 미관측(라우터 지연 메트릭 미결선 — 후속).
	P99LatencyMs int32
	// PVCCapacityBytes 는 shard 데이터 PVC 의 용량(bytes, spec.shards.storage.size). 0 =
	// 미상(용량 미상 시 PVCUtilizationPercent 트리거는 breach 하지 않음). PVC 사용률(%)
	// 트리거(K-5)가 SizeBytes/PVCCapacityBytes 로 사용률을 계산하는 데 쓴다.
	PVCCapacityBytes int64
}

// ShardMetricsObserver 는 cluster 의 shard 별 관측치를 수집한다. default 구현
// statusShardObserver 는 cluster.Status.Shards 에서 순수하게 읽어 테스트 가능하며,
// metrics 소스 결선 시 이 인터페이스만 교체한다.
type ShardMetricsObserver interface {
	ObserveShards(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) []ShardObservation
}

// statusShardObserver 는 cluster.Status.Shards 에서 관측치를 읽는 base observer.
// SizeBytes 는 aggregate_status 가 primary 보고값으로 채운다. CPU / latency 는 여기서
// 0(미관측). CPU 는 cpuAugmentingObserver 가 metrics.k8s.io 로 보강한다.
type statusShardObserver struct{}

func (statusShardObserver) ObserveShards(_ context.Context, cluster *postgresv1alpha1.PostgresCluster) []ShardObservation {
	if cluster == nil {
		return nil
	}
	// PVC 용량은 shard 공통(spec.shards.storage.size). PVC 사용률(%) 트리거용.
	pvcCapacityBytes := cluster.Spec.Shards.Storage.Size.Value()
	obs := make([]ShardObservation, 0, len(cluster.Status.Shards))
	for i := range cluster.Status.Shards {
		s := &cluster.Status.Shards[i]
		if s.Name == "" {
			continue
		}
		obs = append(obs, ShardObservation{
			ShardID:          s.Name,
			SizeBytes:        s.SizeBytes,
			PVCCapacityBytes: pvcCapacityBytes,
			// CPUPercent 는 cpuAugmentingObserver 가 보강, P99LatencyMs 는 미결선 → base 는 0.
		})
	}
	return obs
}

// autoSplitTriggerBreached 는 관측치가 활성 트리거(임계값 > 0)를 *모두* 만족하는지
// 판정한다(스키마 정의: 모든 트리거 AND). 반환:
//   - breached: 활성 트리거가 1개 이상이고 그 전부가 임계 이상.
//   - enabledLat/enabledPVC: 각 트리거 활성 여부(condition 메시지용).
//
// size 는 GB → bytes 환산 후 비교. cpu / latency 는 관측치가 임계 이상이어야 한다 —
// 현재 관측치가 0(소스 미결선)이면 임계(>0)를 넘지 못해 breached=false 가 되어 오탐을
// 막는다.
func autoSplitTriggerBreached(t *postgresv1alpha1.AutoSplitTriggers, obs ShardObservation) (breached, enabledLat, enabledPVC bool) {
	if t == nil {
		return false, false, false
	}
	enabledSize := t.SizeThresholdGB > 0
	enabledCPU := t.CPUPercent > 0
	enabledLat = t.P99LatencyMs > 0
	enabledPVC = t.PVCUtilizationPercent > 0
	if !enabledSize && !enabledCPU && !enabledLat && !enabledPVC {
		return false, false, false
	}
	if enabledSize && obs.SizeBytes < int64(t.SizeThresholdGB)*bytesPerGB {
		return false, enabledLat, enabledPVC
	}
	if enabledCPU && obs.CPUPercent < t.CPUPercent {
		return false, enabledLat, enabledPVC
	}
	if enabledLat && obs.P99LatencyMs < t.P99LatencyMs {
		return false, enabledLat, enabledPVC
	}
	// PVC 사용률(%) = SizeBytes / PVCCapacityBytes * 100. 용량 미상(0) 이면 사용률 미상 →
	// 오탐 방지로 breach 하지 않는다(K-5: "80% 과적재 시 오토스케일" 을 절대 GB 환산 없이 표현).
	if enabledPVC && !pvcUtilizationBreached(obs, t.PVCUtilizationPercent) {
		return false, enabledLat, enabledPVC
	}
	return true, enabledLat, enabledPVC
}

// pvcUtilizationBreached 는 shard 데이터 사용률(SizeBytes/PVCCapacityBytes*100)이 threshold%
// 이상인지 본다. 용량/크기 미관측(0) 이면 false(오탐 방지). 순수 함수 — 단위테스트 용이.
func pvcUtilizationBreached(obs ShardObservation, thresholdPercent int32) bool {
	if obs.PVCCapacityBytes <= 0 || obs.SizeBytes <= 0 {
		return false
	}
	// 정수 오버플로 회피: 100 곱 대신 비율 비교. SizeBytes*100 >= threshold*Capacity.
	return obs.SizeBytes*100 >= int64(thresholdPercent)*obs.PVCCapacityBytes
}

// autoSplitSustained 는 breach 상태가 dur 동안 *지속*되었는지 in-memory 로 추적한다
// (shouldPromoteAfterDebounce 미러). breach 해제 시 키를 지워 window 를 리셋한다.
// dur==0 이면 첫 관측 즉시 true(durationMinutes=0 = immediate).
func (r *PostgresClusterReconciler) autoSplitSustained(key string, breached bool, now time.Time, dur time.Duration) bool {
	r.autoSplitBreachMu.Lock()
	defer r.autoSplitBreachMu.Unlock()
	if !breached {
		delete(r.autoSplitBreach, key)
		return false
	}
	if r.autoSplitBreach == nil {
		r.autoSplitBreach = map[string]time.Time{}
	}
	first, ok := r.autoSplitBreach[key]
	if !ok {
		r.autoSplitBreach[key] = now
		return dur == 0
	}
	return now.Sub(first) >= dur
}

// autoSplitDuration 은 triggers.DurationMinutes → time.Duration.
func autoSplitDuration(t *postgresv1alpha1.AutoSplitTriggers) time.Duration {
	if t == nil || t.DurationMinutes <= 0 {
		return 0
	}
	return time.Duration(t.DurationMinutes) * time.Minute
}

// computeHashSplitTargets 는 hash vindex range entry 를 중점에서 둘로 나눈 두 개의
// ShardSplitTarget 을 만든다. target shardID 는 소스 범위에서 파생한 결정론적·DNS-safe
// 짧은 이름(as<6hex>a / as<6hex>b)이라 같은 소스 범위에 대해 항상 동일하다(멱등).
func computeHashSplitTargets(entry postgresv1alpha1.ShardRangeEntry) ([]postgresv1alpha1.ShardSplitTarget, error) {
	lo0, hi0, lo1, hi1, err := router.SplitHashRange(entry.Lo, entry.Hi)
	if err != nil {
		return nil, err
	}
	short := shortRangeHash(entry.Lo, entry.Hi)
	id0 := "as" + short + "a"
	id1 := "as" + short + "b"
	return []postgresv1alpha1.ShardSplitTarget{
		{ShardID: id0, Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: lo0, Hi: hi0, Shard: id0}}},
		{ShardID: id1, Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: lo1, Hi: hi1, Shard: id1}}},
	}, nil
}

// shortRangeHash 는 [lo,hi] 로부터 6자리 hex 를 만든다(target/job 이름의 안정 suffix).
func shortRangeHash(lo, hi string) string {
	sum := crc32.ChecksumIEEE([]byte(lo + "-" + hi))
	return fmt.Sprintf("%06x", sum&0xffffff)
}

// autoSplitJobName 은 소스 shard·keyspace·범위로부터 결정론적 ShardSplitJob 이름을
// 만든다(멱등 — 이미 존재하면 재생성 skip). DNS-1123 subdomain 안전.
func autoSplitJobName(keyspace, sourceShard, lo, hi string) string {
	base := fmt.Sprintf("autosplit-%s-%s-%s", sanitizeName(keyspace), sanitizeName(sourceShard), shortRangeHash(lo, hi))
	if len(base) > 63 {
		base = base[:63]
	}
	return strings.TrimRight(base, "-")
}

// sanitizeName 은 임의 문자열을 DNS-1123 label 조각으로 정규화한다(소문자·영숫자·하이픈).
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// buildAutoSplitJob 은 자동 생성 ShardSplitJob 객체를 만든다(owner=cluster, 관측
// 트리거 표식 annotation 포함). requireApproval 이면 approval=required 로 표식해
// SSJ 컨트롤러가 승인 annotation 전까지 Pending 을 유지하게 한다.
func buildAutoSplitJob(
	cluster *postgresv1alpha1.PostgresCluster,
	keyspace, sourceShard string,
	sourceRange postgresv1alpha1.ShardRangeEntry,
	targets []postgresv1alpha1.ShardSplitTarget,
	requireApproval bool,
) *postgresv1alpha1.ShardSplitJob {
	approval := "auto"
	if requireApproval {
		approval = autoSplitApprovalRequired
	}
	return &postgresv1alpha1.ShardSplitJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autoSplitJobName(keyspace, sourceShard, sourceRange.Lo, sourceRange.Hi),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"postgres.keiailab.io/cluster":   cluster.Name,
				"postgres.keiailab.io/autosplit": "true",
			},
			Annotations: map[string]string{
				AnnotationAutoSplit:         "true",
				AnnotationAutoSplitApproval: approval,
			},
		},
		Spec: postgresv1alpha1.ShardSplitJobSpec{
			Cluster:   cluster.Name,
			Keyspace:  keyspace,
			Direction: postgresv1alpha1.ShardSplitDirectionSplit,
			Sources:   []string{sourceShard},
			Targets:   targets,
			// CutoverWindow / CDCMaxLag / Online 은 CRD kubebuilder default(60s /
			// 16MB / offline)로 apiserver 가 채운다 — 자동 split 은 보수적으로 offline
			// (유지보수 창) 기본. 운영자가 승인 전 online 으로 편집 가능.
		},
	}
}

// autoSplitHoldForApproval 은 이 job 이 승인 대기 중(autosplit=true + approval=required
// + approved 미설정)인지 판정한다. SSJ 컨트롤러가 Pending 전이 게이트로 사용한다.
func autoSplitHoldForApproval(ssj *postgresv1alpha1.ShardSplitJob) bool {
	if ssj == nil || ssj.Annotations == nil {
		return false
	}
	if ssj.Annotations[AnnotationAutoSplit] != labelTrue {
		return false
	}
	if ssj.Annotations[AnnotationAutoSplitApproval] != autoSplitApprovalRequired {
		return false
	}
	return ssj.Annotations[AnnotationAutoSplitApproved] != labelTrue
}

// shardSplitJobActive 는 job 이 아직 종료(Completed/Failed/Aborted)되지 않았는지 본다.
func shardSplitJobActive(ssj *postgresv1alpha1.ShardSplitJob) bool {
	switch ssj.Status.Phase {
	case postgresv1alpha1.ShardSplitPhaseCompleted,
		postgresv1alpha1.ShardSplitPhaseFailed,
		postgresv1alpha1.ShardSplitPhaseAborted:
		return false
	default:
		return true
	}
}

// reconcileAutoSplit 은 AutoSplit 결정 루프다. eligible 후보 수, condition reason /
// message 를 반환하고, 자격 후보가 있고 동시 진행 job 이 없으면 ShardSplitJob 1건을
// 멱등 생성한다(cluster 당 한 번에 하나의 split). 관측/후보계산만 하는 read-heavy
// 경로라 실패는 best-effort(로그 + reason)로 degrade 한다.
func (r *PostgresClusterReconciler) reconcileAutoSplit(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	now time.Time,
) (eligible int, reason, message string) {
	logger := log.FromContext(ctx)

	as := cluster.Spec.AutoSplit
	if as == nil || !as.Enabled {
		return 0, autoSplitReasonDisabled, "autoSplit disabled"
	}
	if cluster.Spec.ShardingMode != postgresv1alpha1.ShardingModeNative {
		// webhook 이 강제하나 방어적으로 no-op.
		return 0, autoSplitReasonDisabled, "autoSplit requires shardingMode=native"
	}

	if r.Observer == nil {
		r.Observer = newDefaultShardObserver(r.Client)
	}
	obsByShard := map[string]ShardObservation{}
	for _, o := range r.Observer.ObserveShards(ctx, cluster) {
		obsByShard[o.ShardID] = o
	}

	// 동시 진행 중인 split 이 있으면 새 job 을 만들지 않는다(cluster 당 하나).
	var jobs postgresv1alpha1.ShardSplitJobList
	if err := r.List(ctx, &jobs, client.InNamespace(cluster.Namespace)); err != nil {
		logger.V(1).Info("autoSplit: ShardSplitJob list 실패(best-effort)", "error", err)
		return 0, autoSplitReasonNone, "ShardSplitJob 조회 실패"
	}
	splitInProgress := false
	existingJob := map[string]bool{}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if j.Spec.Cluster != cluster.Name {
			continue
		}
		existingJob[j.Name] = true
		if shardSplitJobActive(j) {
			splitInProgress = true
		}
	}

	var ranges postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &ranges, client.InNamespace(cluster.Namespace)); err != nil {
		logger.V(1).Info("autoSplit: ShardRange list 실패(best-effort)", "error", err)
		return 0, autoSplitReasonNone, "ShardRange 조회 실패"
	}

	dur := autoSplitDuration(as.Triggers)
	unsourcedTrigger := false
	unsupportedVindex := false

	type candidate struct {
		keyspace string
		shard    string
		entry    postgresv1alpha1.ShardRangeEntry
		spec     postgresv1alpha1.ShardRangeSpec
	}
	var chosen *candidate

	// 결정론 순회를 위해 keyspace 정렬.
	sort.Slice(ranges.Items, func(i, j int) bool {
		return ranges.Items[i].Spec.Keyspace < ranges.Items[j].Spec.Keyspace
	})

	for i := range ranges.Items {
		sr := &ranges.Items[i]
		if sr.Spec.Cluster != cluster.Name {
			continue
		}
		if sr.Spec.Vindex.Type != postgresv1alpha1.VindexTypeHash {
			// hash 만 중점 분할이 정의된다(range/consistent-hash/lookup 은 후속).
			unsupportedVindex = true
			continue
		}
		// shard 별로 소유 range 개수를 센다 — 정확히 1개인 shard만 자동 split 대상
		// (여러 range 소유 shard 의 분할은 복합 계획이라 후속).
		rangeCount := map[string]int{}
		for j := range sr.Spec.Ranges {
			rangeCount[sr.Spec.Ranges[j].Shard]++
		}
		for j := range sr.Spec.Ranges {
			entry := sr.Spec.Ranges[j]
			if entry.Shard == "" || rangeCount[entry.Shard] != 1 {
				continue
			}
			obs := obsByShard[entry.Shard]
			breached, enLat, _ := autoSplitTriggerBreached(as.Triggers, obs)
			// P99 latency 는 아직 metrics 소스 미결선(CPU 는 cpuAugmentingObserver 로
			// metrics.k8s.io 결선됨). latency 트리거만 unsourced 로 표기한다.
			if enLat {
				unsourcedTrigger = true
			}
			key := cluster.Namespace + "/" + cluster.Name + "/" + sr.Spec.Keyspace + "/" + entry.Shard
			if !r.autoSplitSustained(key, breached, now, dur) {
				continue
			}
			eligible++
			if chosen == nil {
				chosen = &candidate{keyspace: sr.Spec.Keyspace, shard: entry.Shard, entry: entry, spec: sr.Spec}
			}
		}
	}

	if eligible == 0 {
		switch {
		case unsupportedVindex:
			return 0, autoSplitReasonUnsupported, "자동 split 은 hash vindex 만 지원(현 keyspace 미지원)"
		case unsourcedTrigger:
			return 0, autoSplitReasonNoMetrics, "p99 latency 트리거가 설정됐으나 metrics 소스 미결선(size·cpu 만 관측)"
		default:
			return 0, autoSplitReasonNone, "임계 초과 지속 shard 없음"
		}
	}

	if splitInProgress {
		return eligible, autoSplitReasonInProgress, fmt.Sprintf("%d개 split 후보 — 진행 중 split 완료 후 생성 대기", eligible)
	}

	// 후보 1건에 대해 멱등 ShardSplitJob 생성.
	targets, err := computeHashSplitTargets(chosen.entry)
	if err != nil {
		logger.Info("autoSplit: split 후보 계산 실패", "shard", chosen.shard, "error", err)
		return eligible, autoSplitReasonNone, fmt.Sprintf("후보 %q 범위 분할 불가: %v", chosen.shard, err)
	}
	job := buildAutoSplitJob(cluster, chosen.keyspace, chosen.shard, chosen.entry, targets, as.RequireApproval)
	if existingJob[job.Name] {
		return eligible, autoSplitReasonEligible, fmt.Sprintf("%d개 split 후보 — job %q 이미 존재", eligible, job.Name)
	}
	if err := controllerutil.SetControllerReference(cluster, job, r.Scheme); err != nil {
		logger.Info("autoSplit: owner ref 설정 실패", "error", err)
		return eligible, autoSplitReasonEligible, "job owner ref 설정 실패"
	}
	if err := r.Create(ctx, job); err != nil {
		logger.Info("autoSplit: ShardSplitJob 생성 실패", "name", job.Name, "error", err)
		return eligible, autoSplitReasonEligible, fmt.Sprintf("job 생성 실패: %v", err)
	}
	logger.Info("autoSplit: ShardSplitJob 자동 생성",
		"name", job.Name, "keyspace", chosen.keyspace, "source", chosen.shard,
		"requireApproval", as.RequireApproval)
	return eligible, autoSplitReasonEligible, fmt.Sprintf("%d개 split 후보 — job %q 생성", eligible, job.Name)
}
