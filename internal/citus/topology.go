/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package citus는 Citus 분산 토폴로지(pg_dist_node)와 K8s 토폴로지
// (PostgresCluster.spec.workers[] + Service Endpoints) 사이의 자동 동기화를
// 담당한다.
//
// 본 패키지는 Pillar P11의 첫 산출물이며, 본 오퍼레이터의 차별화 1(ADR 0001 v2
// — Citus 1급) 핵심 구현이다.
//
// 설계 결정 단일 출처: docs/rfcs/0002-metadata-sync.md.
//
// 본 패키지는 internal/controller가 직접 import 한다 — depguard 규칙
// (.golangci.yml)에서 "구체 플러그인 차단" 대상이 internal/plugin/<kind>/<impl>/
// 에 한정되며, 본 패키지는 P11 핵심 도메인 로직이므로 reconciler가 직접 사용해야
// 한다.
package citus

import (
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// Node는 단일 Citus 노드(pg_dist_node 한 행)를 표현한다(RFC 0002 §1).
type Node struct {
	// Group은 pg_dist_node.groupid다. coordinator는 0, worker pool i(spec 순서)는 i+1.
	Group int32
	// Name은 hostname (Pod DNS). headless Service가 보장하는 안정적 형식.
	Name string
	// Port는 PostgreSQL 포트(5432).
	Port int32
	// Role은 "coordinator" | "worker".
	Role string
	// Pool은 worker pool 이름. coordinator는 빈 문자열.
	Pool string
	// Index는 같은 pool 내 ordinal(StatefulSet replica 순서).
	Index int32
	// ShouldHaveShards는 pg_dist_node.shouldhaveshards. RFC 0002 §3 기본값.
	ShouldHaveShards bool
}

const (
	// CoordinatorGroup은 Citus 표준이다. coordinator의 pg_dist_node.groupid.
	CoordinatorGroup int32 = 0

	// PGPort는 모든 노드의 표준 PostgreSQL 포트.
	PGPort int32 = 5432
)

// PodDNS는 headless Service가 보장하는 Pod의 안정적 DNS를 만든다.
//
// 형식: <statefulset-name>-<index>.<service-name>.<namespace>.svc.cluster.local
//
// 본 함수는 internal/controller/names.go의 *StatefulSetName/*ServiceName과
// 의도적으로 분리돼 있다. 후자는 K8s 자원 명명 단일 출처이고, 본 함수는 Citus
// 도메인이 K8s 명명 결과를 어떻게 소비하는지의 단일 출처다.
func PodDNS(stsName, svcName, namespace string, index int32) string {
	return fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", stsName, index, svcName, namespace)
}

// DesiredNodes는 PostgresCluster CR로부터 기대 pg_dist_node 등재 항목을 평탄
// 리스트로 계산한다(RFC 0002 §4).
//
// 본 함수는 순수 함수다(side-effect 0, 입력→출력 결정적). 결과 정렬 키는
// (Group, Index). 단위 테스트는 본 함수의 결정성을 보장한다.
//
// 호출자(reconciler)는 본 함수가 만든 stsName/svcName과 internal/controller/
// names.go의 *StatefulSetName/*ServiceName이 반드시 동일 결과를 내야 한다는
// 결합을 인지해야 한다. 향후 둘을 단일 함수로 통합 검토(RFC 0009 시점).
func DesiredNodes(cluster *postgresv1alpha1.PostgresCluster) []Node {
	out := make([]Node, 0, 1+len(cluster.Spec.Workers))

	// 1) coordinator
	coordSvc := coordinatorServiceName(cluster.Name)
	coordSTS := coordinatorStatefulSetName(cluster.Name)
	for i := int32(0); i < cluster.Spec.Coordinator.Members; i++ {
		out = append(out, Node{
			Group:            CoordinatorGroup,
			Name:             PodDNS(coordSTS, coordSvc, cluster.Namespace, i),
			Port:             PGPort,
			Role:             "coordinator",
			Index:            i,
			ShouldHaveShards: coordinatorShouldHaveShards(cluster),
		})
	}

	// 2) worker pools (spec 순서로 groupid 1, 2, 3, ...)
	for poolIdx, pool := range cluster.Spec.Workers {
		group := int32(poolIdx + 1)
		svc := workerServiceName(cluster.Name, pool.Name)
		sts := workerStatefulSetName(cluster.Name, pool.Name)
		for i := int32(0); i < pool.Members; i++ {
			out = append(out, Node{
				Group:            group,
				Name:             PodDNS(sts, svc, cluster.Namespace, i),
				Port:             PGPort,
				Role:             "worker",
				Pool:             pool.Name,
				Index:            i,
				ShouldHaveShards: true,
			})
		}
	}

	return out
}

// coordinatorShouldHaveShards는 RFC 0002 §3 기본값(false)을 반환하되 사용자
// override를 존중한다.
func coordinatorShouldHaveShards(cluster *postgresv1alpha1.PostgresCluster) bool {
	if cluster.Spec.Coordinator.ShouldHaveShards != nil {
		return *cluster.Spec.Coordinator.ShouldHaveShards
	}
	return false
}

// 본 패키지가 internal/controller/names.go의 명명 규약을 그대로 알고 있어야
// DesiredNodes가 K8s 자원과 정렬된다. names.go와의 중복 정의는 의도적이며
// 양쪽이 동일 결과를 내는지 단위 테스트(topology_test.go)가 보장한다.
//
// 향후 두 패키지를 어댑터 인터페이스로 분리 검토(RFC 0009).

func coordinatorStatefulSetName(cluster string) string {
	return fmt.Sprintf("%s-coordinator", cluster)
}

func coordinatorServiceName(cluster string) string {
	return fmt.Sprintf("%s-coordinator", cluster)
}

func workerStatefulSetName(cluster, pool string) string {
	return fmt.Sprintf("%s-worker-%s", cluster, pool)
}

func workerServiceName(cluster, pool string) string {
	return fmt.Sprintf("%s-worker-%s", cluster, pool)
}
