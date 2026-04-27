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
// 여기서 한 번 정의된 명명 규약은 build/images/Dockerfile, charts/, e2e
// 테스트, 그리고 사용자 가이드(docs/guides/)의 단일 진실이다.
//
// 명명 원칙:
//   - 모든 자원 이름은 PostgresCluster.metadata.name 접두사를 사용한다.
//   - 역할(coordinator/worker/router)은 두 번째 토큰.
//   - worker pool 이름은 세 번째 토큰.
//   - K8s lease는 ADR 0002 §결과의 명명 규약 "<cluster>-<role>-primary"를 따름.

// CoordinatorStatefulSetName은 coordinator StatefulSet의 이름을 반환한다.
func CoordinatorStatefulSetName(cluster string) string {
	return fmt.Sprintf("%s-coordinator", cluster)
}

// CoordinatorServiceName은 coordinator의 headless Service 이름을 반환한다.
// StatefulSet의 안정적 DNS는 <pod>.<service>.<namespace>.svc.cluster.local 형태.
func CoordinatorServiceName(cluster string) string {
	return fmt.Sprintf("%s-coordinator", cluster)
}

// CoordinatorConfigMapName은 coordinator의 postgresql.conf 등을 담는 ConfigMap 이름.
func CoordinatorConfigMapName(cluster string) string {
	return fmt.Sprintf("%s-coordinator-config", cluster)
}

// WorkerStatefulSetName은 worker pool의 StatefulSet 이름을 반환한다.
func WorkerStatefulSetName(cluster, pool string) string {
	return fmt.Sprintf("%s-worker-%s", cluster, pool)
}

// WorkerServiceName은 worker pool의 headless Service 이름을 반환한다.
func WorkerServiceName(cluster, pool string) string {
	return fmt.Sprintf("%s-worker-%s", cluster, pool)
}

// WorkerConfigMapName은 worker pool의 ConfigMap 이름을 반환한다.
func WorkerConfigMapName(cluster, pool string) string {
	return fmt.Sprintf("%s-worker-%s-config", cluster, pool)
}

// RouterDeploymentName은 QueryRouter Deployment 이름을 반환한다.
// PVC 부재(ADR 0003)이므로 Deployment를 사용한다(StatefulSet 아님).
func RouterDeploymentName(cluster string) string {
	return fmt.Sprintf("%s-router", cluster)
}

// RouterServiceName은 클라이언트 진입점이 되는 Service 이름을 반환한다.
// 사용자가 "host=<cluster>-router" 형태로 접속.
func RouterServiceName(cluster string) string {
	return fmt.Sprintf("%s-router", cluster)
}

// RouterConfigMapName은 라우터 PgBouncer 등의 설정을 담는 ConfigMap.
func RouterConfigMapName(cluster string) string {
	return fmt.Sprintf("%s-router-config", cluster)
}

// SelectorLabels는 부모 PostgresCluster + 역할 + (선택) pool 식별 레이블이다.
// reconciler가 Service의 selector와 Pod template label에 동일하게 적용한다.
func SelectorLabels(cluster, role, pool string) map[string]string {
	out := map[string]string{
		"app.kubernetes.io/name":       "postgrescluster",
		"app.kubernetes.io/instance":   cluster,
		"app.kubernetes.io/component":  role,
		"app.kubernetes.io/managed-by": "keiailab-postgres-operator",
	}
	if pool != "" {
		out["postgres.keiailab.io/pool"] = pool
	}
	return out
}
