/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ShardSplitJob CRD — G4 online resharding 7-step orchestrator (D.9.1).
//
// 7-step workflow (RFC-0002 §online-resharding 정합):
//
//  1. Snapshot + WAL capture — source shard 의 시점 일관 base snapshot 확보
//  2. Bootstrap target shard — 신규 shard StatefulSet 생성 + PG init
//  3. Initial copy — base snapshot 적용 (logical 또는 pg_basebackup)
//  4. CDC catch-up — source 의 변경분을 logical replication 으로 따라잡기
//  5. Cutover — write 차단 최소화 윈도우 + router 라우팅 갱신
//  6. Routing update — ShardRange CRD 의 ranges 갱신 + metadata store sync
//  7. Source cleanup — old shard 의 split-out 키 범위 데이터 회수
//
// 본 CRD 는 *state machine 만 정의* — 실 step 구현은 internal/controller/
// shardsplit/ + internal/router/ 에 위임 (P-D §D.9.* 후속).

// ShardSplitJobPhase 는 resharding state machine 의 현재 phase 이다.
// +kubebuilder:validation:Enum=Pending;SnapshotWAL;Bootstrap;InitialCopy;CDCCatchup;Cutover;RoutingUpdate;Cleanup;Promote;Completed;Failed;Aborted
type ShardSplitJobPhase string

const (
	ShardSplitPhasePending       ShardSplitJobPhase = "Pending"
	ShardSplitPhaseSnapshotWAL   ShardSplitJobPhase = "SnapshotWAL"
	ShardSplitPhaseBootstrap     ShardSplitJobPhase = "Bootstrap"
	ShardSplitPhaseInitialCopy   ShardSplitJobPhase = "InitialCopy"
	ShardSplitPhaseCDCCatchup    ShardSplitJobPhase = "CDCCatchup"
	ShardSplitPhaseCutover       ShardSplitJobPhase = "Cutover"
	ShardSplitPhaseRoutingUpdate ShardSplitJobPhase = "RoutingUpdate"
	ShardSplitPhaseCleanup       ShardSplitJobPhase = "Cleanup"
	ShardSplitPhasePromote       ShardSplitJobPhase = "Promote"
	ShardSplitPhaseCompleted     ShardSplitJobPhase = "Completed"
	ShardSplitPhaseFailed        ShardSplitJobPhase = "Failed"
	ShardSplitPhaseAborted       ShardSplitJobPhase = "Aborted"
)

// ShardSplitDirection 은 split 의 방향 의도이다.
// +kubebuilder:validation:Enum=split;merge
type ShardSplitDirection string

const (
	// ShardSplitDirectionSplit — 1 shard 의 키 범위를 N 으로 분할.
	ShardSplitDirectionSplit ShardSplitDirection = "split"
	// ShardSplitDirectionMerge — N shard 의 키 범위를 1 로 병합.
	ShardSplitDirectionMerge ShardSplitDirection = "merge"
)

// ShardSplitJobSpec 는 사용자 의도된 shard split/merge 작업이다.
// +kubebuilder:validation:XValidation:rule="size(self.sources) > 0",message="sources must not be empty"
// +kubebuilder:validation:XValidation:rule="size(self.targets) > 0",message="targets must not be empty"
type ShardSplitJobSpec struct {
	// Cluster 는 본 작업이 속한 PostgresCluster 의 이름 (동일 namespace).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// Keyspace 는 ShardRange 의 keyspace 식별자.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{0,62}$`
	Keyspace string `json:"keyspace"`

	// Direction 은 split 또는 merge 방향. 기본 split.
	// +kubebuilder:default=split
	// +optional
	Direction ShardSplitDirection `json:"direction,omitempty"`

	// Sources 는 source shard ID 목록 (split: 1, merge: N).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Sources []string `json:"sources"`

	// Targets 는 target shard 정의 목록 (split: N, merge: 1).
	// 각 target 은 자체 키 범위와 placement hint 를 갖는다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Targets []ShardSplitTarget `json:"targets"`

	// CutoverWindow 는 cutover phase 의 최대 write-block 시간이다 (예: "30s").
	// 초과 시 자동 abort + rollback. 기본 60s.
	// +kubebuilder:default="60s"
	// +optional
	CutoverWindow metav1.Duration `json:"cutoverWindow,omitempty"`

	// CDCMaxLag 은 CDC catch-up phase 에서 cutover 진입 허용 LSN 차이 (bytes).
	// 기본 16MB.
	// +kubebuilder:default=16777216
	// +optional
	CDCMaxLag int64 `json:"cdcMaxLag,omitempty"`

	// AllowForwardOnly 는 true 면 cutover 이후 rollback 불가 (D.9.10).
	// 기본 false — rollback 가능 (역방향 logical replication 유지).
	// +kubebuilder:default=false
	// +optional
	AllowForwardOnly bool `json:"allowForwardOnly,omitempty"`

	// Online 은 true 면 CDC(논리복제) 기반 *무중단* 이동을 쓴다 — InitialCopy 의 정적 bulk
	// 복사 대신 CDCCatchup phase 가 subscription(copy_data=true)으로 라이브 쓰기를 따라잡고,
	// write-block 은 최종 drain 구간만 짧게 건다. false(기본)는 offline 모드(InitialCopy
	// 범위복사 + cutover write-block) — 동시 쓰기 없는 유지보수 창에 단순·빠름.
	// +kubebuilder:default=false
	// +optional
	Online bool `json:"online,omitempty"`
}

