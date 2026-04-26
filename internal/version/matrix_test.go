package version

import "testing"

func TestIsSupported_StablePG16Citus13(t *testing.T) {
	c, ok := IsSupported("16", "13.0", nil)
	if !ok {
		t.Fatalf("PG16 + Citus 13.0은 stable이어야 함")
	}
	if c.Channel != ChannelStable {
		t.Errorf("expected stable, got %s", c.Channel)
	}
}

func TestIsSupported_PG18WithoutFeatureGate(t *testing.T) {
	// PG18 항목이 추후 추가되면 본 테스트는 활성화된다. 현재는 placeholder.
	if _, ok := IsSupported("18", "13.2", nil); ok {
		t.Errorf("PG18은 feature gate 없으면 거절되어야 함 (또는 매트릭스에 미등록)")
	}
}

func TestIsSupported_UnknownCombo(t *testing.T) {
	if _, ok := IsSupported("15", "10.0", nil); ok {
		t.Errorf("미지원 조합이 통과됨")
	}
}

func TestStable_NotEmpty(t *testing.T) {
	if len(Stable()) == 0 {
		t.Fatal("최소 1개 stable 조합이 있어야 함")
	}
}
