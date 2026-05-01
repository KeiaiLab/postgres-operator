// Package version은 PostgreSQL × (선택적) Citus 호환 매트릭스를 정의한다.
//
// 매 reconcile / webhook 검증 시 사용자가 지정한 spec.version.postgres × spec.version.citus
// 조합이 본 매트릭스에 존재하는지 확인한다.
//
// 0.2.0-alpha 이후 정책 (RFC 0002 GH Actions 폐기 + ADR 0010 license/sharding):
//   - Vanilla PostgreSQL 조합 (CitusVersion == "") 이 Stable 기본값.
//   - Citus 통합 조합은 Beta 채널로 강등됨. 사용자가 명시적 opt-in 시에만 활성화되며,
//     opt-in 시 Citus의 AGPL-3.0 라이센스(특히 §13 SaaS 의무)를 사용자가 부담한다.
//   - 매트릭스 갱신은 RFC 0002 §7 예외 외에는 로컬에서 사람이 PR로 진행 (자동 cron 폐기).
package version

// Channel은 본 오퍼레이터의 릴리즈 채널을 표현한다.
type Channel string

const (
	// ChannelStable은 production 권장 조합. 0.2.0-alpha 이후 vanilla PG 조합만 해당.
	ChannelStable Channel = "stable"
	// ChannelBeta는 검증 중 또는 라이센스 의식 조건부 조합.
	// 현재 Citus(AGPL-3.0) 통합 조합은 모두 Beta — 사용자가 license 부담을 명시 수용해야 한다.
	ChannelBeta Channel = "beta"
	// ChannelPreviewPG18은 deprecated — PG18이 Stable 진입(0.2.0-alpha)으로 더 이상 사용되지 않음.
	// 호환을 위해 상수는 유지하되 매트릭스에서는 사용하지 않는다.
	ChannelPreviewPG18 Channel = "preview-pg18"
)

// Combo는 (PG major, 선택적 Citus version) 단일 조합을 표현한다.
type Combo struct {
	// PostgresMajor는 "16" | "17" | "18" 중 하나.
	PostgresMajor string
	// CitusVersion은 minor 단위 버전 문자열(예: "13.0"). 빈 문자열이면 vanilla(Citus 없음).
	CitusVersion string
	// Image는 빌드 이미지 태그(예: "ghcr.io/keiailab/pg:18", "ghcr.io/keiailab/pg:17-citus13.0").
	Image string
	// Channel은 안정성 등급.
	Channel Channel
	// FeatureGate는 활성화에 필요한 operator feature gate(없으면 빈 문자열).
	FeatureGate string
}

// supported는 본 오퍼레이터가 빌드/검증 매트릭스로 지원하는 조합 전체.
//
// 갱신 정책: Stable 추가/제거는 ADR. Beta 추가는 routine. Channel 강등(Stable→Beta)은 ADR.
var supported = []Combo{
	// ============================================================================
	// Vanilla PostgreSQL — Stable Tier (ADR 0010, 0.2.0-alpha 이후)
	// ============================================================================
	// Sharding/distributed SQL 없이 단일 노드 또는 외부 솔루션과 결합.
	// 분산 SQL이 필요하면 Native sharding plugin (RFC 0005) 또는 Citus opt-in (Beta) 사용.

	// PG 18 — 권장 default (최신 stable, vanilla).
	{PostgresMajor: "18", CitusVersion: "", Image: "ghcr.io/keiailab/pg:18", Channel: ChannelStable},
	// PG 17 — vanilla, gradual upgrade path.
	{PostgresMajor: "17", CitusVersion: "", Image: "ghcr.io/keiailab/pg:17", Channel: ChannelStable},
	// PG 16 — vanilla, legacy support.
	{PostgresMajor: "16", CitusVersion: "", Image: "ghcr.io/keiailab/pg:16", Channel: ChannelStable},

	// ============================================================================
	// Citus 통합 — Beta Tier (AGPL-3.0 opt-in)
	// ============================================================================
	// 사용자가 Citus의 분산 SQL 기능을 명시적으로 활성화하는 경우. AGPL-3.0 라이센스
	// (특히 §13 SaaS 의무)는 사용자가 부담한다. ADR 0010 참조.
	// Native sharding plugin이 RFC 0005 Phase 2+로 구현되면 Citus 의존을 단계적으로 제거한다.
	{PostgresMajor: "16", CitusVersion: "12.1", Image: "ghcr.io/keiailab/pg:16-citus12.1", Channel: ChannelBeta},
	{PostgresMajor: "16", CitusVersion: "13.0", Image: "ghcr.io/keiailab/pg:16-citus13.0", Channel: ChannelBeta},
	{PostgresMajor: "17", CitusVersion: "13.0", Image: "ghcr.io/keiailab/pg:17-citus13.0", Channel: ChannelBeta},
}

// IsSupported는 주어진 조합이 매트릭스에 있는지 확인한다.
// citusVersion이 빈 문자열이면 vanilla(Citus 없음) 조합으로 조회한다.
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
