/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// 본 단위 테스트는 RFC 0001 §4 의 *cross-field 도메인 의미* 검증 (F01a 범위) 만
// 다룬다. enum / min / max / required / CEL XValidation 3 개는 CRD schema 가
// 직접 거부하므로 webhook 단위에서는 검증하지 않는다 (envtest 통합은 F01b).

func validBaseCluster() *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			PostgresVersion: "18",
			ShardingMode:    postgresv1alpha1.ShardingModeNone,
			Shards: postgresv1alpha1.ShardsSpec{
				InitialCount: 1,
				Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
				Replicas:     1,
			},
		},
	}
}

func newWebhook(t *testing.T) *PostgresClusterWebhook {
	t.Helper()
	return &PostgresClusterWebhook{
		FeatureGates: map[string]bool{},
		Plugins:      plugin.NewRegistry(),
	}
}

func TestValidate_Happy(t *testing.T) {
	w := newWebhook(t)
	if _, err := w.ValidateCreate(context.Background(), validBaseCluster()); err != nil {
		t.Fatalf("expected nil error for valid cluster, got: %v", err)
	}
}

func TestValidate_PG18_Accepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.PostgresVersion = "18"
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("PG18 must be Stable: %v", err)
	}
}

func TestValidate_VersionRejected_NotInMatrix(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.PostgresVersion = "99"
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for unsupported postgres version")
	}
	if !strings.Contains(err.Error(), "supported matrix") {
		t.Errorf("error message lacks 'supported matrix': %v", err)
	}
}

func TestValidate_EmptyVersion_DefaultsTo18(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.PostgresVersion = "" // CRD default 미적용 경로
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("empty postgresVersion must default to 18 inside webhook: %v", err)
	}
}

func TestValidate_AutoSplitEnabled_RequiresAtLeastOneTrigger(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.ShardingMode = postgresv1alpha1.ShardingModeNative
	c.Spec.AutoSplit = &postgresv1alpha1.AutoSplitSpec{
		Enabled:  true,
		Triggers: &postgresv1alpha1.AutoSplitTriggers{}, // 모두 0
	}
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for autoSplit.enabled=true without any trigger")
	}
	if !strings.Contains(err.Error(), "trigger") {
		t.Errorf("error message lacks 'trigger': %v", err)
	}
}

func TestValidate_AutoSplitEnabled_WithTrigger_Accepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.ShardingMode = postgresv1alpha1.ShardingModeNative
	c.Spec.AutoSplit = &postgresv1alpha1.AutoSplitSpec{
		Enabled:  true,
		Triggers: &postgresv1alpha1.AutoSplitTriggers{SizeThresholdGB: 100},
	}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("autoSplit with trigger should be accepted: %v", err)
	}
}

func TestValidate_AutoSplitDisabled_NoTriggerOk(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.AutoSplit = &postgresv1alpha1.AutoSplitSpec{Enabled: false}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("disabled autoSplit must not require triggers: %v", err)
	}
}

func TestValidate_BackupEnabled_RequiresSchedule(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Backup = &postgresv1alpha1.ClusterBackupSpec{Enabled: true} // schedule=""
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for backup.enabled=true with empty schedule")
	}
	if !strings.Contains(err.Error(), "schedule") {
		t.Errorf("error message lacks 'schedule': %v", err)
	}
}

func TestValidate_BackupEnabled_WithSchedule_Accepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Backup = &postgresv1alpha1.ClusterBackupSpec{
		Enabled:  true,
		Schedule: "0 2 * * *",
	}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("backup with schedule should be accepted: %v", err)
	}
}

func TestValidate_StorageSize_BelowMin_Rejected(t *testing.T) {
	// 1Gi 하한 invariant.
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Shards.Storage.Size = resource.MustParse("512Mi")
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for storage.size < 1Gi")
	}
	if !strings.Contains(err.Error(), "storage.size") {
		t.Errorf("expected storage.size keyword in error, got: %v", err)
	}
}

