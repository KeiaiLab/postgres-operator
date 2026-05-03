/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package v1alpha1 은 PostgresCluster CR 의 ValidatingWebhook 구현이다.
//
// 본 webhook 은 RFC 0001 §4 의 *cross-field 도메인 의미* 만 강제한다.
// kubebuilder marker 가 표현 가능한 단순 제약 (enum / min / max / required) 과
// 3 개 CEL XValidation 규칙 (shardingMode↔shards, router↔native, autoSplit↔native)
// 은 CRD schema 자체에 박혀 K8s API server 가 직접 거부하므로 webhook 은
// 다음 도메인 검증에 집중한다 (F01a 범위):
//
//  1. spec.postgresVersion ∈ internal/version/matrix.go (ADR 0001 vanilla 단일)
//  2. spec.autoSplit.enabled=true 이면 triggers 중 하나 이상이 0 보다 커야 한다
//  3. spec.backup.enabled=true 이면 schedule 문자열이 비어있지 않아야 한다
//
// cron expression 정밀 parse / duration parse / monitoring interval 검증은
// F01b 또는 F02 에서 robfig/cron 도입과 함께 추가된다.
package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
	"github.com/keiailab/postgres-operator/internal/version"
)

// PostgresClusterWebhook 는 ValidatingWebhook 핸들러다.
type PostgresClusterWebhook struct {
	// FeatureGates 는 reconciler 와 동일한 인스턴스를 공유한다 (matrix lookup).
	FeatureGates map[string]bool

	// Plugins 는 P3 시점 extension 화이트리스트 검증에 사용될 예정이다.
	// 현재 schema 에는 extensions 필드가 없어 미사용 — 시그너처 유지로 호출자 (cmd/main.go)
	// 변경을 회피한다.
	Plugins *plugin.Registry
}

// SetupPostgresClusterWebhookWithManager 는 webhook 을 Manager 에 등록한다.
func SetupPostgresClusterWebhookWithManager(mgr ctrl.Manager, gates map[string]bool, plugins *plugin.Registry) error {
	return ctrl.NewWebhookManagedBy(mgr, &postgresv1alpha1.PostgresCluster{}).
		WithValidator(&PostgresClusterWebhook{FeatureGates: gates, Plugins: plugins}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-postgres-keiailab-io-v1alpha1-postgrescluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=postgres.keiailab.io,resources=postgresclusters,verbs=create;update,versions=v1alpha1,name=vpostgrescluster.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*postgresv1alpha1.PostgresCluster] = &PostgresClusterWebhook{}

func (w *PostgresClusterWebhook) ValidateCreate(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	logf.FromContext(ctx).WithValues("postgrescluster", cluster.Name).Info("Validating create")
	return w.validate(cluster)
}

func (w *PostgresClusterWebhook) ValidateUpdate(ctx context.Context, _ *postgresv1alpha1.PostgresCluster, newObj *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	logf.FromContext(ctx).WithValues("postgrescluster", newObj.Name).Info("Validating update")
	return w.validate(newObj)
}

func (w *PostgresClusterWebhook) ValidateDelete(_ context.Context, _ *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	return nil, nil
}

// validate 는 ValidateCreate / ValidateUpdate 공통 본체다.
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	gv := schema.GroupKind{Group: postgresv1alpha1.GroupVersion.Group, Kind: "PostgresCluster"}

	pgVersion := c.Spec.PostgresVersion
	if pgVersion == "" {
		// CRD default="18" 이지만 webhook 호출 시점에 default 가 채워지지 않은
		// 예외적 경로를 방어 (e.g. dry-run + omitempty).
		pgVersion = "18"
	}
	if _, ok := version.IsSupported(pgVersion, w.FeatureGates); !ok {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.postgresVersion",
			fmt.Sprintf("postgres=%q is not in supported matrix (see internal/version/matrix.go)", pgVersion)))
	}

	if as := c.Spec.AutoSplit; as != nil && as.Enabled {
		if !hasAnyTrigger(as.Triggers) {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.autoSplit.triggers",
				"at least one trigger threshold (sizeThresholdGB / p99LatencyMs / cpuPercent) must be > 0 when autoSplit.enabled=true"))
		}
	}

	if b := c.Spec.Backup; b != nil && b.Enabled && b.Schedule == "" {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.backup.schedule",
			"schedule must be non-empty when backup.enabled=true (cron expression, e.g. \"0 2 * * *\")"))
	}

	// RFC 0006 R1: spec.extensions 의 모든 이름이 operator Registry 에 등록된
	// ExtensionPlugin 이어야 함. 미등록 이름 = admission 차단.
	if len(c.Spec.Extensions) > 0 && w.Plugins != nil {
		_, missing := w.Plugins.EnabledExtensions(c.Spec.Extensions)
		if len(missing) > 0 {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.extensions",
				fmt.Sprintf("unknown extension(s): %v — operator Registry 에 미등록. 운영자에게 새 ExtensionPlugin 추가 또는 image 의 .so 보유 여부 확인 요청.", missing)))
		}
	}

	return nil, nil
}

// hasAnyTrigger 는 autoSplit triggers 중 하나라도 활성 (>0) 인지 반환한다.
func hasAnyTrigger(t *postgresv1alpha1.AutoSplitTriggers) bool {
	if t == nil {
		return false
	}
	return t.SizeThresholdGB > 0 || t.P99LatencyMs > 0 || t.CPUPercent > 0
}

// fieldErr 는 apierrors.NewInvalid 에 넘길 단일 항목 ErrorList 를 만든다.
func fieldErr(path, detail string) field.ErrorList {
	return field.ErrorList{field.Invalid(field.NewPath(path), nil, detail)}
}
