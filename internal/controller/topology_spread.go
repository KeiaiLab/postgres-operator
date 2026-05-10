/*
Copyright 2026 Keiailab.
*/

package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultedTopologySpread — Spec.Shards.TopologySpreadConstraints 가 비어있고
// replicas (primary 외 추가 복제본 수) >= 1 (= 총 pod 2 이상) 시 zone + node 2-축
// spread 자동 주입. valkey-operator PR #48 패턴 이식 — HA defaults out-of-box.
//
// MaxSkew=1, ScheduleAnyway (강제 unschedulable 회피).
//
// 사용자가 user-provided TSC 명시 시 그대로 우선 (override).
//
// replicas 가 0 (HA 미구성, 개발용) 시 미주입 — pod 1개 spread 의미 없음.
func defaultedTopologySpread(
	user []corev1.TopologySpreadConstraint,
	replicas int32,
	selector map[string]string,
) []corev1.TopologySpreadConstraint {
	if len(user) > 0 {
		return user
	}
	if replicas < 1 {
		return nil
	}
	labelSelector := &metav1.LabelSelector{MatchLabels: selector}
	return []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector:     labelSelector,
		},
		{
			MaxSkew:           1,
			TopologyKey:       "kubernetes.io/hostname",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector:     labelSelector,
		},
	}
}
