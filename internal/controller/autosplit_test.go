/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestAutoSplitTriggerBreached(t *testing.T) {
	oneGB := bytesPerGB
	tests := []struct {
		name     string
		triggers *postgresv1alpha1.AutoSplitTriggers
		obs      ShardObservation
		want     bool
	}{
		{
			name:     "nil triggers never breach",
			triggers: nil,
			obs:      ShardObservation{SizeBytes: 100 * oneGB},
			want:     false,
		},
		{
			name:     "no enabled trigger never breaches",
			triggers: &postgresv1alpha1.AutoSplitTriggers{},
			obs:      ShardObservation{SizeBytes: 100 * oneGB},
			want:     false,
		},
		{
			name:     "size at threshold breaches",
			triggers: &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 10},
			obs:      ShardObservation{SizeBytes: 10 * oneGB},
			want:     true,
		},
		{
			name:     "size below threshold does not breach",
			triggers: &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 10},
			obs:      ShardObservation{SizeBytes: 9 * oneGB},
			want:     false,
		},
		{
			name:     "AND semantics: size ok but cpu unsourced blocks",
			triggers: &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 10, CPUPercent: 80},
			obs:      ShardObservation{SizeBytes: 100 * oneGB, CPUPercent: 0},
			want:     false,
		},
		{
			name:     "AND semantics: both size and cpu met",
			triggers: &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 10, CPUPercent: 80},
			obs:      ShardObservation{SizeBytes: 100 * oneGB, CPUPercent: 85},
			want:     true,
		},
		{
			name:     "latency enabled but unsourced blocks",
			triggers: &postgresv1alpha1.AutoSplitTriggers{P99LatencyMs: 50},
			obs:      ShardObservation{P99LatencyMs: 0},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := autoSplitTriggerBreached(tc.triggers, tc.obs)
			if got != tc.want {
				t.Fatalf("breached = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAutoSplitSustained(t *testing.T) {
	r := &PostgresClusterReconciler{}
	base := time.Now()
	dur := 5 * time.Minute
	key := "ns/c/ks/shard-0"

	// 첫 breach 관측: 아직 지속 안 됨 → false.
	if r.autoSplitSustained(key, true, base, dur) {
		t.Fatalf("first breach should not be sustained yet")
	}
	// duration 미달 → 여전히 false.
	if r.autoSplitSustained(key, true, base.Add(4*time.Minute), dur) {
		t.Fatalf("4m < 5m should not be sustained")
	}
	// duration 도달 → true.
	if !r.autoSplitSustained(key, true, base.Add(5*time.Minute), dur) {
		t.Fatalf("5m >= 5m should be sustained")
	}
	// breach 해제 → window 리셋(false), 재관측은 다시 처음부터.
	if r.autoSplitSustained(key, false, base.Add(6*time.Minute), dur) {
		t.Fatalf("cleared breach returns false")
	}
	if r.autoSplitSustained(key, true, base.Add(7*time.Minute), dur) {
		t.Fatalf("re-breach restarts the window")
	}

	// duration 0 = 즉시.
	if !r.autoSplitSustained("ns/c/ks/shard-1", true, base, 0) {
		t.Fatalf("duration 0 should be sustained immediately")
	}
}

func TestComputeHashSplitTargets(t *testing.T) {
	entry := postgresv1alpha1.ShardRangeEntry{Lo: "0x00000000", Hi: "0xffffffff", Shard: "shard-0"}
	targets, err := computeHashSplitTargets(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	// 결정론: 같은 입력은 항상 같은 target ID(멱등).
	targets2, _ := computeHashSplitTargets(entry)
	if targets[0].ShardID != targets2[0].ShardID || targets[1].ShardID != targets2[1].ShardID {
		t.Fatalf("target IDs are not deterministic: %v vs %v", targets, targets2)
	}
	// ID 는 target 필드 정규식 ^[a-z]([a-z0-9-]*[a-z0-9])?$ 을 만족해야 한다.
	for _, tg := range targets {
		if len(tg.ShardID) == 0 || len(tg.ShardID) > 30 {
			t.Fatalf("target ID %q length invalid", tg.ShardID)
		}
		if tg.ShardID[0] < 'a' || tg.ShardID[0] > 'z' {
			t.Fatalf("target ID %q must start with a letter", tg.ShardID)
		}
		if tg.Ranges[0].Shard != tg.ShardID {
			t.Fatalf("range.Shard %q != ShardID %q", tg.Ranges[0].Shard, tg.ShardID)
		}
	}
	// 범위 커버리지: [0,7fff...],[8000...,ffff...].
	if targets[0].Ranges[0].Lo != "0x00000000" || targets[0].Ranges[0].Hi != "0x7fffffff" {
		t.Fatalf("target0 range = [%s,%s]", targets[0].Ranges[0].Lo, targets[0].Ranges[0].Hi)
	}
	if targets[1].Ranges[0].Lo != "0x80000000" || targets[1].Ranges[0].Hi != "0xffffffff" {
		t.Fatalf("target1 range = [%s,%s]", targets[1].Ranges[0].Lo, targets[1].Ranges[0].Hi)
	}

	// 단일 키 범위는 split 불가.
	if _, err := computeHashSplitTargets(postgresv1alpha1.ShardRangeEntry{Lo: "0x5", Hi: "0x5", Shard: "s"}); err == nil {
		t.Fatalf("single-key range should error")
	}
}

func TestSanitizeNameAndJobName(t *testing.T) {
	if got := sanitizeName("Shard_0"); got != shard0 {
		t.Fatalf("sanitizeName = %q, want shard-0", got)
	}
	name := autoSplitJobName("myks", "shard-0", "0x00000000", "0xffffffff")
	if len(name) == 0 || len(name) > 63 {
		t.Fatalf("job name %q length invalid", name)
	}
	// 결정론.
	if autoSplitJobName("myks", "shard-0", "0x00000000", "0xffffffff") != name {
		t.Fatalf("job name not deterministic")
	}
}

func TestAutoSplitHoldForApproval(t *testing.T) {
	mk := func(ann map[string]string) *postgresv1alpha1.ShardSplitJob {
		return &postgresv1alpha1.ShardSplitJob{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	tests := []struct {
		name string
		ann  map[string]string
		want bool
	}{
		{"no annotations", nil, false},
		{"not autosplit", map[string]string{AnnotationAutoSplitApproval: "required"}, false},
		{"autosplit auto approval", map[string]string{AnnotationAutoSplit: "true", AnnotationAutoSplitApproval: "auto"}, false},
		{"autosplit required not approved", map[string]string{AnnotationAutoSplit: "true", AnnotationAutoSplitApproval: "required"}, true},
		{"autosplit required approved", map[string]string{AnnotationAutoSplit: "true", AnnotationAutoSplitApproval: "required", AnnotationAutoSplitApproved: "true"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoSplitHoldForApproval(mk(tc.ann)); got != tc.want {
				t.Fatalf("hold = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusShardObserver(t *testing.T) {
	cluster := &postgresv1alpha1.PostgresCluster{
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{
				{Name: "shard-0", SizeBytes: 42},
				{Name: "", SizeBytes: 99}, // 이름 없음 → skip.
				{Name: "shard-1", SizeBytes: 0},
			},
		},
	}
	obs := statusShardObserver{}.ObserveShards(context.Background(), cluster)
	if len(obs) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(obs))
	}
	if obs[0].ShardID != shard0 || obs[0].SizeBytes != 42 {
		t.Fatalf("obs[0] = %+v", obs[0])
	}
	if obs[0].CPUPercent != 0 || obs[0].P99LatencyMs != 0 {
		t.Fatalf("cpu/latency should be unsourced (0)")
	}
}

// fakeObserver 는 고정 관측치를 반환하는 테스트 observer.
type fakeObserver struct{ obs []ShardObservation }

func (f fakeObserver) ObserveShards(_ context.Context, _ *postgresv1alpha1.PostgresCluster) []ShardObservation {
	return f.obs
}

func autoSplitCluster(name, ns string, requireApproval bool) *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			ShardingMode: postgresv1alpha1.ShardingModeNative,
			AutoSplit: &postgresv1alpha1.AutoSplitSpec{
				Enabled:         true,
				RequireApproval: requireApproval,
				Triggers:        &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 10},
			},
		},
	}
}

func autoSplitShardRange(cluster, ns, keyspace, shard string) *postgresv1alpha1.ShardRange {
	return &postgresv1alpha1.ShardRange{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + "-" + keyspace, Namespace: ns},
		Spec: postgresv1alpha1.ShardRangeSpec{
			Cluster:  cluster,
			Keyspace: keyspace,
			Vindex:   postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: postgresv1alpha1.VindexHashMurmur3},
			Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: shard}},
		},
	}
}

