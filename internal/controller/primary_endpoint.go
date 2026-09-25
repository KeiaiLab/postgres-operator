/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// primaryEndpointForShard 는 런타임 ConfigMap 의 첫 PRIMARY_ENDPOINT 폴백이다 — 관측할
// primary 가 없는 새 shard 에서만 쓴다(ADR-0030). 이후 값은 promoteAndPublish·채택만 바꾼다.
// status 기록이 있으면 그것, 없으면 결정적인 ordinal-0 DNS(초기 HA bootstrap).
func primaryEndpointForShard(cluster *postgresv1alpha1.PostgresCluster, shardOrdinal int32, svcName string) string {
	for i := range cluster.Status.Shards {
		shard := &cluster.Status.Shards[i]
		if shard.Ordinal != shardOrdinal || shard.Primary == nil {
			continue
		}
		if shard.Primary.Endpoint != "" {
			return shard.Primary.Endpoint
		}
		if shard.Primary.Pod != "" {
			return fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d", shard.Primary.Pod, svcName, cluster.Namespace, pgPort)
		}
	}

	stsName := ShardStatefulSetName(cluster.Name, shardOrdinal)
	return fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:%d", stsName, svcName, cluster.Namespace, pgPort)
}
