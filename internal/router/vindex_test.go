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
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestResolveShard 는 vindex policy 4 분기 (hash / range / consistent-hash /
// lookup) + 4 hash function (murmur3 / fnv / crc32 / unknown) + overlap
// validation 의 결정성을 보장한다 (D.8.2).
func TestResolveShard(t *testing.T) {
	t.Run("hash murmur3 결정성 + 분기", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Cluster:  "c",
			Keyspace: "ks",
			Vindex: v1alpha1.VindexSpec{
				Type:     v1alpha1.VindexTypeHash,
				Column:   "user_id",
				Function: v1alpha1.VindexHashMurmur3,
			},
			Ranges: []v1alpha1.ShardRangeEntry{
				{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-a"},
				{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-b"},
			},
		}
		shard, err := ResolveShard(spec, "user-42")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if shard != "shard-a" && shard != "shard-b" {
			t.Fatalf("unexpected shard %q", shard)
		}
		// 결정성: 동일 key → 동일 shard.
		shard2, _ := ResolveShard(spec, "user-42")
		if shard != shard2 {
			t.Fatalf("non-deterministic: %s vs %s", shard, shard2)
		}
	})

	t.Run("hash fnv + crc32 도 동작", func(t *testing.T) {
		ranges := []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0xffffffff", Shard: "only"},
		}
		for _, fn := range []v1alpha1.VindexHashFunction{
			v1alpha1.VindexHashFNV, v1alpha1.VindexHashCRC32, v1alpha1.VindexHashMurmur3,
		} {
			spec := v1alpha1.ShardRangeSpec{
				Cluster: "c", Keyspace: "ks",
				Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "k", Function: fn},
				Ranges: ranges,
			}
			if shard, err := ResolveShard(spec, "key-1"); err != nil || shard != "only" {
				t.Fatalf("fn=%s: shard=%q err=%v", fn, shard, err)
			}
		}
	})

	t.Run("range 사전식 비교", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Cluster: "c", Keyspace: "ks",
			Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeRange, Column: "country"},
			Ranges: []v1alpha1.ShardRangeEntry{
				{Lo: "AA", Hi: "MM", Shard: "shard-west"},
				{Lo: "MN", Hi: "ZZ", Shard: "shard-east"},
			},
		}
		cases := map[string]string{
			"AA": "shard-west",
			"KR": "shard-west",
			"MN": "shard-east",
			"US": "shard-east",
		}
		for key, want := range cases {
			got, err := ResolveShard(spec, key)
			if err != nil || got != want {
				t.Fatalf("range key=%q want=%q got=%q err=%v", key, want, got, err)
			}
		}
	})

	t.Run("range gap 키 ErrVindexNoMatch", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Cluster: "c", Keyspace: "ks",
			Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeRange, Column: "k"},
			Ranges: []v1alpha1.ShardRangeEntry{
				{Lo: "A", Hi: "B", Shard: "x"},
			},
		}
		_, err := ResolveShard(spec, "Z")
		if !errors.Is(err, ErrVindexNoMatch) {
			t.Fatalf("expected ErrVindexNoMatch, got %v", err)
		}
	})

	t.Run("consistent-hash + lookup 은 ErrVindexUnsupported", func(t *testing.T) {
		for _, vt := range []v1alpha1.VindexType{
			v1alpha1.VindexTypeConsistentHash, v1alpha1.VindexTypeLookup,
		} {
			spec := v1alpha1.ShardRangeSpec{
				Cluster: "c", Keyspace: "ks",
				Vindex: v1alpha1.VindexSpec{Type: vt},
				Ranges: []v1alpha1.ShardRangeEntry{{Lo: "0", Hi: "1", Shard: "x"}},
			}
			_, err := ResolveShard(spec, "key")
			if !errors.Is(err, ErrVindexUnsupported) {
				t.Fatalf("type=%s expected ErrVindexUnsupported, got %v", vt, err)
			}
		}
	})

	t.Run("ValidateNoOverlap 정상", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Function: v1alpha1.VindexHashMurmur3},
			Ranges: []v1alpha1.ShardRangeEntry{
				{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "a"},
				{Lo: "0x80000000", Hi: "0xffffffff", Shard: "b"},
			},
		}
		if err := ValidateNoOverlap(spec); err != nil {
			t.Fatalf("unexpected overlap err: %v", err)
		}
	})

	t.Run("ValidateNoOverlap overlap 검출", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Function: v1alpha1.VindexHashMurmur3},
			Ranges: []v1alpha1.ShardRangeEntry{
				{Lo: "0x00000000", Hi: "0x80000000", Shard: "a"},
				{Lo: "0x70000000", Hi: "0xffffffff", Shard: "b"},
			},
		}
		if err := ValidateNoOverlap(spec); err == nil {
			t.Fatalf("expected overlap detection, got nil")
		}
	})

	t.Run("ValidateNoOverlap non-hash 는 skip", func(t *testing.T) {
		spec := v1alpha1.ShardRangeSpec{
			Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeRange},
			Ranges: []v1alpha1.ShardRangeEntry{{Lo: "A", Hi: "M", Shard: "x"}, {Lo: "B", Hi: "Z", Shard: "y"}},
		}
		if err := ValidateNoOverlap(spec); err != nil {
			t.Fatalf("non-hash 는 검증 skip 이어야 함: %v", err)
		}
	})

	t.Run("murmur3 known vector", func(t *testing.T) {
		// Apache Vitess murmur3 reference: empty string seed=0 → 0
		if h := murmur3Sum32(nil, 0); h != 0 {
			t.Fatalf("murmur3('') seed=0 want=0 got=0x%08x", h)
		}
	})
}