func TestReconcileAutoSplit_CreatesApprovalGatedJob(t *testing.T) {
	scheme := newScheme(t)
	ns := metav1.NamespaceDefault
	cluster := autoSplitCluster("demo", ns, true)
	sr := autoSplitShardRange("demo", ns, "myks", "shard-0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sr).Build()

	// 10GB 임계 초과(20GB) 관측 + duration 0(즉시).
	r := &PostgresClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Observer: fakeObserver{obs: []ShardObservation{{ShardID: "shard-0", SizeBytes: 20 * bytesPerGB}}},
	}

	eligible, reason, _ := r.reconcileAutoSplit(context.Background(), cluster, time.Now())
	if eligible != 1 {
		t.Fatalf("eligible = %d, want 1", eligible)
	}
	if reason != autoSplitReasonEligible {
		t.Fatalf("reason = %q, want %q", reason, autoSplitReasonEligible)
	}

	// job 이 생성됐는지 + approval=required + owner ref + split target 확인.
	var jobs postgresv1alpha1.ShardSplitJobList
	if err := c.List(context.Background(), &jobs); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 ShardSplitJob, got %d", len(jobs.Items))
	}
	job := jobs.Items[0]
	if job.Annotations[AnnotationAutoSplit] != labelTrue {
		t.Fatalf("job missing autosplit annotation")
	}
	if job.Annotations[AnnotationAutoSplitApproval] != autoSplitApprovalRequired {
		t.Fatalf("approval = %q, want required", job.Annotations[AnnotationAutoSplitApproval])
	}
	if !autoSplitHoldForApproval(&job) {
		t.Fatalf("generated job should hold for approval")
	}
	if job.Spec.Cluster != demoCluster || job.Spec.Keyspace != "myks" {
		t.Fatalf("job spec target wrong: %+v", job.Spec)
	}
	if len(job.Spec.Sources) != 1 || job.Spec.Sources[0] != shard0 {
		t.Fatalf("job sources = %v", job.Spec.Sources)
	}
	if len(job.Spec.Targets) != 2 {
		t.Fatalf("job targets = %d, want 2", len(job.Spec.Targets))
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Name != demoCluster {
		t.Fatalf("job owner ref = %v", job.OwnerReferences)
	}

	// 멱등: 재실행 시 새 job 을 만들지 않는다(같은 이름 이미 존재).
	if _, _, _ = r.reconcileAutoSplit(context.Background(), cluster, time.Now()); true {
		var again postgresv1alpha1.ShardSplitJobList
		if err := c.List(context.Background(), &again); err != nil {
			t.Fatalf("list jobs (2nd): %v", err)
		}
		if len(again.Items) != 1 {
			t.Fatalf("idempotent reconcile created duplicate: %d jobs", len(again.Items))
		}
	}
}

