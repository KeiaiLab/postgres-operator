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

package plugin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// 본 파일은 P13-T1 인터페이스 5종이 컴파일 가능하고 Registry 등록·조회가
// 결정적임을 보장하는 최소 회귀 테스트다. 구체 플러그인 구현체는 P10/P11
// 등에서 별도 패키지로 추가된다.

// ----------------------------------------------------------------------------
// 더미 구현체 (compile-test + 정렬 결정성 검증용)
// ----------------------------------------------------------------------------

type dummyBackup struct{ name string }

func (d *dummyBackup) Name() string { return d.name }
func (d *dummyBackup) PerformBackup(_ context.Context, _ ClusterTarget, _ BackupOptions) (BackupResult, error) {
	return BackupResult{}, nil
}
func (d *dummyBackup) RestorePIT(_ context.Context, _ ClusterTarget, _ time.Time) error { return nil }
func (d *dummyBackup) Validate(_ *BackupSpec) error                                     { return nil }

type dummyExporter struct{ name string }

func (d *dummyExporter) Name() string                    { return d.name }
func (d *dummyExporter) SidecarSpec() corev1.Container   { return corev1.Container{Name: d.name} }
func (d *dummyExporter) DashboardJSON() ([]byte, error)  { return nil, nil }
func (d *dummyExporter) AlertRulesYAML() ([]byte, error) { return nil, nil }

type dummyExtension struct {
	name  string
	order int
}

func (d *dummyExtension) Name() string                                   { return d.name }
func (d *dummyExtension) SharedPreloadOrder() int                        { return d.order }
func (d *dummyExtension) PreInstall(_ context.Context, _ *sql.DB) error  { return nil }
func (d *dummyExtension) PostInstall(_ context.Context, _ *sql.DB) error { return nil }
func (d *dummyExtension) Validate(_ string) error                        { return nil }

type dummyRouter struct{ name string }

func (d *dummyRouter) Name() string { return d.name }
func (d *dummyRouter) BuildRouterPodSpec(_ ClusterTarget) (corev1.PodSpec, error) {
	return corev1.PodSpec{}, nil
}
func (d *dummyRouter) HealthProbe(_ context.Context, _, _ string) error { return nil }

type dummyAuth struct{ name string }

func (d *dummyAuth) Name() string                                       { return d.name }
func (d *dummyAuth) Configure(_ context.Context, _ ClusterTarget) error { return nil }
func (d *dummyAuth) SecretSchemaJSON() ([]byte, error)                  { return nil, nil }
func (d *dummyAuth) RotateSecret(_ context.Context, _ ClusterTarget, _ *corev1.SecretReference) (*corev1.SecretReference, error) {
	return nil, nil
}

// ----------------------------------------------------------------------------
// 인터페이스 만족 컴파일 가드 (interface satisfaction guards)
// ----------------------------------------------------------------------------

var (
	_ BackupPlugin    = (*dummyBackup)(nil)
	_ ExporterPlugin  = (*dummyExporter)(nil)
	_ ExtensionPlugin = (*dummyExtension)(nil)
	_ RouterPlugin    = (*dummyRouter)(nil)
	_ AuthPlugin      = (*dummyAuth)(nil)
)

// ----------------------------------------------------------------------------
// Registry 등록·조회 회귀 테스트
// ----------------------------------------------------------------------------

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()

	r.RegisterBackup(&dummyBackup{name: "pgbackrest"})
	r.RegisterExporter(&dummyExporter{name: "pgmonitor"})
	r.RegisterExtension(&dummyExtension{name: "demo-ext", order: 0})
	r.RegisterRouter(&dummyRouter{name: "demo-router"})
	r.RegisterAuth(&dummyAuth{name: "scram"})

	if p, ok := r.Backup("pgbackrest"); !ok || p.Name() != "pgbackrest" {
		t.Fatalf("Backup lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Exporter("pgmonitor"); !ok || p.Name() != "pgmonitor" {
		t.Fatalf("Exporter lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Extension("demo-ext"); !ok || p.Name() != "demo-ext" {
		t.Fatalf("Extension lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Router("demo-router"); !ok || p.Name() != "demo-router" {
		t.Fatalf("Router lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Auth("scram"); !ok || p.Name() != "scram" {
		t.Fatalf("Auth lookup failed: ok=%v p=%v", ok, p)
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.RegisterBackup(&dummyBackup{name: "dup"})

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate BackupPlugin registration")
		}
	}()
	r.RegisterBackup(&dummyBackup{name: "dup"})
}

// TestBackupOptions_ExecutionModeField는 P1-6 추가의 컴파일 가드다.
// ADR 0005 §변경 정책에 따라 추가 필드만 허용되며, 본 테스트는 ExecutionMode
// 가 BackupOptions struct에 정의되고 string 타입으로 사용 가능함을 회귀 차단한다.
//
// 향후 누군가 ExecutionMode를 제거하거나 타입을 바꾸면 본 테스트가 즉시 fail.
func TestBackupOptions_ExecutionModeField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mode string
	}{
		{"sidecar", "sidecar"},
		{"job", "job"},
		{"empty (plugin default)", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := BackupOptions{ExecutionMode: tc.mode}
			if o.ExecutionMode != tc.mode {
				t.Errorf("ExecutionMode: want %q, got %q", tc.mode, o.ExecutionMode)
			}
		})
	}
}

