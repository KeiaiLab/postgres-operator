/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package failover 는 PostgresCluster 의 자동 failover 로직을 담는다 (ROADMAP
// Gate G1 §자동 failover). 본 패키지는 의도적으로 *순수 결정 함수* 중심으로 설계
// 되어 testability + reasoning 을 단순화한다. 클러스터 mutation (promote/rejoin)
// 은 별도 단계 (`promotion.go`, `rejoin.go`) 에서 본 패키지의 Decision 을 입력으로
// 받아 실행한다.
//
// 책임 분리:
//   - detection.go (본 파일): primary 실패 *판정* (mutation 없음, network call 없음).
//   - promotion.go (후속 sub-task): replica 를 primary 로 승격.
//   - rejoin.go (후속 sub-task): 옛 primary 를 replica 로 재합류.
//   - lease.go (후속 sub-task): HA election 분산락 (K8s Lease).
package failover

import (
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// FailureReason 은 primary 실패 *원인* 의 enum. 빈 문자열은 실패 없음.
type FailureReason string

const (
	// ReasonNone — 실패 미감지 (primary 정상).
	ReasonNone FailureReason = ""

	// ReasonNoPrimary — ShardStatus.Primary == nil. instance manager 가 어떤 Pod
	// 도 primary 로 보고하지 않은 상태. 부팅 직후 또는 primary Pod 전원 다운.
	ReasonNoPrimary FailureReason = "NoPrimary"

	// ReasonPrimaryNotReady — Primary 가 존재하지만 readiness false. PostgreSQL
	// 프로세스 응답 불가 / network partition / disk IO error 등.
	ReasonPrimaryNotReady FailureReason = "PrimaryNotReady"

	// ReasonNoEligibleReplica — primary 실패 *감지* 됐으나 승격 가능한 replica
	// 가 없는 상태. failover 진행 자체가 불가능 — manual intervention 필요.
	ReasonNoEligibleReplica FailureReason = "NoEligibleReplica"
)

// Decision 은 primary 실패 판정 결과. Failed=true 일 때 PromotionCandidate 가
// 채워져 있으면 다음 단계 (promotion.go) 가 즉시 사용 가능. nil 이면 manual.
type Decision struct {
	// Failed 는 primary 실패 감지 여부.
	Failed bool

	// Reason 은 실패 원인 enum (ReasonNone 일 때만 Failed=false).
	Reason FailureReason

	// Message 는 사용자/운영자 향 설명 — Condition.Message 에 그대로 인용.
	Message string

	// PromotionCandidate 는 promotion.go 가 승격 대상으로 사용할 replica
	// endpoint. 수신 위치가 가장 앞선 Ready replica 를 선택. Failed=false 또는 후보
	// 없음이면 nil.
	PromotionCandidate *postgresv1alpha1.ShardEndpoint
}

// positionUnknown 은 모르는 수신 위치다 — 아는 위치(≥ 0)보다 늘 뒤다.
const positionUnknown int64 = -1

// DetectPrimaryFailure 는 단일 shard 의 primary 실패 여부를 *순수 함수* 로 판정.
// receive 는 멤버별 수신 위치(bytes)다 — 없거나 음수면 모름.
//
// 정책:
//
//  1. shard.Primary == nil → ReasonNoPrimary, 승격 후보 = 수신 위치가 가장 앞선 Ready replica.
//
//  2. shard.Primary.Ready == false → ReasonPrimaryNotReady, 승격 후보 동일.
//
//  3. 실패 감지됐으나 Ready replica 0 개 → Failed=true, Reason=ReasonNoEligibleReplica,
//     Candidate=nil. promotion.go 가 nil 입력 받으면 알람만 발생 (manual).
//
// 본 함수는 *network call 없음* + *side-effect 없음* — election lease 획득 후
// reconcile 루프가 본 함수를 1회 호출해 입력을 promotion.go 로 전달하는 패턴.
func DetectPrimaryFailure(shard postgresv1alpha1.ShardStatus, receive map[string]int64) Decision {
	if shard.Primary != nil && shard.Primary.Ready {
		return Decision{Reason: ReasonNone}
	}

	var reason FailureReason
	var msg string
	if shard.Primary == nil {
		reason = ReasonNoPrimary
		msg = fmt.Sprintf("shard %q has no primary endpoint reported (instance manager heartbeat absent)", shard.Name)
	} else {
		reason = ReasonPrimaryNotReady
		msg = fmt.Sprintf("shard %q primary pod %q readiness=false", shard.Name, shard.Primary.Pod)
	}

	candidate := SelectPromotionCandidate(shard.Replicas, receive)
	if candidate == nil {
		return Decision{
			Failed:  true,
			Reason:  ReasonNoEligibleReplica,
			Message: msg + " — no eligible replica for promotion (manual intervention required)",
		}
	}

	return Decision{
		Failed:             true,
		Reason:             reason,
		Message:            msg,
		PromotionCandidate: candidate,
	}
}

// SelectPromotionCandidate 는 Ready replica 중 *수신 위치가 가장 앞선* 것을 고른다
// (설계 2026-09-25 §3.3). 동률이면 lag 작은 쪽, 그다음 Pod 이름 사전순(재시도 시 일관성).
// 위치를 모르는 replica 는 아는 replica 가 있으면 밀린다 — 모두 모르면 lag → 이름 순이다.
//
// Why: 끊긴 standby 는 receive == replay 라 lag 가 0 이다. lag 최소를 고르면 가장 낡은
// standby 가 1순위가 된다(2026-09-14 사고). 수신 위치는 멤버끼리 직접 비교된다.
func SelectPromotionCandidate(replicas []postgresv1alpha1.ShardEndpoint, receive map[string]int64) *postgresv1alpha1.ShardEndpoint {
	var best *postgresv1alpha1.ShardEndpoint
	for i := range replicas {
		r := &replicas[i]
		if !r.Ready {
			continue
		}
		if best == nil || promotesBefore(r, best, receive) {
			best = r
		}
	}
	return best
}

// promotesBefore 는 a 가 b 보다 앞선 후보인지다 — 수신 위치 → lag → 이름.
func promotesBefore(a, b *postgresv1alpha1.ShardEndpoint, receive map[string]int64) bool {
	posA, posB := receivePosition(receive, a.Pod), receivePosition(receive, b.Pod)
	if posA != posB {
		return posA > posB
	}
	if a.LagBytes != b.LagBytes {
		return a.LagBytes < b.LagBytes
	}
	return a.Pod < b.Pod
}

// receivePosition 은 pod 의 수신 위치다 — 없거나 음수면 positionUnknown.
func receivePosition(receive map[string]int64, pod string) int64 {
	pos, ok := receive[pod]
	if !ok || pos < 0 {
		return positionUnknown
	}
	return pos
}
