/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 승격 상태를 파드 템플릿 밖에 둔다(ADR-0030, 설계 2026-09-25 §3.2).
//
//	CR 애노테이션 shard-runtime ── 정본: epoch(단조) · initialized(끈적) · promotionLSN
//	      │ promoteAndPublish(승격) · adoptPrimary(발행 전 크래시 채택) · ensureRuntime(생성만)
//	      │ 승격·채택의 CM 쓰기는 밀려난 primary 의 MEMBER_HOLD 를 같이 싣는다(I9)
//	      ▼
//	ConfigMap <cluster>-<shard>-runtime ── 사본: PRIMARY_ENDPOINT · PROMOTION_EPOCH · CLUSTER_INITIALIZED
//	      │                                  재구축 엔진(ADR-0031): MEMBER_HOLD · RESEED_REQUESTS
//	      │ configMapKeyRef(Optional=false) — 컨테이너를 만들 때마다 읽힌다
//	      ▼
//	bootstrap(init) · instance(main) env
//
// 승격이 템플릿을 바꾸지 않으므로 StatefulSet 이 primary 를 재시작하지 않는다(I1).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

const (
	// 런타임 CM 키 = 컨테이너 env 이름.
	runtimeKeyPrimaryEndpoint    = "PRIMARY_ENDPOINT"
	runtimeKeyPromotionEpoch     = "PROMOTION_EPOCH"
	runtimeKeyClusterInitialized = "CLUSTER_INITIALIZED"
	runtimeKeyMemberHold         = "MEMBER_HOLD"
	runtimeKeyReseedRequests     = "RESEED_REQUESTS"

	// runtimeListSeparator·reseedTokenSeparator 는 목록 키의 형식이다
	// (MEMBER_HOLD = pod,pod · RESEED_REQUESTS = pod=token,pod=token).
	runtimeListSeparator = ","
	reseedTokenSeparator = "="

	runtimeFlagOn  = "1"
	runtimeFlagOff = "0"

	// shardRuntimeAnnotation 은 승격 정본(JSON v1)을 담는 CR 애노테이션이다.
	shardRuntimeAnnotation = "postgres.keiailab.io/shard-runtime"
	runtimeStateVersion    = 1

	// timelineHistoryQuery 는 새 타임라인 history 파일을 읽는다 — 마지막 줄이 이번 분기점.
	timelineHistoryQuery = "SELECT pg_read_file('pg_wal/' || substr(pg_walfile_name(pg_current_wal_lsn()), 1, 8) || '.history')"

	// pgBoolTrue·pgBoolFalse 는 psql -At 의 boolean 표기다.
	pgBoolTrue  = "t"
	pgBoolFalse = "f"

	// recoveryQuery 는 실제 primary 판정의 질의다 — f 면 primary.
	recoveryQuery = "SELECT pg_is_in_recovery()"

	// recoveryProbeTimeout 은 실제 primary 확인 exec 1회의 상한이다 — 무응답 exec 가
	// reconcile 을 붙잡지 않는다.
	recoveryProbeTimeout = statusPollInterval

	// promotionExecutedReason 은 승격 exec 가 실제로 승격했을 때의 이벤트 사유다.
	promotionExecutedReason = "PromotionExecuted"
)

// primaryCheck 는 멤버가 실제 primary 인지의 판정이다 — pg_is_in_recovery=f ∧ 하트비트 신선 ∧
// unfenced 일 때만 확인이다(§3.2). 값은 Warning·condition 메시지에 그대로 쓰는 사유다.
type primaryCheck string

const (
	primaryConfirmed  primaryCheck = "confirmed"
	primaryAbsent     primaryCheck = "pod absent"
	primaryFenced     primaryCheck = "pvc fenced"
	primaryStale      primaryCheck = "heartbeat stale"
	primaryNotRunning primaryCheck = "pod not running"
	primaryInRecovery primaryCheck = "in recovery"
	primaryUnknown    primaryCheck = "recovery state unknown"
)

// bootstrapRuntimeKeys 는 init 컨테이너가 받는 런타임 키다(main 은 PRIMARY_ENDPOINT 만).
var bootstrapRuntimeKeys = []string{
	runtimeKeyPrimaryEndpoint, runtimeKeyPromotionEpoch, runtimeKeyClusterInitialized,
	runtimeKeyMemberHold, runtimeKeyReseedRequests,
}

// promotionRuntimeKeys 는 승격·채택이 늘 쓰는 키다 — MEMBER_HOLD 는 밀려난 primary 를 더할 때만 같이 쓰고
// (writePromotion), 재시드 키는 재구축 엔진만 쓴다.
var promotionRuntimeKeys = []string{runtimeKeyPrimaryEndpoint, runtimeKeyPromotionEpoch, runtimeKeyClusterInitialized}

// memberRuntimeKeys 는 재구축 엔진의 목록 키다 — 비어도 늘 쓴다(템플릿이 Optional=false 로 참조한다).
var memberRuntimeKeys = []string{runtimeKeyMemberHold, runtimeKeyReseedRequests}

// lsnPattern 은 PostgreSQL LSN 표기(예: 0/3000160)다.
var lsnPattern = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)

