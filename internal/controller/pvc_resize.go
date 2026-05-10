/*
Copyright 2026 Keiailab.

PVC auto-resize. valkey-operator PR #39 패턴 cross-operator 이식.
StatefulSet.spec.volumeClaimTemplates 는 immutable 이므로 *기존 PVC 직접 patch* +
StorageClass.AllowVolumeExpansion 사전 검증. CSI online resize 미지원 시 PVC.status
가 FileSystemResizePending 으로 남고 다음 pod restart 시 완료.
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// dataPVCNamePrefix — STS VCT name "data" + STS name = `data-<stsName>-<ord>`.
func dataPVCNamePrefix(stsName string) string {
	return "data-" + stsName + "-"
}

// expandDataPVCs — Spec.Storage.Size 증가 시 기존 PVC 직접 patch.
// stsNames: 한 PostgresCluster 의 모든 shard STS 이름 슬라이스.
func expandDataPVCs(
	ctx context.Context,
	c client.Client,
	namespace string,
	stsNames []string,
	desiredSize resource.Quantity,
) error {
	logger := log.FromContext(ctx).WithName("pvc-resize").WithValues("namespace", namespace)
	if desiredSize.IsZero() {
		return nil
	}

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := c.List(ctx, pvcList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list PVCs in %s: %w", namespace, err)
	}

	stsPrefixes := make([]string, 0, len(stsNames))
	for _, n := range stsNames {
		stsPrefixes = append(stsPrefixes, dataPVCNamePrefix(n))
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		match := false
		for _, p := range stsPrefixes {
			if strings.HasPrefix(pvc.Name, p) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if err := expandSinglePVC(ctx, c, pvc, desiredSize); err != nil {
			logger.Error(err, "PVC expansion failed", "pvc", pvc.Name)
		}
	}
	return nil
}

func expandSinglePVC(
	ctx context.Context,
	c client.Client,
	pvc *corev1.PersistentVolumeClaim,
	desiredSize resource.Quantity,
) error {
	logger := log.FromContext(ctx).WithName("pvc-resize")
	if pvc.Status.Phase != corev1.ClaimBound {
		logger.V(1).Info("skip non-Bound PVC", "pvc", pvc.Name, "phase", pvc.Status.Phase)
		return nil
	}
	currentSize, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if !ok {
		return fmt.Errorf("PVC %s missing spec.resources.requests.storage", pvc.Name)
	}
	if desiredSize.Cmp(currentSize) <= 0 {
		return nil
	}
	// StorageClass.AllowVolumeExpansion 검증.
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		sc := &storagev1.StorageClass{}
		if err := c.Get(ctx, types.NamespacedName{Name: *pvc.Spec.StorageClassName}, sc); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("skip: StorageClass not found",
					"pvc", pvc.Name, "storageClass", *pvc.Spec.StorageClassName)
				return nil
			}
			return fmt.Errorf("get StorageClass %s: %w", *pvc.Spec.StorageClassName, err)
		}
		if sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
			logger.Info("skip: StorageClass does not allow expansion",
				"pvc", pvc.Name, "storageClass", sc.Name)
			return nil
		}
	}
	patched := pvc.DeepCopy()
	if patched.Spec.Resources.Requests == nil {
		patched.Spec.Resources.Requests = corev1.ResourceList{}
	}
	patched.Spec.Resources.Requests[corev1.ResourceStorage] = desiredSize
	if err := c.Patch(ctx, patched, client.MergeFrom(pvc)); err != nil {
		return fmt.Errorf("patch PVC %s: %w", pvc.Name, err)
	}
	logger.Info("PVC expansion patched",
		"pvc", pvc.Name, "from", currentSize.String(), "to", desiredSize.String())
	return nil
}
