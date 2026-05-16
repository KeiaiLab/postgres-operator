package router

import (
	"context"
	"errors"
	"testing"
)

// TestScatterGather_NotImplemented 는 skeleton 단계 contract 를 freeze 한다.
//
//   - *ScatterGather 가 Executor interface 를 만족한다 (compile-time + runtime).
//   - Execute 는 항상 ErrNotImplemented sentinel 을 반환한다.
//   - 응답 row slice 는 nil 이다 (caller 가 len() 으로 안전히 zero-check 가능).
//
// 실 구현 진입 시 본 test 는 *반드시* 실패 → 실 구현 test 로 교체된다. 본
// failure 자체가 "skeleton 졸업" signal 이다.
func TestScatterGather_NotImplemented(t *testing.T) {
	t.Parallel()

	var exec Executor = NewScatterGather()

	rows, err := exec.Execute(context.Background(), "SELECT count(*) FROM users", []ShardID{"shard-0", "shard-1"})

	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Execute() err = %v, want ErrNotImplemented sentinel", err)
	}
	if rows != nil {
		t.Fatalf("Execute() rows = %v, want nil (skeleton contract)", rows)
	}
}

// TestScatterGather_EmptyShards 는 빈 shard list 도 동일 sentinel 을 반환하여
// planner 의 fan-out 안전 단축 가정 (empty fan-out = no-op) 을 freeze 한다.
func TestScatterGather_EmptyShards(t *testing.T) {
	t.Parallel()

	sg := NewScatterGather()
	rows, err := sg.Execute(context.Background(), "SELECT 1", nil)

	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Execute() err = %v, want ErrNotImplemented", err)
	}
	if rows != nil {
		t.Fatalf("Execute() rows = %v, want nil", rows)
	}
}
