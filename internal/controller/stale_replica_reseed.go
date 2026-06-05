/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	// staleStandbyReseedTimeout 은 정상 부팅 + pg_basebackup 시간을 충분히 넘기는
	// 기준. 이 시간 이상 not-ready 인 standby 는 rejoin 실패로 간주한다 (#205).
	staleStandbyReseedTimeout = 8 * time.Minute
	// reseedCooldown 은 동일 standby 의 연속 re-seed 사이 최소 간격. fresh basebackup
	// 이 반복 실패할 때 무한 re-seed 루프를 막는다.
	reseedCooldown = 10 * time.Minute
	// reseedAnnotationPrefix + <pod> 는 cluster annotation 에 마지막 re-seed 시각
	// (RFC3339) 을 기록해 cooldown 을 추적한다.
	reseedAnnotationPrefix = "postgres.keiailab.io/last-reseed-"
)

// reconcileStaleReplicas re-seeds a standby that failed to rejoin after a
// primary restart/failover (#205). A standby that stays not-ready for
// staleStandbyReseedTimeout while its shard already has a ready primary is
// treated as stuck (e.g. startup recovery "waiting for WAL", streaming never
// established). The fix mirrors the verified manual recovery: delete the pod +
// its PVC so the StatefulSet recreates it with a fresh pg_basebackup from the
// current primary.
//
// Conservative by design:
//   - requires a ready primary in the same shard (the replication source);
//   - only acts after staleStandbyReseedTimeout (well past a normal boot);
//   - a per-pod cooldown prevents re-seed loops if basebackup keeps failing.
//
// Best-effort: errors are returned to the caller which logs and continues.
func (r *PostgresClusterReconciler) reconcileStaleReplicas(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardStatuses []postgresv1alpha1.ShardStatus,
	now time.Time,
) error {
	for _, ss := range shardStatuses {
		if ss.Primary == nil || !ss.Primary.Ready {
			continue // no ready replication source — never re-seed
		}
		for _, rep := range ss.Replicas {
			if rep.Ready || rep.Pod == "" {
				continue
			}
			var pod corev1.Pod
			if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: rep.Pod}, &pod); err != nil {
				continue // pod gone or unreadable — skip
			}
			if now.Sub(pod.CreationTimestamp.Time) < staleStandbyReseedTimeout {
				continue // still within the normal boot/basebackup window
			}
			cdKey := reseedAnnotationPrefix + rep.Pod
			if last := cluster.Annotations[cdKey]; last != "" {
				if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < reseedCooldown {
					continue // re-seeded recently; let it settle
				}
			}
			if err := r.reseedStandby(ctx, cluster, rep.Pod); err != nil {
				return err
			}
			before := cluster.DeepCopy()
			if cluster.Annotations == nil {
				cluster.Annotations = map[string]string{}
			}
			cluster.Annotations[cdKey] = now.UTC().Format(time.RFC3339)
			if err := r.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
				return fmt.Errorf("record re-seed cooldown for %q: %w", rep.Pod, err)
			}
			if r.Recorder != nil {
				r.Recorder.Eventf(cluster, nil, corev1.EventTypeWarning, "StandbyReseeded", "StandbyReseeded",
					"standby %s not ready for %s with a ready primary; re-seeding (delete pod+PVC → fresh pg_basebackup)",
					rep.Pod, staleStandbyReseedTimeout)
			}
		}
	}
	return nil
}

// reseedStandby deletes the standby pod and its data PVC (`data-<pod>`). The
// StatefulSet controller recreates both, and the init container performs a
// fresh pg_basebackup from the current primary. Idempotent w.r.t. absent
// objects.
func (r *PostgresClusterReconciler) reseedStandby(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	podName string,
) error {
	pvcName := "data-" + podName
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: pvcName}, &pvc); err == nil {
		if err := r.Delete(ctx, &pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale standby pvc %q: %w", pvcName, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get stale standby pvc %q: %w", pvcName, err)
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: podName}, &pod); err == nil {
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale standby pod %q: %w", podName, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get stale standby pod %q: %w", podName, err)
	}
	return nil
}

// reconcileRoguePrimaries reseeds any shard member flagged as a rogue primary
// (reports Primary but lacks the operator-promote marker while a real promoted
// primary exists — see aggregateShardStatus). Reseeding deletes the rogue's
// pod+PVC so the StatefulSet recreates it and the init container performs a fresh
// pg_basebackup from the current promoted primary, turning the rogue into a clean
// standby. The promoted (data-holding) primary is never flagged, so its data is
// never at risk (#220 clean-rejoin).
func (r *PostgresClusterReconciler) reconcileRoguePrimaries(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) error {
	logger := log.FromContext(ctx)
	for i := range shards {
		shard := &shards[i]
		// Only act once a legitimate (ready) primary is established for the shard.
		if shard.Primary == nil || !shard.Primary.Ready {
			continue
		}
		for j := range shard.Replicas {
			rep := &shard.Replicas[j]
			if rep.Reason != roguePrimaryReason || rep.Pod == "" || rep.Pod == shard.Primary.Pod {
				continue
			}
			logger.Info("reseeding rogue primary into clean standby",
				"pod", rep.Pod, "primary", shard.Primary.Pod)
			if err := r.reseedStandby(ctx, cluster, rep.Pod); err != nil {
				return fmt.Errorf("reseed rogue primary %q: %w", rep.Pod, err)
			}
		}
	}
	return nil
}
