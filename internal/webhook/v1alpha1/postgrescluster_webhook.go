/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package v1alpha1은 PostgresCluster CR의 admission webhook 구현이다.
//
// 본 webhook은 RFC 0001 §4 검증 규칙을 단일 출처로 강제한다.
// CRD 스키마(api/v1alpha1/postgrescluster_types.go)의 kubebuilder 마커가
// 표현 가능한 제약(필수, enum, 패턴, 최소값)은 K8s API server가 거절하고,
// 본 webhook은 cross-field 의존성과 도메인 의미론(matrix lookup, 홀수 강제,
// production 모드 멤버 하한, pool 이름 unique)을 처리한다.
//
// 강제 규칙(RFC 0001 §4):
//
//  1. coordinator.members 홀수 + ≥1
//  2. workers[].members 홀수 + ≥1
//  3. workers[].name DNS-1123 + 동일 클러스터 내 unique
//  4. routers.replicas ≥1 (CRD minimum과 중복이지만 명시적 강제)
//  5. postgres ∈ matrix.IsSupported. 0.3.0-alpha (ADR 0001) 이후 vanilla PG18+
//     단일 스택. AGPL/BUSL 백엔드 영구 금지 (ADR 0003).
//  6. deployment=production이면 coordinator.members ≥3, workers[].members ≥3
//  7. extensions[].name 화이트리스트 — 본 webhook은 P10-T2 시점에 활성화
//     (현재는 ExtensionPlugin Registry에 등록된 이름만 허용)
package v1alpha1

