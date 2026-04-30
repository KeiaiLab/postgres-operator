/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"context"
	"sync"
)

// SQLExecutor는 ComputeActions가 만든 Action 리스트를 실 PG에 적용한다
// (RFC 0002 §6). 본 인터페이스의 변경은 RFC 갱신 필수.
type SQLExecutor interface {
	// Apply는 actions를 입력된 순서대로 적용한다.
	// 부분 실패 시 첫 실패 시점에 error 반환 — 호출자(reconciler)는 다음 reconcile
	// 매 회 재계산하여 잔여 Action을 자동 적용한다(멱등성).
	//
	// ctx는 ClusterFromContext로 cluster 식별자를 추출 가능 (P0-6 phase 2b).
	// LibPQExecutor.DSNFunc가 cluster별 DSN을 lookup할 때 사용한다.
	Apply(ctx context.Context, actions []Action) error
}

// ClusterID는 reconcile 중인 PostgresCluster를 식별하는 (Namespace, Name)
// tuple이다. P0-6 phase 2b 다중 cluster 지원의 토대 — LibPQExecutor.DSNFunc가
// ctx에서 추출하여 cluster별 DSN을 lookup.
type ClusterID struct {
	Namespace string
	Name      string
}

// String은 "<namespace>/<name>" 형식의 사람-친화 문자열을 반환한다.
// 환경 변수 키 등 ASCII 식별자에는 SafeKey 사용 권장.
func (c ClusterID) String() string {
	return c.Namespace + "/" + c.Name
}

// SafeKey는 ClusterID를 환경 변수 키 등에 사용 가능한 ASCII 식별자로 변환한다.
// "<namespace>__<name>" 형식 (대시는 보존). 충돌 회피 위해 "/" 대신 "__".
func (c ClusterID) SafeKey() string {
	return c.Namespace + "__" + c.Name
}

// ctxClusterKey는 context.WithValue에 사용하는 unexported 식별자다.
// (외부 패키지가 동일 키로 ctx에 set하지 못하도록 방지.)
type ctxClusterKey struct{}

// WithCluster는 ClusterID를 ctx에 주입한다. reconciler가 매 reconcile에서
// 호출하여 SQLExecutor가 cluster별 DSN을 lookup할 수 있도록 한다.
//
// 사용 (postgrescluster_controller.go):
//
//	ctxWithCluster := citus.WithCluster(ctx, cluster.Namespace, cluster.Name)
//	if err := r.CitusExec.Apply(ctxWithCluster, actions); err != nil { ... }
func WithCluster(ctx context.Context, namespace, name string) context.Context {
	return context.WithValue(ctx, ctxClusterKey{}, ClusterID{
		Namespace: namespace,
		Name:      name,
	})
}

// ClusterFromContext는 ctx에서 ClusterID를 추출한다. ok=false면 cluster
// context가 주입되지 않았음을 의미 — caller(DSNFunc)는 fallback 또는 error.
//
// 사용 (cmd/main.go DSNFunc):
//
//	DSNFunc: func(ctx context.Context) (string, error) {
//	    cl, ok := citus.ClusterFromContext(ctx)
//	    if !ok {
//	        return os.Getenv("CITUS_LIBPQ_DSN"), nil // single-cluster fallback
//	    }
//	    // multi-cluster: env var per cluster
//	    return os.Getenv("CITUS_LIBPQ_DSN_" + cl.SafeKey()), nil
//	}
func ClusterFromContext(ctx context.Context) (ClusterID, bool) {
	c, ok := ctx.Value(ctxClusterKey{}).(ClusterID)
	return c, ok
}

// NullExecutor는 Action을 적용하지 않는다(M0 spike 기본값).
//
// 본 구현은 P11-M0(현재) 시점에 reconciler에 주입되어 desired state 계산과
// Status 반영만 활성화한다. 실 SQL 실행은 P11-M1의 LibPQExecutor가 담당한다.
//
// envtest 통합 테스트에도 본 구현을 사용한다 — envtest는 PG 컨테이너를 띄우지
// 않으므로 SQL 실행은 단위 테스트 + 향후 kind 기반 e2e의 책임이다.
type NullExecutor struct{}

// Apply는 no-op이다.
func (NullExecutor) Apply(_ context.Context, _ []Action) error {
	return nil
}

// MockExecutor는 호출된 Action을 모두 기록한다(단위 테스트 보조).
type MockExecutor struct {
	mu      sync.Mutex
	Applied [][]Action
	// Err가 nil 아니면 Apply가 즉시 그 error를 반환 (실패 경로 테스트용).
	Err error
}

// Apply는 actions를 기록 후 (또는 Err 설정 시) 반환한다.
func (m *MockExecutor) Apply(_ context.Context, actions []Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	// 입력 슬라이스 변경 회피 위해 복사.
	cp := make([]Action, len(actions))
	copy(cp, actions)
	m.Applied = append(m.Applied, cp)
	return nil
}

// Calls는 누적 호출 횟수다.
func (m *MockExecutor) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Applied)
}

// Compile-time interface satisfaction guards. 본 어셔션이 시그니처 동결을 보장.
var (
	_ SQLExecutor = (*NullExecutor)(nil)
	_ SQLExecutor = (*MockExecutor)(nil)
)
