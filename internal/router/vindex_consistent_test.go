/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"fmt"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func chSpec(shards ...string) v1alpha1.ShardRangeSpec {
	spec := v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{
			Type:         v1alpha1.VindexTypeConsistentHash,
			Function:     v1alpha1.VindexHashMurmur3,
			VirtualNodes: 160,
		},
	}
	for _, s := range shards {
		spec.Ranges = append(spec.Ranges, v1alpha1.ShardRangeEntry{Lo: "0", Hi: "0", Shard: s})
	}
	return spec
}

// TestConsistentHash_Deterministic 는 같은 key 가 항상 같은 shard 로 가고, 모든 shard
// 가 사용됨을 검증한다.
func TestConsistentHash_Deterministic(t *testing.T) {
	spec := chSpec("shard-0", "shard-1", "shard-2")
	seen := map[string]bool{}
	for i := 0; i < 600; i++ {
		key := fmt.Sprintf("tenant-%d", i)
		a, err := ResolveShard(spec, key)
		if err != nil {
			t.Fatalf("ResolveShard(%q): %v", key, err)
		}
		b, _ := ResolveShard(spec, key)
		if a != b {
			t.Fatalf("non-deterministic: %q → %q vs %q", key, a, b)
		}
		seen[a] = true
	}
	if len(seen) != 3 {
		t.Fatalf("not all shards used: %v", seen)
	}
}

// TestConsistentHash_MinimalMovement 는 consistent-hash 의 핵심 속성을 증명한다 —
// 샤드 1개 추가 시 *약 1/N 의 키만* 이동한다(modulo 해시는 ~3/4 가 이동). 이게
// 리밸런싱 친화성의 근거.
func TestConsistentHash_MinimalMovement(t *testing.T) {
	const n = 4000
	before := chSpec("shard-0", "shard-1", "shard-2")
	after := chSpec("shard-0", "shard-1", "shard-2", "shard-3")

	moved := 0
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("tenant-%d", i)
		a, _ := ResolveShard(before, key)
		b, _ := ResolveShard(after, key)
		if a != b {
			moved++
		}
	}
	frac := float64(moved) / float64(n)
	// 3→4 샤드: 이상적 이동 비율 ≈ 1/4. virtual node 분산 편차를 고려해 넉넉히 0.40 상한,
	// 0.10 하한(전혀 안 옮기면 분포가 깨진 것). modulo 해시라면 ~0.75 가 옮겨졌을 것.
	if frac < 0.10 || frac > 0.40 {
		t.Fatalf("moved fraction = %.3f, want ~0.25 (in [0.10, 0.40]); modulo hash 였다면 ~0.75", frac)
	}
	t.Logf("샤드 3→4 추가 시 이동 키 비율: %.1f%% (이상적 25%%)", frac*100)
}

// TestConsistentHash_DefaultVirtualNodes 는 VirtualNodes=0 일 때 기본값으로 동작함을
// 검증한다.
func TestConsistentHash_DefaultVirtualNodes(t *testing.T) {
	spec := chSpec("a", "b")
	spec.Vindex.VirtualNodes = 0 // 미설정 → defaultVirtualNodes
	ring, err := NewConsistentHashRing(spec)
	if err != nil {
		t.Fatalf("NewConsistentHashRing: %v", err)
	}
	if len(ring.points) != 2*defaultVirtualNodes {
		t.Fatalf("ring points = %d, want %d", len(ring.points), 2*defaultVirtualNodes)
	}
}

// TestConsistentHash_NoShards 는 샤드가 없으면 에러임을 검증한다.
func TestConsistentHash_NoShards(t *testing.T) {
	if _, err := ResolveShard(chSpec(), "k"); err == nil {
		t.Fatal("no shards: expected error")
	}
}
