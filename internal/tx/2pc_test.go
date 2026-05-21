package tx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeParticipant 는 테스트용 in-memory Participant.
type fakeParticipant struct {
	shard       ShardID
	prepareErr  error
	commitErr   error
	rollbackErr error
	prepared    atomic.Int32
	committed   atomic.Int32
	rolledBack  atomic.Int32
}

func (f *fakeParticipant) Shard() ShardID { return f.shard }
func (f *fakeParticipant) Prepare(_ context.Context, _ string) error {
	f.prepared.Add(1)
	return f.prepareErr
}
func (f *fakeParticipant) Commit(_ context.Context, _ string) error {
	f.committed.Add(1)
	return f.commitErr
}
func (f *fakeParticipant) Rollback(_ context.Context, _ string) error {
	f.rolledBack.Add(1)
	return f.rollbackErr
}

// TestCoordinatorInterface 는 compile-time 인터페이스 만족 검증.
func TestCoordinatorInterface(t *testing.T) {
	var _ Coordinator = (*TwoPhaseCommit)(nil)
}

//nolint:gocyclo // 2PC state-machine coverage requires sequential prepare/commit/abort branches
func TestTwoPhaseCommit(t *testing.T) {
	ctx := context.Background()

	t.Run("happy path Begin/Enlist/Prepare/Commit", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		txid, err := c.Begin(ctx)
		if err != nil || txid == "" {
			t.Fatalf("Begin: %v / id=%q", err, txid)
		}
		p1 := &fakeParticipant{shard: "s-0"}
		p2 := &fakeParticipant{shard: "s-1"}
		if err := c.Enlist(ctx, txid, p1); err != nil {
			t.Fatalf("Enlist p1: %v", err)
		}
		if err := c.Enlist(ctx, txid, p2); err != nil {
			t.Fatalf("Enlist p2: %v", err)
		}
		if err := c.Prepare(ctx, txid); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if state, _ := c.State(txid); state != StatePrepared {
			t.Fatalf("state want=Prepared got=%s", state)
		}
		if err := c.Commit(ctx, txid); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if state, _ := c.State(txid); state != StateCommitted {
			t.Fatalf("state want=Committed got=%s", state)
		}
		if p1.prepared.Load() != 1 || p2.prepared.Load() != 1 {
			t.Fatalf("prepared count want=1/1 got=%d/%d", p1.prepared.Load(), p2.prepared.Load())
		}
		if p1.committed.Load() != 1 || p2.committed.Load() != 1 {
			t.Fatalf("committed count want=1/1 got=%d/%d", p1.committed.Load(), p2.committed.Load())
		}
	})

	t.Run("Prepare 부분 실패 자동 Rollback", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		txid, _ := c.Begin(ctx)
		p1 := &fakeParticipant{shard: "s-0"}
		p2 := &fakeParticipant{shard: "s-1", prepareErr: errors.New("simulated")}
		_ = c.Enlist(ctx, txid, p1)
		_ = c.Enlist(ctx, txid, p2)

		err := c.Prepare(ctx, txid)
		if !errors.Is(err, ErrPrepareFailed) {
			t.Fatalf("want ErrPrepareFailed, got %v", err)
		}
		if state, _ := c.State(txid); state != StateRolledBack {
			t.Fatalf("state want=RolledBack got=%s", state)
		}
		if p1.rolledBack.Load() != 1 || p2.rolledBack.Load() != 1 {
			t.Fatalf("rollback count want=1/1 got=%d/%d", p1.rolledBack.Load(), p2.rolledBack.Load())
		}
	})

	t.Run("Commit 부분 실패 InDoubt", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		txid, _ := c.Begin(ctx)
		p1 := &fakeParticipant{shard: "s-0"}
		p2 := &fakeParticipant{shard: "s-1", commitErr: errors.New("network blip")}
		_ = c.Enlist(ctx, txid, p1)
		_ = c.Enlist(ctx, txid, p2)
		if err := c.Prepare(ctx, txid); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		err := c.Commit(ctx, txid)
		if !errors.Is(err, ErrInDoubt) {
			t.Fatalf("want ErrInDoubt, got %v", err)
		}
		if state, _ := c.State(txid); state != StateInDoubt {
			t.Fatalf("state want=InDoubt got=%s", state)
		}
	})

	t.Run("Rollback active 상태에서", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		txid, _ := c.Begin(ctx)
		p1 := &fakeParticipant{shard: "s-0"}
		_ = c.Enlist(ctx, txid, p1)
		if err := c.Rollback(ctx, txid); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if state, _ := c.State(txid); state != StateRolledBack {
			t.Fatalf("state want=RolledBack got=%s", state)
		}
		if p1.rolledBack.Load() != 1 {
			t.Fatalf("rollback count want=1 got=%d", p1.rolledBack.Load())
		}
	})

	t.Run("Enlist 중복 shard idempotent", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		txid, _ := c.Begin(ctx)
		p := &fakeParticipant{shard: "s-0"}
		_ = c.Enlist(ctx, txid, p)
		if err := c.Enlist(ctx, txid, p); err != nil {
			t.Fatalf("dup Enlist: %v", err)
		}
		if err := c.Prepare(ctx, txid); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if p.prepared.Load() != 1 {
			t.Fatalf("dup enlist must dedupe — prepared want=1 got=%d", p.prepared.Load())
		}
	})

	t.Run("UnknownTx + InvalidState", func(t *testing.T) {
		c := NewTwoPhaseCommit("test")
		if err := c.Prepare(ctx, "no-such"); !errors.Is(err, ErrUnknownTx) {
			t.Fatalf("want ErrUnknownTx, got %v", err)
		}
		txid, _ := c.Begin(ctx)
		if err := c.Commit(ctx, txid); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("want ErrInvalidState, got %v", err)
		}
	})

	t.Run("GID + State 디버그 조회", func(t *testing.T) {
		c := NewTwoPhaseCommit("op-leader-0")
		txid, _ := c.Begin(ctx)
		gid, ok := c.GID(txid)
		if !ok || gid == "" {
			t.Fatalf("GID ok=%v gid=%q", ok, gid)
		}
		if _, ok := c.State(txid); !ok {
			t.Fatalf("State must exist")
		}
		if _, ok := c.GID("no-such"); ok {
			t.Fatalf("GID for unknown must return false")
		}
	})

	t.Run("State.String 매핑", func(t *testing.T) {
		cases := map[State]string{
			StateActive: "Active", StatePrepared: "Prepared",
			StateCommitted: "Committed", StateRolledBack: "RolledBack", StateInDoubt: "InDoubt",
		}
		for s, want := range cases {
			if got := s.String(); got != want {
				t.Fatalf("State(%d).String want=%s got=%s", s, want, got)
			}
		}
	})
}
