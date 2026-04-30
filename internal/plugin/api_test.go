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
	r.RegisterExtension(&dummyExtension{name: "citus", order: 0})
	r.RegisterRouter(&dummyRouter{name: "citus"})
	r.RegisterAuth(&dummyAuth{name: "scram"})

	if p, ok := r.Backup("pgbackrest"); !ok || p.Name() != "pgbackrest" {
		t.Fatalf("Backup lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Exporter("pgmonitor"); !ok || p.Name() != "pgmonitor" {
		t.Fatalf("Exporter lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Extension("citus"); !ok || p.Name() != "citus" {
		t.Fatalf("Extension lookup failed: ok=%v p=%v", ok, p)
	}
	if p, ok := r.Router("citus"); !ok || p.Name() != "citus" {
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

// TestExtensions_PreloadOrder는 Crunchy PGO Issue #3194 회귀 방지 테스트다.
// "Citus는 shared_preload_libraries의 첫 번째여야 한다"는 규약을 본 SDK가
// SharedPreloadOrder()=0 정책으로 강제하는지 검증한다.
func TestExtensions_PreloadOrder(t *testing.T) {
	r := NewRegistry()
	// 등록 순서를 일부러 뒤섞어도 결과는 order 오름차순 + name 사전순이어야 한다.
	r.RegisterExtension(&dummyExtension{name: "pg_cron", order: 200})
	r.RegisterExtension(&dummyExtension{name: "pgaudit", order: 100})
	r.RegisterExtension(&dummyExtension{name: "citus", order: 0})
	r.RegisterExtension(&dummyExtension{name: "pgvector", order: 100}) // pgaudit과 동률

	got := r.Extensions()
	wantNames := []string{"citus", "pgaudit", "pgvector", "pg_cron"}
	if len(got) != len(wantNames) {
		t.Fatalf("expected %d extensions, got %d", len(wantNames), len(got))
	}
	for i, want := range wantNames {
		if got[i].Name() != want {
			t.Errorf("position %d: want %q, got %q", i, want, got[i].Name())
		}
	}
}
