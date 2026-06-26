/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — vindex_consistent.go 는 *consistent-hash* vindex 를 구현한다.
//
// 일반 해시(`function(key) % 2^32` → [Lo,Hi] 범위)는 샤드를 추가/제거하면 키 대부분이
// 다른 샤드로 옮겨간다(대규모 데이터 이동). consistent-hash 는 각 샤드를 해시 *링* 위의
// VirtualNodes 개 가상 노드로 흩뿌리고, 키를 시계방향으로 가장 가까운 가상 노드의
// 샤드로 보낸다 — 샤드 1개 추가 시 *약 1/N 의 키만* 이동한다(리밸런싱 친화적, Citus/
// Vitess 와 동일 계열).
//
// 본 구현은 ShardRangeSpec.Ranges 의 *샤드 집합* + Vindex.Function(murmur3/fnv/crc32)
// + Vindex.VirtualNodes 만 사용한다 (Lo/Hi 범위는 consistent-hash 에서 무의미).
package router

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ringCache 는 fingerprint(function|vnodes|정렬된 shard 집합) → *ConsistentHashRing.
// ResolveShard 가 매 호출 링을 재구성하던 핫패스 낭비를 제거한다. 링은 불변(읽기 전용
// Lookup)이라 동시성 안전. 토폴로지가 바뀌면 fingerprint 가 달라져 새 링이 만들어진다
// (옛 엔트리는 누수되나 토폴로지 종류가 적어 무시 가능).
var ringCache sync.Map

// ringFingerprint 는 링 구성에 영향을 주는 필드(해시 함수 + 가상 노드 수 + distinct
// shard 집합)로 캐시 키를 만든다.
func ringFingerprint(spec v1alpha1.ShardRangeSpec) string {
	seen := make(map[string]bool)
	var shards []string
	for _, r := range spec.Ranges {
		if r.Shard != "" && !seen[r.Shard] {
			seen[r.Shard] = true
			shards = append(shards, r.Shard)
		}
	}
	sort.Strings(shards)
	return string(spec.Vindex.Function) + "|" + strconv.Itoa(int(spec.Vindex.VirtualNodes)) + "|" + strings.Join(shards, ",")
}

// defaultVirtualNodes 는 VirtualNodes 미지정(0) 시 가상 노드 수. 클수록 분포가 고르나
// 링 구성 비용 증가. CRD 는 64~65536 을 강제하므로 0 은 "미설정" 의미.
const defaultVirtualNodes = 128

// chPoint 는 해시 링의 한 점(가상 노드)이다.
type chPoint struct {
	hash  uint32
	shard string
}

// ConsistentHashRing 은 샤드 집합으로 구성한 해시 링이다. 동일 spec 에서 결정적.
// 라우터는 토폴로지 로드 시 1회 구성해 캐시할 수 있다 (ResolveShard 는 매 호출 구성 —
// 캐싱은 호출자 최적화).
type ConsistentHashRing struct {
	points []chPoint // hash 오름차순 정렬
}

// NewConsistentHashRing 은 spec 의 샤드 + VirtualNodes + 해시 함수로 링을 구성한다.
func NewConsistentHashRing(spec v1alpha1.ShardRangeSpec) (*ConsistentHashRing, error) {
	vnodes := int(spec.Vindex.VirtualNodes)
	if vnodes <= 0 {
		vnodes = defaultVirtualNodes
	}
	// Ranges 에서 등장 순서대로 distinct shard 수집.
	seen := make(map[string]bool)
	var shards []string
	for _, r := range spec.Ranges {
		if r.Shard != "" && !seen[r.Shard] {
			seen[r.Shard] = true
			shards = append(shards, r.Shard)
		}
	}
	if len(shards) == 0 {
		return nil, fmt.Errorf("%w: consistent-hash has no shards", ErrVindexNoMatch)
	}
	points := make([]chPoint, 0, len(shards)*vnodes)
	for _, s := range shards {
		for v := 0; v < vnodes; v++ {
			h, err := hashKey(spec.Vindex.Function, s+":"+strconv.Itoa(v))
			if err != nil {
				return nil, err
			}
			points = append(points, chPoint{hash: h, shard: s})
		}
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].hash != points[j].hash {
			return points[i].hash < points[j].hash
		}
		return points[i].shard < points[j].shard // 충돌 시 결정성
	})
	return &ConsistentHashRing{points: points}, nil
}

// Lookup 은 key 해시 h 에 대해 시계방향으로 가장 가까운 가상 노드의 shard 를 반환한다.
func (r *ConsistentHashRing) Lookup(h uint32) string {
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i].hash >= h })
	if i == len(r.points) {
		i = 0 // 링 wrap-around.
	}
	return r.points[i].shard
}

// resolveConsistentHash 는 ResolveShard 의 consistent-hash 분기다. 링은 fingerprint
// 로 캐시되어 재사용된다.
func resolveConsistentHash(spec v1alpha1.ShardRangeSpec, key string) (string, error) {
	fp := ringFingerprint(spec)
	var ring *ConsistentHashRing
	if v, ok := ringCache.Load(fp); ok {
		ring = v.(*ConsistentHashRing)
	} else {
		r, err := NewConsistentHashRing(spec)
		if err != nil {
			return "", err
		}
		actual, _ := ringCache.LoadOrStore(fp, r) // 동시 생성 시 한쪽으로 정규화.
		ring = actual.(*ConsistentHashRing)
	}
	h, err := hashKey(spec.Vindex.Function, key)
	if err != nil {
		return "", err
	}
	return ring.Lookup(h), nil
}
