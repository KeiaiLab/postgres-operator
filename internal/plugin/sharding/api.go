/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package sharding은 자체 분산 SQL plugin 인터페이스 동결 산출물이다(RFC 0001~0005).
//
// 본 패키지는 ADR 0001 keystone (자체 분산 SQL) + ADR 0003 (license policy) 정책
// 하에서 분산 SQL capability 를 Apache-2.0/BSD/MIT 호환 plugin path로 제공하기
// 위한 *인터페이스 동결*만 다룬다. 실제 구현은 Phase P3+의 별도 PR에서 단계적으로
// 진행된다.
//
// 인터페이스 동결 정책:
//   - 본 인터페이스의 메서드 시그니처는 alpha 단계에서 추가만 허용 (non-breaking).
//   - 기존 메서드 변경/제거는 후속 RFC에서 일괄.
//   - 본 패키지는 stdlib + database/sql + context 외 외부 의존을 갖지 않는다.
//
// 매핑 (RFC 0001~0005):
//   - C3 placement → PreparePlacement, CreateDistributedTable
//   - C2 executor / C1 planner → RouteQuery (단순 case) 또는 백엔드 위임
//   - C6 reference → CreateReferenceTable
//   - C4 rebalance → RebalanceShards
//   - C5 2PC (RFC 0005), C7 columnar → 별도 인터페이스로 분리 검토
package sharding

import (
	"context"
	"database/sql"
	"time"
)

// ShardingPlugin은 분산 sharding 백엔드를 추상화한다.
//
// 구현 후보 (RFC 0001~0005, ADR 0003 라이선스 정책 준수 필수):
//   - "native-fdw" — postgres_fdw 기반 hash sharding (Apache-2.0, Phase P3+)
//   - 자체 hash sharding 백엔드 (BSD/Apache/MIT/PG License 준수, Phase P4+)
//   - AGPL/BUSL/CSL/SSPL 백엔드는 ADR 0003 에 의해 등록 금지.
//
// 본 인터페이스는 0.3.0-alpha 시점에 alpha-frozen.
type ShardingPlugin interface {
	// Name은 본 플러그인의 고유 식별자.
	// PostgresClusterSpec.Sharding.Backend 와 일치해야 한다.
	Name() string

	// Capabilities는 본 백엔드가 지원하는 기능 집합을 보고한다.
	// 사용자가 ShardingSpec에 unsupported 기능을 지정하면 webhook이 거절한다.
	Capabilities() Capabilities

	// PreparePlacement는 PostgresCluster topology가 변경됐을 때 shard placement
	// 갱신을 수행한다 (노드 추가/제거). 멱등이며 reconcile loop에서 매번 호출된다.
	PreparePlacement(ctx context.Context, target ClusterRef, topo Topology) error

	// CreateDistributedTable은 사용자 SQL DDL을 해석하여 shard 생성 + metadata 등록.
	CreateDistributedTable(ctx context.Context, conn *sql.DB, spec DistributedTableSpec) error

	// CreateReferenceTable은 모든 노드에 동기 복제되는 작은 테이블 생성.
	// 백엔드가 reference table을 지원하지 않으면 ErrUnsupported 반환.
	CreateReferenceTable(ctx context.Context, conn *sql.DB, table string) error

	// RebalanceShards는 shard 재배치를 트리거한다 (백그라운드 비동기).
	RebalanceShards(ctx context.Context, conn *sql.DB) (RebalanceJob, error)

	// RouteQuery는 SQL을 받아 어느 shard/worker에 보낼지 결정한다.
	// 백엔드가 NativeQueryPlanner=true를 광고하면 본 메서드는 호출되지 않을 수 있다.
	RouteQuery(ctx context.Context, query string, params []any) ([]ShardTarget, error)

	// Validate는 ShardingSpec 사용자 입력을 본 백엔드 관점에서 검사한다.
	// webhook 단계에서 호출. 미지원 capability 사용 시 거절.
	Validate(spec *ShardingSpec) error
}

// Capabilities는 백엔드 기능 광고. webhook은 ShardingSpec과 본 정보를 매칭해 거절한다.
type Capabilities struct {
	// DistributedTables: hash/range distribution 지원.
	DistributedTables bool
	// ReferenceTables: 모든 노드 broadcast 테이블 지원.
	ReferenceTables bool
	// DistributedJoin: multi-shard join (push-down 또는 distributed plan) 지원.
	DistributedJoin bool
	// Distributed2PC: cross-shard ACID transaction 지원.
	Distributed2PC bool
	// OnlineRebalance: non-blocking shard move 지원.
	OnlineRebalance bool
	// ColumnarStorage: columnar access method 지원.
	ColumnarStorage bool
	// NativeQueryPlanner: 백엔드 자체 distributed planner 보유. false면 RouterPlugin
	// 이 자체 routing 으로 처리 (RFC 0004 stateless QueryRouter).
	NativeQueryPlanner bool
}

