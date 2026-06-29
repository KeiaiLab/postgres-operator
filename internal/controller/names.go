/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"

	commonslabels "github.com/keiailab/keiailab-commons/pkg/labels"
)

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

// --- G3 online-resharding: target shard 격리 식별 (ADR-0027) ---
//
// resharding 의 target shard 는 라이브 cluster 의 *ordinal shard 모델과 격리된
// 식별 namespace* 를 사용한다. 자원 이름은 `-rsd-` (resharding) segment 로
// ordinal shard (`-shard-<ord>`) 와 분리되어 collision 이 불가능하다. 식별이
// 분리되어 있으므로 `aggregateShardStatus` / metrics / failover 가 transient
// target 에 blind 하여 #220-class identity 혼동을 *구조적으로* 차단한다.
//
// precondition: shardID 는 DNS-1123 label-safe (소문자 영숫자 + 하이픈, 영문자
// 시작, 영숫자 종료) 여야 한다. `ShardSplitJob.Spec.Targets[].ShardID` 에 CRD
// pattern `^[a-z]([a-z0-9-]*[a-z0-9])?$` + MaxLength=30 이 부착돼 apiserver 가
// admission 단계에서 강제한다 (ADR-0027 P2 prerequisite, envtest 검증). 형제 필드
// (Keyspace / ShardRangeEntry.Shard)의 패턴 `^[a-z][a-z0-9_]{0,62}$` 는 언더스코어
// 를 허용해 DNS 에 무효이므로 ShardID 는 의도적으로 더 엄격한 패턴을 쓴다.

// TargetShardStatefulSetName 은 resharding target shard 의 StatefulSet 이름.
func TargetShardStatefulSetName(cluster, shardID string) string {
	return fmt.Sprintf("%s-rsd-%s", cluster, shardID)
}

// TargetShardServiceName 은 target shard 의 headless Service 이름.
func TargetShardServiceName(cluster, shardID string) string {
	return fmt.Sprintf("%s-rsd-%s-headless", cluster, shardID)
}