func TestValidate_TLS_Enabled_NoIssuer_Rejected(t *testing.T) {
	// Phase 2 (alpha.6) — IssuerRef 필수. 미설정 시 reject.
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.TLS = &postgresv1alpha1.TLSSpec{Enabled: true}
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("expected rejection for spec.tls.enabled=true without issuerRef (Phase 2)")
	}
	if !strings.Contains(err.Error(), "issuerRef") {
		t.Errorf("expected issuerRef keyword in error, got: %v", err)
	}
}

func TestValidate_TLS_Enabled_WithIssuer_Accepted(t *testing.T) {
	// Phase 2 — TLS.Enabled=true + IssuerRef 명시 시 admission 통과.
	// reconciler 가 Certificate CR 자동 emit. Phase 3 까지는 STS volume mount 미통합.
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.TLS = &postgresv1alpha1.TLSSpec{
		Enabled:   true,
		IssuerRef: &postgresv1alpha1.TLSIssuerRef{Name: "keiailab-ca", Kind: "ClusterIssuer"},
	}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("tls.enabled=true with IssuerRef should be accepted, got: %v", err)
	}
}

func TestValidate_TLS_Disabled_Accepted(t *testing.T) {
	// TLS 미설정 또는 enabled=false 는 통과 (default behavior).
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.TLS = &postgresv1alpha1.TLSSpec{Enabled: false}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("tls.enabled=false should be accepted, got: %v", err)
	}
}

func TestValidate_StorageSize_Boundary1Gi_Accepted(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Shards.Storage.Size = resource.MustParse("1Gi")
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("exactly 1Gi should be accepted, got: %v", err)
	}
}

func TestValidate_StorageSize_Zero_Skipped(t *testing.T) {
	// CRD default 가 채워지지 않은 dry-run path — IsZero() skip 동작 확인
	// (Type B defensive, ADR-0017).
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Shards.Storage.Size = resource.Quantity{}
	// 다른 violation 없으므로 통과 (storage 영역 만 zero).
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Errorf("zero size should skip (Type B defensive), got: %v", err)
	}
}

func TestValidate_Update_AppliesSameRules(t *testing.T) {
	w := newWebhook(t)
	old := validBaseCluster()
	updated := validBaseCluster()
	updated.Spec.PostgresVersion = "99" // matrix 미등록
	_, err := w.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Fatal("expected rejection on update with unsupported version")
	}
}

// RFC 0006 R1 — spec.extensions 검증.
func TestValidate_Extensions_AllRegistered_Accepted(t *testing.T) {
	w := newWebhook(t)
	// Registry 에 dummy extension 등록 후 cluster 가 그것 opt-in.
	w.Plugins.RegisterExtension(testExtension{name: "pgaudit"})
	c := validBaseCluster()
	c.Spec.Extensions = []string{"pgaudit"}
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("registered extension should be accepted: %v", err)
	}
}

func TestValidate_Extensions_UnknownRejected(t *testing.T) {
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Extensions = []string{"nonexistent"}
	_, err := w.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Fatal("unknown extension should be rejected")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error must name the missing extension: %v", err)
	}
}

func TestValidate_Extensions_Empty_NoCheck(t *testing.T) {
	// nil/빈 extensions → vanilla PG, 검증 우회 (cross-validation bug 2 fix 핵심).
	w := newWebhook(t)
	c := validBaseCluster()
	c.Spec.Extensions = nil
	if _, err := w.ValidateCreate(context.Background(), c); err != nil {
		t.Fatalf("empty extensions must pass: %v", err)
	}
}

// testExtension 은 webhook 테스트용 최소 ExtensionPlugin 구현.
type testExtension struct{ name string }

func (e testExtension) Name() string                                   { return e.name }
func (e testExtension) SharedPreloadOrder() int                        { return 100 }
func (e testExtension) PreInstall(_ context.Context, _ *sql.DB) error  { return nil }
func (e testExtension) PostInstall(_ context.Context, _ *sql.DB) error { return nil }
func (e testExtension) Validate(_ string) error                        { return nil }
