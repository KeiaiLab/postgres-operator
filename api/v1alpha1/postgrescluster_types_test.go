/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestShardingMode 는 ShardingMode 타입의 상수 값과 필드 round-trip 을 검증한다.
// RFC 0001 §3.1, RFC 0002 §shardrange-crd 분기 정합.
//
// CRD enum validation 은 kubebuilder marker (+kubebuilder:validation:Enum=none;native)
// 으로 apiserver 단계에서 강제되므로 본 unit test 는 Go-level 식별자 + 기본값 + round-trip
// 만 검사한다 (§3 Surgical — 신규 validation 로직 도입 금지).
func TestShardingMode(t *testing.T) {
	t.Run("상수 값이 RFC 0001 §3.1 와 일치한다", func(t *testing.T) {
		if ShardingModeNone != "none" {
			t.Errorf("ShardingModeNone = %q, want %q", ShardingModeNone, "none")
		}
		if ShardingModeNative != "native" {
			t.Errorf("ShardingModeNative = %q, want %q", ShardingModeNative, "native")
		}
	})

	t.Run("PostgresClusterSpec round-trip 에서 ShardingMode 가 보존된다", func(t *testing.T) {
		cases := []struct {
			name string
			mode ShardingMode
		}{
			{"기본 none", ShardingModeNone},
			{"native 모드", ShardingModeNative},
			{"빈 값 (apiserver default 적용 전)", ShardingMode("")},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				spec := PostgresClusterSpec{ShardingMode: tc.mode}
				if spec.ShardingMode != tc.mode {
					t.Errorf("round-trip 실패: got %q, want %q", spec.ShardingMode, tc.mode)
				}
			})
		}
	})

	t.Run("ShardingMode 는 string underlying 으로 비교 가능하다", func(t *testing.T) {
		// reconciler / webhook 분기에서 직접 비교가 사용된다 (RFC 0001 §3.1).
		var m ShardingMode = "native"
		if m != ShardingModeNative {
			t.Errorf("string 비교 실패: %q != %q", m, ShardingModeNative)
		}
	})
}

// TestShardsSpec 는 ShardsSpec 필드 round-trip + deepcopy 동작을 검증한다.
// RFC 0001 §3.1 shard topology 정합.
func TestShardsSpec(t *testing.T) {
	t.Run("필드 round-trip", func(t *testing.T) {
		spec := ShardsSpec{
			InitialCount: 4,
			Replicas:     2,
			Storage: StorageSpec{
				StorageClass: "rook-ceph-block",
				Size:         resource.MustParse("10Gi"),
			},
			PriorityClassName: "postgres-shard-critical",
		}
		if spec.InitialCount != 4 {
			t.Errorf("InitialCount round-trip 실패: %d", spec.InitialCount)
		}
		if spec.Replicas != 2 {
			t.Errorf("Replicas round-trip 실패: %d", spec.Replicas)
		}
		if spec.Storage.StorageClass != "rook-ceph-block" {
			t.Errorf("Storage.StorageClass round-trip 실패: %q", spec.Storage.StorageClass)
		}
		if spec.PriorityClassName != "postgres-shard-critical" {
			t.Errorf("PriorityClassName round-trip 실패: %q", spec.PriorityClassName)
		}
	})

	t.Run("DeepCopy 가 독립 복제본을 만든다", func(t *testing.T) {
		original := ShardsSpec{
			InitialCount: 3,
			Replicas:     1,
			Storage: StorageSpec{
				StorageClass: "ceph",
				Size:         resource.MustParse("5Gi"),
			},
			Tolerations: []corev1.Toleration{
				{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "postgres"},
			},
		}
		clone := original.DeepCopy()
		if clone == nil {
			t.Fatal("DeepCopy returned nil")
		}
		if clone.InitialCount != original.InitialCount {
			t.Errorf("DeepCopy InitialCount 불일치: %d vs %d", clone.InitialCount, original.InitialCount)
		}
		// 슬라이스 독립성 — 원본 수정이 clone 에 영향 주지 않는다.
		original.Tolerations[0].Value = "mutated"
		if clone.Tolerations[0].Value == "mutated" {
			t.Error("DeepCopy Tolerations 슬라이스가 공유됨 (독립 복제 실패)")
		}
	})

	t.Run("Replicas=0 (HA 없음, dev only) 도 허용된다", func(t *testing.T) {
		// RFC 0001 §3.1: Replicas=0 은 schema 상 합법 (Minimum=0).
		spec := ShardsSpec{InitialCount: 1, Replicas: 0}
		if spec.Replicas != 0 {
			t.Errorf("Replicas=0 round-trip 실패: %d", spec.Replicas)
		}
	})
}
