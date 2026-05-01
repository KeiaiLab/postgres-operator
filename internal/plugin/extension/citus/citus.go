/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package citus는 Citus extension의 ExtensionPlugin 구현이다.
//
// ============================================================================
// 라이센스 경고 (ADR 0010, 0.2.0-alpha 이후 강제):
// ============================================================================
//
// 본 plugin은 Citus extension(https://github.com/citusdata/citus)을 PostgreSQL
// 인스턴스에 활성화한다. **Citus는 AGPL-3.0** (GNU Affero General Public License v3)
// 라이센스다. 본 operator(Apache-2.0) 자체는 Citus 소스를 포함하지 않으며 별도
// 프로세스로 실행되는 PostgreSQL extension을 *제어*만 하므로 mere aggregation으로
// 본 plugin 패키지는 Apache-2.0 으로 유지된다.
//
// 그러나 본 plugin을 **활성화하여 운영하는 사용자**는 다음을 부담한다:
//
//   - AGPL-3.0 §13 (network use clause): Citus를 SaaS 형태로 사용자에게 제공하면
//     사용자에게 Citus 소스 코드(수정분 포함)를 제공할 의무가 발생한다.
//   - 사용자가 Citus를 수정한 경우 그 수정분도 AGPL-3.0으로 공개해야 한다.
//   - 결합 서비스(operator + Citus + 사용자 코드)의 라이센스 호환성을 사용자가 검토.
//
// 0.2.0-alpha 이후 default stack은 vanilla PostgreSQL이며, Citus는 의식적 opt-in.
// 분산 SQL이 필요하나 AGPL 부담을 회피하려면 RFC 0005 (Native sharding plugin)
// 진행 상황을 참조하라.
//
// ============================================================================
//
// 본 패키지는 Pillar P13(Plugin SDK)의 첫 실사용 사례다. 5종 인터페이스
// (BackupPlugin, ExporterPlugin, ExtensionPlugin, RouterPlugin, AuthPlugin)
// 중 ExtensionPlugin을 구현하며, 다음을 강제한다.
//
//   - SharedPreloadOrder()=0 → Citus가 항상 shared_preload_libraries의 첫
//     번째에 위치하도록 강제한다. Crunchy PGO Issue #3194
//     ("Citus must be first") 회귀 차단의 SDK 차원 메커니즘이다.
//   - Validate(version): internal/version/matrix.go의 IsSupported를 통해
//     PG x Citus 호환 매트릭스에 등록된 minor 버전만 통과시킨다.
//   - PreInstall/PostInstall: 현재는 stub. P11(Citus Topology) 작업에서
//     `citus_set_coordinator_host`, `citus_add_node` 등 분산 토폴로지 부트스트랩
//     SQL이 채워진다.
//
// 본 패키지는 핵심 reconciler에서 직접 import 되지 않는다. cmd/main.go가
// blank import로 이 패키지의 init()을 트리거하면, init()이 Default Registry에
// 자체 등록한다. 이 패턴은 ADR 0005 §강제 메커니즘의 표준 등록 방식이며,
// 향후 모든 ExtensionPlugin/BackupPlugin 등이 동일 패턴을 따른다.
package citus

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/keiailab/postgres-operator/internal/plugin"
	"github.com/keiailab/postgres-operator/internal/version"
)

const (
	// Name은 ExtensionSpec.Name과 매칭되는 식별자다.
	Name = "citus"

	// PreloadOrder는 shared_preload_libraries 직렬화 시 본 extension의 위치다.
	// 0은 가장 앞을 의미하며, "Citus must be first" 규약을 코드 차원에서 보존한다.
	// 본 상수는 ADR 0005 §SharedPreloadOrder 권장 표의 단일 출처(SOT)다.
	PreloadOrder = 0
)

// Plugin은 ExtensionPlugin 인터페이스 구현이다.
type Plugin struct{}

// Compile-time interface satisfaction guard. 인터페이스 시그니처가 변경되면
// 빌드가 깨지므로 ADR 0005의 동결 정책 위반을 즉시 감지한다.
var _ plugin.ExtensionPlugin = (*Plugin)(nil)

// Name은 ExtensionPlugin.Name 구현이다.
func (Plugin) Name() string { return Name }

// SharedPreloadOrder는 ExtensionPlugin.SharedPreloadOrder 구현이다.
func (Plugin) SharedPreloadOrder() int { return PreloadOrder }

// PreInstall은 CREATE EXTENSION citus 호출 전에 실행된다.
//
// 현재는 no-op. P11(Citus Topology) 작업에서 다음이 추가된다:
//   - 권한 검사 (CREATE EXTENSION는 superuser 또는 trusted role 필요)
//   - Citus가 의존하는 스키마/role 사전 생성
func (Plugin) PreInstall(_ context.Context, _ *sql.DB) error {
	return nil
}

// PostInstall은 CREATE EXTENSION citus 호출 후 실행된다.
//
// 현재는 no-op. P11(Citus Topology) 작업에서 다음이 추가된다:
//   - coordinator: SELECT citus_set_coordinator_host(...)
//   - worker primary 변경 시: SELECT citus_update_node(...)
//   - 분산 테이블/참조 테이블 부트스트랩(별도 reconciler가 호출)
func (Plugin) PostInstall(_ context.Context, _ *sql.DB) error {
	return nil
}

// Validate는 사용자가 ExtensionSpec.Version으로 지정한 Citus minor 버전이 본
// 오퍼레이터의 호환 매트릭스(internal/version/matrix.go)에 존재하는지 검증한다.
//
// 빈 문자열은 "기본값 사용" 의미로 통과시킨다. 기본값 결정은 reconciler가
// PostgresClusterSpec.Version.Citus와 매트릭스 lookup으로 수행한다.
func (Plugin) Validate(versionStr string) error {
	if versionStr == "" {
		return nil
	}
	for _, c := range version.All() {
		if c.CitusVersion == versionStr {
			return nil
		}
	}
	return fmt.Errorf("citus extension: version %q is not in supported matrix (see internal/version/matrix.go)", versionStr)
}

// Register는 Default Registry에 본 플러그인을 등록한다. cmd/main.go의 blank
// import에 의해 init()에서 호출된다.
//
// 분리된 함수로 노출하는 이유:
//   - 단위 테스트가 자체 Registry를 만들어 격리 등록 가능
//   - 외부 컨트리뷰터 가이드(P13-T6)에서 등록 방식의 표준 문서화 가능
func Register(r *plugin.Registry) {
	r.RegisterExtension(Plugin{})
}
