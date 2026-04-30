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
	"strings"
	"testing"
)

// 본 파일은 LibPQExecutor의 컴파일 + SQL 변환 패턴을 단위 테스트로 검증한다.
// 실 PostgreSQL을 띄우는 SQL 실행 검증은 phase 2(kind e2e)에서 별도 다룬다.
//
// RFC 0002 §6 production path의 SQL 매핑 회귀 차단이 본 파일의 역할.

func TestActionToSQL_Add_FormatsAllPositionalArgs(t *testing.T) {
	t.Parallel()

	a := Action{
		Op: OpAdd,
		Node: Node{
			Group: 1,
			Name:  "worker-pool-a-0.svc.cluster.local",
			Port:  5432,
		},
	}
	q, args, err := actionToSQL(a)
	if err != nil {
		t.Fatalf("actionToSQL: %v", err)
	}
	want := "SELECT citus_add_node($1, $2, 1, 'primary', 'default')"
	if q != want {
		t.Errorf("query:\n  want %q\n  got  %q", want, q)
	}
	if len(args) != 2 {
		t.Fatalf("args: want 2, got %d", len(args))
	}
	if args[0] != "worker-pool-a-0.svc.cluster.local" {
		t.Errorf("args[0]: want hostname, got %v", args[0])
	}
	if port, ok := args[1].(int); !ok || port != 5432 {
		t.Errorf("args[1]: want int(5432), got %v", args[1])
	}
}

func TestActionToSQL_Add_GroupIDInlineFormat(t *testing.T) {
	t.Parallel()

	// groupid는 inline %d (positional binding 미사용 — Citus 함수 시그니처 제약)
	a := Action{Op: OpAdd, Node: Node{Group: 7, Name: "n", Port: 5432}}
	q, _, err := actionToSQL(a)
	if err != nil {
		t.Fatalf("actionToSQL: %v", err)
	}
	if !strings.Contains(q, ", 7, 'primary'") {
		t.Errorf("groupid inline: want '7' in query, got %q", q)
	}
}

func TestActionToSQL_Remove_ParameterizesNameAndPort(t *testing.T) {
	t.Parallel()

	a := Action{Op: OpRemove, Node: Node{Name: "worker-x", Port: 5432}}
	q, args, err := actionToSQL(a)
	if err != nil {
		t.Fatalf("actionToSQL: %v", err)
	}
	want := "SELECT citus_remove_node($1, $2)"
	if q != want {
		t.Errorf("query: want %q, got %q", want, q)
	}
	if len(args) != 2 || args[0] != "worker-x" {
		t.Errorf("args: %v", args)
	}
}

func TestActionToSQL_Update_BothShouldHaveShardsValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		shouldHaveShards bool
		wantSubstr       string
	}{
		{"true", true, "'shouldhaveshards', true"},
		{"false", false, "'shouldhaveshards', false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := Action{
				Op: OpUpdate,
				Node: Node{
					Name:             "worker-x",
					Port:             5432,
					ShouldHaveShards: tc.shouldHaveShards,
				},
			}
			q, _, err := actionToSQL(a)
			if err != nil {
				t.Fatalf("actionToSQL: %v", err)
			}
			if !strings.Contains(q, tc.wantSubstr) {
				t.Errorf("query: want substring %q, got %q", tc.wantSubstr, q)
			}
			if !strings.HasPrefix(q, "SELECT citus_set_node_property(") {
				t.Errorf("query prefix: %q", q)
			}
		})
	}
}

func TestActionToSQL_UnknownOpReturnsError(t *testing.T) {
	t.Parallel()

	a := Action{Op: Op("frobnicate"), Node: Node{}}
	_, _, err := actionToSQL(a)
	if err == nil {
		t.Fatal("expected error for unknown Op")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error must mention unknown Op: %v", err)
	}
}

func TestLibPQExecutor_EmptyActionsIsNoOp_NoDSNFuncRequired(t *testing.T) {
	t.Parallel()

	// 빈 actions는 DB open을 시도하지 않으므로 DSNFunc 없이도 안전.
	e := &LibPQExecutor{}
	if err := e.Apply(context.Background(), nil); err != nil {
		t.Errorf("Apply(nil): want no error, got %v", err)
	}
	if err := e.Apply(context.Background(), []Action{}); err != nil {
		t.Errorf("Apply([]): want no error, got %v", err)
	}
}

func TestLibPQExecutor_NilDSNFuncIsErrorWhenActionsPresent(t *testing.T) {
	t.Parallel()

	e := &LibPQExecutor{} // DSNFunc nil
	err := e.Apply(context.Background(), []Action{
		{Op: OpAdd, Node: Node{Name: "n", Port: 5432}},
	})
	if err == nil {
		t.Fatal("expected error when DSNFunc is nil and actions are present")
	}
	if !strings.Contains(err.Error(), "DSNFunc") {
		t.Errorf("error must mention DSNFunc: %v", err)
	}
}

func TestLibPQExecutor_DSNFuncErrorIsPropagated(t *testing.T) {
	t.Parallel()

	wantErr := "synthetic dsn lookup failure"
	e := &LibPQExecutor{
		DSNFunc: func(_ context.Context) (string, error) {
			return "", &dummyError{msg: wantErr}
		},
	}
	err := e.Apply(context.Background(), []Action{
		{Op: OpAdd, Node: Node{Name: "n", Port: 5432}},
	})
	if err == nil {
		t.Fatal("expected error from DSNFunc to propagate")
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Errorf("error must wrap DSNFunc error: %v", err)
	}
}

// dummyError is a minimal error type for tests.
type dummyError struct{ msg string }

func (e *dummyError) Error() string { return e.msg }
