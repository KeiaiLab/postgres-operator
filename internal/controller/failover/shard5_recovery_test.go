/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package failover

// shard5_recovery_test.go — 5-shard 분산 클러스터에서 3번째 샤드(shard-2) Primary
// 장애 발생 시 자동 복구 파이프라인 전체를 검증하는 시나리오 테스트.
//
// 검증 범위:
//   1. 5개 샤드 중 shard-2 만 장애 감지 (다른 4개는 정상)
//   2. shard-2 복구 후보 자동 선정 (LagBytes 기준 최적 Replica)
//   3. Promotion Plan 생성 + 실행
//   4. 복구 후 상태 갱신 → 전체 재검사 시 5개 샤드 모두 정상
//
// 이 테스트는 실제 K8s / PostgreSQL 없이 순수 로직 레이어를 검증한다.
// e2e (Kind 클러스터 위 실제 Pod) 수준 검증은 test/e2e/failover_e2e_test.go 담당.

import (
	"context"
	"fmt"
	"testing"
	"time"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ── 헬퍼 ──────────────────────────────────────────────────────────────────────

// makeHealthyShard 는 Primary 1개 + Replica 2개가 모두 Ready 상태인 shard 를 반환.
func makeHealthyShard(name string) postgresv1alpha1.ShardStatus {
	return postgresv1alpha1.ShardStatus{
		Name: name,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod: name + "-0", Ready: true, LagBytes: 0,
		},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: name + "-1", Ready: true, LagBytes: 50},
			{Pod: name + "-2", Ready: true, LagBytes: 120},
		},
	}
}

// makeFailedShard 는 Primary 가 NotReady 이고 두 Replica 가 각기 다른 Lag 를 가진 shard.
// 복구 후보는 LagBytes 가 작은 replica 로 결정되어야 한다.
func makeFailedShard(name string) postgresv1alpha1.ShardStatus {
	return postgresv1alpha1.ShardStatus{
		Name: name,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod: name + "-0", Ready: false, // Primary 비정상
		},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: name + "-1", Ready: true, LagBytes: 200}, // 후보 아님 (lag 큼)
			{Pod: name + "-2", Ready: true, LagBytes: 30},  // ← 최적 후보 (lag 최소)
		},
	}
}

// applyPromotion 은 Promoter 가 실행한 뒤 shard 상태를 업데이트하는 것을 시뮬레이션.
// 실제 환경에서는 instance manager 가 pg_ctl promote 를 실행하고 annotation 을 갱신한다.
func applyPromotion(shard postgresv1alpha1.ShardStatus, plan PromotionPlan) postgresv1alpha1.ShardStatus {
	// 승격된 Replica 가 새 Primary 로 등재
	newPrimary := &postgresv1alpha1.ShardEndpoint{
		Pod: plan.Target.Pod, Ready: true, LagBytes: 0,
	}
	// 기존 Primary 는 Replica 로 재합류 (rejoin — standby.signal 생성 후 streaming replication)
	oldPrimaryReplica := postgresv1alpha1.ShardEndpoint{
		Pod: shard.Primary.Pod, Ready: true, LagBytes: 80,
	}
	// 나머지 Replica 들 (승격된 것 제외)
	var remainingReplicas []postgresv1alpha1.ShardEndpoint
	remainingReplicas = append(remainingReplicas, oldPrimaryReplica)
	for _, r := range shard.Replicas {
		if r.Pod != plan.Target.Pod {
			remainingReplicas = append(remainingReplicas, r)
		}
	}
	return postgresv1alpha1.ShardStatus{
		Name:     shard.Name,
		Primary:  newPrimary,
		Replicas: remainingReplicas,
	}
}

// recordingPromoter 는 Promotion 실행을 기록만 하고 성공을 반환 (테스트용).
type recordingPromoter struct {
	plans     []PromotionPlan
	latencies []time.Duration
}

func (r *recordingPromoter) Execute(_ context.Context, plan PromotionPlan) error {
	start := time.Now()
	// 실제 pg_ctl promote 는 수 초 걸리지만 단위 테스트에서는 즉시 완료로 처리.
	r.plans = append(r.plans, plan)
	r.latencies = append(r.latencies, time.Since(start))
	return nil
}

// ── 메인 시나리오 테스트 ────────────────────────────────────────────────────────

