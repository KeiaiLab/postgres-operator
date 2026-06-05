/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package election

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// 본 파일은 Pillar P2-T1 spike의 단위 회귀다. RFC 0003 §1~§9 결정의 코드 차원
// 강제.

// ----------------------------------------------------------------------------
// Lease 매개변수 sanity (RFC 0003 §2)
// ----------------------------------------------------------------------------

func TestLeaseParameters_Defaults(t *testing.T) {
	d := Durations{}.withDefaults()
	if d.LeaseDuration != DefaultLeaseDuration {
		t.Errorf("LeaseDuration = %s, want %s", d.LeaseDuration, DefaultLeaseDuration)
	}
	if d.RenewDeadline != DefaultRenewDeadline {
		t.Errorf("RenewDeadline = %s, want %s", d.RenewDeadline, DefaultRenewDeadline)
	}
	if d.RetryPeriod != DefaultRetryPeriod {
		t.Errorf("RetryPeriod = %s, want %s", d.RetryPeriod, DefaultRetryPeriod)
	}
}

func TestLeaseParameters_RenewDeadlineMustBeLessThanLeaseDuration(t *testing.T) {
	bad := Durations{
		LeaseDuration: 5 * time.Second,
		RenewDeadline: 10 * time.Second, // 위반
		RetryPeriod:   1 * time.Second,
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error when RenewDeadline >= LeaseDuration")
	}
}

func TestLeaseParameters_RetryPeriodMustBeLessThanRenewDeadline(t *testing.T) {
	bad := Durations{
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   10 * time.Second, // 위반
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error when RetryPeriod >= RenewDeadline")
	}
}