// shardRuntime 은 런타임 CM 한 벌의 값이다.
type shardRuntime struct {
	PrimaryEndpoint    string
	PromotionEpoch     int64
	ClusterInitialized bool
	Hold               []string          // MEMBER_HOLD — 부트스트랩이 멈추는 멤버(정렬)
	Reseed             map[string]string // RESEED_REQUESTS — 멤버 → 재시드 토큰
}

// runtimeState 는 CR 정본 — {version:1, shards:{<shard>:{epoch, initialized, promotionLSN}}}.
type runtimeState struct {
	Version int                   `json:"version"`
	Shards  map[string]shardCanon `json:"shards"`
}

// shardCanon 은 샤드 하나의 정본이다. epoch 을 줄이는 코드·initialized 를 끄는 코드는 없다(I6·I28).
type shardCanon struct {
	Epoch        int64  `json:"epoch"`
	Initialized  bool   `json:"initialized"`
	PromotionLSN string `json:"promotionLSN"`
}

// promotionKind 는 승격 경로다.
type promotionKind string

const (
	promotionFailover   promotionKind = "failover"
	promotionSwitchover promotionKind = "switchover"
)

// shardRef 는 런타임 CM 한 벌이 걸리는 샤드다(ordinal·named 공통).
type shardRef struct {
	name     string                 // 논리 샤드 이름 — "shard-0" · "t1"
	sts      string                 // StatefulSet 이름
	service  string                 // headless Service 이름
	fallback string                 // 관측할 primary 가 없는 새 샤드의 첫 PRIMARY_ENDPOINT
	member   func(*corev1.Pod) bool // 샤드 멤버 판정
}

func ordinalShardRef(cluster *postgresv1alpha1.PostgresCluster, ord int32) shardRef {
	service := ShardServiceName(cluster.Name, ord)
	return shardRef{
		name:     ShardIDForOrdinal(ord),
		sts:      ShardStatefulSetName(cluster.Name, ord),
		service:  service,
		fallback: primaryEndpointForShard(cluster, ord, service),
		member:   func(pod *corev1.Pod) bool { return podMatchesShardIdentity(pod, ord) },
	}
}

func namedShardRef(cluster *postgresv1alpha1.PostgresCluster, shardID string) shardRef {
	service := TargetShardServiceName(cluster.Name, shardID)
	return shardRef{
		name:     shardID,
		sts:      TargetShardStatefulSetName(cluster.Name, shardID),
		service:  service,
		fallback: primaryEndpointForNamedShard(cluster, shardID, service),
		member:   func(pod *corev1.Pod) bool { return podMatchesNamedShardIdentity(pod, shardID) },
	}
}

// shardRefFor 는 관측된 샤드 상태의 런타임 CM 샤드다.
func shardRefFor(cluster *postgresv1alpha1.PostgresCluster, ss *postgresv1alpha1.ShardStatus) shardRef {
	if isOrdinalShardID(cluster, ss.Name) {
		return ordinalShardRef(cluster, ss.Ordinal)
	}
	return namedShardRef(cluster, ss.Name)
}

// loadRuntimeState 는 CR 정본을 읽는다 — 없으면 빈 v1, 깨졌으면 오류(덮어써서 epoch 을 잃지 않는다).
func loadRuntimeState(cluster *postgresv1alpha1.PostgresCluster) (runtimeState, error) {
	state := runtimeState{Version: runtimeStateVersion, Shards: map[string]shardCanon{}}
	raw := cluster.Annotations[shardRuntimeAnnotation]
	if raw == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return runtimeState{}, fmt.Errorf("decode %s: %w", shardRuntimeAnnotation, err)
	}
	if state.Version != runtimeStateVersion {
		return runtimeState{}, fmt.Errorf("%s version %d unsupported", shardRuntimeAnnotation, state.Version)
	}
	if state.Shards == nil {
		state.Shards = map[string]shardCanon{}
	}
	return state, nil
}

// saveRuntimeState 는 정본을 낙관적 잠금 병합 패치로 쓴다 — 캐시가 낡았으면 충돌로 멈춘다.
// 이 reconcile 이 계산한 status 는 두고 메타데이터만 돌려받는다.
func (r *PostgresClusterReconciler) saveRuntimeState(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	state runtimeState,
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
	target.Annotations[shardRuntimeAnnotation] = string(raw)
	if err := r.Patch(ctx, target, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("save %s: %w", shardRuntimeAnnotation, err)
	}
	cluster.Annotations = target.Annotations
	cluster.ResourceVersion = target.ResourceVersion
	return nil
}

// buildRuntimeConfigMap 은 런타임 CM 이다 — 소유자 참조는 호출자가 건다(기존 Owns(ConfigMap)).
func buildRuntimeConfigMap(cluster *postgresv1alpha1.PostgresCluster, shard string, rt shardRuntime) *corev1.ConfigMap {
	labels := SelectorLabels(cluster.Name, "shard", -1)
	labels[ShardIDLabelKey] = shard
	initialized := runtimeFlagOff
	if rt.ClusterInitialized {
		initialized = runtimeFlagOn
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ShardRuntimeConfigMapName(cluster.Name, shard),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			runtimeKeyPrimaryEndpoint:    rt.PrimaryEndpoint,
			runtimeKeyPromotionEpoch:     strconv.FormatInt(rt.PromotionEpoch, 10),
			runtimeKeyClusterInitialized: initialized,
			runtimeKeyMemberHold:         formatHold(rt.Hold),
			runtimeKeyReseedRequests:     formatReseed(rt.Reseed),
		},
	}
}

