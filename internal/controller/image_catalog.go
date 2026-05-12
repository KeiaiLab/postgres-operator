/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/version"
)

const (
	cnpgAPIGroup = "postgresql.cnpg.io"
)

type resolvedPostgresImage struct {
	Image         string
	PostgresMajor string
}

func imageMajorFromSpec(cluster *postgresv1alpha1.PostgresCluster) string {
	if cluster != nil && cluster.Spec.ImageCatalogRef != nil && cluster.Spec.ImageCatalogRef.Major > 0 {
		return strconv.Itoa(int(cluster.Spec.ImageCatalogRef.Major))
	}
	if cluster == nil || cluster.Spec.PostgresVersion == "" {
		return "18"
	}
	return cluster.Spec.PostgresVersion
}

func (r *PostgresClusterReconciler) resolvePostgresImage(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	combo version.Combo,
) (resolvedPostgresImage, error) {
	ref := cluster.Spec.ImageCatalogRef
	if ref == nil {
		return resolvedPostgresImage{Image: combo.Image, PostgresMajor: combo.PostgresMajor}, nil
	}
	if !imageCatalogAPIGroupAllowed(ref.APIGroup) {
		return resolvedPostgresImage{}, fmt.Errorf("imageCatalogRef.apiGroup %q is not supported", ref.APIGroup)
	}
	if cluster.Spec.PostgresVersion != "" && cluster.Spec.PostgresVersion != strconv.Itoa(int(ref.Major)) {
		return resolvedPostgresImage{}, fmt.Errorf("postgresVersion %q must match imageCatalogRef.major %d", cluster.Spec.PostgresVersion, ref.Major)
	}

	spec, err := r.imageCatalogSpec(ctx, cluster.Namespace, ref)
	if err != nil {
		return resolvedPostgresImage{}, err
	}
	for _, entry := range spec.Images {
		if entry.Major == ref.Major {
			if entry.Image == "" {
				return resolvedPostgresImage{}, fmt.Errorf("%s %q major %d has empty image", ref.Kind, ref.Name, ref.Major)
			}
			return resolvedPostgresImage{Image: entry.Image, PostgresMajor: strconv.Itoa(int(ref.Major))}, nil
		}
	}
	return resolvedPostgresImage{}, fmt.Errorf("%s %q does not define major %d", ref.Kind, ref.Name, ref.Major)
}

func (r *PostgresClusterReconciler) imageCatalogSpec(
	ctx context.Context,
	namespace string,
	ref *postgresv1alpha1.ImageCatalogRef,
) (postgresv1alpha1.ImageCatalogSpec, error) {
	switch ref.Kind {
	case "ImageCatalog":
		var catalog postgresv1alpha1.ImageCatalog
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &catalog); err != nil {
			return postgresv1alpha1.ImageCatalogSpec{}, fmt.Errorf("get ImageCatalog %s/%s: %w", namespace, ref.Name, err)
		}
		return catalog.Spec, nil
	case "ClusterImageCatalog":
		var catalog postgresv1alpha1.ClusterImageCatalog
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, &catalog); err != nil {
			return postgresv1alpha1.ImageCatalogSpec{}, fmt.Errorf("get ClusterImageCatalog %s: %w", ref.Name, err)
		}
		return catalog.Spec, nil
	default:
		return postgresv1alpha1.ImageCatalogSpec{}, fmt.Errorf("imageCatalogRef.kind %q is not supported", ref.Kind)
	}
}

func imageCatalogAPIGroupAllowed(apiGroup string) bool {
	return apiGroup == "" || apiGroup == postgresv1alpha1.GroupVersion.Group || apiGroup == cnpgAPIGroup
}

func imageCatalogRefMatches(ref *postgresv1alpha1.ImageCatalogRef, kind string, name string) bool {
	return ref != nil && ref.Kind == kind && ref.Name == name && imageCatalogAPIGroupAllowed(ref.APIGroup)
}

func (r *PostgresClusterReconciler) postgresClustersForImageCatalog(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	var clusters postgresv1alpha1.PostgresClusterList
	if err := r.List(ctx, &clusters, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, cluster := range clusters.Items {
		if imageCatalogRefMatches(cluster.Spec.ImageCatalogRef, "ImageCatalog", obj.GetName()) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			}})
		}
	}
	return requests
}

func (r *PostgresClusterReconciler) postgresClustersForClusterImageCatalog(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	var clusters postgresv1alpha1.PostgresClusterList
	if err := r.List(ctx, &clusters); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, cluster := range clusters.Items {
		if imageCatalogRefMatches(cluster.Spec.ImageCatalogRef, "ClusterImageCatalog", obj.GetName()) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			}})
		}
	}
	return requests
}
