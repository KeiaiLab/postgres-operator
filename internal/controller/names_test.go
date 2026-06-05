/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"strings"
	"testing"
)

// 본 파일은 G3 online-resharding 의 target shard *격리 식별* (ADR-0027) 회귀
// 차단용 단위 테스트다.
//
// 핵심 불변식: resharding target shard 는 라이브 cluster 의 ordinal shard 모델과
// *격리된 식별 namespace* 를 사용해야 한다. 격리가 깨지면 (예: 누군가 target 에
// ordinal `postgres.keiailab.io/shard` label 을 부여하면) aggregateShardStatus /
// failover 가 transient target 을 라이브 shard 로 오인 → #220-class identity
// 혼동(데이터 손실)이 재발한다. 본 테스트가 그 회귀를 PR 단계에서 차단한다.

func TestTargetShardNaming_IsolatedFromOrdinal(t *testing.T) {
	t.Parallel()

	const (
		cluster = "pg"
		shardID = "shard-0a"
	)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"statefulset", TargetShardStatefulSetName(cluster, shardID), "pg-rsd-shard-0a"},
		{"service", TargetShardServiceName(cluster, shardID), "pg-rsd-shard-0a-headless"},
		{"configmap", TargetShardConfigMapName(cluster, shardID), "pg-rsd-shard-0a-config"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestTargetShardNaming_NoCollisionWithOrdinal 는 target shard 자원 이름이 ordinal
// shard 자원 이름과 *prefix 로 분리* 되어 collision 이 불가능함을 봉인한다. ordinal
// 은 `-shard-<n>`, target 은 `-rsd-<id>` segment 를 쓴다 (ADR-0027).
func TestTargetShardNaming_NoCollisionWithOrdinal(t *testing.T) {
	t.Parallel()

	const cluster = "pg"

	ordinalSTS := ShardStatefulSetName(cluster, 0)         // pg-shard-0
	targetSTS := TargetShardStatefulSetName(cluster, "0a") // pg-rsd-0a

	if ordinalSTS == targetSTS {
		t.Fatalf("ordinal 과 target STS 이름이 충돌: %q", ordinalSTS)
	}
	if !strings.Contains(targetSTS, "-rsd-") {
		t.Errorf("target STS 이름에 -rsd- segment 부재: %q", targetSTS)
	}
	if strings.Contains(targetSTS, "-shard-") {
		t.Errorf("target STS 이름이 ordinal -shard- segment 를 포함(격리 위반): %q", targetSTS)
	}
}

// TestReshardTargetSelectorLabels_ExcludesOrdinalShardLabel 은 ADR-0027 의 핵심
// 격리 불변식을 봉인한다 — target label 집합이 ordinal `postgres.keiailab.io/shard`
// key 를 *절대 포함하지 않아야* 한다 (포함 시 #220-class 혼동 재발).
func TestReshardTargetSelectorLabels_ExcludesOrdinalShardLabel(t *testing.T) {
	t.Parallel()

	labels := ReshardTargetSelectorLabels("pg", "shard-0a")

	// (1) ordinal shard label 부재 — #220-class 격리의 핵심.
	if _, ok := labels["postgres.keiailab.io/shard"]; ok {
		t.Fatalf("reshard target 이 ordinal shard label 보유 — failover/status 격리 위반: %v", labels)
	}

	// (2) reshard-target label 로 식별.
	if got := labels[ReshardTargetLabelKey]; got != "shard-0a" {
		t.Errorf("%s = %q, want %q", ReshardTargetLabelKey, got, "shard-0a")
	}

	// (3) component 가 "reshard-target" — broad component=shard 와 분리.
	if got := labels["app.kubernetes.io/component"]; got != "reshard-target" {
		t.Errorf("component = %q, want %q", got, "reshard-target")
	}

	// (4) 표준 4-key convention 의 instance/managed-by 보존.
	if got := labels["app.kubernetes.io/instance"]; got != "pg" {
		t.Errorf("instance = %q, want %q", got, "pg")
	}
	if got := labels["app.kubernetes.io/managed-by"]; got != "keiailab-postgres-operator" {
		t.Errorf("managed-by = %q, want %q", got, "keiailab-postgres-operator")
	}
}
