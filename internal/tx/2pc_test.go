package tx

import (
	"context"
	"errors"
	"testing"
)

// TestCoordinatorInterface 는 *TwoPhaseCommit 가 Coordinator 인터페이스를
// 만족함을 compile-time 검증 + skeleton sentinel 확인.
func TestCoordinatorInterface(t *testing.T) {
	var _ Coordinator = (*TwoPhaseCommit)(nil)

	c := NewTwoPhaseCommit()
	ctx := context.Background()

	if _, err := c.Begin(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Begin: want ErrNotImplemented, got %v", err)
	}
	if err := c.Enlist(ctx, "", nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Enlist: want ErrNotImplemented, got %v", err)
	}
	if err := c.Prepare(ctx, ""); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Prepare: want ErrNotImplemented, got %v", err)
	}
	if err := c.Commit(ctx, ""); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Commit: want ErrNotImplemented, got %v", err)
	}
	if err := c.Rollback(ctx, ""); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Rollback: want ErrNotImplemented, got %v", err)
	}
}

// TestErrNotImplementedSentinel 은 sentinel error 가 stable 함을 확인.
func TestErrNotImplementedSentinel(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented must be non-nil")
	}
	if ErrNotImplemented.Error() == "" {
		t.Fatal("ErrNotImplemented must have a non-empty message")
	}
}
