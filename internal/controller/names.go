/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import "fmt"

// 본 파일은 reconciler가 생성하는 K8s 자원 이름을 단일 출처로 모은다.
// 명명 규약은 RFC 0001 PostgresCluster CRD v2 의 shard ordinal 모델을 따른다:
//   - 자원 이름은 PostgresCluster.metadata.name 접두사를 사용한다.
//   - 역할은 "shard" 또는 "router".
//   - shard 식별자는 0-based ordinal (e.g. shard-0, shard-1).
//   - K8s lease는 ADR 0002 명명 규약 "<cluster>-<role>-primary"를 따름.

// ShardStatefulSetName은 shard ordinal 의 StatefulSet 이름을 반환한다.
// StatefulSet 의 pod 들은 <name>-0 (primary), <name>-1.. (async replicas) 가 된다.
func ShardStatefulSetName(cluster string, ordinal int32) string {
	return fmt.Sprintf("%s-shard-%d", cluster, ordinal)
}

// ShardServiceName은 shard 의 headless Service 이름을 반환한다.
// pod 의 안정적 DNS = <pod>.<service>.<ns>.svc.cluster.local.
func ShardServiceName(cluster string, ordinal int32) string {
	return fmt.Sprintf("%s-shard-%d-headless", cluster, ordinal)
}

// ShardConfigMapName은 shard 의 postgresql.conf 등을 담는 ConfigMap 이름.
func ShardConfigMapName(cluster string, ordinal int32) string {
	return fmt.Sprintf("%s-shard-%d-config", cluster, ordinal)
}

// RouterDeploymentName은 QueryRouter Deployment 이름을 반환한다.
// PVC 부재(ADR 0003)이므로 Deployment를 사용한다(StatefulSet 아님).
func RouterDeploymentName(cluster string) string {
	return fmt.Sprintf("%s-router", cluster)
}

// RouterServiceName은 클라이언트 진입점이 되는 ClusterIP Service 이름을 반환한다.
// 사용자가 "host=<cluster>-router" 형태로 접속.
func RouterServiceName(cluster string) string {
	return fmt.Sprintf("%s-router", cluster)
}

// RouterConfigMapName은 라우터 PgBouncer 등의 설정을 담는 ConfigMap.
func RouterConfigMapName(cluster string) string {
	return fmt.Sprintf("%s-router-config", cluster)
}

// SelectorLabels는 부모 PostgresCluster + 역할 + shard ordinal 식별 레이블이다.
// reconciler가 Service의 selector와 Pod template label에 동일하게 적용한다.
//
// router 처럼 shard 차원이 없는 자원은 ordinal=-1 을 전달한다 — 이때 pool 레이블
// 자체가 부재한다 (router 의 backend 는 모든 shard 이므로 ordinal 분리 무의미).
func SelectorLabels(cluster, role string, shardOrdinal int32) map[string]string {
	out := map[string]string{
		"app.kubernetes.io/name":       "postgrescluster",
		"app.kubernetes.io/instance":   cluster,
		"app.kubernetes.io/component":  role,
		"app.kubernetes.io/managed-by": "keiailab-postgres-operator",
	}
	if shardOrdinal >= 0 {
		out["postgres.keiailab.io/shard"] = fmt.Sprintf("%d", shardOrdinal)
	}
	return out
}