// runtimeFromData 는 CM 데이터의 값이다 — 없는 키는 빈 값이다.
func runtimeFromData(data map[string]string) shardRuntime {
	return shardRuntime{
		PrimaryEndpoint:    data[runtimeKeyPrimaryEndpoint],
		PromotionEpoch:     parseEpoch(data[runtimeKeyPromotionEpoch]),
		ClusterInitialized: data[runtimeKeyClusterInitialized] == runtimeFlagOn,
		Hold:               parseHold(data[runtimeKeyMemberHold]),
		Reseed:             parseReseed(data[runtimeKeyReseedRequests]),
	}
}

// parseHold 는 MEMBER_HOLD(pod,pod)를 정렬된 멤버 목록으로 읽는다.
func parseHold(raw string) []string {
	var pods []string
	for pod := range strings.SplitSeq(raw, runtimeListSeparator) {
		if pod = strings.TrimSpace(pod); pod != "" && !slices.Contains(pods, pod) {
			pods = append(pods, pod)
		}
	}
	slices.Sort(pods)
	return pods
}

// formatHold 는 멤버 목록을 MEMBER_HOLD 로 쓴다(정렬 — 같은 목록은 같은 글자).
func formatHold(pods []string) string {
	return strings.Join(parseHold(strings.Join(pods, runtimeListSeparator)), runtimeListSeparator)
}

// parseReseed 는 RESEED_REQUESTS(pod=token,…)를 읽는다 — 형식이 틀린 항목은 버린다.
func parseReseed(raw string) map[string]string {
	tokens := map[string]string{}
	for pair := range strings.SplitSeq(raw, runtimeListSeparator) {
		pod, token, ok := strings.Cut(strings.TrimSpace(pair), reseedTokenSeparator)
		if !ok || pod == "" || token == "" {
			continue
		}
		tokens[pod] = token
	}
	return tokens
}

// formatReseed 는 재시드 토큰을 RESEED_REQUESTS 로 쓴다(멤버 순).
func formatReseed(tokens map[string]string) string {
	pairs := make([]string, 0, len(tokens))
	for _, pod := range slices.Sorted(maps.Keys(tokens)) {
		pairs = append(pairs, pod+reseedTokenSeparator+tokens[pod])
	}
	return strings.Join(pairs, runtimeListSeparator)
}

// parseLSN 은 PostgreSQL LSN 표기(X/Y, 16진)를 절대 바이트 위치로 읽는다.
func parseLSN(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if !lsnPattern.MatchString(s) {
		return 0, fmt.Errorf("LSN %q is malformed", s)
	}
	hi, lo, _ := strings.Cut(s, "/")
	high, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, err
	}
	low, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, err
	}
	return high<<32 | low, nil
}

// runtimeEnvVars 는 런타임 CM 키를 같은 이름 env 로 싣는다 — Optional=false 라 CM·키가
// 없으면 컨테이너가 뜨지 않는다. 값은 컨테이너를 만들 때마다 kubelet 이 읽는다.
func runtimeEnvVars(cmName string, keys ...string) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(keys))
	for _, key := range keys {
		env = append(env, corev1.EnvVar{
			Name: key,
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				Key:                  key,
				Optional:             ptrBool(false), //nolint:modernize // typed-value pointer required
			}},
		})
	}
	return env
}

// readRuntime 은 런타임 CM 을 읽는다(found=false 면 없음).
func (r *PostgresClusterReconciler) readRuntime(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
) (shardRuntime, bool, error) {
	cm, found, err := r.runtimeConfigMap(ctx, cluster, shard)
	if err != nil || !found {
		return shardRuntime{}, found, err
	}
	return runtimeFromData(cm.Data), true, nil
}

// runtimeConfigMap 은 런타임 CM 객체다(found=false 면 없음) — 잠금 쓰기가 resourceVersion 을 쓴다.
func (r *PostgresClusterReconciler) runtimeConfigMap(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
) (*corev1.ConfigMap, bool, error) {
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: ShardRuntimeConfigMapName(cluster.Name, shard)}
	if err := r.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get runtime ConfigMap %q: %w", key.Name, err)
	}
	return &cm, true, nil
}

// runtimeForShard 는 STS 가 참조할 런타임 CM 이름과 upsert 가부다.
// 복제 클러스터는 로컬 승격이 없어 외부 원천을 템플릿에 인라인한다(CM 없음).
func (r *PostgresClusterReconciler) runtimeForShard(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
) (string, bool, error) {
	if standaloneReplicaEnabled(cluster) {
		return "", true, nil
	}
	ready, err := r.ensureRuntime(ctx, cluster, ref)
	return ShardRuntimeConfigMapName(cluster.Name, ref.name), ready, err
}

