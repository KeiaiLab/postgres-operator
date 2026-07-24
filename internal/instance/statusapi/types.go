/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package statusapi 는 instance manager (per-Pod) 와 controller (operator) 사이의
// *상태 피드백 채널* 데이터 모델을 정의한다 (RFC 0006 R2).
//
// 전송 매체: Pod 자기 자신의 annotation.
//
//	key:   "postgres.keiailab.io/instance-status"
//	value: JSON-encoded Status struct
//
// 동기:
//   - 기존: PostgresCluster.status.shards[] 가 reconcile-time 근사값. 실제 election
//     결과 / PG round-trip / WAL lag 미반영.
//   - RFC 0006 R2: instance 가 자기 상태를 자기 Pod annotation 에 5s 마다 patch.
//     controller 는 Pod annotation 을 aggregate 해 status.shards[].primary/replicas
//     을 *실 PG 상태* 로 갱신.
//
// 본 패키지는 import 자체가 가벼워야 한다 — instance binary (cgo=0, distroless) 와
// controller manager (controller-runtime) 양쪽에서 import.
package statusapi

import "time"

// AnnotationKey 는 Pod annotation 으로 사용되는 키. RFC 0001 group prefix 따름.
const AnnotationKey = "postgres.keiailab.io/instance-status"

// Role 은 election + supervise 합산 결과의 *최종 운영 역할*.
//
// 단일 Pod 의 진실: election 이 leader 면 primary, 그 외 standby (replica).
// supervise-disabled (dev) 모드에서는 election 만으로 결정 — Unknown 가능.
type Role string

const (
	// RoleStarting 은 instance 가 부트스트랩 중 — election + supervise 둘 다 미완.
	RoleStarting Role = "starting"
	// RolePrimary 는 election leader + supervise 가 primary 인 정상 상태.
	RolePrimary Role = "primary"
	// RoleReplica 는 election follower + supervise 가 standby 인 정상 상태.
	RoleReplica Role = "replica"
	// RoleStopping 은 OnStoppedLeading 직후 또는 main shutdown 진행 중.
	RoleStopping Role = "stopping"
	// RoleUnknown 은 sup==nil (dev) 또는 race 시점.
	RoleUnknown Role = "unknown"
)

// Status 는 instance manager 가 자기 Pod annotation 으로 송출하는 단일 진실.
//
// 최소 필드만 — 5s 주기로 patch 되므로 객체 크기 + 직렬화 비용을 누적 고려.
// 대규모 메트릭은 별도 /metrics endpoint 로 (RFC 0006 R3 후속).
type Status struct {
	// Role 은 instance 의 현재 운영 역할.
	Role Role `json:"role"`

	// Promoted 는 본 pod 의 PGDATA 가 operator exec-promote durable marker
	// (.keiailab-promoted-primary) 를 보유하는지 여부다. failover 로 승격된 *진짜*
	// primary 만 보유한다. 복귀한 옛 primary 가 stale PRIMARY_ENDPOINT(=self) 로
	// 빈 rogue primary 를 부팅한 경우 marker 가 없어, controller 가 진짜 primary 와
	// rogue 를 구분해 rogue 만 reseed 한다 (#220 clean-rejoin, 데이터 보유 primary 보호).
	Promoted bool `json:"promoted,omitempty"`

	// Ready 는 supervise.IsReady 결과 — postgres 가 SELECT 1 응답.
	// supervise-disabled 모드에서는 election Status 가 Leader/Follower 면 true.
	Ready bool `json:"ready"`

	// Endpoint 는 cluster 내부 DNS 이름 (host:port). 클라이언트가 직접 접속하는 form.
	// 예: "demo-shard-0-0.demo-shard-0-headless.default.svc.cluster.local:5432"
	Endpoint string `json:"endpoint"`

	// LagBytes 는 replica 의 WAL lag (bytes). primary 는 항상 0.
	// 미관측 (예: pg_stat_replication 권한 부재) 시 -1 — controller 가 N/A 로 표기.
	LagBytes int64 `json:"lagBytes"`

	// WALLSNBytes 는 이 인스턴스의 *절대* WAL 위치(bytes, '0/0' 기준)다 — primary 는
	// current write LSN, replica 는 replay LSN. LagBytes(상대 지연)와 달리 멤버 간
	// *직접 비교*가 가능한 진본 신선도 신호다: 높을수록 커밋된 WAL 이 더 많다.
	// controller 의 #220 failback guard 가 fenced 후보 vs 서빙 멤버의 데이터 우열을
	// 판정하고(후보가 명백히 앞서면 guard override), replication-health 가 서빙자 대비
	// 뒤처짐(단절/동결 replica)을 검출하는 데 쓴다. 미관측(연결/질의 실패 또는 구
	// 인스턴스 미보고) 시 -1 — 비교 불가로 처리(안전 기본값: guard 유지).
	// omitempty 미부착 (LagBytes 정합): 구 인스턴스는 필드 부재 → unmarshal 0 → 비교
	// 상 우열 없음으로 안전 degrade (신규 인스턴스만 실 위치 보고).
	WALLSNBytes int64 `json:"walLsnBytes"`

	// SizeBytes 는 이 shard 데이터베이스의 크기 (bytes) 다 — primary 만 보고한다
	// (pg_database_size(current_database())). controller 가 shard 별로 집계해
	// ShardStatus.SizeBytes 로 노출하며, AutoSplit 의 sizeThresholdGB 트리거가 이를
	// 관측한다. 미관측(replica / 권한 부재 / 질의 실패) 시 0.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Reason 은 Ready=false 또는 degraded 상태의 machine-readable 원인이다.
	// 예: RejoinPreparationFailed.
	Reason string `json:"reason,omitempty"`

	// Message 는 운영자가 바로 볼 수 있는 human-readable 상세 메시지다.
	Message string `json:"message,omitempty"`

	// LastUpdate 는 본 status struct 가 patch 된 시각 (UTC). controller 가
	// staleness 검사 (예: >30s 부재면 Pod heartbeat 끊김) 에 사용.
	LastUpdate time.Time `json:"lastUpdate"`
}

// IsStale 는 LastUpdate 가 thresh 보다 오래되었는지 — controller heartbeat 검사용.
func (s *Status) IsStale(now time.Time, thresh time.Duration) bool {
	return now.Sub(s.LastUpdate) > thresh
}