// TestShard5_Shard2PrimaryFailureAndRecovery 는 5-shard 분산 클러스터에서
// shard-2 (3번째 샤드, 0-indexed) Primary 장애 → 자동 복구 전 과정을 검증한다.
//
// 시나리오:
//
//	[초기] shard-0 ~ shard-4 전부 정상
//	[장애] shard-2 Primary (shard-2-0) Ready=false
//	[감지] 5개 샤드 순회 → shard-2 만 Failed 판정
//	[복구] shard-2-2 (lag=30) 자동 선정 → PromotionPlan 생성 → Promoter 실행
//	[갱신] shard-2 상태 업데이트 (새 Primary: shard-2-2, 옛 Primary: Replica 재합류)
//	[재검사] 5개 샤드 전부 정상 확인
func TestShard5_Shard2PrimaryFailureAndRecovery(t *testing.T) {
	t.Parallel()

	const (
		failedShardIndex = 2 // 0-indexed → 3번째 샤드
		totalShards      = 5
	)

	// ── 단계 1: 5개 샤드 초기 상태 구성 ─────────────────────────────────────────
	shards := make([]postgresv1alpha1.ShardStatus, totalShards)
	for i := range shards {
		shards[i] = makeHealthyShard(fmt.Sprintf("shard-%d", i))
	}
	// 3번째 샤드 (index=2) 만 Primary 장애 주입
	shards[failedShardIndex] = makeFailedShard("shard-2")

	t.Logf("=== [단계 1] 초기 상태 ===")
	for _, s := range shards {
		if s.Primary != nil {
			t.Logf("  %-10s  primary=%-12s ready=%-5v  replicas=%d",
				s.Name, s.Primary.Pod, s.Primary.Ready, len(s.Replicas))
		} else {
			t.Logf("  %-10s  primary=<nil>  replicas=%d", s.Name, len(s.Replicas))
		}
	}

	// ── 단계 2: 5개 샤드 전체 장애 감지 순회 ──────────────────────────────────
	t.Logf("\n=== [단계 2] 장애 감지 순회 ===")
	decisions := make([]Decision, totalShards)
	failedCount := 0

	for i, shard := range shards {
		d := DetectPrimaryFailure(shard)
		decisions[i] = d
		status := "정상"
		if d.Failed {
			status = fmt.Sprintf("장애(%s)", d.Reason)
			failedCount++
		}
		t.Logf("  %-10s  %s", shard.Name, status)
		if d.Failed && d.PromotionCandidate != nil {
			t.Logf("    └─ 복구 후보: %s (lag=%d bytes)", d.PromotionCandidate.Pod, d.PromotionCandidate.LagBytes)
		}
	}

	// 검증: 정확히 1개 샤드(shard-2)만 장애
	if failedCount != 1 {
		t.Fatalf("장애 샤드 수 = %d, 기대값 = 1", failedCount)
	}

	// 검증: 나머지 4개 샤드는 정상
	for i, d := range decisions {
		if i == failedShardIndex {
			if !d.Failed {
				t.Errorf("shard-%d 는 장애여야 함", i)
			}
		} else {
			if d.Failed {
				t.Errorf("shard-%d 는 정상이어야 하는데 장애 판정됨: reason=%s", i, d.Reason)
			}
		}
	}

	// ── 단계 3: shard-2 장애 상세 검증 ──────────────────────────────────────────
	t.Logf("\n=== [단계 3] shard-2 장애 상세 ===")
	d2 := decisions[failedShardIndex]

	if d2.Reason != ReasonPrimaryNotReady {
		t.Errorf("Reason = %q, 기대값 = %q", d2.Reason, ReasonPrimaryNotReady)
	}
	if d2.PromotionCandidate == nil {
		t.Fatal("복구 후보가 nil — 자동 복구 불가")
	}
	// lag=30 인 shard-2-2 가 선정되어야 한다 (shard-2-1 lag=200 보다 작음)
	if d2.PromotionCandidate.Pod != "shard-2-2" {
		t.Errorf("복구 후보 Pod = %q, 기대값 = 'shard-2-2' (lag 최소)", d2.PromotionCandidate.Pod)
	}
	t.Logf("  장애 이유: %s", d2.Reason)
	t.Logf("  메시지: %s", d2.Message)
	t.Logf("  복구 후보: %s (lag=%d bytes)", d2.PromotionCandidate.Pod, d2.PromotionCandidate.LagBytes)

	// ── 단계 4: Promotion Plan 생성 + 실행 ───────────────────────────────────────
	t.Logf("\n=== [단계 4] Promotion Plan 생성 및 실행 ===")
	promoter := &recordingPromoter{}

	err := PromoteFromDecision(context.Background(), "shard-2", d2, promoter)
	if err != nil {
		t.Fatalf("PromoteFromDecision 실패: %v", err)
	}
	if len(promoter.plans) != 1 {
		t.Fatalf("Promoter 실행 횟수 = %d, 기대값 = 1", len(promoter.plans))
	}

	plan := promoter.plans[0]
	t.Logf("  대상 샤드: %s", plan.Target.ShardName)
	t.Logf("  승격 Pod: %s", plan.Target.Pod)
	t.Logf("  실행 단계 (%d개):", len(plan.Steps))
	for j, step := range plan.Steps {
		t.Logf("    [%d] %s", j+1, step)
	}

	// 4단계 순서 고정 검증
	wantSteps := []PromotionStep{
		StepRemoveStandbySignal,
		StepPgCtlPromote,
		StepWaitNotInRecovery,
		StepUpdateInstanceRole,
	}
	if len(plan.Steps) != len(wantSteps) {
		t.Fatalf("Steps 수 = %d, 기대값 = %d", len(plan.Steps), len(wantSteps))
	}
	for j, want := range wantSteps {
		if plan.Steps[j] != want {
			t.Errorf("Steps[%d] = %q, 기대값 = %q", j, plan.Steps[j], want)
		}
	}

	// ── 단계 5: 복구 후 상태 갱신 ────────────────────────────────────────────────
	t.Logf("\n=== [단계 5] 복구 후 상태 갱신 ===")
	// instance manager 가 pg_ctl promote 완료 후 annotation 을 갱신하는 것을 시뮬레이션
	shards[failedShardIndex] = applyPromotion(shards[failedShardIndex], plan)
	recovered := shards[failedShardIndex]

	t.Logf("  새 Primary: %s (ready=%v)", recovered.Primary.Pod, recovered.Primary.Ready)
	t.Logf("  Replica 목록 (%d개):", len(recovered.Replicas))
	for _, r := range recovered.Replicas {
		t.Logf("    - %s (lag=%d bytes)", r.Pod, r.LagBytes)
	}

	// 검증: 새 Primary 는 승격된 Replica
	if recovered.Primary.Pod != d2.PromotionCandidate.Pod {
		t.Errorf("새 Primary Pod = %q, 기대값 = %q", recovered.Primary.Pod, d2.PromotionCandidate.Pod)
	}
	if !recovered.Primary.Ready {
		t.Errorf("새 Primary 가 Ready=false — 복구 실패")
	}

	// 검증: 옛 Primary 가 Replica 로 재합류
	oldPrimaryPod := "shard-2-0"
	oldPrimaryFound := false
	for _, r := range recovered.Replicas {
		if r.Pod == oldPrimaryPod {
			oldPrimaryFound = true
			if !r.Ready {
				t.Errorf("옛 Primary %s 가 Replica 로 재합류했지만 Ready=false", oldPrimaryPod)
			}
			break
		}
	}
	if !oldPrimaryFound {
		t.Errorf("옛 Primary %s 가 Replica 목록에 없음 — rejoin 누락", oldPrimaryPod)
	}

	// ── 단계 6: 복구 후 전체 재검사 ─────────────────────────────────────────────
	t.Logf("\n=== [단계 6] 복구 후 전체 재검사 ===")
	postRecoveryFailed := 0
	for _, shard := range shards {
		d := DetectPrimaryFailure(shard)
		status := "✓ 정상"
		if d.Failed {
			status = fmt.Sprintf("✗ 장애(%s)", d.Reason)
			postRecoveryFailed++
		}
		t.Logf("  %-10s  %s", shard.Name, status)
	}

	if postRecoveryFailed != 0 {
		t.Errorf("복구 후 장애 샤드 수 = %d, 기대값 = 0 (전체 정상)", postRecoveryFailed)
	}

	t.Logf("\n=== 결과: 5개 샤드 모두 정상 복구 완료 ===")
}