// ensureRuntime 은 런타임 CM 을 만든다 — 생성만 한다. primary·epoch 를 바꾸는 길은
// promoteAndPublish 와 채택뿐이다(I28). 참이면 STS upsert 를 해도 된다. CM 이 없을 때:
//
//	건강한 Role=Primary 정확히 하나 → 그 파드로(정본이 없으면 첫 파생)
//	새 샤드(STS·멤버 없음)          → 첫 값 폴백
//	그 밖(0 또는 둘 이상)           → 만들지 않는다 — 옛 템플릿 유지, RuntimeConfigPending
//
// CM 이 있으면 initialized 만 올린다(끈적, I6).
func (r *PostgresClusterReconciler) ensureRuntime(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
) (bool, error) {
	state, err := loadRuntimeState(cluster)
	if err != nil {
		return false, err
	}
	cm, found, err := r.runtimeConfigMap(ctx, cluster, ref.name)
	if err != nil {
		return false, err
	}
	if found {
		if err := r.fillRuntimeKeys(ctx, cm); err != nil {
			return false, err
		}
		return true, r.raiseInitialized(ctx, cluster, ref, state, runtimeFromData(cm.Data))
	}

	pods, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		return false, err
	}
	primaries, err := r.healthyPrimaries(ctx, cluster, pods)
	if err != nil {
		return false, err
	}
	endpoint := ref.fallback
	switch {
	case len(primaries) == 1:
		endpoint = memberEndpoint(&primaries[0], ref.service)
	case len(pods) == 0:
		fresh, err := r.stsAbsent(ctx, cluster, ref.sts)
		if err != nil || !fresh {
			return false, err
		}
	default:
		return false, nil
	}

	// 정본이 없을 때만(L1 첫 reconcile) 파생한다 — epoch 은 멤버 마커 최대.
	canon, ok := state.Shards[ref.name]
	if !ok {
		canon = shardCanon{
			Epoch:       maxEpoch(r.markerEpochs(ctx, pods)),
			Initialized: len(primaries) == 1 || statusRecordsPrimary(cluster, ref.name),
		}
		state.Shards[ref.name] = canon
		if err := r.saveRuntimeState(ctx, cluster, state); err != nil {
			return false, err
		}
	}
	cm = buildRuntimeConfigMap(cluster, ref.name, shardRuntime{
		PrimaryEndpoint:    endpoint,
		PromotionEpoch:     canon.Epoch,
		ClusterInitialized: canon.Initialized,
	})
	if err := controllerutil.SetControllerReference(cluster, cm, r.Scheme); err != nil {
		return false, fmt.Errorf("set controller reference: %w", err)
	}
	if err := r.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("create runtime ConfigMap %q: %w", cm.Name, err)
	}
	return true, nil
}

// raiseInitialized 는 CM 이 있을 때 initialized 를 올린다 — 건강한 primary 가 보이면 참(끈적).
// 정본을 잃었으면 CM 사본에서 되살린다(epoch 을 줄이지 않는다).
func (r *PostgresClusterReconciler) raiseInitialized(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
	state runtimeState,
	rt shardRuntime,
) error {
	canon, ok := state.Shards[ref.name]
	if !ok {
		canon = shardCanon{Epoch: rt.PromotionEpoch, Initialized: rt.ClusterInitialized}
	}
	if !canon.Initialized {
		pods, err := r.shardMembers(ctx, cluster, ref.member)
		if err != nil {
			return err
		}
		primaries, err := r.healthyPrimaries(ctx, cluster, pods)
		if err != nil {
			return err
		}
		canon.Initialized = len(primaries) > 0
	}
	if !ok || canon.Initialized != state.Shards[ref.name].Initialized {
		state.Shards[ref.name] = canon
		if err := r.saveRuntimeState(ctx, cluster, state); err != nil {
			return err
		}
	}
	if !canon.Initialized || rt.ClusterInitialized {
		return nil
	}
	return r.patchRuntime(ctx, cluster, ref.name, map[string]string{runtimeKeyClusterInitialized: runtimeFlagOn})
}

// patchRuntime 은 런타임 CM 의 키만 병합 패치한다(잠금 없음 — 캐시가 낡아도 이 값이 남는다).
func (r *PostgresClusterReconciler) patchRuntime(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	data map[string]string,
) error {
	raw, err := json.Marshal(map[string]map[string]string{"data": data})
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      ShardRuntimeConfigMapName(cluster.Name, shard),
		Namespace: cluster.Namespace,
	}}
	return r.Patch(ctx, cm, client.RawPatch(types.MergePatchType, raw))
}

// patchRuntimeLocked 는 읽은 CM 위에 키를 병합 패치한다 — 낙관적 잠금이라 낡은 캐시로 읽은
// 목록 키가 새 값을 덮지 못한다(충돌이면 다음 reconcile 이 다시 읽는다).
func (r *PostgresClusterReconciler) patchRuntimeLocked(
	ctx context.Context,
	cm *corev1.ConfigMap,
	data map[string]string,
) error {
	target := cm.DeepCopy()
	before := cm.DeepCopy()
	if target.Data == nil {
		target.Data = map[string]string{}
	}
	maps.Copy(target.Data, data)
	if err := r.Patch(ctx, target, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch runtime ConfigMap %q: %w", cm.Name, err)
	}
	return nil
}

