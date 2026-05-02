package version

import "testing"

// 0.3.0-alpha (ADR 0001): vanilla PG 조합 단일 스택. AGPL/BUSL 백엔드 영구 금지.

func TestIsSupported_StablePG18(t *testing.T) {
	c, ok := IsSupported("18", nil)
	if !ok {
		t.Fatalf("PG18 은 stable 이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
	if c.Image != "ghcr.io/keiailab/pg:18" {
		t.Errorf("expected vanilla PG18 image, got %q", c.Image)
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
