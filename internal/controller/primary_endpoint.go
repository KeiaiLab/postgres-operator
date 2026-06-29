/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func primaryEndpointForShard(
	cluster *postgresv1alpha1.PostgresCluster,
	shardOrdinal int32,
	svcName string,
	replicaBootstrap *replicaBootstrapConfig,
	hibernating bool,
) string {
	if replicaBootstrap != nil {
		return replicaBootstrap.Endpoint
	}
	if cluster == nil || hibernating {
		return ""
	}

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

	if cluster.Spec.Shards.Replicas > 0 {
		stsName := ShardStatefulSetName(cluster.Name, shardOrdinal)
		return fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:%d", stsName, svcName, cluster.Namespace, pgPort)
	}
	return ""
}
