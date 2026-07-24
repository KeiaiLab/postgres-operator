/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// replicationLagUnhealthyBytes 는 replica 가 primary 대비 이 바이트 이상 뒤처지면
// (Ready 로 보고하더라도) 복제 단절/동결로 간주하는 임계값이다. 1 GiB — 동결/단절
// replica 의 WAL 위치는 primary 가 전진하는 동안 정지하므로 활성 DB 에서는 곧 이
// 임계를 넘는다(원 사고: 7일 동결 replica). 한편 idle DB 에서 잠깐 뒤처지는 replica 는
// 데이터 손실 위험이 없어 이 임계 미만이면 flag 하지 않는다(noise 회피).
const replicationLagUnhealthyBytes int64 = 1 << 30

// reconcileReplicationHealth surfaces replica disconnection/freeze as the
// ReplicationHealthy condition (+ a Warning event on transition to unhealthy).
//
// #220 RCA (postgres-prod 2026-07-24): a replica frozen/disconnected for 7 days
// never appeared in CR status or any alert, so a stale member was silently
// promoted, destroying keycloak/forgejo data. This makes replication breakage a
// first-class, observable status: for each shard with a ready primary, a replica
// is flagged when it is not Ready (fenced/rogue/stale/pod-not-ready) OR when its
// reported WAL position lags the primary's by >= replicationLagUnhealthyBytes —
// the latter catches a fully disconnected replica whose relative LagBytes reads 0
// because both receive and replay froze at the same old position.
//
// Best-effort and side-effect-light: it only mutates cluster.Status.Conditions
// (persisted by the caller's Status().Update) and emits an event; failures here
// must never block the reconcile.
func (r *PostgresClusterReconciler) reconcileReplicationHealth(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) {
	healthy := true
	sawReplica := false
	var issues []string

	for i := range shards {
		shard := &shards[i]
		if shard.Primary == nil || !shard.Primary.Ready || len(shard.Replicas) == 0 {
			continue
		}
		primaryWAL := r.reportedWALPosition(ctx, cluster.Namespace, shard.Primary.Pod)
		for j := range shard.Replicas {
			rep := &shard.Replicas[j]
			if rep.Pod == "" || rep.Pod == shard.Primary.Pod {
				continue
			}
			sawReplica = true
			if !rep.Ready {
				reason := rep.Reason
				if reason == "" {
					reason = "not-ready"
				}
				issues = append(issues, fmt.Sprintf("%s/%s: %s", shard.Name, rep.Pod, reason))
				healthy = false
				continue
			}
			// Ready 로 보고하지만 WAL 위치가 primary 대비 크게 뒤처짐 = 동결/단절.
			if primaryWAL < 0 {
				continue // primary position unknown → cannot compare this shard
			}
			repWAL := r.reportedWALPosition(ctx, cluster.Namespace, rep.Pod)
			if repWAL >= 0 && primaryWAL-repWAL >= replicationLagUnhealthyBytes {
				issues = append(issues, fmt.Sprintf("%s/%s: %d bytes behind primary",
					shard.Name, rep.Pod, primaryWAL-repWAL))
				healthy = false
			}
		}
	}

	if !sawReplica {
		// No replicas configured (single-instance / replica-less) → not applicable.
		setCondition(&cluster.Status.Conditions, ConditionReplicationHealthy,
			metav1.ConditionTrue, ReasonReplicationNotApplic, "no replicas configured", cluster.Generation)
		return
	}

	prev := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReplicationHealthy)
	if healthy {
		setCondition(&cluster.Status.Conditions, ConditionReplicationHealthy,
			metav1.ConditionTrue, ReasonReplicasHealthy,
			"all replicas connected and following the primary", cluster.Generation)
		return
	}

	msg := "replication degraded: " + strings.Join(issues, "; ")
	setCondition(&cluster.Status.Conditions, ConditionReplicationHealthy,
		metav1.ConditionFalse, ReasonReplicaDisconnected, msg, cluster.Generation)
	// Emit only on transition into unhealthy — avoids per-reconcile event noise.
	if prev == nil || prev.Status != metav1.ConditionFalse {
		commonsevents.EmitWarningf(r.Recorder, cluster, "ReplicationDegraded", "%s", msg)
	}
}
