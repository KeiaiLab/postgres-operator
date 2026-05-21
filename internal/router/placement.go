/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package router

import (
	"errors"
	"fmt"
	"sort"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// D.8.8 Manual shard placement + GitOps drift guard (ROADMAP G3 L156+L157).
//
// 본 파일은 *순수 함수* — ShardRange CRD spec (의도) 과 *관찰된* 실 shard
// 배치 상태를 비교하여 (a) 누락된 shard, (b) 미사용 shard, (c) 키범위 갭/
// overlap (vindex 의도와 배치 불일치) 를 detect 한다. reconciler 가 본
// 함수 결과로 Condition 작성 + 이벤트 발행.

// ErrPlacementInvalid 는 ValidatePlacement 의 sentinel.
var ErrPlacementInvalid = errors.New("router: shard placement invalid")

// PlacementSpec 는 사용자 의도된 shard 배치 (PlacementHints) 이다.
type PlacementSpec struct {
	// ShardID 는 RFC-0002 ShardRange.spec.ranges[].shard 와 동일 식별자.
	ShardID ShardID
	// PreferredZone 은 의도된 K8s topology zone (`topology.kubernetes.io/zone`).
	// 빈 문자열이면 별 제약 없음.
	PreferredZone string
	// PreferredNode 는 의도된 Node 이름 (특수 hardware 의도 등). 비권장.
	PreferredNode string
	// Weight 는 다중 shard 분산 시 상대 비중 (rebalancer 입력). 기본 1.
	Weight int32
}

// ObservedShard 는 cluster 가 현재 보유한 shard 의 관찰값이다.
type ObservedShard struct {
	// ShardID 는 동일 식별자.
	ShardID ShardID
	// Zone 은 실제 배치된 zone (Pod nodeAffinity 결과).
	Zone string
	// Node 는 실 배치 노드.
	Node string
	// Ready 는 현재 readiness.
	Ready bool
}

// PlacementDriftReason 은 drift 항목의 원인 분류이다.
type PlacementDriftReason string

const (
	// PlacementDriftMissing — spec 에 정의되었으나 관찰되지 않음.
	PlacementDriftMissing PlacementDriftReason = "Missing"
	// PlacementDriftExtra — 관찰되었으나 spec 에 없음 (gc 누락 신호).
	PlacementDriftExtra PlacementDriftReason = "Extra"
	// PlacementDriftZoneMismatch — preferredZone 과 observed zone 불일치.
	PlacementDriftZoneMismatch PlacementDriftReason = "ZoneMismatch"
	// PlacementDriftNodeMismatch — preferredNode 와 observed node 불일치.
	PlacementDriftNodeMismatch PlacementDriftReason = "NodeMismatch"
	// PlacementDriftNotReady — spec + observed 일치하나 ready=false.
	PlacementDriftNotReady PlacementDriftReason = "NotReady"
	// PlacementDriftRangeUncovered — ShardRange 의 ranges[] 가 참조한 shard 가 spec 에 없음.
	PlacementDriftRangeUncovered PlacementDriftReason = "RangeUncovered"
)

// PlacementDrift 는 단일 drift 항목이다.
type PlacementDrift struct {
	ShardID ShardID
	Reason  PlacementDriftReason
	Detail  string
}

// DetectPlacementDrift 는 spec (placement intent) 과 observed (cluster 실 상태)
// 를 비교하여 drift 목록을 반환한다. 결정적 — 동일 입력 → 동일 출력.
//
// 추가로 ShardRange.spec.ranges[].shard 가 모두 *spec 에 정의된 shard* 인지
// 검증 (RangeUncovered drift). 본 검사는 GitOps drift guard 의 핵심 — sharding
// metadata 와 실 배치가 어긋나면 라우팅 실패 (라우팅된 shard 에 데이터 없음).
func DetectPlacementDrift(
	spec []PlacementSpec,
	observed []ObservedShard,
	ranges []v1alpha1.ShardRangeEntry,
) []PlacementDrift {
	specByID := make(map[ShardID]PlacementSpec, len(spec))
	for _, s := range spec {
		specByID[s.ShardID] = s
	}
	obsByID := make(map[ShardID]ObservedShard, len(observed))
	for _, o := range observed {
		obsByID[o.ShardID] = o
	}

	var out []PlacementDrift

	// Missing: spec 에 있으나 observed 에 없음.
	for _, s := range spec {
		o, ok := obsByID[s.ShardID]
		if !ok {
			out = append(out, PlacementDrift{
				ShardID: s.ShardID, Reason: PlacementDriftMissing,
				Detail: fmt.Sprintf("shard %s in spec but not observed in cluster", s.ShardID),
			})
			continue
		}
		// Zone mismatch.
		if s.PreferredZone != "" && o.Zone != "" && s.PreferredZone != o.Zone {
			out = append(out, PlacementDrift{
				ShardID: s.ShardID, Reason: PlacementDriftZoneMismatch,
				Detail: fmt.Sprintf("preferredZone=%s observed=%s", s.PreferredZone, o.Zone),
			})
		}
		// Node mismatch.
		if s.PreferredNode != "" && o.Node != "" && s.PreferredNode != o.Node {
			out = append(out, PlacementDrift{
				ShardID: s.ShardID, Reason: PlacementDriftNodeMismatch,
				Detail: fmt.Sprintf("preferredNode=%s observed=%s", s.PreferredNode, o.Node),
			})
		}
		// NotReady.
		if !o.Ready {
			out = append(out, PlacementDrift{
				ShardID: s.ShardID, Reason: PlacementDriftNotReady,
				Detail: fmt.Sprintf("shard %s observed but ready=false", s.ShardID),
			})
		}
	}

	// Extra: observed 에 있으나 spec 에 없음.
	for _, o := range observed {
		if _, ok := specByID[o.ShardID]; !ok {
			out = append(out, PlacementDrift{
				ShardID: o.ShardID, Reason: PlacementDriftExtra,
				Detail: fmt.Sprintf("shard %s observed but not in spec (GC candidate)", o.ShardID),
			})
		}
	}

	// RangeUncovered: ShardRange.spec.ranges[].shard 가 spec 에 없음.
	rangeShards := make(map[string]bool, len(ranges))
	for _, r := range ranges {
		rangeShards[r.Shard] = true
	}
	for shard := range rangeShards {
		if _, ok := specByID[ShardID(shard)]; !ok {
			out = append(out, PlacementDrift{
				ShardID: ShardID(shard), Reason: PlacementDriftRangeUncovered,
				Detail: fmt.Sprintf("ShardRange ranges[].shard=%s but not in PlacementSpec", shard),
			})
		}
	}

	// 결정성: ShardID + Reason 기준 정렬.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ShardID != out[j].ShardID {
			return out[i].ShardID < out[j].ShardID
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// ValidatePlacement 는 PlacementSpec 자체의 정합성을 검증한다 (reconciler webhook).
//
// 규칙:
//   - 중복 ShardID 금지
//   - Weight < 0 금지
//   - PreferredNode + PreferredZone 동시 지정 시 일관성만 검증 (실 mapping 은 K8s 가 처리)
func ValidatePlacement(spec []PlacementSpec) error {
	seen := make(map[ShardID]bool, len(spec))
	for _, s := range spec {
		if s.ShardID == "" {
			return fmt.Errorf("%w: empty ShardID", ErrPlacementInvalid)
		}
		if seen[s.ShardID] {
			return fmt.Errorf("%w: duplicate ShardID %s", ErrPlacementInvalid, s.ShardID)
		}
		seen[s.ShardID] = true
		if s.Weight < 0 {
			return fmt.Errorf("%w: shard %s Weight=%d (negative)", ErrPlacementInvalid, s.ShardID, s.Weight)
		}
	}
	return nil
}

// HasDrift 는 결과에 1+ drift 가 있는지 boolean 으로 반환한다 (reconciler 분기용).
func HasDrift(drifts []PlacementDrift) bool { return len(drifts) > 0 }
