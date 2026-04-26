// Package version은 PostgreSQL × Citus 호환 매트릭스를 정의한다.
//
// 매 reconcile / webhook 검증 시 사용자가 지정한 spec.postgres.version × spec.citus.version
// 조합이 본 매트릭스에 존재하는지 확인한다. 매트릭스는 .github/workflows/upstream-watch.yml이
// 매일 cron으로 PostgreSQL · Citus 새 릴리스를 감지하여 자동 PR로 갱신한다.
package version

// Channel은 본 오퍼레이터의 릴리즈 채널을 표현한다.
type Channel string

const (
	// ChannelStable은 production 권장 조합.
	ChannelStable Channel = "stable"
	// ChannelBeta는 검증 중인 조합. e2e는 통과하나 광범위 production 사용 부족.
	ChannelBeta Channel = "beta"
	// ChannelPreviewPG18은 PG18 + Citus PG18-호환 마이너 발표 시점부터 활성화되는 격리 채널.
	ChannelPreviewPG18 Channel = "preview-pg18"
)

// Combo는 (PG major, Citus version) 단일 조합을 표현한다.
type Combo struct {
	// PostgresMajor는 "16" | "17" | "18" 중 하나.
	PostgresMajor string
	// CitusVersion은 "13.0" 같은 minor 단위.
	CitusVersion string
	// Image는 빌드 이미지 태그(예: "ghcr.io/keiailab/pg:17-citus13-2026.04").
	Image string
	// Channel은 안정성 등급.
	Channel Channel
	// FeatureGate는 활성화에 필요한 operator feature gate(없으면 빈 문자열).
	FeatureGate string
}

// supported는 본 오퍼레이터가 빌드/검증 매트릭스로 지원하는 조합 전체.
//
// 갱신 정책: 새 항목 추가는 RFC 불필요(routine). 기존 항목 제거는 deprecation 마이너 1버전 후 RFC.
var supported = []Combo{
	// PG 16 — Stable Tier 1
	{PostgresMajor: "16", CitusVersion: "12.1", Image: "ghcr.io/keiailab/pg:16-citus12.1", Channel: ChannelStable},
	{PostgresMajor: "16", CitusVersion: "13.0", Image: "ghcr.io/keiailab/pg:16-citus13.0", Channel: ChannelStable},

	// PG 17 — Stable Tier 1
	{PostgresMajor: "17", CitusVersion: "13.0", Image: "ghcr.io/keiailab/pg:17-citus13.0", Channel: ChannelStable},

	// PG 18 — preview 채널, Citus가 PG18 호환 마이너를 발표하면 upstream-watch가 자동 PR로 활성화.
	// 활성화 전 본 항목은 placeholder. FeatureGate=PostgresEighteen 필수.
	// 예시(미래): {PostgresMajor: "18", CitusVersion: "13.2", Image: "ghcr.io/keiailab/pg:18-citus13.2", Channel: ChannelPreviewPG18, FeatureGate: "PostgresEighteen"},
}

// IsSupported는 주어진 조합이 매트릭스에 있는지 확인한다.
// gates는 활성화된 feature gate 집합(예: {"PostgresEighteen": true}).
func IsSupported(pgMajor, citusVersion string, gates map[string]bool) (Combo, bool) {
	for _, c := range supported {
		if c.PostgresMajor != pgMajor || c.CitusVersion != citusVersion {
			continue
		}
		if c.FeatureGate != "" && !gates[c.FeatureGate] {
			continue
		}
		return c, true
	}
	return Combo{}, false
}

// All은 매트릭스 전체를 반환한다(CI matrix 생성용).
func All() []Combo {
	out := make([]Combo, len(supported))
	copy(out, supported)
	return out
}

// Stable은 stable 채널 조합만 반환한다.
func Stable() []Combo {
	var out []Combo
	for _, c := range supported {
		if c.Channel == ChannelStable {
			out = append(out, c)
		}
	}
	return out
}