// ── 보완 케이스 ─────────────────────────────────────────────────────────────────

// TestShard5_AllShardsHealthy 는 장애가 없을 때 5개 샤드 모두 정상 판정을 보장.
func TestShard5_AllShardsHealthy(t *testing.T) {
	t.Parallel()
	shards := make([]postgresv1alpha1.ShardStatus, 5)
	for i := range shards {
		shards[i] = makeHealthyShard(fmt.Sprintf("shard-%d", i))
	}
	for _, s := range shards {
		d := DetectPrimaryFailure(s)
		if d.Failed {
			t.Errorf("%s 가 장애 판정됨 (기대: 정상) reason=%s", s.Name, d.Reason)
		}
	}
}

// TestShard5_MultipleShardFailure 는 여러 샤드가 동시에 실패했을 때 각각 독립적으로
// 감지·복구 후보가 선정되는지 검증한다.
func TestShard5_MultipleShardFailure(t *testing.T) {
	t.Parallel()

	shards := make([]postgresv1alpha1.ShardStatus, 5)
	for i := range shards {
		shards[i] = makeHealthyShard(fmt.Sprintf("shard-%d", i))
	}

	// shard-1 과 shard-3 동시 장애
	failedIndices := map[int]bool{1: true, 3: true}
	shards[1] = makeFailedShard("shard-1")
	shards[3] = makeFailedShard("shard-3")

	failedCount := 0
	for i, shard := range shards {
		d := DetectPrimaryFailure(shard)
		if failedIndices[i] {
			if !d.Failed {
				t.Errorf("shard-%d 는 장애여야 함", i)
			}
			if d.PromotionCandidate == nil {
				t.Errorf("shard-%d 복구 후보가 nil", i)
			} else if d.PromotionCandidate.Pod != fmt.Sprintf("shard-%d-2", i) {
				t.Errorf("shard-%d 복구 후보 = %q, 기대값 = shard-%d-2",
					i, d.PromotionCandidate.Pod, i)
			}
			failedCount++
		} else {
			if d.Failed {
				t.Errorf("shard-%d 는 정상이어야 함", i)
			}
		}
	}
	if failedCount != 2 {
		t.Errorf("감지된 장애 샤드 수 = %d, 기대값 = 2", failedCount)
	}
}

