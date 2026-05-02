package v1alpha1

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
	pluginextpgaudit "github.com/keiailab/postgres-operator/internal/plugin/extension/pgaudit"
)

// 본 단위 테스트는 RFC 0001 §4 검증 규칙 9개 각각에 대해 최소 1건의 happy/거절
// 케이스를 보유한다. 이 표는 Pillar P1-T4 DoD("RFC 0001 §4 검증 규칙 webhook")의
// 통과 증거다.

func validBaseCluster() *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			Version: postgresv1alpha1.VersionSpec{Postgres: "17"},
			Coordinator: postgresv1alpha1.CoordinatorSpec{
				Members: 3,
				Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
			},
			Workers: []postgresv1alpha1.WorkerPoolSpec{{
				Name:    "pool-a",
				Members: 3,
				Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
			}},
			Routers: postgresv1alpha1.RouterSpec{Replicas: 3},
			// Deployment 빈 문자열 → production 취급
		},
	}
}

func newWebhook(t *testing.T) *PostgresClusterWebhook {
	t.Helper()
	r := plugin.NewRegistry()
	pluginextpgaudit.Register(r)
	return &PostgresClusterWebhook{
		FeatureGates: map[string]bool{},
		Plugins:      r,
	}
}

func TestValidate_Happy(t *testing.T) {
	w := newWebhook(t)
	if _, err := w.ValidateCreate(context.Background(), validBaseCluster()); err != nil {
		t.Fatalf("expected nil error for valid cluster, got: %v", err)
	}
}

func TestValidate_VersionRejected_NotInMatrix(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Version.Postgres = "99" // 매트릭스 미등록 PG major
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for unsupported postgres version")
	}
	if !strings.Contains(err.Error(), "supported matrix") {
		t.Errorf("error message lacks 'supported matrix': %v", err)
	}
}

// 0.3.0-alpha (ADR 0001): PG18 은 Stable default, vanilla 단일 스택.
func TestValidate_PG18_Accepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Version.Postgres = "18"
	_, err := w.ValidateCreate(context.Background(), c)
	if err != nil {
		t.Fatalf("PG18 은 stable 이어야 하나 거절됨: %v", err)
	}
}

func TestValidate_Coordinator_EvenMembers_Rejected(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Coordinator.Members = 2
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for even coordinator.members")
	}
	if !strings.Contains(err.Error(), "must be odd") {
		t.Errorf("error message lacks 'must be odd': %v", err)
	}
}

func TestValidate_ProductionRequires_Coordinator_GE3(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Coordinator.Members = 1 // 홀수지만 production은 ≥3 필요
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for production coordinator.members=1")
	}
	if !strings.Contains(err.Error(), ">=3") {
		t.Errorf("error message lacks '>=3': %v", err)
	}
}

func TestValidate_Development_Allows_Members1(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Deployment = postgresv1alpha1.DeploymentDevelopment
	c.Spec.Coordinator.Members = 1
	c.Spec.Workers[0].Members = 1
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("development mode should allow members=1, got: %v", err)
	}
}

func TestValidate_WorkerPool_NameInvalid(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Workers[0].Name = "Pool_A" // 대문자 + underscore
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for non-DNS-1123 worker name")
	}
	if !strings.Contains(err.Error(), "DNS-1123") {
		t.Errorf("error message lacks 'DNS-1123': %v", err)
	}
}

func TestValidate_WorkerPool_NameDuplicate(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Workers = append(c.Spec.Workers, postgresv1alpha1.WorkerPoolSpec{
		Name:    "pool-a", // 중복
		Members: 3,
		Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
	})
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for duplicate worker pool name")
	}
	if !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("error message lacks 'duplicated': %v", err)
	}
}

func TestValidate_WorkerPool_EvenMembers_Rejected(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Workers[0].Members = 4
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for even worker.members")
	}
}

func TestValidate_Extensions_UnknownRejected(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Extensions = []postgresv1alpha1.ExtensionSpec{{Name: "not-registered"}}
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for unregistered extension")
	}
	if !strings.Contains(err.Error(), "ExtensionPlugin") {
		t.Errorf("error message lacks 'ExtensionPlugin': %v", err)
	}
}

func TestValidate_Extensions_KnownAccepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Extensions = []postgresv1alpha1.ExtensionSpec{{Name: "pgaudit"}}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("pgaudit extension should be accepted (registered by default): %v", err)
	}
}

func TestValidate_Update_AppliesSameRules(t *testing.T) {
	w := newWebhook(t)
	old := validBaseCluster()
	updated := validBaseCluster()
	updated.Spec.Coordinator.Members = 2 // even
	_, err := w.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Fatal("expected rejection on update with even members")
	}
}

// TestValidate_NoResources_Smoke은 Resources 미지정이 검증 거절을 일으키지 않음을
// 확인한다(빌더가 nil-safe인지 회귀 테스트).
func TestValidate_NoResources_Smoke(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Coordinator.Resources = corev1.ResourceRequirements{}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("empty Resources should be allowed, got: %v", err)
	}
}
