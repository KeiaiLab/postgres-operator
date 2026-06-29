/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestReshardSpecPreservesRangeVindexType(t *testing.T) {
	spec := reshardSpec("range", "tenant_id", "", "target-a:a:m,target-b:n:z")
	if spec.Vindex.Type != v1alpha1.VindexTypeRange {
		t.Fatalf("vindex type = %q, want %q", spec.Vindex.Type, v1alpha1.VindexTypeRange)
	}
	if spec.Vindex.Column != "tenant_id" {
		t.Fatalf("vindex column = %q, want tenant_id", spec.Vindex.Column)
	}
	if spec.Vindex.Function != "" {
		t.Fatalf("range vindex function = %q, want empty", spec.Vindex.Function)
	}
	if len(spec.Ranges) != 2 {
		t.Fatalf("ranges = %d, want 2", len(spec.Ranges))
	}
}

func TestReshardSpecDefaultsToHashMurmur3(t *testing.T) {
	spec := reshardSpec("", "id", "", "")
	if spec.Vindex.Type != v1alpha1.VindexTypeHash {
		t.Fatalf("vindex type = %q, want %q", spec.Vindex.Type, v1alpha1.VindexTypeHash)
	}
	if spec.Vindex.Function != "murmur3" {
		t.Fatalf("vindex function = %q, want murmur3", spec.Vindex.Function)
	}
	if len(spec.Ranges) != 2 {
		t.Fatalf("default ranges = %d, want 2", len(spec.Ranges))
	}
}
