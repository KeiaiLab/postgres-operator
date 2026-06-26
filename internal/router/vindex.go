/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ShardRange CRD 의 vindex (가상 인덱스) policy → 단일 키의 shard 결정
// 함수 (D.8.2 / ROADMAP G3 L150). 본 패키지는 *순수 함수* 만 노출 — K8s
// reconciler 와 wire-protocol 은 별도 layer. RFC-0002 §3.2 분기 정합.

// ErrVindexUnsupported 는 lookup 등 미구현 vindex 호출 시 반환된다 (hash · range ·
// consistent-hash 는 구현됨). 호출자 (pg-router) 는 본 sentinel 로 fallback 라우팅
// 또는 명시적 에러 응답을 선택.
var ErrVindexUnsupported = errors.New("router: vindex type not supported in this build")

// ErrVindexNoMatch 는 ranges 어디에도 매칭되지 않는 키이다. ShardRange 의
// gap 또는 사용자 입력 오류 신호. 호출자가 에러 응답 또는 scatter fallback
// 으로 처리.
var ErrVindexNoMatch = errors.New("router: key did not match any range")

// ResolveShard 는 단일 키를 ShardRangeSpec 에 따라 평가하여 shard 이름을 반환한다.
//
// 결정 분기 (vindex.type 별):
//
//   - hash: function(key) % 2^32 → uint32, ranges 의 [Lo, Hi] hex 와 비교 (포함 비교)
//   - range: key 자체를 ranges 의 [Lo, Hi] 와 사전식 비교
//   - consistent-hash: function(shard:vnode) 로 만든 해시 링 위에서 key 를 시계방향
//     으로 가장 가까운 virtual node 의 shard 로 (vindex_consistent.go)
//   - lookup: ErrVindexUnsupported (ShardLookup CRD P3+)
//
// 본 함수는 결정적 (deterministic) — 동일 spec + 동일 key 에서 동일 shard 반환.
func ResolveShard(spec v1alpha1.ShardRangeSpec, key string) (string, error) {
	switch spec.Vindex.Type {
	case v1alpha1.VindexTypeHash:
		return resolveHash(spec, key)
	case v1alpha1.VindexTypeRange:
		return resolveRange(spec, key)
	case v1alpha1.VindexTypeConsistentHash:
		return resolveConsistentHash(spec, key)
	case v1alpha1.VindexTypeLookup:
		return "", fmt.Errorf("%w: lookup (ShardLookup CRD P3+)", ErrVindexUnsupported)
	default:
		return "", fmt.Errorf("%w: unknown vindex type %q", ErrVindexUnsupported, spec.Vindex.Type)
	}
}

func resolveHash(spec v1alpha1.ShardRangeSpec, key string) (string, error) {
	h, err := hashKey(spec.Vindex.Function, key)
	if err != nil {
		return "", err
	}
	for _, r := range spec.Ranges {
		lo, err := parseHashBound(r.Lo)
		if err != nil {
			return "", fmt.Errorf("router: range[%s..%s] lo: %w", r.Lo, r.Hi, err)
		}
		hi, err := parseHashBound(r.Hi)
		if err != nil {
			return "", fmt.Errorf("router: range[%s..%s] hi: %w", r.Lo, r.Hi, err)
		}
		if h >= lo && h <= hi {
			return r.Shard, nil
		}
	}
	return "", fmt.Errorf("%w: hash=0x%08x key=%q", ErrVindexNoMatch, h, key)
}

func resolveRange(spec v1alpha1.ShardRangeSpec, key string) (string, error) {
	for _, r := range spec.Ranges {
		// 사전식 비교 — `lo <= key <= hi` (양 끝 포함).
		if key >= r.Lo && key <= r.Hi {
			return r.Shard, nil
		}
	}
	return "", fmt.Errorf("%w: key=%q", ErrVindexNoMatch, key)
}

