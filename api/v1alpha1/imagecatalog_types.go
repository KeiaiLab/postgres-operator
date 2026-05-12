/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ImageCatalogRef selects the PostgreSQL runtime image for a PostgresCluster from a catalog
// using the same field shape as CloudNativePG's spec.imageCatalogRef.
type ImageCatalogRef struct {
	// APIGroup is the catalog API group. Empty string, postgres.keiailab.io, and
	// postgresql.cnpg.io are accepted. postgresql.cnpg.io is accepted for compatibility
	// when porting CNPG manifests; lookups are still resolved against the keiailab CRDs.
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// Kind is ImageCatalog or ClusterImageCatalog.
	// +kubebuilder:validation:Enum=ImageCatalog;ClusterImageCatalog
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name is the catalog name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Major is the PostgreSQL major version to select within the catalog.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Major int32 `json:"major"`
}

// ImageCatalogEntry is the operand image for a single PostgreSQL major version.
type ImageCatalogEntry struct {
	// Major is the PostgreSQL major version. It must be unique within the catalog.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Major int32 `json:"major"`

	// Image is the immutable image reference used for this major version. Production catalogs
	// should pin by digest.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Extensions is the list of extension image candidates available with this image.
	// In 0.3.0-alpha only the storage and validation surfaces are provided; the actual
	// volume-extension mount path is wired up in a later phase.
	// +optional
	Extensions []ImageCatalogExtension `json:"extensions,omitempty"`
}

// ImageCatalogExtension is the metadata for an extension image attached to a catalog entry.
type ImageCatalogExtension struct {
	// Name is the extension identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Image is the extension image reference.
	// +kubebuilder:validation:Required
	Image ImageCatalogExtensionImage `json:"image"`
}

// ImageCatalogExtensionImage describes the location of an extension image.
type ImageCatalogExtensionImage struct {
	// Reference is the OCI image reference.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Reference string `json:"reference"`
}

// ImageCatalogSpec is the spec shared by namespaced and cluster-wide catalogs.
type ImageCatalogSpec struct {
	// Images is the list of images per PostgreSQL major version.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=major
	// +kubebuilder:validation:Required
	Images []ImageCatalogEntry `json:"images"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=pgic,categories=postgres;image;all
// +kubebuilder:printcolumn:name="Images",type=integer,JSONPath=".spec.images[*].major"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ImageCatalog is a namespace-scoped PostgreSQL image catalog.
type ImageCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ImageCatalogSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ImageCatalogList is a collection of ImageCatalog resources.
type ImageCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageCatalog `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pgcic,categories=postgres;image;all
// +kubebuilder:printcolumn:name="Images",type=integer,JSONPath=".spec.images[*].major"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ClusterImageCatalog is a cluster-wide PostgreSQL image catalog shared across namespaces.
type ClusterImageCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ImageCatalogSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterImageCatalogList is a collection of ClusterImageCatalog resources.
type ClusterImageCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterImageCatalog `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageCatalog{}, &ImageCatalogList{}, &ClusterImageCatalog{}, &ClusterImageCatalogList{})
}
