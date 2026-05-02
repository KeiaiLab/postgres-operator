/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// 본 파일은 PostgresClusterStatus.Conditions를 다루는 헬퍼들이다.
// 표준 K8s 패턴(metav1.Condition + meta.SetStatusCondition)을 사용한다.
//
// 표준 Condition 타입 (RFC 0001 §3.4 권장 카탈로그):
//   - Ready              : 클러스터 전체가 사용 가능 상태 (모든 shard primary ready + router ready (있다면))
//   - Progressing        : 진행 중인 reconcile 이 있음 (Phase=Provisioning|Reconfiguring 동안 True)
//   - ShardsReady        : 모든 shard 의 primary 가 ready (Replicas 가 0 일 때도 primary 만 보면 됨)
//   - RouterReady        : Spec.Router 가 nil 이거나 Enabled=false 면 NotApplicable, 그 외엔 readyReplicas==Replicas
//   - BackupHealthy      : Spec.Backup 이 nil 이면 NotApplicable, 그 외엔 BackupJob 최근 결과로 판정 (P4 에서 활성화)
//   - AutoSplitEligible  : Spec.AutoSplit 활성 시 split 후보가 있음을 알림 (P5 에서 활성화)
//
// Condition Reason은 본 파일의 상수 집합으로 통일한다. 새 reason 추가는 본
// 파일에 추가하는 것이 단일 출처(SOT) 규약이다.
//
// Reason 카탈로그 (사용 영역):
//   - 일반 lifecycle: Reconciling / Available / Progressing / NotApplicable / ResourcesCreated / VersionRejected
//   - HA / Failover (P2-T3 이후 사용):
//       Promoting     — replica → primary 전환 중
//       Demoting      — primary → replica 강등 중
//       ElectionWon   — election lease holder 획득
//       ElectionLost  — election lease holder 상실(다른 후보로 전환)
//   - 분산 SQL topology (P3+ 활성):
//       TopologyDrift — 분산 메타데이터 ↔ desired 사이 drift 검출
//   - Auth / 인증 (P7 이후 사용):
//       Rotating      — Secret/credential 회전 진행 중

const (
	// Condition types — RFC 0001 §3.4 권장 카탈로그
	ConditionReady             = "Ready"
	ConditionProgressing       = "Progressing"
	ConditionShardsReady       = "ShardsReady"
	ConditionRouterReady       = "RouterReady"
	ConditionBackupHealthy     = "BackupHealthy"
	ConditionAutoSplitEligible = "AutoSplitEligible"

	// Reasons — 일반 lifecycle
	ReasonReconciling      = "Reconciling"
	ReasonResourcesCreated = "ResourcesCreated"
	ReasonVersionRejected  = "VersionRejected"
	ReasonAvailable        = "Available"
	ReasonProgressing      = "Progressing"
	ReasonNotApplicable    = "NotApplicable"

	// Reasons — HA / Failover (P2-T3 이후 활성)
	ReasonPromoting    = "Promoting"
	ReasonDemoting     = "Demoting"
	ReasonElectionWon  = "ElectionWon"
	ReasonElectionLost = "ElectionLost"

	// Reasons — 분산 SQL topology (P3+ 활성)
	ReasonTopologyDrift = "TopologyDrift"

	// Reasons — Auth / 인증 (P7 이후 활성)
	ReasonRotating = "Rotating"
)

// setCondition은 지정된 type/status/reason/message로 Condition을 추가/갱신한다.
// LastTransitionTime은 status가 바뀌었을 때만 갱신된다(meta.SetStatusCondition
// 의 표준 동작).
func setCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}