// hashKey 는 hash function 별로 32-bit hash 를 계산한다.
//
// 지원: murmur3 (자체 구현 — 별 dep 회피), fnv (hash/fnv 표준), crc32 (hash/crc32 표준).
func hashKey(fn v1alpha1.VindexHashFunction, key string) (uint32, error) {
	switch fn {
	case v1alpha1.VindexHashMurmur3:
		return murmur3Sum32([]byte(key), 0), nil
	case v1alpha1.VindexHashFNV:
		h := fnv.New32a()
		_, _ = h.Write([]byte(key))
		return h.Sum32(), nil
	case v1alpha1.VindexHashCRC32:
		return crc32.ChecksumIEEE([]byte(key)), nil
	default:
		return 0, fmt.Errorf("%w: hash function %q", ErrVindexUnsupported, fn)
	}
}

// parseHashBound 는 hex 문자열 ("0x..." 또는 "ffffffff") 또는 10진수를 uint32 로 해석한다.
func parseHashBound(s string) (uint32, error) {
	if v, err := strconv.ParseUint(s, 0, 64); err == nil {
		return uint32(v), nil
	}
	if v, err := strconv.ParseUint(s, 16, 64); err == nil {
		return uint32(v), nil
	}
	return 0, fmt.Errorf("invalid hash bound %q (expected hex or decimal)", s)
}

// ValidateNoOverlap 는 ranges 의 hash vindex 분기에서 *영역 겹침 / gap* 을 검사한다.
//
// reconciler 가 ShardRange spec validation 단계에서 호출 — overlap 발견 시
// Condition `type=Valid` reason=`RangesOverlap` 으로 거부.
func ValidateNoOverlap(spec v1alpha1.ShardRangeSpec) error {
	if spec.Vindex.Type != v1alpha1.VindexTypeHash {
		return nil // range / lookup / consistent-hash 는 별 검증 규칙
	}
	type r struct{ lo, hi uint32 }
	parsed := make([]r, 0, len(spec.Ranges))
	for _, e := range spec.Ranges {
		lo, err := parseHashBound(e.Lo)
		if err != nil {
			return fmt.Errorf("range[%s..%s]: %w", e.Lo, e.Hi, err)
		}
		hi, err := parseHashBound(e.Hi)
		if err != nil {
			return fmt.Errorf("range[%s..%s]: %w", e.Lo, e.Hi, err)
		}
		if lo > hi {
			return fmt.Errorf("range[%s..%s]: lo>hi", e.Lo, e.Hi)
		}
		parsed = append(parsed, r{lo, hi})
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].lo < parsed[j].lo })
	for i := 1; i < len(parsed); i++ {
		if parsed[i].lo <= parsed[i-1].hi {
			return fmt.Errorf("ranges overlap: [0x%08x..0x%08x] and [0x%08x..0x%08x]",
				parsed[i-1].lo, parsed[i-1].hi, parsed[i].lo, parsed[i].hi)
		}
	}
	return nil
}

// murmur3Sum32 은 MurmurHash3 x86 32-bit 구현이다 (Austin Appleby, public domain).
// 외부 의존성 회피 위해 내장 — Citus / Vitess 와 동일 함수족 (cross-system parity).
func murmur3Sum32(data []byte, seed uint32) uint32 {
	const (
		c1 = 0xcc9e2d51
		c2 = 0x1b873593
	)
	h := seed
	n := len(data)
	tail := n - (n % 4)
	for i := 0; i < tail; i += 4 {
		k := binary.LittleEndian.Uint32(data[i:])
		k *= c1
		k = (k << 15) | (k >> 17)
		k *= c2
		h ^= k
		h = (h << 13) | (h >> 19)
		h = h*5 + 0xe6546b64
	}
	var k uint32
	switch n & 3 {
	case 3:
		k ^= uint32(data[tail+2]) << 16
		fallthrough
	case 2:
		k ^= uint32(data[tail+1]) << 8
		fallthrough
	case 1:
		k ^= uint32(data[tail])
		k *= c1
		k = (k << 15) | (k >> 17)
		k *= c2
		h ^= k
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}
