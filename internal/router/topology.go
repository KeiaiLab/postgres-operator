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
	// ClearCacheOnMissing 가 true 면, list 는 성공했으나 매칭 ShardRange 가 없을 때(예:
	// keyspace 삭제) 캐시를 비운다 → Current 가 stale 토폴로지 대신 에러를 낸다. false
	// (기본)면 transient 보호를 위해 마지막 캐시를 유지한다. list 자체 실패(API blip)는
	// 항상 캐시를 보존한다.
	ClearCacheOnMissing bool

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
	// list 는 성공했으나 매칭 없음 (keyspace 삭제 등). 정책에 따라 캐시 비움.
	if p.ClearCacheOnMissing {
		p.mu.Lock()
		p.cached, p.loaded = Topology{}, false
		p.mu.Unlock()
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

// StatusBackendResolver 는 shard 를 그 *현재 Ready primary* endpoint(쓰기) 또는 Ready
// replica(읽기 분산)로 매핑한다 — PostgresCluster.status 의 캐시 스냅샷 기반
// (failover-aware). Update 는 refresh 루프가, Resolve/ResolveRead 는 연결마다 호출한다.
// Ready 대상이 없는 shard(down / failover 중)는 error 를 내어 라우터가 클라이언트를
// 우아하게 실패시키게 한다.
type StatusBackendResolver struct {
	// MaxReplicaLagBytes 가 >0 이면 lag 가 그를 초과하는 replica 는 읽기 대상에서 제외한다
	// (bounded staleness). 0 = 무제한(모든 Ready replica 허용).
	MaxReplicaLagBytes int64

	mu        sync.Mutex
	primaries map[string]string   // shard → primary endpoint (Ready 만)
	replicas  map[string][]string // shard → Ready replica endpoints (lag 임계 통과)
	rr        map[string]int      // shard → round-robin 커서 (읽기 분산)
}

// NewStatusBackendResolver 는 빈 resolver 를 만든다. Update 전에는 모든 Resolve 가
// 에러(아직 status 미수신).
func NewStatusBackendResolver() *StatusBackendResolver {
	return &StatusBackendResolver{
		primaries: map[string]string{},
		replicas:  map[string][]string{},
		rr:        map[string]int{},
	}
}

// Update 는 샤드 status 목록으로 primary/replica 매핑을 통째로 교체한다 (Ready 만 포함).
// failover 후 새 status 가 들어오면 자동으로 새 primary/replica 를 가리키게 된다.
func (r *StatusBackendResolver) Update(shards []v1alpha1.ShardStatus) {
	pm := make(map[string]string, len(shards))
	rm := make(map[string][]string, len(shards))
	for i := range shards {
		s := shards[i]
		if s.Primary != nil && s.Primary.Ready && s.Primary.Endpoint != "" {
			pm[s.Name] = s.Primary.Endpoint
		}
		for j := range s.Replicas {
			rep := s.Replicas[j]
			if !rep.Ready || rep.Endpoint == "" {
				continue
			}
			if r.MaxReplicaLagBytes > 0 && rep.LagBytes > r.MaxReplicaLagBytes {
				continue // 너무 stale → 읽기 대상 제외 (bounded staleness).
			}
			rm[s.Name] = append(rm[s.Name], rep.Endpoint)
		}
	}
	r.mu.Lock()
	r.primaries, r.replicas = pm, rm
	r.mu.Unlock()
}

// Resolve 는 shard 의 현재 Ready primary endpoint(쓰기 경로)를 반환한다. 없으면 error.
func (r *StatusBackendResolver) Resolve(shardID string) (string, error) {
	r.mu.Lock()
	ep := r.primaries[shardID]
	r.mu.Unlock()
	if ep == "" {
		return "", fmt.Errorf("router: shard %q has no Ready primary (down or mid-failover)", shardID)
	}
	return ep, nil
}

// ResolveRead 는 읽기 쿼리용으로 Ready replica 를 round-robin 선택한다(읽기 확장 +
// primary 부하 경감). replica 가 없으면 primary 로, 그것도 없으면 error 로 폴백한다.
// BackendResolver 시그니처와 호환 — (E) 쿼리 라우팅에서 읽기/쓰기로 분기 사용.
func (r *StatusBackendResolver) ResolveRead(shardID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reps := r.replicas[shardID]; len(reps) > 0 {
		i := r.rr[shardID] % len(reps)
		r.rr[shardID] = (r.rr[shardID] + 1) % (1 << 30)
		return reps[i], nil
	}
	if ep := r.primaries[shardID]; ep != "" {
		return ep, nil // replica 부재 → primary 읽기 폴백 (안전).
	}
	return "", fmt.Errorf("router: shard %q has no Ready replica or primary", shardID)
}

var (
	_ TopologyProvider = StaticTopologyProvider{}
	_ TopologyProvider = (*CRDTopologyProvider)(nil)
	_ BackendResolver  = (*StatusBackendResolver)(nil).Resolve
	_ BackendResolver  = (*StatusBackendResolver)(nil).ResolveRead
)