// TestExtensions_PreloadOrder 는 SharedPreloadOrder 정렬 규약 (낮은 숫자가 앞쪽,
// 동률은 사전순) 의 회귀 방지 테스트다.
func TestExtensions_PreloadOrder(t *testing.T) {
	r := NewRegistry()
	// 등록 순서를 일부러 뒤섞어도 결과는 order 오름차순 + name 사전순이어야 한다.
	r.RegisterExtension(&dummyExtension{name: "pg_cron", order: 200})
	r.RegisterExtension(&dummyExtension{name: "pgaudit", order: 100})
	r.RegisterExtension(&dummyExtension{name: "demo-ext", order: 0})
	r.RegisterExtension(&dummyExtension{name: "pgvector", order: 100}) // pgaudit과 동률

	got := r.Extensions()
	wantNames := []string{"demo-ext", "pgaudit", "pgvector", "pg_cron"}
	if len(got) != len(wantNames) {
		t.Fatalf("expected %d extensions, got %d", len(wantNames), len(got))
	}
	for i, want := range wantNames {
		if got[i].Name() != want {
			t.Errorf("position %d: want %q, got %q", i, want, got[i].Name())
		}
	}
}

// RFC 0006 R1 — EnabledExtensions 가 names 필터링 + 정렬 + missing 보고를 모두
// 수행하는지. cross-validation bug 2 영구 fix 의 회귀 차단.
func TestEnabledExtensions_FilterAndSort(t *testing.T) {
	r := NewRegistry()
	r.RegisterExtension(&dummyExtension{name: "pg_cron", order: 200})
	r.RegisterExtension(&dummyExtension{name: "pgaudit", order: 100})
	r.RegisterExtension(&dummyExtension{name: "pgvector", order: 100})

	t.Run("subset_filter_sorted", func(t *testing.T) {
		// 사용자가 일부 (pgaudit, pg_cron) 만 opt-in. 입력 순서 무관, 출력은 정렬.
		enabled, missing := r.EnabledExtensions([]string{"pg_cron", "pgaudit"})
		if len(missing) != 0 {
			t.Errorf("missing should be empty, got %v", missing)
		}
		want := []string{"pgaudit", "pg_cron"} // order 100 < 200
		if len(enabled) != len(want) {
			t.Fatalf("len(enabled) = %d, want %d", len(enabled), len(want))
		}
		for i, w := range want {
			if enabled[i].Name() != w {
				t.Errorf("position %d: want %q, got %q", i, w, enabled[i].Name())
			}
		}
	})

	t.Run("missing_reported", func(t *testing.T) {
		_, missing := r.EnabledExtensions([]string{"pgaudit", "nonexistent"})
		if len(missing) != 1 || missing[0] != "nonexistent" {
			t.Errorf("missing = %v, want [nonexistent]", missing)
		}
	})

	t.Run("nil_names_returns_empty", func(t *testing.T) {
		// 핵심 회귀 — nil/빈 names 시 vanilla PG (extension 미적용).
		enabled, missing := r.EnabledExtensions(nil)
		if len(enabled) != 0 || len(missing) != 0 {
			t.Errorf("nil names: enabled=%d missing=%v, want both empty", len(enabled), missing)
		}
	})
}
