/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// 본 파일은 PostgresUser CRD 가 categories 에 "role" 을 싣지 않는지 본다(#1) — 실으면
// `kubectl get role` 이 RBAC Role 대신 PostgresUser 를 보인다.
package chart

import (
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

// reservedCategory 는 RBAC Role 의 리소스 이름과 겹치는 카테고리다.
const reservedCategory = "role"

// userCRDCopies 는 PostgresUser CRD 의 세 사본이다(생성 · 차트 · OLM 번들).
var userCRDCopies = []string{
	"../../config/crd/bases/postgres.keiailab.io_postgresusers.yaml",
	chartPath + "/crds/postgres.keiailab.io_postgresusers.yaml",
	"../../bundle/manifests/postgres.keiailab.io_postgresusers.yaml",
}

type crdNames struct {
	Spec struct {
		Names struct {
			Categories []string `json:"categories"`
		} `json:"names"`
	} `json:"spec"`
}

// TestUserCRDNoRoleCategory 는 세 사본 어디에도 "role" 카테고리가 없는지 본다.
func TestUserCRDNoRoleCategory(t *testing.T) {
	t.Parallel()
	for _, path := range userCRDCopies {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		var doc crdNames
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if slices.Contains(doc.Spec.Names.Categories, reservedCategory) {
			t.Errorf("%s categories %v contain %q", path, doc.Spec.Names.Categories, reservedCategory)
		}
	}
}