import (
	"context"
	"fmt"
	"regexp"

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

// dns1123Label은 DNS-1123 label 형식 정규식이다.
// kubebuilder marker와 동일한 규칙을 webhook 단계에서 한 번 더 검증한다.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// PostgresClusterWebhook은 ValidatingWebhook 핸들러를 보유한다.
type PostgresClusterWebhook struct {
	// FeatureGates는 reconciler와 동일한 인스턴스를 공유한다(PG18 같은 격리
	// 채널 결정에 사용).
	FeatureGates map[string]bool

	// Plugins는 ExtensionSpec.Name 화이트리스트 검증에 사용된다. nil이면
	// extensions 검증을 건너뛴다(P10-T2 활성화 전 단계).
	Plugins *plugin.Registry
}

// SetupPostgresClusterWebhookWithManager는 본 webhook을 controller-runtime
// Manager에 등록한다.
func SetupPostgresClusterWebhookWithManager(mgr ctrl.Manager, gates map[string]bool, plugins *plugin.Registry) error {
	return ctrl.NewWebhookManagedBy(mgr, &postgresv1alpha1.PostgresCluster{}).
		WithValidator(&PostgresClusterWebhook{FeatureGates: gates, Plugins: plugins}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-postgres-keiailab-io-v1alpha1-postgrescluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=postgres.keiailab.io,resources=postgresclusters,verbs=create;update,versions=v1alpha1,name=vpostgrescluster.kb.io,admissionReviewVersions=v1

// controller-runtime v0.23+는 generic admission.Validator[T]를 사용한다.
// (webhook.CustomValidator는 Validator[runtime.Object]의 비-generic 별칭일 뿐)
// 본 어셔션이 시그니처 변경을 컴파일 타임에 감지한다.
var _ admission.Validator[*postgresv1alpha1.PostgresCluster] = &PostgresClusterWebhook{}

// ValidateCreate는 새로 생성되는 PostgresCluster를 검증한다.
func (w *PostgresClusterWebhook) ValidateCreate(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	logger := logf.FromContext(ctx).WithValues("postgrescluster", cluster.Name)
	logger.Info("Validating create")
	return w.validate(cluster)
}

// ValidateUpdate는 기존 PostgresCluster의 spec 변경을 검증한다.
// 현재는 ValidateCreate와 동일 규칙. immutable 필드 보호는 Pillar P9(Upgrade)
// 시점에 추가된다(예: spec.version.postgres downgrade 거절).
func (w *PostgresClusterWebhook) ValidateUpdate(ctx context.Context, _ *postgresv1alpha1.PostgresCluster, newObj *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	logger := logf.FromContext(ctx).WithValues("postgrescluster", newObj.Name)
	logger.Info("Validating update")
	return w.validate(newObj)
}

// ValidateDelete는 현재 검증 없음. finalizer 정책은 Pillar P4(Backup) 시점에 추가.
func (w *PostgresClusterWebhook) ValidateDelete(_ context.Context, _ *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	return nil, nil
}

// validate는 ValidateCreate/ValidateUpdate 공통 검증 로직이다.
func (w *PostgresClusterWebhook) validate(c *postgresv1alpha1.PostgresCluster) (admission.Warnings, error) {
	gv := schema.GroupKind{Group: postgresv1alpha1.GroupVersion.Group, Kind: "PostgresCluster"}

	// 1) 버전 매트릭스 검증 (ADR 0001: vanilla PG18+ 단일 스택).
	if _, ok := version.IsSupported(c.Spec.Version.Postgres, w.FeatureGates); !ok {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.version",
			fmt.Sprintf("postgres=%q is not in supported matrix (see internal/version/matrix.go)",
				c.Spec.Version.Postgres)))
	}

	// 2) coordinator.members 홀수 + production 하한
	if c.Spec.Coordinator.Members%2 == 0 {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.coordinator.members",
			fmt.Sprintf("must be odd (got %d) to prevent split-brain in lease election (RFC 0003)", c.Spec.Coordinator.Members)))
	}
	if isProduction(c) && c.Spec.Coordinator.Members < 3 {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.coordinator.members",
			fmt.Sprintf("production deployment requires members >=3 (got %d)", c.Spec.Coordinator.Members)))
	}

	// 3) workers
	if len(c.Spec.Workers) == 0 {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.workers",
			"at least one worker pool is required"))
	}
	seen := make(map[string]struct{}, len(c.Spec.Workers))
	for i, pool := range c.Spec.Workers {
		path := fmt.Sprintf("spec.workers[%d]", i)

		if !dns1123Label.MatchString(pool.Name) {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr(path+".name",
				fmt.Sprintf("%q is not a valid DNS-1123 label", pool.Name)))
		}
		if _, dup := seen[pool.Name]; dup {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr(path+".name",
				fmt.Sprintf("worker pool name %q is duplicated", pool.Name)))
		}
		seen[pool.Name] = struct{}{}

		if pool.Members%2 == 0 {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr(path+".members",
				fmt.Sprintf("must be odd (got %d)", pool.Members)))
		}
		if isProduction(c) && pool.Members < 3 {
			return nil, apierrors.NewInvalid(gv, c.Name, fieldErr(path+".members",
				fmt.Sprintf("production deployment requires members >=3 (got %d)", pool.Members)))
		}
	}

	// 4) routers — replicas는 CRD minimum=1로 보장됐지만 명시적으로 한 번 더.
	if c.Spec.Routers.Replicas < 1 {
		return nil, apierrors.NewInvalid(gv, c.Name, fieldErr("spec.routers.replicas",
			fmt.Sprintf("must be >=1 (got %d)", c.Spec.Routers.Replicas)))
	}

	// 5) extensions — Plugin Registry가 있을 때만 화이트리스트 검사
	if w.Plugins != nil {
		for i, ext := range c.Spec.Extensions {
			path := fmt.Sprintf("spec.extensions[%d].name", i)
			if _, ok := w.Plugins.Extension(ext.Name); !ok {
				return nil, apierrors.NewInvalid(gv, c.Name, fieldErr(path,
					fmt.Sprintf("%q is not a registered ExtensionPlugin (see ADR 0005, internal/plugin/extension/)", ext.Name)))
			}
		}
	}

	return nil, nil
}

// isProduction은 deployment 모드가 production인지 검사한다.
// CRD의 default가 production이지만, 빈 문자열로 들어오는 경우(생성 직후 webhook)
// 도 production으로 취급한다.
func isProduction(c *postgresv1alpha1.PostgresCluster) bool {
	return c.Spec.Deployment != postgresv1alpha1.DeploymentDevelopment
}

// fieldErr는 apierrors.NewInvalid에 넘길 단일 항목 field.ErrorList를 만든다.
// 본 webhook은 첫 위반에서 즉시 거절하므로 ErrorList는 항상 1건이다.
func fieldErr(path, detail string) field.ErrorList {
	return field.ErrorList{
		field.Invalid(field.NewPath(path), nil, detail),
	}
}
