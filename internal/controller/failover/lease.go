/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package failover

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"

	"github.com/keiailab/postgres-operator/internal/instance/election"
)

// HA election 분산락 (ROADMAP G1 §자동 failover).
//
// 책임: 다중 replica 로 배포된 operator manager 중 *오직 한 인스턴스* 만
// failover decision/promotion 을 실 수행하도록 K8s coordination.k8s.io/v1
// Lease 기반 leader-election 을 적용한다. 일반 controller-runtime 의 manager
// leader-election 과 별도 lease — failover-only 책임을 분리하여
// reconciler/finalizer 비차단 + failover hot-path 격리.
//
// 본 파일은 의도적으로 *thin adapter* — 실 client-go leaderelection wrapper
// 는 `internal/instance/election.Real` 가 단일 출처 (§2 Simplicity). 본
// adapter 는 failover scope 의 lease 명명 규약 + 기본 매개변수 + 콜백
// 시그니처 만 노출한다.
//
// 배선 상태 (RFC 0007 P2-T3, 미완): 본 adapter 는 *아직 production 에 배선되지
// 않은 building block* 이다. 현재 자동 failover (detection + promotion) 는
// PostgresCluster reconcile 루프 (clusterFailoverDecision -> executeCluster
// Promotion) 에서 실행되며, 이는 controller-runtime manager 의 자체 leader
// election 으로 이미 단일 replica 로 게이팅된다. 따라서 별도 failover lease 없이도
// "오직 한 operator 가 failover 수행" 이 보장된다.
//
// 본 lease 를 reconcile 루프 failover 의 게이트로 *순진하게* 연결하면 안 된다:
// reconciler 는 manager-lease holder 에서만 돌고, 본 lease 는 그와 독립적인
// 별도 lease 라 holder 가 다른 Pod 일 수 있다 — 그 경우 failover 가 어느 Pod
// 에서도 실행되지 않는 deadlock 이 된다. 제대로 된 P2-T3 는 failover 를
// reconcile 루프 밖의 leader-election-agnostic runnable 로 먼저 분리한 뒤,
// 그 runnable 을 본 lease 로 게이팅해야 한다 (후속 과제).
//
// 의도된 사용 형태 (P2-T3 완성 시):
//
//	cfg := failover.LeaseConfig{
//	    Client:    clientset,
//	    Namespace: operatorNS,
//	    Identity:  podName,
//	    OnStartedLeading: func(ctx context.Context) { failoverRunnable.Enable() },
//	    OnStoppedLeading: func() { failoverRunnable.Disable() },
//	}
//	lease, err := failover.NewLease(cfg)
//	go lease.Run(ctx)

// FailoverLeaseName 은 operator 단위 failover-controller lease 의 표준 명칭이다.
// instance-단위 lease (election.PrimaryLeaseName) 와 *별도 lease key* — 이름
// 충돌 방지 + 책임 분리.
const FailoverLeaseName = "postgres-operator-failover-leader"

// LeaseConfig 는 NewLease 의 입력이다. 0 값 Durations 는 election.Default* 로
// 대체된다 (15s / 10s / 2s, RFC 0003 §2 표준).
type LeaseConfig struct {
	// Client 는 K8s clientset. coordination.k8s.io/v1 Lease 권한 필요.
	Client kubernetes.Interface
	// Namespace 는 lease 가 거주할 ns (보통 operator 가 배포된 ns).
	Namespace string
	// Identity 는 본 manager Pod 의 고유 identifier (POD_NAME 권장).
	Identity string
	// LeaseName 이 비어 있으면 FailoverLeaseName 사용.
	LeaseName string
	// OnStartedLeading 은 본 Pod 가 leader 가 된 직후 호출.
	OnStartedLeading func(ctx context.Context)
	// OnStoppedLeading 은 본 Pod 가 leader 였다가 lease 를 잃은 직후 호출.
	OnStoppedLeading func()
	// OnNewLeader 는 임의의 leader 변경 시 호출. identity == cfg.Identity 면
	// 본 Pod 가 leader.
	OnNewLeader func(identity string)
	// Durations 가 zero 값이면 election.Default* 사용.
	Durations election.Durations
}

// Lease 는 failover-controller scope 의 HA election 핸들이다.
type Lease struct {
	inner *election.Real
}

// NewLease 는 LeaseConfig 를 검증하고 election.Real adapter 를 구성한다.
//
// 검증 실패 시 nil + error 반환 — 호출자(cmd/main) 는 이를 기동 실패로 처리.
func NewLease(cfg LeaseConfig) (*Lease, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("failover: Lease.Client must not be nil")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("failover: Lease.Namespace must not be empty")
	}
	if cfg.Identity == "" {
		return nil, fmt.Errorf("failover: Lease.Identity must not be empty")
	}
	name := cfg.LeaseName
	if name == "" {
		name = FailoverLeaseName
	}
	r, err := election.NewReal(election.RealConfig{
		Client:    cfg.Client,
		LeaseName: name,
		Namespace: cfg.Namespace,
		Identity:  cfg.Identity,
		Callbacks: election.Callbacks{
			OnStartedLeading: cfg.OnStartedLeading,
			OnStoppedLeading: cfg.OnStoppedLeading,
			OnNewLeader:      cfg.OnNewLeader,
		},
		Durations: cfg.Durations,
	})
	if err != nil {
		return nil, fmt.Errorf("failover: NewLease: %w", err)
	}
	return &Lease{inner: r}, nil
}

// Run 은 ctx 종료 시까지 blocking 으로 election 루프를 실행한다.
// 호출자는 별도 goroutine 에서 호출해야 한다.
func (l *Lease) Run(ctx context.Context) error {
	return l.inner.Run(ctx)
}

// Identity 는 본 Pod 의 lease identity 를 반환한다.
func (l *Lease) Identity() string {
	return l.inner.Identity()
}

// IsLeader 는 현재 본 Pod 가 leader 인지 동시성 안전하게 반환한다.
func (l *Lease) IsLeader() bool {
	return l.inner.Status() == election.StatusLeader
}