// ClusterRef은 plugin이 동작 대상으로 삼는 PostgresCluster 인스턴스.
// internal/plugin.ClusterTarget과 동일 의미이지만 의존 cycle 방지를 위해 분리.
type ClusterRef struct {
	Namespace string
	Name      string
}

// Topology는 PostgresCluster의 현재 노드 토폴로지 snapshot.
type Topology struct {
	Coordinator *NodeInfo
	Workers     []NodeInfo
}

// NodeInfo는 단일 노드 (coordinator 또는 worker pool 멤버).
type NodeInfo struct {
	// Pool은 worker pool 이름. coordinator는 빈 문자열.
	Pool string
	// Host는 Pod headless DNS hostname.
	Host string
	// Port는 PostgreSQL 포트 (5432).
	Port int32
	// GroupID는 백엔드별 노드 식별자 (RFC 0002 ShardRange.GroupID 매핑).
	GroupID int32
}

// DistributedTableSpec은 distributed table 정의.
type DistributedTableSpec struct {
	// Name은 스키마 포함 (e.g. "public.events").
	Name string
	// DistributionCol은 shard key column 이름.
	DistributionCol string
	// ShardCount는 shard 개수. 0이면 백엔드 default (보통 32).
	ShardCount int32
	// ColocateWith는 같은 distribution을 갖는 다른 테이블과 collocate. 빈 문자열이면 standalone.
	ColocateWith string
	// Strategy는 "hash" | "range". 빈 문자열이면 "hash" default.
	Strategy string
}

// ShardTarget은 query를 보낼 단일 shard 위치.
type ShardTarget struct {
	Worker  string // hostname (Pod DNS)
	Port    int32
	ShardID int64
}

// RebalanceJob은 진행 중 rebalance 작업 추적용.
type RebalanceJob struct {
	ID      string
	Started time.Time
	// Status는 "pending" | "running" | "complete" | "failed".
	Status string
}

// ShardingSpec은 PostgresCluster CRD의 spec.sharding 서브필드.
// 0.3.0-alpha entry — 본 구조체는 동결되며 추가 필드만 허용.
type ShardingSpec struct {
	// Backend는 ShardingPlugin.Name과 일치하는 백엔드 식별자.
	// 예: "native-fdw" (Apache-2.0, Phase P3+).
	Backend string
	// DistributedTables는 distributed table 정의 목록.
	DistributedTables []DistributedTableSpec
	// ReferenceTables는 broadcast 테이블 이름 목록 (스키마 포함).
	ReferenceTables []string
	// DefaultShardCount는 DistributedTableSpec.ShardCount=0 일 때 fallback. 0이면 백엔드 default.
	DefaultShardCount int32
}

// ErrUnsupported는 백엔드가 요청 capability를 지원하지 않을 때 반환한다.
// webhook은 ShardingSpec 검증 시 본 sentinel을 사용하여 의미 있는 에러 메시지를 만든다.
type ErrUnsupported struct {
	Backend    string
	Capability string
}

func (e *ErrUnsupported) Error() string {
	return "sharding backend " + e.Backend + " does not support capability " + e.Capability
}

// Registry는 등록된 ShardingPlugin들을 보관한다.
// internal/plugin.Registry와 별개로 본 패키지에 둔 이유: sharding은 다른 5개
// plugin과 다른 reconcile path (cluster 조정)를 가지며, 의존 cycle 방지.
type Registry struct {
	plugins map[string]ShardingPlugin
}

// NewRegistry는 비어있는 Registry를 만든다.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]ShardingPlugin)}
}

// Register는 plugin을 등록한다. 같은 Name 이미 있으면 덮어쓴다.
func (r *Registry) Register(p ShardingPlugin) {
	r.plugins[p.Name()] = p
}

// Get은 backend 이름으로 plugin을 찾는다. 없으면 (nil, false).
func (r *Registry) Get(name string) (ShardingPlugin, bool) {
	p, ok := r.plugins[name]
	return p, ok
}

// Names는 등록된 모든 backend 이름을 반환한다 (테스트 / 검증용).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		out = append(out, name)
	}
	return out
}
