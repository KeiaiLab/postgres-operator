/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package loopdrill 은 kind 훈련장(설계 2026-09-25 §3.12)의 창 상수와 순수 판정이다.
// 태그 없이 internal 아래에 둬 land 검사·이미지 test 단계가 판정 시험을 돈다. 드라이버는 test/e2e(build tag e2e).
package loopdrill

import "time"

// 오퍼레이터 상수를 덮는 경계 — 값은 설계의 수정판 값이고 기점 값 이상이다(교차 확인 시험이 ≥ 를 본다).
const (
	// StatusPollInterval ≥ statusPollInterval(postgrescluster_controller.go) — reconcile 재큐 주기.
	StatusPollInterval = 5 * time.Second

	// StatusStaleThresh ≥ statusStaleThresh(aggregate_status.go) — 하트비트 끊김 문턱.
	StatusStaleThresh = 30 * time.Second

	// DebounceBound ≥ failoverDebounceThreshold — 기점 8s, 설계 §3.3 에서 statusStaleThresh(30s).
	DebounceBound = 30 * time.Second

	// RetargetBound ≥ memberRetargetAfter(설계 §3.4, = statusStaleThresh).
	RetargetBound = 30 * time.Second

	// StaleBound ≥ staleStandbyReseedTimeout(stale_replica_reseed.go) — Stale 판정·롤아웃 동결 상한.
	StaleBound = 8 * time.Minute

	// SnapshotReadyBound ≥ snapshotReadyTimeout(설계 부록 A, = staleStandbyReseedTimeout).
	SnapshotReadyBound = 8 * time.Minute

	// CooldownBound ≥ reseedCooldown — 멤버 쿨다운.
	CooldownBound = 10 * time.Minute

	// StuckTerminatingBound ≥ stuckTerminatingForceDeleteThreshold — force delete 문턱.
	StuckTerminatingBound = 2 * time.Minute

	// PromotionExecBudget 은 승격 exec 상한 — postgresPromotionCommand 의 pg_promote(true, 30) + 30×1s 대기.
	PromotionExecBudget = 60 * time.Second

	// RebuildBudgetWindow 는 재구축 예산 창(설계 §3.5 rebuildBudgetWindow).
	RebuildBudgetWindow = time.Hour
)

// 드라이버 창 — 위 경계에서 파생한다.
const (
	// ConvergeBound 는 유발 뒤 수렴 상한 = Stale 판정 + snapshotReadyTimeout + reseedCooldown.
	ConvergeBound = StaleBound + SnapshotReadyBound + CooldownBound

	// LoopObservationWindow 는 수렴 뒤 추가 승격·정지를 보는 창 = reseedCooldown(루프 주기 ≈3분의 3배 이상).
	LoopObservationWindow = CooldownBound

	// StallStopDuration 은 S5 가 워커를 세워 두는 시간.
	// 기점 정지는 두 번째 재시드에서 온다: 끊김 판정(statusStaleThresh) 뒤 첫 재시드가 파드를 지우고,
	// reseedCooldown 뒤 둘째가 Terminating ≥ stuckTerminatingForceDeleteThreshold 인 파드의 노드를 읽는다.
	// 여유로 force delete 문턱 한 번을 더 둔다. 설계 하한(force delete 문턱 + Stale 판정)보다 길다.
	StallStopDuration = StatusStaleThresh + CooldownBound + StuckTerminatingBound

	// StandbyAgeFloor 는 S5 대상 standby 의 최소 나이 — 기점 재시드 조건(파드 나이 ≥ staleStandbyReseedTimeout).
	StandbyAgeFloor = StaleBound + StatusPollInterval

	// FaultPause 는 S3·S4 의 정지 길이(설계 45s) — 리스 갱신 시한(10s)과 statusStaleThresh 를 넘긴다.
	FaultPause = 45 * time.Second

	// StandbyFreeze 는 S6·S7 의 standby 정지 길이 — 복제 그래프 끊김이 Stale 판정을 넘겨야 GREEN 이 재구축한다.
	StandbyFreeze = StaleBound + StatusStaleThresh

	// WritableBound 는 J8 — 유발부터 새 primary 쓰기 가능까지의 상한.
	WritableBound = DebounceBound + PromotionExecBudget + 2*StatusPollInterval

	// RejoinBound 는 J9 — 승격부터 생존 standby 가 새 primary 그래프에 붙기까지의 상한.
	RejoinBound = RetargetBound + 2*StatusPollInterval
)
