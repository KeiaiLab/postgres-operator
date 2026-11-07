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

	// SwitchoverRestartWait ≥ switchoverRestartWait(switchover.go, = statusStaleThresh).
	SwitchoverRestartWait = StatusStaleThresh

	// SwitchoverBudget ≥ switchoverBudget = switchoverRestartWait + promotionExecBudget + 2 × statusPollInterval(부록 A 100 s).
	SwitchoverBudget = SwitchoverRestartWait + PromotionExecBudget + 2*StatusPollInterval

	// MemberConvergeBudget ≥ memberConvergeBudget(shard_rollout.go, = staleStandbyReseedTimeout) — 롤아웃 막힘 문턱.
	MemberConvergeBudget = StaleBound
)

// 쓰기 탐침(설계 부록 A · B-3) — testdata/write-probe.yaml 과 운영 L1 이 같은 값을 쓴다.
const (
	// WriteProbeInterval = statusPollInterval — 탐침 INSERT 간격.
	WriteProbeInterval = StatusPollInterval

	// WriteGapBudget = statusStaleThresh = PostgresRWEndpointsZero for — primary 단계 쓰기 공백 상한.
	WriteGapBudget = StatusStaleThresh

	// ReplicaGapBudget 은 replica 단계(primary 무관) 쓰기 공백 상한(부록 B-3).
	ReplicaGapBudget = 10 * time.Second

	// WriteProbeWarmup = 2 × writeGapBudget ÷ writeProbeInterval — 유발 전 연속 성공 수.
	WriteProbeWarmup = int(2 * WriteGapBudget / WriteProbeInterval)
)

// kind 가 k8s 기본값을 쓰는 자리.
const (
	// NodeMonitorGrace 는 --node-monitor-grace-period 기본(1.32+) — 노드 사망이 PodReady False 가 되기까지.
	NodeMonitorGrace = 50 * time.Second

	// OperatorGrace 는 오퍼레이터 파드 terminationGracePeriodSeconds(차트 미설정 = k8s 기본).
	OperatorGrace = 30 * time.Second
)

// WalSenderTimeout 은 PostgreSQL 기본 wal_sender_timeout — 렌더 conf 가 정하지 않는다(kind pg_settings source=default).
// 얼린 standby 의 walsender 는 이만큼 지나야 pg_stat_replication 에서 빠진다.
const WalSenderTimeout = 60 * time.Second

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
	// 끊김은 walsender 가 WalSenderTimeout 뒤 빠져야 시작되고, 정지(파드 삭제)는 hold 전파 · 재큐 뒤다.
	StandbyFreeze = WalSenderTimeout + StaleBound + StatusStaleThresh + 2*StatusPollInterval

	// PromotionBound 는 J8 — PodReady False 부터 승격까지 = statusStaleThresh + failoverDebounceThreshold + 2 × statusPollInterval.
	PromotionBound = StatusStaleThresh + DebounceBound + 2*StatusPollInterval

	// RejoinBound 는 J9 — 승격부터 생존 standby 가 새 primary 그래프에 붙기까지의 상한.
	RejoinBound = RetargetBound + 2*StatusPollInterval

	// DetachLead 는 S6b — standby 를 끊고 primary 를 지우기까지(설계 60 s = 2 × memberRetargetAfter).
	DetachLead = 2 * RetargetBound

	// NodeDeathBound 는 노드 사망에서 승격까지의 예상(설계 §3.3 = node-monitor-grace + 디바운스 + statusPollInterval).
	NodeDeathBound = NodeMonitorGrace + DebounceBound + StatusPollInterval

	// NodeDeathHold 는 S9 — primary 워커를 세워 두는 시간. 승격 exec 까지 노드가 죽어 있게 NodeDeathBound 를 넘긴다.
	NodeDeathHold = NodeDeathBound + PromotionExecBudget

	// RolloutBlockHold 는 S8 — 새 replica 를 세워 두는 시간. RolloutBlocked 가 설 만큼 memberConvergeBudget 을 넘긴다.
	RolloutBlockHold = MemberConvergeBudget + 2*StatusPollInterval

	// SwitchoverPoll 은 S0·S8·S10 의 switchover 관측 간격 = pgPromotePoll(1 s) — Stopping·복귀를 놓치지 않는다.
	SwitchoverPoll = time.Second
)

// J10 쓰기 공백 상한 — 시나리오의 승격 종류마다(감지 + 승격 + 라벨·Endpoints + 탐침 간격).
const (
	// FailoverGapBound 는 파드·프로세스 사망 failover — 감지(statusStaleThresh) + PromotionBound + 2 × statusPollInterval + 탐침 간격.
	FailoverGapBound = StatusStaleThresh + PromotionBound + 2*StatusPollInterval + WriteProbeInterval

	// NodeDeathGapBound 는 노드 사망 failover — 감지가 node-monitor-grace 다.
	NodeDeathGapBound = NodeMonitorGrace + PromotionBound + 2*StatusPollInterval + WriteProbeInterval

	// EmergencyGapBound 는 S10 — Stopping 관측 + hold(파드 grace 두 번) + recover(switchoverBudget) + 탐침 간격.
	EmergencyGapBound = StatusPollInterval + 2*OperatorGrace + SwitchoverBudget + WriteProbeInterval
)

// RolloutBound 는 단계화 롤아웃 창(부록 A rolloutBound) = rolloutNominal + snapshotReadyTimeout + staleStandbyReseedTimeout,
// rolloutNominal = members × memberConvergeBudget + switchoverBudget.
func RolloutBound(members int) time.Duration {
	return time.Duration(members)*MemberConvergeBudget + SwitchoverBudget + SnapshotReadyBound + StaleBound
}