// fillRuntimeKeys 는 옛 CM 에 없는 재구축 키를 빈 값으로 더한다 — 템플릿이 Optional=false 로
// 참조하므로 키가 없으면 컨테이너가 뜨지 않는다. 있는 키는 건드리지 않는다.
func (r *PostgresClusterReconciler) fillRuntimeKeys(ctx context.Context, cm *corev1.ConfigMap) error {
	missing := map[string]string{}
	for _, key := range memberRuntimeKeys {
		if _, ok := cm.Data[key]; !ok {
			missing[key] = ""
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return r.patchRuntimeLocked(ctx, cm, missing)
}

// publishRuntime 은 승격 결과를 CM 에 한 번 쓴다 — CM 이 없으면(옛 템플릿 창) 그 값으로 만든다.
// 있으면 승격 키만 쓴다 — hold·재시드 키는 재구축 엔진의 것이라 덮지 않는다. rt.Hold 는 같은 쓰기로
// MEMBER_HOLD 에 더할 멤버다(I9 — 새 primary 발행과 옛 primary hold 사이에 창이 없다).
func (r *PostgresClusterReconciler) publishRuntime(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	rt shardRuntime,
) error {
	cm := buildRuntimeConfigMap(cluster, shard, rt)
	promotion := make(map[string]string, len(promotionRuntimeKeys))
	for _, key := range promotionRuntimeKeys {
		promotion[key] = cm.Data[key]
	}
	err := r.writePromotion(ctx, cluster, shard, promotion, rt.Hold)
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(cluster, cm, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference: %w", err)
	}
	return r.Create(ctx, cm)
}

// writePromotion 은 있는 CM 에 승격 키를 쓴다(없으면 NotFound). held 가 없으면 잠금 없이 쓴다 — 캐시가
// 낡아도 이 값이 남는다. 있으면 같은 쓰기로 MEMBER_HOLD 에 더한다 — 목록 키라 낙관적 잠금이고, 낡은
// 캐시로 충돌하면 다시 읽는다.
func (r *PostgresClusterReconciler) writePromotion(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	promotion map[string]string,
	held []string,
) error {
	if len(held) == 0 {
		return r.patchRuntime(ctx, cluster, shard, promotion)
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cm, found, err := r.runtimeConfigMap(ctx, cluster, shard)
		if err != nil {
			return err
		}
		if !found {
			return apierrors.NewNotFound(corev1.Resource("configmaps"), ShardRuntimeConfigMapName(cluster.Name, shard))
		}
		data := maps.Clone(promotion)
		data[runtimeKeyMemberHold] = formatHold(append(runtimeFromData(cm.Data).Hold, held...))
		return r.patchRuntimeLocked(ctx, cm, data)
	})
}

// promoteAndPublish 는 CM primary·epoch 를 바꾸는 승격 경로다(§3.2-1):
//
//	CR epoch E+1 선기록 → 승격 exec(epoch) → PROMOTE_RESULT=promoted 면 지표·이벤트
//	  → CM 한 번(endpoint · epoch · failover 면 옛 primary hold) → CR promotionLSN → Demoted 요청
//
// 발행 전 크래시는 CR epoch 이 앞서 있어 채택(OP2b)이 잇는다. Demoted 항목 쓰기가 실패해도 옛 primary 는
// hold 에 있고 다음 reconcile 이 복원한다(restoreHeldRebuilds).
func (r *PostgresClusterReconciler) promoteAndPublish(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardName string,
	target failover.PromotionTarget,
	oldPrimary string,
	kind promotionKind,
) error {
	if target.Endpoint == "" {
		return fmt.Errorf("promotion target %q has no endpoint to publish", target.Pod)
	}
	state, err := loadRuntimeState(cluster)
	if err != nil {
		return err
	}
	rt, _, err := r.readRuntime(ctx, cluster, shardName)
	if err != nil {
		return err
	}
	// 옛 분기점은 새 epoch 에 맞지 않는다 — 새로 읽기 전까지 모름으로 둔다.
	canon := state.Shards[shardName]
	canon.Epoch = max(canon.Epoch, rt.PromotionEpoch) + 1
	canon.Initialized = true
	canon.PromotionLSN = ""
	state.Shards[shardName] = canon
	if err := r.saveRuntimeState(ctx, cluster, state); err != nil {
		return fmt.Errorf("record promotion epoch %d: %w", canon.Epoch, err)
	}

	promoter := &clusterPodPromoter{
		Namespace:   cluster.Namespace,
		Client:      r.Client,
		PodExecutor: r.PromotionPodExecutor,
		Now:         time.Now,
		Epoch:       canon.Epoch,
	}
	promoteErr := promoter.Execute(ctx, failover.PromotionPlan{Target: target})
	if !promoter.promoted {
		return promoteErr
	}
	// 타임라인이 바뀌었다 — 발행 성패와 무관하게 센다(O1 PostgresFailoverStorm).
	MetricPromotions.WithLabelValues(cluster.Namespace, cluster.Name, shardName, string(kind)).Inc()
	commonsevents.Emitf(r.Recorder, cluster, promotionExecutedReason,
		"shard=%s pod=%s epoch=%d kind=%s", shardName, target.Pod, canon.Epoch, kind)
	published := shardRuntime{PrimaryEndpoint: target.Endpoint, PromotionEpoch: canon.Epoch, ClusterInitialized: true}
	demoted := demotedMember(kind, oldPrimary, target.Pod)
	if demoted != "" {
		published.Hold = []string{demoted}
	}
	if err := r.publishRuntime(ctx, cluster, shardName, published); err != nil {
		return errors.Join(promoteErr, fmt.Errorf("publish primary %q epoch %d: %w", target.Pod, canon.Epoch, err))
	}
	logger := log.FromContext(ctx).WithValues("shard", shardName, "pod", target.Pod, "epoch", canon.Epoch)
	logger.Info("primary published", "previous", oldPrimary, "kind", kind)

	r.recordPromotionLSN(ctx, cluster, shardName, target.Pod, state)
	if demoted == "" {
		return promoteErr
	}
	if err := r.requestRebuild(ctx, cluster, shardName, demoted, rebuildDemoted, time.Now()); err != nil {
		logger.Info("demoted member left held without an entry; next reconcile restores it", "member", demoted, "error", err)
	}
	return promoteErr
}

// demotedMember 는 발행과 같은 쓰기로 hold 할 옛 primary 다 — failover 로 밀려난 다른 멤버만(§3.2). 없으면
// 빈 문자열이다.
func demotedMember(kind promotionKind, oldPrimary, target string) string {
	if kind != promotionFailover || oldPrimary == target {
		return ""
	}
	return oldPrimary
}

// recordPromotionLSN 은 새 타임라인 분기점을 정본에 적는다 — 모르면 비워 둔다(재구축 판정이 모름을 보존
// 쪽으로 다룬다).
func (r *PostgresClusterReconciler) recordPromotionLSN(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard, pod string,
	state runtimeState,
) {
	canon := state.Shards[shard]
	logger := log.FromContext(ctx).WithValues("shard", shard, "pod", pod, "epoch", canon.Epoch)
	lsn, err := r.promotionLSN(ctx, cluster.Namespace, pod)
	if err != nil {
		logger.Info("promotion LSN unknown", "error", err)
		return
	}
	canon.PromotionLSN = lsn
	state.Shards[shard] = canon
	if err := r.saveRuntimeState(ctx, cluster, state); err != nil {
		logger.Info("promotion LSN not recorded", "error", err)
	}
}

// adoptPrimary 는 발행 전 크래시를 잇는다(§3.2-2): CM epoch 이 CR epoch 보다 작고 마커 epoch 이
// CR epoch 인 실제 primary 가 정확히 하나면 그 파드로 CM 을 한 번 쓴다(endpoint·epoch · 옛 CM primary
// hold). 마커가 모자라면(승격 exec 실패) 채택하지 않는다 — 다음 승격이 epoch 을 더 올린다(단조).
func (r *PostgresClusterReconciler) adoptPrimary(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	ref shardRef,
) error {
	state, err := loadRuntimeState(cluster)
	if err != nil {
		return err
	}
	canon, ok := state.Shards[ref.name]
	if !ok {
		return nil
	}
	rt, found, err := r.readRuntime(ctx, cluster, ref.name)
	if err != nil || !found || rt.PromotionEpoch >= canon.Epoch {
		return err
	}

	// 그 epoch 으로 승격된 실제 primary — 마커는 승격 exec 만 쓴다.
	pods, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		return err
	}
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		return err
	}
	now := time.Now()
	var actual []corev1.Pod
	for i := range pods {
		// 관측으로 먼저 거른다 — epoch 이 앞선 채 남아도 standby 에는 exec 하지 않는다.
		if !claimsPrimary(&pods[i], fenced, now) || r.checkPrimary(ctx, &pods[i], fenced, now) != primaryConfirmed {
			continue
		}
		actual = append(actual, pods[i])
	}
	markers := r.markerEpochs(ctx, actual)
	var adopted []*corev1.Pod
	for i := range actual {
		if epoch, read := markers[actual[i].Name]; !read || epoch != canon.Epoch {
			continue
		}
		adopted = append(adopted, &actual[i])
	}
	logger := log.FromContext(ctx).WithValues("shard", ref.name, "epoch", canon.Epoch)
	if len(adopted) != 1 {
		if len(adopted) > 1 {
			logger.Info("primary adoption withheld: several primaries carry the epoch", "count", len(adopted))
		}
		return nil
	}

	published := shardRuntime{
		PrimaryEndpoint:    memberEndpoint(adopted[0], ref.service),
		PromotionEpoch:     canon.Epoch,
		ClusterInitialized: canon.Initialized || rt.ClusterInitialized,
		Hold:               adoptionHold(pods, endpointPod(rt.PrimaryEndpoint), adopted[0].Name, fenced, now),
	}
	if err := r.publishRuntime(ctx, cluster, ref.name, published); err != nil {
		return fmt.Errorf("adopt primary %q epoch %d: %w", adopted[0].Name, canon.Epoch, err)
	}
	logger.Info("primary adopted", "pod", adopted[0].Name, "previous", rt.PrimaryEndpoint)
	return nil
}

