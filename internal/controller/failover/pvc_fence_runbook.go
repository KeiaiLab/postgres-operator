/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package failover

import (
	"fmt"
	"time"
)

// PVC fencing runbook decision logic (D.1.1 / ROADMAP G1 L76).
//
// PVC fencing 은 split-brain 시 *데이터 영역* 차원에서 두 번째 primary 의
// 쓰기를 차단하기 위한 절차이다. operator 가 control-plane 차원에서
// promotion 을 단일화해도 (operator-driven only, T30), volume-attach 다중화
// 가능성 (storage class 의 RWO 보장 실패 / multi-attach allowed / CSI
// driver 버그) 이 잠재 존재. 본 파일은 *순수 결정 로직* — 실제 K8s API
// 호출은 별 controller 에서 본 함수 결과를 받아 수행한다 (§2 Simplicity).

// PVCFenceReason 은 fencing 을 trigger 하는 원인 분류이다.
type PVCFenceReason string

const (
	// PVCFenceReasonSplitBrain 은 두 개 이상의 Pod 가 primary instance-role 을
	// 동시에 주장하는 경우이다. operator-driven only 보장이 깨졌다는 신호.
	PVCFenceReasonSplitBrain PVCFenceReason = "SplitBrain"
	// PVCFenceReasonStaleLease 는 lease holder identity 와 실 primary Pod 가
	// 불일치하며, 양쪽 모두 write 가능 상태인 경우이다.
	PVCFenceReasonStaleLease PVCFenceReason = "StaleLease"
	// PVCFenceReasonMultiAttach 는 CSI controller 가 동일 PVC 의 multi-attach
	// 를 보고하는 경우이다 (storage class 의 RWO 위반 신호).
	PVCFenceReasonMultiAttach PVCFenceReason = "MultiAttach"
	// PVCFenceReasonHealthyOperatorPromotionRace 는 operator restart 직후
	// election 이 settle 되기 전에 이전 primary 의 promotion 잔재가 남은 경우.
	PVCFenceReasonHealthyOperatorPromotionRace PVCFenceReason = "PromotionRace"
)

// PVCFenceDecision 은 단일 PVC 에 대한 fencing 결정이다.
type PVCFenceDecision struct {
	// PVCName 은 대상 PersistentVolumeClaim 이름이다.
	PVCName string
	// PodName 은 본 PVC 를 마운트한 Pod 이다 (관찰 시점).
	PodName string
	// ShouldFence 는 본 PVC 를 fencing 해야 하는지 여부이다.
	ShouldFence bool
	// Reason 은 ShouldFence=true 인 경우 fencing 원인이다.
	Reason PVCFenceReason
	// Detail 은 사람-가독 진단 메시지이다 (이벤트 / Condition 메시지로 노출).
	Detail string
}

// PVCFenceInput 은 DecidePVCFence 의 입력이다. 호출자가 K8s API 에서
// 관찰한 정보를 본 struct 로 fill 후 전달.
type PVCFenceInput struct {
	// PVCName 은 평가 대상 PVC 이다.
	PVCName string
	// MountedPods 는 본 PVC 를 마운트한 Pod 이름 + instance-role 라벨 + ready 여부 의 목록이다.
	MountedPods []PVCFenceMountedPod
	// LeaseHolderIdentity 는 election lease 의 현재 holder identity 이다 (보통 Pod 이름).
	LeaseHolderIdentity string
	// LeaseRenewTime 은 lease 의 renewTime annotation 값이다.
	LeaseRenewTime time.Time
	// LeaseDurationSeconds 는 lease 의 leaseDurationSeconds 이다.
	LeaseDurationSeconds int32
	// Now 는 현재 시각이다 (테스트 결정성 위해 명시 주입).
	Now time.Time
	// CSIControllerReportsMultiAttach 는 CSI driver 가 multi-attach 를 보고하는지 여부.
	CSIControllerReportsMultiAttach bool
}

// PVCFenceMountedPod 는 본 PVC 를 마운트한 Pod 의 관찰값이다.
type PVCFenceMountedPod struct {
	// Name 은 Pod 이름.
	Name string
	// InstanceRole 은 `postgres.keiailab.io/instance-role` 라벨 값. "primary" / "replica" / "" (미설정).
	InstanceRole string
	// Ready 는 readiness probe PASS 여부.
	Ready bool
}