// TargetShardConfigMapName 은 target shard 의 postgresql.conf ConfigMap 이름.
func TargetShardConfigMapName(cluster, shardID string) string {
	return fmt.Sprintf("%s-rsd-%s-config", cluster, shardID)
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

// RouterHPAName은 QueryRouter Deployment 를 대상으로 하는 HPA 이름을 반환한다.
func RouterHPAName(cluster string) string {
	return fmt.Sprintf("%s-router", cluster)
}

// PoolerDeploymentName 은 Pooler CR 이 소유하는 PgBouncer Deployment 이름이다.
func PoolerDeploymentName(pooler string) string {
	return fmt.Sprintf("%s-pooler", pooler)
}

// PoolerServiceName 은 application 이 접속하는 PgBouncer Service 이름이다.
func PoolerServiceName(pooler string) string {
	return fmt.Sprintf("%s-pooler", pooler)
}

// PoolerConfigMapName 은 pgbouncer.ini 를 담는 ConfigMap 이름이다.
func PoolerConfigMapName(pooler string) string {
	return fmt.Sprintf("%s-pooler-config", pooler)
}

// PoolerPDBName 은 Pooler CR 이 소유하는 PodDisruptionBudget 이름이다.
func PoolerPDBName(pooler string) string {
	return fmt.Sprintf("%s-pooler-pdb", pooler)
}

// InstanceServiceAccountName 은 cluster 단위 instance manager ServiceAccount 이름.
// 모든 shard Pod 가 동일 SA 를 공유 — namespace 안 leases + 자기 PVC patch 권한.
func InstanceServiceAccountName(cluster string) string {
	return fmt.Sprintf("%s-instance", cluster)
}

// InstanceRoleName 은 InstanceServiceAccount 에 부착되는 Role 이름.
func InstanceRoleName(cluster string) string {
	return fmt.Sprintf("%s-instance", cluster)
}

// InstanceRoleBindingName 은 SA↔Role 결합 RoleBinding 이름.
func InstanceRoleBindingName(cluster string) string {
	return fmt.Sprintf("%s-instance", cluster)
}

// SelectorLabels는 부모 PostgresCluster + 역할 + shard ordinal 식별 레이블이다.
// reconciler가 Service의 selector와 Pod template label에 동일하게 적용한다.
//
// router 처럼 shard 차원이 없는 자원은 ordinal=-1 을 전달한다 — 이때 pool 레이블
// 자체가 부재한다 (router 의 backend 는 모든 shard 이므로 ordinal 분리 무의미).
//
// iteration 28 (2026-05-07): keiailab-commons/pkg/labels 위임 — 4-key
// app.kubernetes.io/* convention 통일. postgres-specific shard label 은 별도 추가.
func SelectorLabels(cluster, role string, shardOrdinal int32) map[string]string {
	out := commonslabels.Set{
		Name:      "postgrescluster",
		Instance:  cluster,
		Component: role,
		ManagedBy: "keiailab-postgres-operator",
		// Version + PartOf 미지정 → 4-key (기존 동작 보존)
	}.All()
	if shardOrdinal >= 0 {
		out["postgres.keiailab.io/shard"] = fmt.Sprintf("%d", shardOrdinal)
	}
	return out
}

// ReshardTargetLabelKey 는 resharding target shard 를 식별하는 label key 다 (ADR-0027).
const ReshardTargetLabelKey = "postgres.keiailab.io/reshard-target"

// RouterAutoscaleLabelKey 는 router Deployment replicas 를 HPA 가 관리하는지 표시한다.
const RouterAutoscaleLabelKey = "postgres.keiailab.io/router-autoscale"

// ShardIDLabelKey 는 *명명(named) shard* 식별 label key 다 (ADR-0029). ordinal shard 는
// `shard-<N>`, 승격된 resharding target 은 그 shardID 를 값으로 갖는다 — ordinal·명명 shard 를
// 통합 식별하는 토대. *셀렉터(STS .spec.selector, Service selector)에는 넣지 않는다* — 기존
// STS selector 는 불변이고, 추가 시 업그레이드 중 구 pod 이 셀렉터에서 누락돼 #220-class race
// 를 유발한다. pod template 의 *부가* label 로만 부여한다(셀렉터의 superset).
const ShardIDLabelKey = "postgres.keiailab.io/shard-id"

// ShardIDForOrdinal 은 ordinal shard 의 명명 식별자(`shard-<N>`)를 반환한다 — 논리 shard
// 이름(ShardRange.spec.ranges[].shard) 및 ShardStatefulSetName 의 segment 와 정합.
func ShardIDForOrdinal(ordinal int32) string {
	return fmt.Sprintf("shard-%d", ordinal)
}

// ReshardTargetSelectorLabels 는 resharding target shard 의 label 집합이다 (ADR-0027).
//
// 라이브 shard 와 격리하기 위해:
//   - ordinal `postgres.keiailab.io/shard` label 을 *부여하지 않는다* — 그래서
//     aggregateShardStatus / per-ordinal headless Service 의 ordinal selector 에
//     매칭되지 않는다 (#220-class failover/status 간섭 차단).
//   - Component 를 "reshard-target" 으로 두어 broad `component=shard` 와도 분리.
//   - 대신 `postgres.keiailab.io/reshard-target=<shardID>` 로 target 을 식별.
//
// (POSTGRES_ROLE env 는 별도 — instance 바이너리는 여전히 "shard" role 로 동작.
// 본 label 은 K8s selection 격리 전용. P2 에서 StatefulSet 에 적용.)
func ReshardTargetSelectorLabels(cluster, shardID string) map[string]string {
	out := commonslabels.Set{
		Name:      "postgrescluster",
		Instance:  cluster,
		Component: "reshard-target",
		ManagedBy: "keiailab-postgres-operator",
	}.All()
	out[ReshardTargetLabelKey] = shardID
	return out
}
