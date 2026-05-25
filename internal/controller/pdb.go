/*
Copyright 2026 Keiailab.
*/

package controller

import (
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ShardPDBName — shard 별 PDB 이름. STS 와 동일 명명 규칙.
func ShardPDBName(clusterName string, shardOrdinal int32) string {
	return ShardStatefulSetName(clusterName, shardOrdinal) + "-pdb"
}

// shouldAutoCreatePDB — HA defaults.
//
// 진리표:
//
//	members >= 2 → true (HA 보호: minAvailable=members-1)
//	members < 2  → false (단일 pod = HA 의미 없음)
//
// 사용자 PDB 명시는 별도 spec field 부재 (postgres-operator 는 항상 자동 default).
// 향후 사용자 opt-out 필요 시 Spec.Shards.PodDisruptionBudget {Enabled bool}
// 추가 (별도 epic).
func shouldAutoCreatePDB(members int32) bool {
	return members >= 2
}

// BuildShardPDB — shard 별 PodDisruptionBudget. minAvailable = members-1 (3
// member shard → minAvailable=2). voluntary disruption 시 quorum 보호.
func BuildShardPDB(cluster *postgresv1alpha1.PostgresCluster, shardOrdinal, members int32) *policyv1.PodDisruptionBudget {
	minAvailable := intstr.FromInt(int(members) - 1)
	labels := SelectorLabels(cluster.Name, "shard", shardOrdinal)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ShardPDBName(cluster.Name, shardOrdinal),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:     &metav1.LabelSelector{MatchLabels: labels},
			MinAvailable: &minAvailable,
		},
	}
}

// BuildPoolerPDB — Pooler 별 PodDisruptionBudget. instances=3 이면
// minAvailable=2 로 voluntary disruption 중에도 접속 계층 과반을 유지한다.
func BuildPoolerPDB(pooler *postgresv1alpha1.Pooler, instances int32) *policyv1.PodDisruptionBudget {
	minAvailable := intstr.FromInt(int(instances) - 1)
	labels := poolerLabels(pooler)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PoolerPDBName(pooler.Name),
			Namespace: pooler.Namespace,
			Labels:    labels,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:     &metav1.LabelSelector{MatchLabels: labels},
			MinAvailable: &minAvailable,
		},
	}
}
