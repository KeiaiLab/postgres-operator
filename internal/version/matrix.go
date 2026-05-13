/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package version은 PostgreSQL 호환 매트릭스를 정의한다.
//
// 매 reconcile / webhook 검증 시 사용자가 지정한 spec.version.postgres가
// 본 매트릭스에 존재하는지 확인한다.
//
// 0.3.0-alpha 정책 (ADR 0001 자체 분산 SQL + ADR 0003 license):
//   - Vanilla PostgreSQL (PG18+ 권장) 단일 스택. AGPL/BUSL/CSL/SSPL 백엔드 영구 금지.
//   - 분산 SQL 기능은 자체 sharding plugin (RFC 0001~0005) 으로 단계 도입.
//   - 매트릭스 갱신은 RFC 0002 §7 예외 외에는 로컬에서 사람이 PR로 진행 (자동 cron 폐기).
package version

import (
	commonsversion "github.com/keiailab/operator-commons/pkg/version"
)

// Channel은 본 오퍼레이터의 릴리즈 채널을 표현한다.
type Channel string

const (
	// ChannelStable은 production 권장 조합. vanilla PG 조합만 해당.
	ChannelStable Channel = "stable"
	// ChannelBeta는 검증 중 또는 조건부 조합. 향후 native sharding 백엔드의 점진적
	// 도입 채널로 사용된다 (ADR 0005 versioning policy).
	ChannelBeta Channel = "beta"
	// ChannelPreviewPG18은 deprecated — PG18이 Stable 진입으로 더 이상 사용되지 않음.
	// 호환을 위해 상수는 유지하되 매트릭스에서는 사용하지 않는다.
	ChannelPreviewPG18 Channel = "preview-pg18"
)

// Combo는 PG major 단일 조합을 표현한다.
type Combo struct {
	// PostgresMajor는 "16" | "17" | "18" 중 하나.
	PostgresMajor string
	// Image는 빌드 이미지 태그(예: "ghcr.io/keiailab/pg:18").
	Image string
	// Channel은 안정성 등급.
	Channel Channel
	// FeatureGate는 활성화에 필요한 operator feature gate(없으면 빈 문자열).
	FeatureGate string
}

// PrimaryKey — commons Matrix[Combo] 의 MatrixEntry interface 구현
// (Plan §2 D12, ADR commons-0004). PostgresMajor 가 unique 식별자 —
// duplicate 시 init-time panic (MustMatrix).
func (c Combo) PrimaryKey() string { return c.PostgresMajor }

// supported는 본 오퍼레이터가 빌드/검증 매트릭스로 지원하는 조합 전체.
//
// 갱신 정책: Stable 추가/제거는 ADR. Beta 추가는 routine. Channel 강등(Stable→Beta)은 ADR.
//
// Plan §2 D12 / commons-ADR-0004: commons `Matrix[Combo]` 로 위임. 기존
// `[]Combo` slice 와 외부 contract (IsSupported / All / Stable /
// SupportedMajors) 동등 — 내부 storage 만 commons 위임.
var supported = commonsversion.MustMatrix(
	// ============================================================================
	// Vanilla PostgreSQL — Stable Tier (ADR 0001, 0.3.0-alpha)
	// ============================================================================
	// 분산 SQL은 자체 sharding plugin (RFC 0001~0005) 으로 단계 도입. 외부 백엔드
	// 의존 (AGPL/BUSL/CSL/SSPL) 은 영구 금지 (ADR 0003).

	// PG 18 — 권장 default (최신 stable).
	Combo{PostgresMajor: "18", Image: "ghcr.io/keiailab/pg:18", Channel: ChannelStable},
	// PG 17 — gradual upgrade path.
	Combo{PostgresMajor: "17", Image: "ghcr.io/keiailab/pg:17", Channel: ChannelStable},
	// PG 16 — legacy support.
	Combo{PostgresMajor: "16", Image: "ghcr.io/keiailab/pg:16", Channel: ChannelStable},
)

// IsSupported는 주어진 PG major가 매트릭스에 있는지 확인한다.
// gates는 활성화된 feature gate 집합(예: {"PostgresEighteen": true}).
//
// Plan §2 D12: commons `supported.Find` 위임. gate 검증은 본 함수가 보존.
func IsSupported(pgMajor string, gates map[string]bool) (Combo, bool) {
	c, ok := supported.Find(pgMajor)
	if !ok {
		return Combo{}, false
	}
	if c.FeatureGate != "" && !gates[c.FeatureGate] {
		return Combo{}, false
	}
	return c, true
}

// All은 매트릭스 전체를 반환한다(CI matrix 생성용).
//
// Plan §2 D12: commons `supported.Entries()` 위임 (방어 복사 보존).
func All() []Combo {
	return supported.Entries()
}

// Stable은 stable 채널 조합만 반환한다.
func Stable() []Combo {
	var out []Combo
	for _, c := range supported.Entries() {
		if c.Channel == ChannelStable {
			out = append(out, c)
		}
	}
	return out
}

// SupportedMajors — PostgresMajor 의 *string-only view*. webhook 에서
// commons.ValidateWithPredicate 의 *allowed []string* 인자 용 (ADR-0009).
//
// Plan §2 D12: commons `supported.Keys()` 위임. dedup 은 MustMatrix
// 가 init-time 검증 — duplicate PrimaryKey panic 으로 *runtime dedup*
// 대신 *init-time 보장*.
func SupportedMajors() []string {
	return supported.Keys()
}