// TestShard5_Shard2NoPrimaryNoReplica 는 shard-2 Primary 도 없고 Ready Replica
// 도 없을 때 NoEligibleReplica 로 판정되어 자동 복구 불가 상태임을 검증한다.
func TestShard5_Shard2NoPrimaryNoReplica(t *testing.T) {
	t.Parallel()

	shards := make([]postgresv1alpha1.ShardStatus, 5)
	for i := range shards {
		shards[i] = makeHealthyShard(fmt.Sprintf("shard-%d", i))
	}
	// shard-2: Primary 없음 + Replica 전부 NotReady → 자동 복구 불가
	shards[2] = postgresv1alpha1.ShardStatus{
		Name:    "shard-2",
		Primary: nil,
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "shard-2-1", Ready: false},
			{Pod: "shard-2-2", Ready: false},
		},
	}

	d := DetectPrimaryFailure(shards[2])
	if !d.Failed {
		t.Fatal("장애로 판정되어야 함")
	}
	if d.Reason != ReasonNoEligibleReplica {
		t.Errorf("Reason = %q, 기대값 = %q", d.Reason, ReasonNoEligibleReplica)
	}
	if d.PromotionCandidate != nil {
		t.Errorf("복구 불가 상태에서 후보 = %v, 기대값 = nil", d.PromotionCandidate)
	}
	t.Logf("수동 개입 필요 메시지: %s", d.Message)
}

// TestShard5_RecoveryDeterminism 은 동일한 5-shard 장애 상태에서 복구 후보가
// 항상 같은 Pod 로 결정됨을 반복 실행으로 검증한다 (결정성 보장).
func TestShard5_RecoveryDeterminism(t *testing.T) {
	t.Parallel()

	shards := make([]postgresv1alpha1.ShardStatus, 5)
	for i := range shards {
		shards[i] = makeHealthyShard(fmt.Sprintf("shard-%d", i))
	}
	shards[2] = makeFailedShard("shard-2")

	var firstCandidate string
	for run := 0; run < 100; run++ {
		d := DetectPrimaryFailure(shards[2])
		if d.PromotionCandidate == nil {
			t.Fatal("복구 후보가 nil")
		}
		if run == 0 {
			firstCandidate = d.PromotionCandidate.Pod
		} else if d.PromotionCandidate.Pod != firstCandidate {
			t.Errorf("run %d: 복구 후보 = %q, 1회차 = %q (결정성 위반)",
				run, d.PromotionCandidate.Pod, firstCandidate)
		}
	}
	t.Logf("100회 반복 결정성 확인: 후보 = %s", firstCandidate)
}
