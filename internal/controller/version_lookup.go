/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/version"
)

// lookupCombo는 PostgresClusterSpec.Version + feature gate 조합을 매트릭스에서
// 조회한다. 본 헬퍼는 reconciler와 webhook 양쪽에서 사용되어 동일한 lookup
// 의미를 보장한다.
func lookupCombo(spec postgresv1alpha1.VersionSpec, gates map[string]bool) (version.Combo, bool) {
	return version.IsSupported(spec.Postgres, gates)
}