// adoptionHold 는 채택이 같은 쓰기로 hold 할 옛 CM primary 다 — 채택한 파드 자신이거나 아직 unfenced
// primary 를 보고하면(primary 둘 — hold 0, Warning 은 reportAuthorities) 없다.
func adoptionHold(pods []corev1.Pod, previous, adopted string, fenced map[string]bool, now time.Time) []string {
	if previous == "" || previous == adopted {
		return nil
	}
	if pod := podNamed(pods, previous); pod != nil && claimsPrimary(pod, fenced, now) {
		return nil
	}
	return []string{previous}
}

// restoreHeldRebuilds 는 hold 에 있고 재구축 항목이 없는 멤버를 Demoted 로 되살린다(§3.2) — 발행·채택의
// hold 뒤 항목 쓰기가 실패해도 멤버는 멈춘 채 남고 엔진이 잇는다. runtimePropagationWait 안에 끝난 항목의
// hold 는 건너뛴다 — 끝내는 쓰기는 CM(hold 해제) 먼저라 캐시가 CR 만 새로 볼 수 있다.
func (r *PostgresClusterReconciler) restoreHeldRebuilds(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	now time.Time,
) {
	logger := log.FromContext(ctx)
	state, err := loadRebuildState(cluster)
	if err != nil {
		logger.Info("held members not restored: rebuild state unreadable", "error", err)
		return
	}
	for i := range shards {
		rt, found, err := r.readRuntime(ctx, cluster, shards[i].Name)
		if err != nil || !found {
			continue
		}
		for _, pod := range rt.Hold {
			if _, active := state.Active[pod]; active || endedWithin(state, pod, now, runtimePropagationWait) {
				continue
			}
			if err := r.requestRebuild(ctx, cluster, shards[i].Name, pod, rebuildDemoted, now); err != nil {
				logger.Info("held member not restored", "shard", shards[i].Name, "pod", pod, "error", err)
			}
		}
	}
}