// ShardSplitTarget 는 split/merge 의 target shard 1건 정의.
type ShardSplitTarget struct {
	// ShardID 는 target shard 의 식별자 (ShardRange.spec.ranges[].shard 와 동일).
	//
	// reconciler 가 shardID 를 K8s 자원명(`<cluster>-rsd-<shardID>`, ADR-0027)에
	// 직접 박으므로 *DNS-1123 label-safe* 여야 한다 — 소문자 영숫자 + 하이픈,
	// 영문자 시작, 영숫자 종료(선행/후행 하이픈 금지). 형제 필드(Keyspace /
	// ShardRangeEntry.Shard)의 패턴은 언더스코어를 허용해 DNS 에 무효이므로 본
	// 필드는 더 엄격한 패턴을 쓴다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=30
	ShardID string `json:"shardID"`

	// Ranges 는 본 target 이 가질 키 범위 목록. ShardRange.spec.ranges 와 동일 형식.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Ranges []ShardRangeEntry `json:"ranges"`

	// Placement 는 target shard 의 nodeAffinity / topology hint.
	// +optional
	Placement *ShardSplitPlacement `json:"placement,omitempty"`
}

// ShardSplitPlacement 는 target shard 의 K8s scheduling hint.
type ShardSplitPlacement struct {
	// PreferredZone 은 의도된 topology zone.
	// +optional
	PreferredZone string `json:"preferredZone,omitempty"`
	// PreferredNode 는 의도된 노드 이름 (특수 hardware).
	// +optional
	PreferredNode string `json:"preferredNode,omitempty"`
}

// ShardSplitJobStatus 는 reconciler 가 관찰한 7-step state machine 상태.
type ShardSplitJobStatus struct {
	// Phase 는 현재 phase (state machine).
	// +optional
	Phase ShardSplitJobPhase `json:"phase,omitempty"`

	// ObservedGeneration 은 마지막으로 처리한 metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// StartedAt 은 본 작업이 Pending → SnapshotWAL 으로 진입한 시각.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt 은 Completed / Failed / Aborted 진입 시각.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// CurrentLagBytes 는 CDCCatchup phase 동안의 lag (bytes).
	// +optional
	CurrentLagBytes int64 `json:"currentLagBytes,omitempty"`

	// CutoverStartedAt 은 Cutover phase 진입 시각 (window 측정 시작).
	// +optional
	CutoverStartedAt *metav1.Time `json:"cutoverStartedAt,omitempty"`

	// SnapshotLSN 은 SnapshotWAL phase 에서 확정된 source 시점 LSN.
	// +optional
	SnapshotLSN string `json:"snapshotLSN,omitempty"`

	// FailureReason 은 Failed phase 의 원인.
	// +optional
	FailureReason string `json:"failureReason,omitempty"`

	// Conditions 는 표준 K8s condition 집합 (StepCompleted, RollbackPossible, etc).
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ssj,categories=postgres;sharding;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster`
// +kubebuilder:printcolumn:name="Keyspace",type=string,JSONPath=`.spec.keyspace`
// +kubebuilder:printcolumn:name="Direction",type=string,JSONPath=`.spec.direction`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Lag",type=integer,JSONPath=`.status.currentLagBytes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ShardSplitJob 은 G4 online resharding 의 7-step orchestrator CRD 이다 (RFC-0002).
type ShardSplitJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ShardSplitJobSpec   `json:"spec,omitempty"`
	Status ShardSplitJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ShardSplitJobList 는 ShardSplitJob 의 컬렉션이다.
type ShardSplitJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ShardSplitJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ShardSplitJob{}, &ShardSplitJobList{})
}
