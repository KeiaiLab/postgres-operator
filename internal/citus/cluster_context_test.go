/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"context"
	"testing"
)

// 본 파일은 P0-6 phase 2b의 ClusterID + WithCluster + ClusterFromContext +
// SafeKey 회귀 차단 단위 테스트다.
//
// LibPQExecutor.DSNFunc가 ClusterFromContext로 cluster 식별 후 cluster별 DSN
// 을 lookup하는 패턴이 본 API에 의존한다. 시그니처/동작 변경 시 즉시 PR fail.

func TestClusterID_StringFormat(t *testing.T) {
	t.Parallel()

	c := ClusterID{Namespace: "default", Name: "my-cluster"}
	if got := c.String(); got != "default/my-cluster" {
		t.Errorf("String: want %q, got %q", "default/my-cluster", got)
	}
}

func TestClusterID_SafeKey_UsesDoubleUnderscore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   ClusterID
		want string
	}{
		{"basic", ClusterID{Namespace: "default", Name: "my-cluster"}, "default__my-cluster"},
		{"with hyphens", ClusterID{Namespace: "ns-a", Name: "pg-cluster-x"}, "ns-a__pg-cluster-x"},
		{"empty ns", ClusterID{Namespace: "", Name: "cluster"}, "__cluster"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.id.SafeKey(); got != tc.want {
				t.Errorf("SafeKey: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestWithCluster_ClusterFromContext_Roundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	enriched := WithCluster(ctx, "default", "my-cluster")

	c, ok := ClusterFromContext(enriched)
	if !ok {
		t.Fatal("ClusterFromContext: ok=false after WithCluster")
	}
	if c.Namespace != "default" || c.Name != "my-cluster" {
		t.Errorf("ClusterID: want {default, my-cluster}, got %+v", c)
	}
}

func TestClusterFromContext_AbsentReturnsFalse(t *testing.T) {
	t.Parallel()

	// Background ctx에 ClusterID가 set되지 않은 상태.
	_, ok := ClusterFromContext(context.Background())
	if ok {
		t.Error("ClusterFromContext: ok=true on bare context (want false)")
	}
}

func TestWithCluster_DoesNotMutateOriginalContext(t *testing.T) {
	t.Parallel()

	// WithCluster는 새 ctx를 반환해야 — 원본 ctx에 cluster가 set되면 안 됨.
	original := context.Background()
	_ = WithCluster(original, "default", "my-cluster")

	if _, ok := ClusterFromContext(original); ok {
		t.Error("WithCluster: 원본 context에 ClusterID가 누출됨 (immutability 위반)")
	}
}