// endedWithin 은 pod 의 재구축이 window 안에 끝났는지다(원장).
func endedWithin(state rebuildState, pod string, now time.Time, window time.Duration) bool {
	for _, x := range state.Ledger {
		if x.Pod == pod && !x.Ended.IsZero() && now.Sub(x.Ended) < window {
			return true
		}
	}
	return false
}

// checkPrimary 는 멤버가 실제 primary 인지 본다 — 싼 판정(fence·하트비트·파드 상태) 뒤 exec 한 번.
func (r *PostgresClusterReconciler) checkPrimary(
	ctx context.Context,
	pod *corev1.Pod,
	fenced map[string]bool,
	now time.Time,
) primaryCheck {
	if pod == nil {
		return primaryAbsent
	}
	if fenced[fencing.PVCName(pod.Name)] {
		return primaryFenced
	}
	if st, ok := parsePodStatus(pod); !ok || st.IsStale(now, statusStaleThresh) {
		return primaryStale
	}
	// 죽은 노드의 파드에 exec 하면 시한까지 붙잡힌다.
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return primaryNotRunning
	}
	inRecovery, err := r.inRecovery(ctx, pod)
	if err != nil {
		log.FromContext(ctx).V(1).Info("recovery state unreadable", "pod", pod.Name, "error", err)
		return primaryUnknown
	}
	if inRecovery {
		return primaryInRecovery
	}
	return primaryConfirmed
}

// claimsPrimary 는 파드가 신선한 하트비트로 Role=Primary 를 보고하고 unfenced 인지다(관측만, exec 없음).
func claimsPrimary(pod *corev1.Pod, fenced map[string]bool, now time.Time) bool {
	if fenced[fencing.PVCName(pod.Name)] {
		return false
	}
	st, ok := parsePodStatus(pod)
	return ok && st.Role == statusapi.RolePrimary && !st.IsStale(now, statusStaleThresh)
}

// inRecovery 는 pg_is_in_recovery() 를 로컬 소켓 exec 로 묻는다.
func (r *PostgresClusterReconciler) inRecovery(ctx context.Context, pod *corev1.Pod) (bool, error) {
	if r.PromotionPodExecutor == nil {
		return false, errors.New("pod executor is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, recoveryProbeTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(probeCtx, BackupSidecarTarget{
		Namespace: pod.Namespace,
		Pod:       pod.Name,
		Container: pgContainerName,
	}, []string{"psql", "-U", "postgres", "-d", "postgres", "-Atqc", recoveryQuery})
	if err != nil {
		return false, err
	}
	switch answer := strings.TrimSpace(string(out)); answer {
	case pgBoolTrue:
		return true, nil
	case pgBoolFalse:
		return false, nil
	default:
		return false, fmt.Errorf("pg_is_in_recovery answered %q", answer)
	}
}

// endpointPod 는 PRIMARY_ENDPOINT(<pod>.<svc>.<ns>.svc.cluster.local:5432)의 파드 이름이다.
func endpointPod(endpoint string) string {
	pod, _, _ := strings.Cut(primaryEndpointHost(endpoint), ".")
	return pod
}

// promotionLSN 은 새 타임라인 history 의 마지막 분기점이다(pg_read_file).
func (r *PostgresClusterReconciler) promotionLSN(ctx context.Context, namespace, pod string) (string, error) {
	out, err := r.PromotionPodExecutor.Exec(ctx, BackupSidecarTarget{
		Namespace: namespace,
		Pod:       pod,
		Container: pgContainerName,
	}, []string{"psql", "-U", "postgres", "-d", "postgres", "-Atqc", timelineHistoryQuery})
	if err != nil {
		return "", err
	}
	lsn, ok := lastSwitchPoint(string(out))
	if !ok {
		return "", errors.New("timeline history has no switch point")
	}
	return lsn, nil
}

// lastSwitchPoint 는 history(부모TLI \t 분기LSN \t 사유 줄들)의 마지막 분기 LSN 이다.
func lastSwitchPoint(history string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(history), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Split(strings.TrimSpace(lines[i]), "\t")
		if len(fields) >= 2 && lsnPattern.MatchString(fields[1]) {
			return fields[1], true
		}
	}
	return "", false
}

