/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// 본 파일은 차트 CRD(D1 이 벤더링하는 파일)가 spec.postgresql 의 렌더 설정 필드를 싣는지 본다(ADR-0035) — 벤더
// CRD 에 필드가 없으면 CreateReplace 가 CR 값을 프루닝으로 잘라 렌더가 조용히 빠진다.
package chart

import (
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

// clusterCRD 는 PostgresCluster CRD 차트 파일이다.
const clusterCRD = chartPath + "/crds/postgres.keiailab.io_postgresclusters.yaml"

// crdSchema 는 OpenAPI 스키마에서 시험이 보는 칸이다.
type crdSchema struct {
	Type        string               `json:"type"`
	Format      string               `json:"format"`
	Minimum     *float64             `json:"minimum"`
	IntOrString bool                 `json:"x-kubernetes-int-or-string"`
	AnyOf       []crdSchema          `json:"anyOf"`
	Properties  map[string]crdSchema `json:"properties"`
}

type crdDocument struct {
	Spec struct {
		Versions []struct {
			Name   string `json:"name"`
			Schema struct {
				OpenAPIV3Schema crdSchema `json:"openAPIV3Schema"`
			} `json:"schema"`
		} `json:"versions"`
	} `json:"spec"`
}

// TestCRDPostgreSQLSettings 는 spec.postgresql.walKeepSize 가 quantity(int-or-string)이고 maxConnections 가
// int32 · minimum 1 인지 본다.
func TestCRDPostgreSQLSettings(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(clusterCRD)
	if err != nil {
		t.Fatalf("read %s: %v", clusterCRD, err)
	}
	var doc crdDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", clusterCRD, err)
	}
	if len(doc.Spec.Versions) == 0 {
		t.Fatalf("%s has no versions", clusterCRD)
	}
	for _, v := range doc.Spec.Versions {
		pg := v.Schema.OpenAPIV3Schema.Properties["spec"].Properties["postgresql"].Properties
		wal, ok := pg["walKeepSize"]
		types := make([]string, 0, len(wal.AnyOf))
		for _, alt := range wal.AnyOf {
			types = append(types, alt.Type)
		}
		if !ok || !wal.IntOrString || !slices.Equal(types, []string{"integer", "string"}) {
			t.Errorf("%s spec.postgresql.walKeepSize = %+v, want a quantity (int-or-string)", v.Name, wal)
		}
		conns, ok := pg["maxConnections"]
		if !ok || conns.Type != "integer" || conns.Format != "int32" || conns.Minimum == nil || *conns.Minimum != 1 {
			t.Errorf("%s spec.postgresql.maxConnections = %+v, want int32 with minimum 1", v.Name, conns)
		}
	}
}