func TestLeaseParameters_DefaultsAreValid(t *testing.T) {
	if err := (Durations{}.withDefaults()).Validate(); err != nil {
		t.Errorf("default lease parameters must be valid: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Lease 명명 규약 (RFC 0001 PostgresCluster CRD v2 — shard 모델)
// ----------------------------------------------------------------------------

func TestPrimaryLeaseName_Shard(t *testing.T) {
	cases := []struct {
		ordinal int32
		want    string
	}{
		{0, "orders-shard-0-primary"},
		{1, "orders-shard-1-primary"},
		{42, "orders-shard-42-primary"},
	}
	for _, c := range cases {
		got, err := PrimaryLeaseName("orders", "shard", c.ordinal)
		if err != nil {
			t.Fatalf("ordinal=%d: unexpected error: %v", c.ordinal, err)
		}
		if got != c.want {
			t.Errorf("ordinal=%d: got %q, want %q", c.ordinal, got, c.want)
		}
	}
}

func TestPrimaryLeaseName_RouterReturnsError(t *testing.T) {
	_, err := PrimaryLeaseName("orders", "router", 0)
	if err == nil {
		t.Fatal("PrimaryLeaseName(role=router) must return error — router has no lease")
	}
}

func TestPrimaryLeaseName_NegativeOrdinalReturnsError(t *testing.T) {
	_, err := PrimaryLeaseName("orders", "shard", -1)
	if err == nil {
		t.Fatal("PrimaryLeaseName(shardOrdinal<0) must return error")
	}
}

// ----------------------------------------------------------------------------
// Reshard target lease 명명 (G3 online-resharding, ADR-0027)
// ----------------------------------------------------------------------------

func TestReshardTargetLeaseName_Format(t *testing.T) {
	cases := []struct {
		shardID string
		want    string
	}{
		{"shard-0a", "orders-rsd-shard-0a-primary"},
		{"t0", "orders-rsd-t0-primary"},
	}
	for _, c := range cases {
		got, err := ReshardTargetLeaseName("orders", c.shardID)
		if err != nil {
			t.Fatalf("shardID=%q: unexpected error: %v", c.shardID, err)
		}
		if got != c.want {
			t.Errorf("shardID=%q: got %q, want %q", c.shardID, got, c.want)
		}
	}
}

func TestReshardTargetLeaseName_EmptyShardIDReturnsError(t *testing.T) {
	if _, err := ReshardTargetLeaseName("orders", ""); err == nil {
		t.Fatal("ReshardTargetLeaseName(shardID=\"\") must return error")
	}
}

// TestReshardTargetLeaseName_NeverCollidesWithOrdinal 은 ADR-0027 의 핵심 격리
// 불변식을 봉인한다 — target lease 가 *어떤 ordinal 과도* 충돌하지 않아야 한다.
// 충돌 시 target instance manager 가 실 shard 의 election 에 끼어들어 split-brain.
func TestReshardTargetLeaseName_NeverCollidesWithOrdinal(t *testing.T) {
	const cluster = "orders"
	// ordinal lease 이름 집합 (넓은 범위) 을 모은다.
	ordinalLeases := map[string]bool{}
	for ord := int32(0); ord < 1000; ord++ {
		name, err := PrimaryLeaseName(cluster, "shard", ord)
		if err != nil {
			t.Fatalf("ordinal=%d: %v", ord, err)
		}
		ordinalLeases[name] = true
	}
	// target lease 이름이 ordinal 집합과 절대 겹치지 않음을 확인. "0"/"42" 처럼
	// 숫자형 shardID 도 `-rsd-` segment 덕에 `-shard-<ord>-` 와 분리된다.
	for _, shardID := range []string{"shard-0a", "0", "42", "t0", "999"} {
		got, err := ReshardTargetLeaseName(cluster, shardID)
		if err != nil {
			t.Fatalf("shardID=%q: %v", shardID, err)
		}
		if ordinalLeases[got] {
			t.Errorf("target lease %q (shardID=%q) 가 ordinal lease 와 충돌", got, shardID)
		}
	}
}

// ----------------------------------------------------------------------------
// Real 입력 검증
// ----------------------------------------------------------------------------

func TestNewReal_RejectsEmptyFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  RealConfig
	}{
		{"nil client", RealConfig{LeaseName: "x", Namespace: "default", Identity: "p1"}},
		{"empty lease", RealConfig{Client: fake.NewClientset(), Namespace: "default", Identity: "p1"}},
		{"empty namespace", RealConfig{Client: fake.NewClientset(), LeaseName: "x", Identity: "p1"}},
		{"empty identity", RealConfig{Client: fake.NewClientset(), LeaseName: "x", Namespace: "default"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewReal(c.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNewReal_HappyPath(t *testing.T) {
	r, err := NewReal(RealConfig{
		Client:    fake.NewClientset(),
		LeaseName: "orders-shard-0-primary",
		Namespace: "default",
		Identity:  "orders-shard-0-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Identity() != "orders-shard-0-0" {
		t.Errorf("Identity = %q", r.Identity())
	}
	if r.Status() != StatusStarting {
		t.Errorf("initial Status = %v, want Starting", r.Status())
	}
}

// ----------------------------------------------------------------------------
// Null election
// ----------------------------------------------------------------------------

func TestNull_AlwaysLeader_FiresCallbacksOnRun(t *testing.T) {
	var (
		startedCh = make(chan struct{}, 1)
		leaderCh  = make(chan string, 1)
		stoppedCh = make(chan struct{}, 1)
	)
	n := NewNull("solo", Callbacks{
		OnStartedLeading: func(_ context.Context) { startedCh <- struct{}{} },
		OnNewLeader:      func(id string) { leaderCh <- id },
		OnStoppedLeading: func() { stoppedCh <- struct{}{} },
	})

	if n.Status() != StatusLeader {
		t.Fatalf("initial Status = %v, want Leader", n.Status())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(done)
	}()

	select {
	case <-startedCh:
	case <-time.After(time.Second):
		t.Fatal("OnStartedLeading not called")
	}
	if id := <-leaderCh; id != "solo" {
		t.Errorf("OnNewLeader id = %q, want 'solo'", id)
	}

	cancel()
	<-done
	select {
	case <-stoppedCh:
	case <-time.After(time.Second):
		t.Fatal("OnStoppedLeading not called after cancel")
	}
}

func TestFollower_NeverPromotes(t *testing.T) {
	var (
		started = false
		stopped = false
	)
	f := NewFollower("replica-0", Callbacks{
		OnStartedLeading: func(context.Context) { started = true },
		OnStoppedLeading: func() { stopped = true },
	})
	if f.Status() != StatusFollower {
		t.Fatalf("initial Status = %v, want Follower", f.Status())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Run(ctx); err != context.Canceled {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if started || stopped {
		t.Fatalf("Follower must never call promotion callbacks, started=%v stopped=%v", started, stopped)
	}
}

// ----------------------------------------------------------------------------
// Mock election
// ----------------------------------------------------------------------------

func TestMock_SetStatus_TriggersStartedLeading(t *testing.T) {
	startedCh := make(chan struct{}, 1)
	m := NewMock("p1", Callbacks{
		OnStartedLeading: func(_ context.Context) { startedCh <- struct{}{} },
	})
	if m.Status() != StatusStarting {
		t.Fatalf("initial = %v", m.Status())
	}
	m.SetStatus(context.Background(), StatusLeader)
	if m.Status() != StatusLeader {
		t.Errorf("after SetStatus(Leader): Status = %v", m.Status())
	}
	select {
	case <-startedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnStartedLeading not fired")
	}
}

func TestMock_SetStatus_TriggersStoppedLeading(t *testing.T) {
	stoppedCh := make(chan struct{}, 1)
	m := NewMock("p1", Callbacks{
		OnStoppedLeading: func() { stoppedCh <- struct{}{} },
	})
	m.SetStatus(context.Background(), StatusLeader)
	m.SetStatus(context.Background(), StatusFollower)
	select {
	case <-stoppedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnStoppedLeading not fired")
	}
}

func TestMock_SetExternalLeader_DemoteIfWasLeader(t *testing.T) {
	stoppedCh := make(chan struct{}, 1)
	leaderCh := make(chan string, 1)
	m := NewMock("p1", Callbacks{
		OnStoppedLeading: func() { stoppedCh <- struct{}{} },
		OnNewLeader:      func(id string) { leaderCh <- id },
	})
	m.SetStatus(context.Background(), StatusLeader)
	// drain leaderCh from the SetStatus call
	<-leaderCh

	m.SetExternalLeader("p2")
	if m.Status() != StatusFollower {
		t.Errorf("Status after external leader = %v, want Follower", m.Status())
	}
	select {
	case <-stoppedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnStoppedLeading not fired")
	}
	if id := <-leaderCh; id != "p2" {
		t.Errorf("OnNewLeader id = %q, want 'p2'", id)
	}
}

// ----------------------------------------------------------------------------
// 인터페이스 일관성 — 3 구현 모두 Election을 만족
// ----------------------------------------------------------------------------

func TestAllImplementations_SatisfyInterface(t *testing.T) {
	var _ Election = (*Real)(nil)
	var _ Election = (*Null)(nil)
	var _ Election = (*Follower)(nil)
	var _ Election = (*Mock)(nil)
}

// ----------------------------------------------------------------------------
// 트리비얼 게터 — Identity가 생성자 인자를 그대로 보존함을 단위 회귀로 보장.
// 본 테스트는 P2-M1 게이트(단위 ≥80%)를 위해 mock/null의 Identity·Run 분기를
// 명시적으로 커버한다.
// ----------------------------------------------------------------------------

func TestNull_Identity(t *testing.T) {
	n := NewNull("solo-pod-0", Callbacks{})
	if got := n.Identity(); got != "solo-pod-0" {
		t.Errorf("Null.Identity = %q, want %q", got, "solo-pod-0")
	}
}

func TestMock_Identity(t *testing.T) {
	m := NewMock("test-pod-7", Callbacks{})
	if got := m.Identity(); got != "test-pod-7" {
		t.Errorf("Mock.Identity = %q, want %q", got, "test-pod-7")
	}
}

func TestMock_Run_BlocksUntilContextDone(t *testing.T) {
	m := NewMock("p1", Callbacks{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Run은 ctx 종료 전에는 반환하지 않아야 한다.
	select {
	case <-done:
		t.Fatal("Mock.Run returned before ctx cancel")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		// ctx.Err()를 그대로 반환해야 한다(인터페이스 계약).
		if err == nil {
			t.Error("Mock.Run returned nil error after cancel; want context.Canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("Mock.Run did not return after ctx cancel")
	}
}
