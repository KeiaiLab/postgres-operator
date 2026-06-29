/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VindexType은 ShardRange 라우팅 함수의 종류이다 (RFC 0002 §3.2).
// +kubebuilder:validation:Enum=hash;range;consistent-hash;lookup
type VindexType string

const (
	// VindexTypeHash은 function(column) % 2^32 결과를 ranges 에 매칭한다.
	VindexTypeHash VindexType = "hash"
	// VindexTypeRange은 정렬 가능한 컬럼 값 자체를 ranges 에 매칭한다.
	VindexTypeRange VindexType = "range"
	// VindexTypeConsistentHash은 가상 노드(virtualNodes)를 통한 일관 해싱 링이다.
	VindexTypeConsistentHash VindexType = "consistent-hash"
	// VindexTypeLookup은 외부 매핑 테이블(ShardLookup, P3+)을 참조한다.
	VindexTypeLookup VindexType = "lookup"
)

// VindexHashFunction은 hash / consistent-hash 타입에서 사용하는 해시 함수이다.
// +kubebuilder:validation:Enum=murmur3;fnv;crc32
type VindexHashFunction string

const (
	VindexHashMurmur3 VindexHashFunction = "murmur3"
	VindexHashFNV     VindexHashFunction = "fnv"
	VindexHashCRC32   VindexHashFunction = "crc32"
)

// VindexSpec은 vindex (가상 인덱스) 정의이다 (RFC 0002 §3.1).
type VindexSpec struct {
	// Type은 vindex 종류이다 (RFC 0002 §3.2).
	// +kubebuilder:validation:Required
	Type VindexType `json:"type"`

	// Column은 해시 / range 계산에 사용하는 컬럼명이다. lookup 타입에서는 생략 가능하다.
	// +optional
	Column string `json:"column,omitempty"`

	// Function은 해시 함수이다. type=hash 또는 type=consistent-hash 일 때 필요.
	// +optional
	Function VindexHashFunction `json:"function,omitempty"`

	// VirtualNodes은 consistent-hash 타입에서 사용하는 가상 노드 수이다.
	// +kubebuilder:validation:Minimum=64
	// +kubebuilder:validation:Maximum=65536
	// +optional
	VirtualNodes int32 `json:"virtualNodes,omitempty"`

	// LookupRef은 lookup 타입에서 사용하는 외부 매핑 CRD 참조이다.
	// +optional
	LookupRef *corev1.LocalObjectReference `json:"lookupRef,omitempty"`
}

// ShardRangeEntry는 [lo, hi] 키 범위와 대상 샤드의 매핑 1건이다.
type ShardRangeEntry struct {
	// Lo는 범위 하한이다. hash vindex 의 경우 16진수 문자열, range 의 경우 임의 정렬가능 값이다.
	// +kubebuilder:validation:Required
	Lo string `json:"lo"`

	// Hi는 범위 상한이다.
	// +kubebuilder:validation:Required
	Hi string `json:"hi"`

	// Shard는 PostgresCluster.status.shards[].name 과 일치하는 대상 샤드 이름이다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Shard string `json:"shard"`
}

// ShardRangeSpec은 ShardRange CRD 의 사용자 의도이다 (RFC 0002 §3.1).
// +kubebuilder:validation:XValidation:rule="self.vindex.type != 'hash' || (has(self.vindex.column) && has(self.vindex.function))",message="hash vindex requires column + function"
// +kubebuilder:validation:XValidation:rule="self.vindex.type != 'lookup' || has(self.vindex.lookupRef)",message="lookup vindex requires lookupRef"
type ShardRangeSpec struct {
	// Cluster은 본 ShardRange 가 속한 PostgresCluster 의 이름이다 (동일 네임스페이스).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// Keyspace은 논리적 파티션 단위 (분산 테이블 그룹) 이름이다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]{0,62}$`
	Keyspace string `json:"keyspace"`

	// Vindex은 키 → 샤드 매핑 함수 정의이다.
	// +kubebuilder:validation:Required
	Vindex VindexSpec `json:"vindex"`

	// Ranges은 키 범위 → 샤드 매핑 목록이다. reconciler 가 overlap / gap 검증한다.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1024
	Ranges []ShardRangeEntry `json:"ranges"`

	// ReferenceTables은 이 키스페이스에서 *모든 샤드에 복제*되는 reference 테이블 목록이다.
	// 이 테이블만 참조하는 쿼리는 샤딩 키 없이 임의 샤드로 라우팅된다 (분산 조인 우회).
	// +optional
	ReferenceTables []string `json:"referenceTables,omitempty"`

	// WriteBlocked은 true 면 라우터가 이 키스페이스로의 *쓰기*를 일시 거부한다(읽기는 통과).
	// online resharding 의 Cutover 동안 라우팅 전환 중 쓰기 유실을 막는 write-block 신호로
	// ShardSplitJob 컨트롤러가 설정/해제한다(Cutover=true, RoutingUpdate 완료 시 false).
	// +optional
	WriteBlocked bool `json:"writeBlocked,omitempty"`
}

// ShardRangeStatus은 reconciler 가 관찰한 ShardRange 상태이다.
type ShardRangeStatus struct {
	// Generation은 spec 변경 시마다 +1 되는 단조 증가 카운터이다. 라우터 캐시 무효화 신호.
	// +optional
	Generation int64 `json:"generation,omitempty"`

	// ObservedGeneration은 reconciler 가 마지막으로 처리한 metadata.generation 이다.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// TotalRanges은 spec.ranges 항목 수이다.
	// +optional
	TotalRanges int32 `json:"totalRanges,omitempty"`

	// RangesByShard은 샤드별 range 항목 수의 분포이다.
	// +optional
	RangesByShard map[string]int32 `json:"rangesByShard,omitempty"`

	// Conditions은 표준 Kubernetes condition 집합이다 (Valid, ShardsExist 등).
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=shr,categories=postgres;sharding;all
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.cluster`
// +kubebuilder:printcolumn:name="Keyspace",type=string,JSONPath=`.spec.keyspace`
// +kubebuilder:printcolumn:name="Vindex",type=string,JSONPath=`.spec.vindex.type`
// +kubebuilder:printcolumn:name="Ranges",type=integer,JSONPath=`.status.totalRanges`
// +kubebuilder:printcolumn:name="Generation",type=integer,JSONPath=`.status.generation`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ShardRange은 분산 라우팅을 위한 키스페이스 + 키범위 → 샤드 매핑의 SSOT 이다 (RFC 0002).
type ShardRange struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ShardRangeSpec   `json:"spec,omitempty"`
	Status ShardRangeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ShardRangeList은 ShardRange 의 컬렉션이다.
type ShardRangeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ShardRange `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ShardRange{}, &ShardRangeList{})
}
