/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestFallbackPrimaryReady_StandbyReadyMasksOutage 는 라이브 RCA (2026-06-04,
// pg-ha-drill cordon chaos) 를 회귀 가드한다. primary Pod 가 죽고 standby 만
// Ready 인 HA shard 에서, status fallback 이 STS readyReplicas>=1 (standby 가
// 그 1) 을 primary readiness 로 오인해 합성 primary 를 Ready=true 로 마킹 →
// DetectPrimaryFailure 가 ReasonNone 을 반환 → 자동 failover 가 영영 발동하지
// 않았다. fallback 은 Ready replica 가 관측되면 primary 부재를 outage 로
// 간주하고 Ready=false 를 반환해야 한다.
func TestFallbackPrimaryReady_StandbyReadyMasksOutage(t *testing.T) {
	readyStandby := []postgresv1alpha1.ShardEndpoint{
		{Pod: "ha-shard-0-0", Ready: false}, // 죽은 primary slot (annotation 부재)
		{Pod: "ha-shard-0-1", Ready: true},  // 살아있는 standby
	}
	// STS readyReplicas>=1 (standby 가 Ready) → 옛 코드의 proxy 는 true.
	if got := fallbackPrimaryReady(true, readyStandby); got != false {
		t.Fatalf("standby-only-ready HA shard 의 fallback primary 는 Ready=false 여야 한다 (failover 발동); got %v", got)
	}
}

// TestFallbackPrimaryReady_EarlyBootKeepsStsProxy 는 단일 primary 부팅 중
// annotation 미수집 구간을 회귀 가드한다. 이 구간엔 Ready replica 가 아직
// 없으므로 STS proxy (readyReplicas) 를 그대로 신뢰해야 클러스터가 정상적으로
// Ready 에 도달한다 (기존 envtest SingleShardNoRouter 시나리오 보존).
func TestFallbackPrimaryReady_EarlyBootKeepsStsProxy(t *testing.T) {
	noReadyReplica := []postgresv1alpha1.ShardEndpoint{
		{Pod: "demo-shard-0-0", Ready: false},
	}
	if got := fallbackPrimaryReady(true, noReadyReplica); got != true {
		t.Fatalf("early-boot (Ready replica 0) 은 STS proxy 를 신뢰해 Ready=true 여야 한다; got %v", got)
	}
	if got := fallbackPrimaryReady(false, noReadyReplica); got != false {
		t.Fatalf("STS proxy=false 면 fallback 도 false; got %v", got)
	}
	if got := fallbackPrimaryReady(true, nil); got != true {
		t.Fatalf("replica 목록이 nil 이면 early-boot 로 보고 STS proxy 신뢰; got %v", got)
	}
}

// TestHasReadyReplica 는 순수 헬퍼를 직접 가드한다.
func TestHasReadyReplica(t *testing.T) {
	if hasReadyReplica(nil) {
		t.Error("nil → false")
	}
	if hasReadyReplica([]postgresv1alpha1.ShardEndpoint{{Ready: false}, {Ready: false}}) {
		t.Error("all not-ready → false")
	}
	if !hasReadyReplica([]postgresv1alpha1.ShardEndpoint{{Ready: false}, {Ready: true}}) {
		t.Error("any ready → true")
	}
}
