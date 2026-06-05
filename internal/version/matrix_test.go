/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package version

import (
	"strings"
	"testing"
)

// 0.3.0-alpha (ADR 0001): vanilla PG 조합 단일 스택. AGPL/BUSL 백엔드 영구 금지.

func TestIsSupported_StablePG18(t *testing.T) {
	c, ok := IsSupported("18", nil)
	if !ok {
		t.Fatalf("PG18 은 stable 이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
	const wantPG18 = "ghcr.io/keiailab/pg:18@sha256:669e6b975a3a2d7b72e778cd9ea5ba87cacad850b19ff220dd4f86740d9b9c97"
	if c.Image != wantPG18 {
		t.Errorf("expected digest-pinned PG18 image %q, got %q", wantPG18, c.Image)
	}
	// #218 RC#1: PG18 은 digest pin (tag@sha256) 이어야 node 캐시 stale 바이너리
	// 부팅 (fence deadlock) 을 차단. mutable tag 로의 회귀를 막는 가드.
	if !strings.Contains(c.Image, "@sha256:") {
		t.Errorf("PG18 image must be digest-pinned (tag@sha256), got %q", c.Image)
	}
}

func TestIsSupported_StablePG17(t *testing.T) {
	c, ok := IsSupported("17", nil)
	if !ok {
		t.Fatalf("PG17 은 stable 이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
}

func TestIsSupported_StablePG16(t *testing.T) {
	c, ok := IsSupported("16", nil)
	if !ok {
		t.Fatalf("PG16 은 stable (legacy) 이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
}

func TestIsSupported_UnknownVersion(t *testing.T) {
	if _, ok := IsSupported("15", nil); ok {
		t.Errorf("미지원 PG major 가 통과됨")
	}
	if _, ok := IsSupported("99", nil); ok {
		t.Errorf("미지원 PG major 가 통과됨")
	}
}

func TestStable_AllVanilla(t *testing.T) {
	stable := Stable()
	if len(stable) == 0 {
		t.Fatal("최소 1개 stable 조합이 있어야 함")
	}
	for _, c := range stable {
		if c.Channel != ChannelStable {
			t.Errorf("Stable() 결과는 모두 ChannelStable 이어야 함: %+v", c)
		}
	}
}
