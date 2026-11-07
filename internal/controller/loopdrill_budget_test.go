/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// kind 훈련장(internal/loopdrill/budgets.go)의 창이 오퍼레이터 상수를 덮는지 교차 확인한다 —
// 창이 짧으면 수정판이 정상 동작해도 판정이 붉거나, 기점 결함이 창 밖으로 빠져 RED 가 초록이 된다.
func TestLoopDrillBudgetsCoverOperator(t *testing.T) {
	// 창 ≥ 오퍼레이터 상수(수정판에서 파생될 값은 설계의 파생 근거 상수로)
	atLeast := []struct {
		window string
		got    time.Duration
		floor  time.Duration
		why    string
	}{
		{"StatusPollInterval", loopdrill.StatusPollInterval, statusPollInterval, "statusPollInterval"},
		{"StatusStaleThresh", loopdrill.StatusStaleThresh, statusStaleThresh, "statusStaleThresh"},
		{"DebounceBound", loopdrill.DebounceBound, failoverDebounceThreshold, "failoverDebounceThreshold"},
		{"DebounceBound", loopdrill.DebounceBound, statusStaleThresh, "설계 §3.3 디바운스 = statusStaleThresh"},
		{"RetargetBound", loopdrill.RetargetBound, statusStaleThresh, "설계 §3.4 memberRetargetAfter = statusStaleThresh"},
		{"StaleBound", loopdrill.StaleBound, staleStandbyReseedTimeout, "staleStandbyReseedTimeout"},
		{"SnapshotReadyBound", loopdrill.SnapshotReadyBound, snapshotReadyTimeout, "snapshotReadyTimeout"},
		{"MemberConvergeBudget", loopdrill.MemberConvergeBudget, memberConvergeBudget, "memberConvergeBudget — 롤아웃 동결 상한"},
		{"PromotionExecBudget", loopdrill.PromotionExecBudget, promotionExecBudget, "promotionExecBudget"},
		{"SwitchoverBudget", loopdrill.SwitchoverBudget, switchoverBudget, "switchoverBudget(부록 A) — J19 는 그 안의 -rw Endpoints 1 을 본다"},
		{"SwitchoverRestartWait", loopdrill.SwitchoverRestartWait, switchoverRestartWait, "switchoverRestartWait"},
		{"WriteGapBudget", loopdrill.WriteGapBudget, switchoverRestartWait, "switchoverRestartWait ≤ writeGapBudget(= statusStaleThresh, 부록 A)"},
		{"CooldownBound", loopdrill.CooldownBound, reseedCooldown, "reseedCooldown"},
		{"StuckTerminatingBound", loopdrill.StuckTerminatingBound, stuckTerminatingForceDeleteThreshold, "stuckTerminatingForceDeleteThreshold"},
		{"ConvergeBound", loopdrill.ConvergeBound, 2*staleStandbyReseedTimeout + reseedCooldown, "Stale 판정 + snapshotReadyTimeout + reseedCooldown"},
		{"LoopObservationWindow", loopdrill.LoopObservationWindow, reseedCooldown, "관측창 = reseedCooldown"},
		{"StallStopDuration", loopdrill.StallStopDuration, stuckTerminatingForceDeleteThreshold + staleStandbyReseedTimeout, "설계 S5 하한"},
		{"StallStopDuration", loopdrill.StallStopDuration, statusStaleThresh + reseedCooldown, "기점 정지 = 끊김 판정 + 두 번째 재시드"},
		{"StandbyAgeFloor", loopdrill.StandbyAgeFloor, staleStandbyReseedTimeout, "기점 재시드 파드 나이 조건"},
		{"StandbyFreeze", loopdrill.StandbyFreeze, staleStandbyReseedTimeout, "S6 끊김 > Stale 판정"},
	}
	for _, c := range atLeast {
		if c.got < c.floor {
			t.Errorf("%s = %s < %s(%s)", c.window, c.got, c.floor, c.why)
		}
	}

	// S6·S7 — 얼린 standby 는 walsender 가 wal_sender_timeout 뒤에야 그래프에서 빠지고, 그 뒤 Stale 판정 ·
	// hold 전파 · 정지까지 얼어 있어야 재구축이 선다(녹으면 재검증이 취소한다). 판정문 S7: 8m30s 정지에 재구축 0.
	frozenUntilStop := loopdrill.WalSenderTimeout + staleStandbyReseedTimeout + runtimePropagationWait + statusPollInterval
	if loopdrill.StandbyFreeze < frozenUntilStop {
		t.Errorf("StandbyFreeze = %s < wal_sender_timeout + Stale 판정 + 전파 + 재큐 %s", loopdrill.StandbyFreeze, frozenUntilStop)
	}

	// S3·S4·S6 정지는 하트비트 끊김 문턱을 넘어야 관측자가 본다
	if loopdrill.FaultPause <= statusStaleThresh {
		t.Errorf("FaultPause = %s ≤ statusStaleThresh %s", loopdrill.FaultPause, statusStaleThresh)
	}
}
