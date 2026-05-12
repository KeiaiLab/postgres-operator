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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// executeClusterPromotion 은 controller-layer failover 의 mutation 지점이다.
// failover 패키지의 순수 decision/plan 을 유지하면서, 실제 K8s Pod exec 와
// annotation patch 는 controller package 에 격리한다.
func (r *PostgresClusterReconciler) executeClusterPromotion(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardName string,
	decision failover.Decision,
) error {
	if !decision.Failed {
		return nil
	}
	if cluster == nil {
		return errors.New("postgres cluster is nil")
	}
	if r.PromotionPodExecutor == nil {
		return errors.New("promotion pod executor is not configured")
	}
	promoter := &clusterPodPromoter{
		Namespace:   cluster.Namespace,
		Client:      r.Client,
		PodExecutor: r.PromotionPodExecutor,
		Now:         time.Now,
	}
	return failover.PromoteFromDecision(ctx, shardName, decision, promoter)
}

type clusterPodPromoter struct {
	Namespace   string
	Client      client.Client
	PodExecutor BackupSidecarExecutor
	Now         func() time.Time
}

func (p *clusterPodPromoter) Execute(ctx context.Context, plan failover.PromotionPlan) error {
	if p == nil || p.PodExecutor == nil {
		return errors.New("promotion pod executor is not configured")
	}
	if p.Namespace == "" || plan.Target.Pod == "" {
		return fmt.Errorf("invalid promotion target: namespace=%q pod=%q", p.Namespace, plan.Target.Pod)
	}
	if _, err := p.PodExecutor.Exec(ctx, BackupSidecarTarget{
		Namespace: p.Namespace,
		Pod:       plan.Target.Pod,
		Container: pgContainerName,
	}, postgresPromotionCommand()); err != nil {
		return err
	}
	if p.Client == nil {
		return nil
	}
	return p.patchPromotedPodStatus(ctx, plan)
}

func (p *clusterPodPromoter) patchPromotedPodStatus(ctx context.Context, plan failover.PromotionPlan) error {
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: p.Namespace, Name: plan.Target.Pod}
	if err := p.Client.Get(ctx, key, &pod); err != nil {
		return fmt.Errorf("get promoted pod: %w", err)
	}

	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	raw, err := json.Marshal(statusapi.Status{
		Role:       statusapi.RolePrimary,
		Ready:      true,
		Endpoint:   plan.Target.Endpoint,
		LagBytes:   0,
		LastUpdate: now,
	})
	if err != nil {
		return err
	}
	pod.Annotations[statusapi.AnnotationKey] = string(raw)
	if err := p.Client.Patch(ctx, &pod, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("patch promoted pod status annotation: %w", err)
	}
	return nil
}

func postgresPromotionCommand() []string {
	const script = `set -eu
BIN="${POSTGRES_BIN_DIR:-/usr/lib/postgresql/18/bin}"
DATA="${POSTGRES_DATA_DIR:-/var/lib/postgresql/data/pgdata}"
DSN="${POSTGRES_LOCAL_DSN:-host=/var/run/postgresql user=postgres dbname=postgres}"

is_primary() {
  "$BIN/psql" "$DSN" -Atqc "SELECT NOT pg_is_in_recovery()" | grep -qx t
}

if is_primary; then
  exit 0
fi

rm -f "$DATA/standby.signal"
"$BIN/pg_ctl" promote -D "$DATA"

i=0
while [ "$i" -lt 30 ]; do
  if is_primary; then
    exit 0
  fi
  i=$((i + 1))
  sleep 1
done

echo "promotion did not reach primary state within 30s" >&2
exit 1
`
	return []string{"sh", "-ec", script}
}
