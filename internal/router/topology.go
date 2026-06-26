/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — topology.go 는 라우터의 *라우팅 상태*를 두 개의 분리된, 교체
// 가능한 관심사로 공급한다:
//
//  1. key → shard : TopologyProvider (vindex spec). 어떤 키가 어떤 shard 인지.
//  2. shard → backend : BackendResolver. 그 shard 의 *현재 연결 주소*가 무엇인지.
//
// 이 둘을 분리하는 이유: backend 주소는 *장애/failover 에 따라 변한다*. 샤드 primary
// 가 죽으면 operator 가 replica 를 승격하고 PostgresCluster.status 의 primary
// endpoint 를 갱신한다. 따라서 backend 해소를 토폴로지(정적 key 매핑)와 분리해야
// failover-aware resolver(StatusBackendResolver)를 독립적으로 끼울 수 있다.
//
// K8s 의존은 ShardRangeLister / ClusterStatusReader 인터페이스로 가장자리에 격리한다
// — 본 패키지는 controller-runtime 을 import 하지 않는다 (호출 binary 가 주입).
package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// Topology 는 key → shard 매핑(vindex spec)이다. backend 주소는 BackendResolver 가
// 별도로 해소한다.
type Topology struct {
	Cluster  string
	Keyspace string
	Spec     v1alpha1.ShardRangeSpec
}

// Shard 는 라우팅 키를 vindex 로 평가해 shard 이름을 반환한다.
func (t Topology) Shard(key string) (string, error) {
	return ResolveShard(t.Spec, key)
}

// BackendResolver 는 shard 이름 → backend "host:port" 를 해소한다. error 는 그 shard
// 에 *쓸 수 있는 backend 가 없음*(down / failover 중)을 뜻하며, 라우터는 hang 대신
// 클라이언트에 우아한 에러를 돌려준다.
type BackendResolver func(shardID string) (string, error)

// TopologyProvider 는 현재 key → shard Topology 를 공급한다 (동시성 안전).
type TopologyProvider interface {
	Current(ctx context.Context) (Topology, error)
}

// StaticTopologyProvider 는 고정 Topology 를 공급한다 (PoC·테스트·부트스트랩).
type StaticTopologyProvider struct{ T Topology }

// Current 는 고정 Topology 를 반환한다.
func (s StaticTopologyProvider) Current(context.Context) (Topology, error) { return s.T, nil }

// ShardRangeLister 는 ShardRange 읽기를 추상화한다 (K8s 가장자리 격리 + 테스트 fake).
type ShardRangeLister interface {
	ListShardRanges(ctx context.Context, namespace string) ([]v1alpha1.ShardRange, error)
}

// CRDTopologyProvider 는 ShardRange CRD 에서 Topology 를 구성한다. 최근 스냅샷을
// 캐시하고 Refresh 로 갱신한다 (호출 binary 가 주기적으로 또는 watch 로 호출).
type CRDTopologyProvider struct {
	Lister    ShardRangeLister
	Namespace string
	Cluster   string
	Keyspace  string

	mu     sync.RWMutex
	cached Topology
	loaded bool
}

// Current 는 캐시된 Topology 를 반환한다. 아직 로드 전이면 1회 Refresh 한다.
func (p *CRDTopologyProvider) Current(ctx context.Context) (Topology, error) {
	p.mu.RLock()
	if p.loaded {
		t := p.cached
		p.mu.RUnlock()
		return t, nil
	}
	p.mu.RUnlock()
	return p.Refresh(ctx)
}

// Refresh 는 ShardRange 를 다시 읽어 cluster+keyspace 매칭 항목으로 Topology 를
// 재구성하고 캐시를 교체한다 (hot-reload). 매칭 항목이 없으면 에러 — 캐시는 보존.
func (p *CRDTopologyProvider) Refresh(ctx context.Context) (Topology, error) {
	items, err := p.Lister.ListShardRanges(ctx, p.Namespace)
	if err != nil {
		return Topology{}, fmt.Errorf("router: list ShardRange: %w", err)
	}
	for i := range items {
		sp := items[i].Spec
		if sp.Cluster != p.Cluster || sp.Keyspace != p.Keyspace {
			continue
		}
		t := Topology{Cluster: sp.Cluster, Keyspace: sp.Keyspace, Spec: sp}
		p.mu.Lock()
		p.cached, p.loaded = t, true
		p.mu.Unlock()
		return t, nil
	}
	return Topology{}, fmt.Errorf(
		"router: no ShardRange for cluster=%q keyspace=%q in namespace=%q",
		p.Cluster, p.Keyspace, p.Namespace)
}

// --- failover-aware backend 해소 ---

// ClusterStatusReader 는 PostgresCluster 의 샤드별 status 읽기를 추상화한다 (K8s
// 가장자리 격리 + 테스트 fake). operator 가 failover 시 갱신하는 *현재 primary
// endpoint* 의 출처.
type ClusterStatusReader interface {
	ClusterShardStatus(ctx context.Context, namespace, cluster string) ([]v1alpha1.ShardStatus, error)
}

// StatusBackendResolver 는 shard 를 그 *현재 Ready primary* endpoint 로 매핑한다 —
// PostgresCluster.status 의 캐시 스냅샷 기반(failover-aware). Update 는 refresh 루프가,
// Resolve 는 연결마다 호출한다. Ready primary 가 없는 shard(down / failover 중)는
// error 를 내어 라우터가 클라이언트를 우아하게 실패시키게 한다.
type StatusBackendResolver struct {
	mu        sync.RWMutex
	primaries map[string]string // shard 이름 → primary endpoint (Ready 만)
}

// NewStatusBackendResolver 는 빈 resolver 를 만든다. Update 전에는 모든 Resolve 가
// 에러(아직 status 미수신).
func NewStatusBackendResolver() *StatusBackendResolver {
	return &StatusBackendResolver{primaries: map[string]string{}}
}

// Update 는 샤드 status 목록으로 primary 매핑을 통째로 교체한다 (Ready primary 만
// 포함). failover 후 새 status 가 들어오면 자동으로 새 primary 를 가리키게 된다.
func (r *StatusBackendResolver) Update(shards []v1alpha1.ShardStatus) {
	m := make(map[string]string, len(shards))
	for i := range shards {
		s := shards[i]
		if s.Primary != nil && s.Primary.Ready && s.Primary.Endpoint != "" {
			m[s.Name] = s.Primary.Endpoint
		}
	}
	r.mu.Lock()
	r.primaries = m
	r.mu.Unlock()
}

// Resolve 는 shard 의 현재 Ready primary endpoint 를 반환한다. 없으면 error
// (down / failover 중 / 아직 status 미수신).
func (r *StatusBackendResolver) Resolve(shardID string) (string, error) {
	r.mu.RLock()
	ep, ok := r.primaries[shardID]
	r.mu.RUnlock()
	if !ok || ep == "" {
		return "", fmt.Errorf("router: shard %q has no Ready primary (down or mid-failover)", shardID)
	}
	return ep, nil
}

var (
	_ TopologyProvider = StaticTopologyProvider{}
	_ TopologyProvider = (*CRDTopologyProvider)(nil)
	_ BackendResolver  = (*StatusBackendResolver)(nil).Resolve
)