func TestReconcileAutoSplit_NoCandidateBelowThreshold(t *testing.T) {
	scheme := newScheme(t)
	ns := metav1.NamespaceDefault
	cluster := autoSplitCluster("demo", ns, false)
	sr := autoSplitShardRange("demo", ns, "myks", "shard-0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sr).Build()

	r := &PostgresClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Observer: fakeObserver{obs: []ShardObservation{{ShardID: "shard-0", SizeBytes: 5 * bytesPerGB}}}, // < 10GB
	}
	eligible, reason, _ := r.reconcileAutoSplit(context.Background(), cluster, time.Now())
	if eligible != 0 {
		t.Fatalf("eligible = %d, want 0", eligible)
	}
	if reason != autoSplitReasonNone {
		t.Fatalf("reason = %q, want %q", reason, autoSplitReasonNone)
	}
	var jobs postgresv1alpha1.ShardSplitJobList
	_ = c.List(context.Background(), &jobs)
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no jobs, got %d", len(jobs.Items))
	}
}

func TestReconcileAutoSplit_UnsourcedMetricsReason(t *testing.T) {
	scheme := newScheme(t)
	ns := metav1.NamespaceDefault
	cluster := autoSplitCluster("demo", ns, false)
	// p99 latency 트리거를 추가 — 라우터 지연 메트릭 미결선이라 breach 불가.
	// (cpu 는 이제 cpuAugmentingObserver 로 결선되어 unsourced 아님.)
	cluster.Spec.AutoSplit.Triggers.P99LatencyMs = 50
	sr := autoSplitShardRange("demo", ns, "myks", "shard-0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sr).Build()

	r := &PostgresClusterReconciler{
		Client:   c,
		Scheme:   scheme,
		Observer: fakeObserver{obs: []ShardObservation{{ShardID: "shard-0", SizeBytes: 100 * bytesPerGB}}},
	}
	eligible, reason, _ := r.reconcileAutoSplit(context.Background(), cluster, time.Now())
	if eligible != 0 {
		t.Fatalf("eligible = %d, want 0 (latency unsourced blocks AND)", eligible)
	}
	if reason != autoSplitReasonNoMetrics {
		t.Fatalf("reason = %q, want %q", reason, autoSplitReasonNoMetrics)
	}
}

// TestPVCUtilizationBreached 는 K-5 — PVC 사용률(%) 트리거 순수함수를 검증한다.
func TestPVCUtilizationBreached(t *testing.T) {
	cases := []struct {
		name      string
		obs       ShardObservation
		threshold int32
		want      bool
	}{
		{"80% 임계, 사용 85% → breach", ShardObservation{SizeBytes: 85, PVCCapacityBytes: 100}, 80, true},
		{"80% 임계, 사용 정확히 80% → breach(>=)", ShardObservation{SizeBytes: 80, PVCCapacityBytes: 100}, 80, true},
		{"80% 임계, 사용 79% → no", ShardObservation{SizeBytes: 79, PVCCapacityBytes: 100}, 80, false},
		{"용량 미상(0) → no(오탐 방지)", ShardObservation{SizeBytes: 999, PVCCapacityBytes: 0}, 80, false},
		{"크기 미관측(0) → no", ShardObservation{SizeBytes: 0, PVCCapacityBytes: 100}, 80, false},
		{"대용량 오버플로 없음(50Gi 중 45Gi=90%)", ShardObservation{SizeBytes: 45 << 30, PVCCapacityBytes: 50 << 30}, 80, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pvcUtilizationBreached(tc.obs, tc.threshold); got != tc.want {
				t.Fatalf("pvcUtilizationBreached(%+v, %d) = %v, want %v", tc.obs, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestAutoSplitTriggerBreached_PVC 는 PVC 트리거가 AND 평가에 결선됐는지 검증한다.
func TestAutoSplitTriggerBreached_PVC(t *testing.T) {
	trig := &postgresv1alpha1.AutoSplitTriggers{PVCUtilizationPercent: 80}
	// 85% → breach
	if b, _, enPVC := autoSplitTriggerBreached(trig, ShardObservation{SizeBytes: 85, PVCCapacityBytes: 100}); !b || !enPVC {
		t.Fatalf("85%% 사용 시 breach=true enPVC=true 기대, got b=%v enPVC=%v", b, enPVC)
	}
	// 50% → no breach
	if b, _, _ := autoSplitTriggerBreached(trig, ShardObservation{SizeBytes: 50, PVCCapacityBytes: 100}); b {
		t.Fatalf("50%% 사용 시 breach=false 기대")
	}
}
