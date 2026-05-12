/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ImageCatalogRef 는 CloudNativePG 의 spec.imageCatalogRef 와 같은 필드 형태로
// PostgresCluster 가 사용할 PostgreSQL runtime image 를 catalog 에서 선택한다.
type ImageCatalogRef struct {
	// APIGroup 은 catalog API group 이다. 빈 값, postgres.keiailab.io, postgresql.cnpg.io 를 허용한다.
	// postgresql.cnpg.io 는 CNPG manifest 이식 편의를 위한 호환 입력이며, 실제 조회는
	// keiailab CRD 에 대해 수행한다.
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// Kind 는 ImageCatalog 또는 ClusterImageCatalog 이다.
	// +kubebuilder:validation:Enum=ImageCatalog;ClusterImageCatalog
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name 은 catalog 이름이다.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Major 는 catalog 안에서 선택할 PostgreSQL major version 이다.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Major int32 `json:"major"`
}

// ImageCatalogEntry 는 PostgreSQL major version 하나에 대응하는 operand image 다.
type ImageCatalogEntry struct {
	// Major 는 PostgreSQL major version 이다. catalog 안에서 유일해야 한다.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Required
	Major int32 `json:"major"`

	// Image 는 해당 major 에 사용할 immutable image reference 다. production catalog 는
	// digest pin 을 권장한다.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Extensions 는 이 이미지와 함께 사용할 수 있는 extension image 후보 목록이다.
	// 0.3.0-alpha 에서는 저장/검증 표면만 제공하고, 실제 volume-extension mount 는
	// 후속 단계에서 연결한다.
	// +optional
	Extensions []ImageCatalogExtension `json:"extensions,omitempty"`
}

// ImageCatalogExtension 은 catalog entry 에 연결된 extension image metadata 다.
type ImageCatalogExtension struct {
	// Name 은 extension 식별자다.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Image 는 extension image reference 다.
	// +kubebuilder:validation:Required
	Image ImageCatalogExtensionImage `json:"image"`
}

// ImageCatalogExtensionImage 는 extension image 위치를 표현한다.
type ImageCatalogExtensionImage struct {
	// Reference 는 OCI image reference 다.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Reference string `json:"reference"`
}

// ImageCatalogSpec 은 namespaced/cluster-wide catalog 가 공유하는 spec 이다.
type ImageCatalogSpec struct {
	// Images 는 PostgreSQL major version 별 image 목록이다.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=major
	// +kubebuilder:validation:Required
	Images []ImageCatalogEntry `json:"images"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=pgic
// +kubebuilder:printcolumn:name="Images",type=integer,JSONPath=".spec.images[*].major"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ImageCatalog 는 namespace 단위 PostgreSQL image catalog 다.
type ImageCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ImageCatalogSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ImageCatalogList 는 ImageCatalog 컬렉션이다.
type ImageCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageCatalog `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pgcic
// +kubebuilder:printcolumn:name="Images",type=integer,JSONPath=".spec.images[*].major"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ClusterImageCatalog 는 cluster 전체에서 공유하는 PostgreSQL image catalog 다.
type ClusterImageCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ImageCatalogSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterImageCatalogList 는 ClusterImageCatalog 컬렉션이다.
type ClusterImageCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterImageCatalog `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageCatalog{}, &ImageCatalogList{}, &ClusterImageCatalog{}, &ClusterImageCatalogList{})
}
