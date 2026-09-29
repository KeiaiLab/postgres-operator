/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 멤버가 마운트한 설정 파일의 적재를 맞춘다(설계 §3.18, ADR-0034). kubelet 이 ConfigMap 을 파일로
// 투영해도 postgres 는 reload 전까지 옛 설정을 쓴다 — pg_hba_file_rules 는 파일이지 적재 상태가 아니다.
//
//	적재 시각(초) > hba 파일 mtime(초)  → 적재 = 파일 → 게이지 = 비-local trust 줄 수(파일 오류면 −1)
//	그 밖                              → pg_reload_conf() · 게이지 −1(다음 reconcile 이 다시 잰다)
//
// kubelet 은 ConfigMap 의 모든 키를 새 디렉터리에 한 번에 쓰므로 hba 파일 mtime 이 설정 변경 전체를 대표한다.

const (
	// hbaJudgmentQuery 는 멤버 한 곳의 적재 판정이다: 신선 | 비-local trust 줄 | 파일 오류 줄. mtime 은 초 단위라
	// 적재 시각도 초로 자른다 — 같은 초 안의 reload 는 파일보다 이를 수 있다.
	hbaJudgmentQuery = "SELECT date_trunc('second', pg_conf_load_time()) > (pg_stat_file(current_setting('hba_file'))).modification, " +
		"count(*) FILTER (WHERE type <> 'local' AND auth_method = 'trust'), " +
		"count(*) FILTER (WHERE error IS NOT NULL) FROM pg_hba_file_rules"
	// hbaJudgmentFields 는 판정 줄의 열 수다.
	hbaJudgmentFields = 3

	// hbaRulesUnknown 은 게이지의 모름 값이다 — 적재가 파일보다 이르거나, 파일 오류이거나, 판정 exec 실패.
	hbaRulesUnknown = -1
)

// hbaLoad 는 멤버 한 곳의 hba 적재 판정이다.
type hbaLoad struct {
	fresh  bool // 적재가 파일보다 늦다 — 적재된 규칙 = 파일 규칙
	trust  int  // 파일의 비-local trust 줄
	errors int  // 파일의 오류 줄(적재는 옛 규칙을 지킨다)
}

// rules 는 게이지 값이다 — 적재가 파일과 같다고 확인될 때만 trust 줄 수.
func (h hbaLoad) rules() int {
	if !h.fresh || h.errors > 0 {
		return hbaRulesUnknown
	}
	return h.trust
}

// hbaJudgment 는 멤버의 hba 적재를 잰다(로컬 소켓 exec 한 번).
func (r *PostgresClusterReconciler) hbaJudgment(ctx context.Context, pod *corev1.Pod) (hbaLoad, error) {
	out, err := r.memberSQL(ctx, pod, hbaJudgmentQuery)
	if err != nil {
		return hbaLoad{}, err
	}
	fields := strings.Split(out, psqlFieldSeparator)
	if len(fields) != hbaJudgmentFields {
		return hbaLoad{}, fmt.Errorf("hba judgment %q unreadable", out)
	}
	trust, trustErr := strconv.Atoi(fields[1])
	bad, badErr := strconv.Atoi(fields[2])
	if (fields[0] != pgBoolTrue && fields[0] != pgBoolFalse) || errors.Join(trustErr, badErr) != nil {
		return hbaLoad{}, fmt.Errorf("hba judgment %q unreadable", out)
	}
	return hbaLoad{fresh: fields[0] == pgBoolTrue, trust: trust, errors: bad}, nil
}

// reloadStaleMembers 는 적재가 파일보다 이른 멤버(스탠바이 포함)에 pg_reload_conf() 를 보내고 hba 게이지를
// 쓴다 — primary 에만 매 reconcile 보내던 reload 를 대신한다. 돌지 않는 멤버는 건너뛴다(죽은 노드 exec 는
// 시한까지 붙잡힌다). 게이지는 판정을 다 모은 뒤 한 번에 바꾼다.
func (r *PostgresClusterReconciler) reloadStaleMembers(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pods []corev1.Pod,
) error {
	logger := log.FromContext(ctx)
	rules := make(map[string]int, len(pods))
	var failed []error
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		load, err := r.hbaJudgment(ctx, pod)
		if err != nil {
			logger.V(1).Info("hba load unknown", "pod", pod.Name, "error", err)
			rules[pod.Name] = hbaRulesUnknown
			continue
		}
		rules[pod.Name] = load.rules()
		if load.fresh {
			continue
		}
		if err := r.reloadPostgresConfig(ctx, pod.Namespace, pod.Name); err != nil {
			failed = append(failed, err)
		}
	}

	MetricHBANetworkTrustRules.DeletePartialMatch(prometheus.Labels{"namespace": cluster.Namespace, "name": cluster.Name})
	for pod, n := range rules {
		MetricHBANetworkTrustRules.WithLabelValues(cluster.Namespace, cluster.Name, pod).Set(float64(n))
	}
	return errors.Join(failed...)
}

// clusterMembers 는 샤드들의 멤버 파드다(라우터 파드는 셀렉터가 같아도 빠진다).
func (r *PostgresClusterReconciler) clusterMembers(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) ([]corev1.Pod, error) {
	refs := make([]shardRef, 0, len(shards))
	for i := range shards {
		refs = append(refs, shardRefFor(cluster, &shards[i]))
	}
	return r.shardMembers(ctx, cluster, func(pod *corev1.Pod) bool {
		for _, ref := range refs {
			if ref.member(pod) {
				return true
			}
		}
		return false
	})
}
