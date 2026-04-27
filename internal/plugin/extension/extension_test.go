/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package extension은 등록된 모든 ExtensionPlugin의 회귀 테스트만 보유한다.
//
// 본 패키지가 별도 존재하는 이유: depguard 규칙(.golangci.yml)이 internal/plugin/
// extension/ 하위 하위 패키지를 reconciler/webhook이 직접 import 하지 못하게
// 막지만, 본 패키지는 모든 구체 플러그인을 import 해 "정렬 정책 정확성"을
// 유일하게 검증할 수 있다. cmd/main.go도 동일 권한이 있으나, cmd/main.go에는
// 테스트가 없으므로 본 패키지가 회귀 차단의 단일 출처(SOT)다.
package extension

import (
	"testing"

	"github.com/keiailab/postgres-operator/internal/plugin"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/citus"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/pgaudit"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/pgcron"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/pgnodemx"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/pgvector"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/postgis"
	"github.com/keiailab/postgres-operator/internal/plugin/extension/setuser"
)

// TestPreloadOrder_AllRegisteredExtensions는 본 오퍼레이터가 동봉하는 7개
// ExtensionPlugin이 모두 등록된 상태에서 Registry.Extensions()의 정렬 결과가
// 결정적임을 검증한다.
//
// "Citus must be first" 규약(PGO Issue #3194 회귀 차단)의 통합 검증이며,
// 향후 새 ExtensionPlugin 추가 시 본 테스트의 wantNames에 위치를 명시해야
// 한다. 추가 위치는 ADR 0005 §SharedPreloadOrder 권장 표를 참조한다.
func TestPreloadOrder_AllRegisteredExtensions(t *testing.T) {
	r := plugin.NewRegistry()
	citus.Register(r)
	pgaudit.Register(r)
	pgcron.Register(r)
	pgnodemx.Register(r)
	pgvector.Register(r)
	postgis.Register(r)
	setuser.Register(r)

	got := r.Extensions()
	// 정렬 규약: SharedPreloadOrder 오름차순, 동률 시 Name() 사전순.
	// citus(0) → pgaudit(100) → pgvector(100) → pg_cron(200) → pgnodemx(300) →
	// postgis(300) → set_user(300)
	wantOrder := []string{
		"citus",    // 0  — must be first (Issue #3194)
		"pgaudit",  // 100
		"pgvector", // 100 — alpha 정렬에서 pgaudit < pgvector
		"pg_cron",  // 200
		"pgnodemx", // 300
		"postgis",  // 300 — alpha 정렬: pgnodemx < postgis
		"set_user", // 300 — alpha 정렬: postgis < set_user
	}

	if len(got) != len(wantOrder) {
		t.Fatalf("expected %d extensions, got %d", len(wantOrder), len(got))
	}
	for i, want := range wantOrder {
		if got[i].Name() != want {
			t.Errorf("position %d: want %q, got %q (full order: %v)",
				i, want, got[i].Name(), namesOf(got))
		}
	}

	// 추가 가드: 첫 번째는 무조건 citus여야 한다.
	if got[0].Name() != "citus" {
		t.Fatalf("CRITICAL: shared_preload_libraries[0] must be 'citus' (PGO Issue #3194), got %q", got[0].Name())
	}
}

func namesOf(exts []plugin.ExtensionPlugin) []string {
	out := make([]string, len(exts))
	for i, e := range exts {
		out[i] = e.Name()
	}
	return out
}