// markerEpochs 는 멤버마다 승격 마커의 epoch 을 읽는다(exec cat) — 빈 옛 마커·없음은 0,
// 못 읽은(돌지 않는) 멤버는 빠진다.
func (r *PostgresClusterReconciler) markerEpochs(ctx context.Context, pods []corev1.Pod) map[string]int64 {
	out := map[string]int64{}
	if r.PromotionPodExecutor == nil {
		return out
	}
	marker := pgDataSubdir + "/" + promotedPrimaryMarker
	for i := range pods {
		// 죽은 노드의 파드에 exec 하면 reconcile 시한까지 붙잡힌다.
		if pods[i].DeletionTimestamp != nil || pods[i].Status.Phase != corev1.PodRunning {
			continue
		}
		raw, err := r.PromotionPodExecutor.Exec(ctx, BackupSidecarTarget{
			Namespace: pods[i].Namespace,
			Pod:       pods[i].Name,
			Container: pgContainerName,
		}, []string{"sh", "-c", `if [ -f "$1" ]; then cat "$1"; fi`, "sh", marker})
		if err != nil {
			log.FromContext(ctx).Info("promoted marker unreadable", "pod", pods[i].Name, "error", err)
			continue
		}
		out[pods[i].Name] = parseEpoch(string(raw))
	}
	return out
}

// parseEpoch 는 epoch 글자를 읽는다 — 비었거나 숫자가 아니면 0(빈 옛 마커).
func parseEpoch(s string) int64 {
	epoch, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || epoch < 0 {
		return 0
	}
	return epoch
}

func maxEpoch(epochs map[string]int64) int64 {
	var top int64
	for _, e := range epochs {
		top = max(top, e)
	}
	return top
}

// statusRecordsPrimary 는 CR status 가 이 샤드의 primary 를 기록한 적이 있는지다.
func statusRecordsPrimary(cluster *postgresv1alpha1.PostgresCluster, shard string) bool {
	for i := range cluster.Status.Shards {
		s := &cluster.Status.Shards[i]
		if s.Name == shard && s.Primary != nil && s.Primary.Pod != "" {
			return true
		}
	}
	return false
}

// shardMembers 는 샤드 멤버 파드다.
func (r *PostgresClusterReconciler) shardMembers(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	member func(*corev1.Pod) bool,
) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(statusAggregationSelectorLabels(cluster.Name)),
	); err != nil {
		return nil, fmt.Errorf("list shard pods: %w", err)
	}
	out := make([]corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		if member(&pods.Items[i]) {
			out = append(out, pods.Items[i])
		}
	}
	return out, nil
}

// healthyPrimaries 는 하트비트가 신선하고 Ready·unfenced 인 Role=Primary 멤버다.
func (r *PostgresClusterReconciler) healthyPrimaries(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pods []corev1.Pod,
) ([]corev1.Pod, error) {
	if len(pods) == 0 {
		return nil, nil
	}
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []corev1.Pod
	for i := range pods {
		st, ok := parsePodStatus(&pods[i])
		if !ok || st.Role != statusapi.RolePrimary || !st.Ready || st.IsStale(now, statusStaleThresh) {
			continue
		}
		if kubernetesPodNotReady(&pods[i]) || fenced["data-"+pods[i].Name] {
			continue
		}
		out = append(out, pods[i])
	}
	return out, nil
}

// fencedPVCs 는 fence 라벨이 붙은 PVC 이름 집합이다.
func (r *PostgresClusterReconciler) fencedPVCs(ctx context.Context, namespace string) (map[string]bool, error) {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list pvcs for fence check: %w", err)
	}
	out := map[string]bool{}
	for i := range pvcs.Items {
		if pvcs.Items[i].Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
			out[pvcs.Items[i].Name] = true
		}
	}
	return out, nil
}

// memberEndpoint 는 멤버가 보고한 endpoint — 없으면 headless DNS.
func memberEndpoint(pod *corev1.Pod, service string) string {
	if st, ok := parsePodStatus(pod); ok && st.Endpoint != "" {
		return st.Endpoint
	}
	return defaultEndpoint(pod.Name, service, pod.Namespace)
}

// stsAbsent 는 StatefulSet 이 아직 없는지(새 샤드)다.
func (r *PostgresClusterReconciler) stsAbsent(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	name string,
) (bool, error) {
	var sts appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, &sts)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

// setRuntimeCondition 은 런타임 CM 을 못 만든 샤드를 condition 으로 보인다(KSM → O1 경보).
func setRuntimeCondition(cluster *postgresv1alpha1.PostgresCluster, pending []string) {
	if len(pending) == 0 {
		setCondition(&cluster.Status.Conditions, ConditionRuntimeConfigPending, metav1.ConditionFalse,
			ReasonRuntimeConfigPublished, "runtime ConfigMap published for every shard", cluster.Generation)
		return
	}
	setCondition(&cluster.Status.Conditions, ConditionRuntimeConfigPending, metav1.ConditionTrue,
		ReasonPrimaryNotUnique,
		fmt.Sprintf("runtime ConfigMap withheld until exactly one healthy primary; old pod template kept: %s",
			strings.Join(pending, ",")),
		cluster.Generation)
}
