package citus

import (
	"strings"
	"testing"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

func TestPlugin_NameAndOrder(t *testing.T) {
	p := Plugin{}
	if got := p.Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
	if got := p.SharedPreloadOrder(); got != PreloadOrder {
		t.Errorf("SharedPreloadOrder() = %d, want %d", got, PreloadOrder)
	}
	if PreloadOrder != 0 {
		t.Fatalf("PreloadOrder must remain 0 to honor 'Citus must be first' invariant (PGO Issue #3194). Changing this constant requires RFC 0011.")
	}
}

func TestPlugin_Validate_AcceptsEmpty(t *testing.T) {
	if err := (Plugin{}).Validate(""); err != nil {
		t.Errorf("Validate(\"\") = %v, want nil (empty means default)", err)
	}
}

func TestPlugin_Validate_AcceptsKnownVersion(t *testing.T) {
	// matrix.go가 보유한 임의 버전 한 개를 사용. 매트릭스 변경 시 본 테스트가
	// 회귀 신호를 준다.
	if err := (Plugin{}).Validate("13.0"); err != nil {
		t.Errorf("Validate(\"13.0\") = %v, want nil (13.0 is in matrix)", err)
	}
}

func TestPlugin_Validate_RejectsUnknownVersion(t *testing.T) {
	err := (Plugin{}).Validate("99.99")
	if err == nil {
		t.Fatal("Validate(\"99.99\") = nil, want error")
	}
	if !strings.Contains(err.Error(), "supported matrix") {
		t.Errorf("error message lacks matrix reference: %v", err)
	}
}

func TestRegister_RegistersToRegistry(t *testing.T) {
	r := plugin.NewRegistry()
	Register(r)

	got, ok := r.Extension(Name)
	if !ok {
		t.Fatalf("Extension(%q) not found after Register", Name)
	}
	if got.Name() != Name {
		t.Errorf("Extension(%q).Name() = %q", Name, got.Name())
	}
	if got.SharedPreloadOrder() != PreloadOrder {
		t.Errorf("registered plugin has wrong PreloadOrder: %d", got.SharedPreloadOrder())
	}
}
