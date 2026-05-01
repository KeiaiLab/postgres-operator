package version

import "testing"

// 0.2.0-alpha 이후: vanilla PG 조합이 Stable, Citus 조합은 Beta (ADR 0010).

func TestIsSupported_StablePG18Vanilla(t *testing.T) {
	c, ok := IsSupported("18", "", nil)
	if !ok {
		t.Fatalf("PG18 vanilla은 stable이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
	if c.Image != "ghcr.io/keiailab/pg:18" {
		t.Errorf("expected vanilla PG18 image, got %q", c.Image)
	}
}

func TestIsSupported_StablePG17Vanilla(t *testing.T) {
	c, ok := IsSupported("17", "", nil)
	if !ok {
		t.Fatalf("PG17 vanilla은 stable이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
}

func TestIsSupported_BetaPG17Citus13(t *testing.T) {
	c, ok := IsSupported("17", "13.0", nil)
	if !ok {
		t.Fatalf("PG17 + Citus 13.0은 beta로 등록되어야 함")
	}
	if c.Channel != ChannelBeta {
		t.Errorf("Citus 조합은 beta여야 함 (ADR 0010 license 격리), got %s", c.Channel)
	}
}

func TestIsSupported_UnknownCombo(t *testing.T) {
	if _, ok := IsSupported("15", "10.0", nil); ok {
		t.Errorf("미지원 조합이 통과됨")
	}
}

func TestStable_VanillaOnly(t *testing.T) {
	stable := Stable()
	if len(stable) == 0 {
		t.Fatal("최소 1개 stable 조합이 있어야 함")
	}
	for _, c := range stable {
		if c.CitusVersion != "" {
			t.Errorf("0.2.0-alpha 이후 Stable 채널에 Citus 조합 금지 (ADR 0010): %+v", c)
		}
	}
}