// DecidePVCFence 는 입력 관찰값으로부터 PVC fencing 결정 *목록* 을 계산한다.
//
// 결정 규칙 (우선순위 순):
//
//  1. CSI multi-attach 신호: 즉시 fence (MultiAttach). data-plane 차원의 RWO 위반.
//  2. 다중 primary 관찰: 두 Pod 모두 `instance-role=primary` + ready=true 면
//     lease holder *아닌* 쪽 PVC 를 fence (SplitBrain).
//  3. Lease stale + primary 불일치: lease 가 renewTime + duration 을 초과했고
//     관찰된 primary 가 holder identity 와 다르면 PromotionRace.
//
// 반환된 slice 의 ShouldFence=true 항목만 호출자가 실 fence 동작 (PVC label
// `postgres.keiailab.io/fenced=true` 부착 + Pod evict) 으로 옮긴다.
func DecidePVCFence(in PVCFenceInput) []PVCFenceDecision {
	out := make([]PVCFenceDecision, 0, len(in.MountedPods))

	// 1. CSI multi-attach — data-plane 차원 가장 강한 신호.
	if in.CSIControllerReportsMultiAttach {
		for _, p := range in.MountedPods {
			out = append(out, PVCFenceDecision{
				PVCName:     in.PVCName,
				PodName:     p.Name,
				ShouldFence: true,
				Reason:      PVCFenceReasonMultiAttach,
				Detail: fmt.Sprintf(
					"CSI controller reports multi-attach on PVC %s while pod %s mounts it; storage RWO invariant broken",
					in.PVCName, p.Name),
			})
		}
		return out
	}

	// 2. 다중 primary 관찰.
	primaries := primaryPods(in.MountedPods)
	if len(primaries) >= 2 {
		for _, p := range primaries {
			if p.Name == in.LeaseHolderIdentity {
				out = append(out, PVCFenceDecision{
					PVCName: in.PVCName, PodName: p.Name, ShouldFence: false,
					Reason: PVCFenceReasonSplitBrain,
					Detail: fmt.Sprintf("pod %s is current lease holder; preserved", p.Name),
				})
				continue
			}
			out = append(out, PVCFenceDecision{
				PVCName: in.PVCName, PodName: p.Name, ShouldFence: true,
				Reason: PVCFenceReasonSplitBrain,
				Detail: fmt.Sprintf(
					"pod %s claims instance-role=primary but is not the lease holder %s; fencing",
					p.Name, in.LeaseHolderIdentity),
			})
		}
		return out
	}

	// 3. Lease stale + primary 불일치.
	if leaseStale(in) && len(primaries) == 1 && primaries[0].Name != in.LeaseHolderIdentity {
		p := primaries[0]
		out = append(out, PVCFenceDecision{
			PVCName: in.PVCName, PodName: p.Name, ShouldFence: true,
			Reason: PVCFenceReasonStaleLease,
			Detail: fmt.Sprintf(
				"lease stale (renew=%s, duration=%ds, now=%s) and primary pod %s != holder %s",
				in.LeaseRenewTime.Format(time.RFC3339), in.LeaseDurationSeconds,
				in.Now.Format(time.RFC3339), p.Name, in.LeaseHolderIdentity),
		})
		return out
	}

	// 정상 — fencing 필요 없음. 명시 결정 (관찰 가능한 audit trail).
	for _, p := range in.MountedPods {
		out = append(out, PVCFenceDecision{
			PVCName: in.PVCName, PodName: p.Name, ShouldFence: false,
			Detail: "no fence condition observed",
		})
	}
	return out
}

func primaryPods(pods []PVCFenceMountedPod) []PVCFenceMountedPod {
	var out []PVCFenceMountedPod
	for _, p := range pods {
		if p.InstanceRole == "primary" && p.Ready {
			out = append(out, p)
		}
	}
	return out
}

func leaseStale(in PVCFenceInput) bool {
	if in.LeaseRenewTime.IsZero() || in.LeaseDurationSeconds == 0 {
		return false
	}
	expiry := in.LeaseRenewTime.Add(time.Duration(in.LeaseDurationSeconds) * time.Second)
	return in.Now.After(expiry)
}
