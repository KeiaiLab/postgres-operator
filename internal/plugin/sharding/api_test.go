/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package sharding

import (
	"context"
	"database/sql"
	"testing"
)

const testBackendNoop = "noop"

// noopPlugin은 인터페이스 동결 검증용 스켈레톤. 실제 구현은 Phase 2A에서.
type noopPlugin struct{}

func (noopPlugin) Name() string { return testBackendNoop }

func (noopPlugin) Capabilities() Capabilities {
	return Capabilities{} // 모든 capability false — placeholder.
}

func (noopPlugin) PreparePlacement(_ context.Context, _ ClusterRef, _ Topology) error {
	return nil
}

func (noopPlugin) CreateDistributedTable(_ context.Context, _ *sql.DB, _ DistributedTableSpec) error {
	return &ErrUnsupported{Backend: testBackendNoop, Capability: "DistributedTables"}
}

func (noopPlugin) CreateReferenceTable(_ context.Context, _ *sql.DB, _ string) error {
	return &ErrUnsupported{Backend: testBackendNoop, Capability: "ReferenceTables"}
}

func (noopPlugin) RebalanceShards(_ context.Context, _ *sql.DB) (RebalanceJob, error) {
	return RebalanceJob{}, &ErrUnsupported{Backend: testBackendNoop, Capability: "OnlineRebalance"}
}

func (noopPlugin) RouteQuery(_ context.Context, _ string, _ []any) ([]ShardTarget, error) {
	return nil, &ErrUnsupported{Backend: testBackendNoop, Capability: "DistributedTables"}
}

func (noopPlugin) Validate(_ *ShardingSpec) error { return nil }

// TestRegistry_Register_Get은 Registry 기본 동작을 검증한다.
func TestRegistry_Register_Get(t *testing.T) {
	r := NewRegistry()
	r.Register(noopPlugin{})

	p, ok := r.Get(testBackendNoop)
	if !ok {
		t.Fatal("등록된 plugin을 찾지 못함")
	}
	if p.Name() != testBackendNoop {
		t.Errorf("plugin Name 불일치: got %q", p.Name())
	}
}

// TestRegistry_Get_NotFound는 미등록 backend 조회 시 (nil, false) 반환을 검증한다.
func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("missing"); ok {
		t.Error("미등록 backend가 조회됨")
	}
}

// TestRegistry_Names는 등록 목록을 반환하는지 검증한다.
func TestRegistry_Names(t *testing.T) {
	r := NewRegistry()
	r.Register(noopPlugin{})
	names := r.Names()
	if len(names) != 1 || names[0] != testBackendNoop {
		t.Errorf("Names() 결과 불일치: %v", names)
	}
}

// TestErrUnsupported_Message는 에러 메시지 형식을 검증한다.
func TestErrUnsupported_Message(t *testing.T) {
	err := &ErrUnsupported{Backend: "native-fdw", Capability: "Distributed2PC"}
	msg := err.Error()
	if msg != "sharding backend native-fdw does not support capability Distributed2PC" {
		t.Errorf("에러 메시지 형식 변경됨: %q", msg)
	}
}

// TestNoopPlugin_InterfaceFreezeCheck는 noopPlugin이 ShardingPlugin 인터페이스를
// 충족하는지 컴파일 타임에 강제한다. 본 테스트는 인터페이스 변경(메서드 추가/제거) 시
// 컴파일 실패로 시그널을 준다.
func TestNoopPlugin_InterfaceFreezeCheck(t *testing.T) {
	var _ ShardingPlugin = noopPlugin{}
}
