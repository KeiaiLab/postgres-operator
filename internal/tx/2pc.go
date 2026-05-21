// Package tx 는 postgres-operator 의 분산 트랜잭션 모델을 구현한다.
//
// 결정: 2PC primary + saga deferred (ADR-0015). PG native PREPARE
// TRANSACTION / COMMIT PREPARED 위에서 pg-router 가 코디네이터 역할.
package tx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotImplemented 는 saga / Recover 등 본 turn scope 외 영역 호출 시 반환.
var ErrNotImplemented = errors.New("tx: not implemented (ADR-0015 saga deferred)")

// ErrUnknownTx 는 알 수 없는 TxID 호출 시 반환.
var ErrUnknownTx = errors.New("tx: unknown TxID")

// ErrInvalidState 는 phase 순서 위반 (예: Prepare 전 Commit) 시 반환.
var ErrInvalidState = errors.New("tx: invalid state transition")

// ErrPrepareFailed 는 1+ participant 의 Prepare 실패. 코디네이터가 자동
// Rollback 실행 후 반환.
var ErrPrepareFailed = errors.New("tx: prepare failed (one or more participants)")

// ErrInDoubt 는 Prepare 성공 후 Commit phase 에서 일부 participant 실패.
// Recoverer 가 처리할 in-doubt 상태로 표시되어 있음.
var ErrInDoubt = errors.New("tx: in-doubt (some participants failed COMMIT PREPARED — Recoverer 처리 대기)")

// TxID 는 분산 트랜잭션의 globally unique 식별자. 형식: `<prefix>-<128-bit-hex>`.
type TxID string

// ShardID 는 참여 shard 의 식별자. RFC-0002 ShardRange 의 spec.id 와 동일.
type ShardID string

// State 는 분산 tx 의 현재 phase 이다.
type State int32

const (
	// StateActive — Begin 직후 ~ Prepare 전.
	StateActive State = iota
	// StatePrepared — Prepare 전체 성공 후 Commit 전.
	StatePrepared
	// StateCommitted — Commit 전체 성공 후 종결.
	StateCommitted
	// StateRolledBack — Rollback 후 종결.
	StateRolledBack
	// StateInDoubt — Prepare 성공 후 Commit 부분 실패. Recoverer 대기.
	StateInDoubt
)

func (s State) String() string {
	switch s {
	case StateActive:
		return "Active"
	case StatePrepared:
		return "Prepared"
	case StateCommitted:
		return "Committed"
	case StateRolledBack:
		return "RolledBack"
	case StateInDoubt:
		return "InDoubt"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Participant 는 2PC 의 한 shard 참여자.
type Participant interface {
	// Shard 는 이 participant 가 담당하는 shard 식별자.
	Shard() ShardID
	// Prepare 는 해당 shard 에 `PREPARE TRANSACTION '<gid>'` 를 발행.
	Prepare(ctx context.Context, gid string) error
	// Commit 은 해당 shard 에 `COMMIT PREPARED '<gid>'` 를 발행.
	Commit(ctx context.Context, gid string) error
	// Rollback 은 해당 shard 에 `ROLLBACK PREPARED '<gid>'` 를 발행
	// (또는 Prepare 전이면 plain `ROLLBACK`).
	Rollback(ctx context.Context, gid string) error
}

// Coordinator 는 분산 트랜잭션의 coordinator 인터페이스.
type Coordinator interface {
	Begin(ctx context.Context) (TxID, error)
	Enlist(ctx context.Context, txid TxID, p Participant) error
	Prepare(ctx context.Context, txid TxID) error
	Commit(ctx context.Context, txid TxID) error
	Rollback(ctx context.Context, txid TxID) error
}

// Recoverer 는 코디네이터 fail-over 시 in-doubt 분산 tx 복구 hook.
// 실 구현은 D.2.2 Lease election + tx log persistence 통합 후.
type Recoverer interface {
	Recover(ctx context.Context) error
}

// TwoPhaseCommit 은 in-memory state machine 기반 2PC coordinator 구현.
//
// 본 구현은 *single-process* 코디네이터 — 분산 leader-election 통합 시
// (D.2.2) 본 struct 를 lease holder pod 안에서만 활성화하면 된다.
// tx log persistence (etcd / PG side-table) 는 D.10.2 후속 sub-task —
// 본 구현은 in-memory entry table 만 보유.
type TwoPhaseCommit struct {
	mu     sync.Mutex
	txs    map[TxID]*txEntry
	seq    atomic.Uint64
	prefix string
	// PrepareTimeout 은 단일 participant Prepare 의 hard timeout. 0 이면 default 5s.
	PrepareTimeout time.Duration
}

type txEntry struct {
	id       TxID
	gid      string
	state    State
	parts    []Participant
	created  time.Time
	prepared time.Time
}

// NewTwoPhaseCommit 은 coordinator 인스턴스를 반환한다. prefix 는 GID
// 식별자 (operator 인스턴스 식별 — leader pod 이름 또는 lease holder
// identity 권장). 빈 문자열이면 "po2pc" default.
func NewTwoPhaseCommit(prefix string) *TwoPhaseCommit {
	if prefix == "" {
		prefix = "po2pc"
	}
	return &TwoPhaseCommit{
		txs:            make(map[TxID]*txEntry),
		prefix:         prefix,
		PrepareTimeout: 5 * time.Second,
	}
}

// Begin 은 새 TxID 를 발급하고 Active state entry 를 생성한다.
func (c *TwoPhaseCommit) Begin(_ context.Context) (TxID, error) {
	seq := c.seq.Add(1)
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("tx: rand: %w", err)
	}
	id := TxID(fmt.Sprintf("%s-%d-%s", c.prefix, seq, hex.EncodeToString(rnd[:8])))
	gid := fmt.Sprintf("%s-%d-%s", c.prefix, seq, hex.EncodeToString(rnd[:]))
	c.mu.Lock()
	c.txs[id] = &txEntry{id: id, gid: gid, state: StateActive, created: time.Now()}
	c.mu.Unlock()
	return id, nil
}

// Enlist 는 active tx 에 participant 를 등록한다. 동일 shard 중복 등록 무시.
func (c *TwoPhaseCommit) Enlist(_ context.Context, txid TxID, p Participant) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.txs[txid]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownTx, txid)
	}
	if e.state != StateActive {
		return fmt.Errorf("%w: enlist after state=%s", ErrInvalidState, e.state)
	}
	for _, existing := range e.parts {
		if existing.Shard() == p.Shard() {
			return nil // idempotent
		}
	}
	e.parts = append(e.parts, p)
	return nil
}

// Prepare 는 등록된 모든 participant 에 PREPARE TRANSACTION 을 *병렬* 발행한다.
// 1+ 실패 시 자동 Rollback 후 ErrPrepareFailed 반환.
func (c *TwoPhaseCommit) Prepare(ctx context.Context, txid TxID) error {
	c.mu.Lock()
	e, ok := c.txs[txid]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownTx, txid)
	}
	if e.state != StateActive {
		c.mu.Unlock()
		return fmt.Errorf("%w: prepare from state=%s", ErrInvalidState, e.state)
	}
	parts := append([]Participant(nil), e.parts...)
	gid := e.gid
	c.mu.Unlock()

	timeout := c.PrepareTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(parts))
	for i, p := range parts {
		wg.Add(1)
		go func(idx int, part Participant) {
			defer wg.Done()
			errs[idx] = part.Prepare(pctx, gid)
		}(i, p)
	}
	wg.Wait()

	var firstErr error
	for _, err := range errs {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		// 부분/전체 실패 — 모든 participant 에 Rollback 발행 (best-effort).
		c.rollbackAll(ctx, parts, gid)
		c.mu.Lock()
		e.state = StateRolledBack
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrPrepareFailed, firstErr)
	}
	c.mu.Lock()
	e.state = StatePrepared
	e.prepared = time.Now()
	c.mu.Unlock()
	return nil
}

// Commit 은 Prepared state 에서 모든 participant 에 COMMIT PREPARED 발행.
// 일부 실패 시 ErrInDoubt + StateInDoubt 표시.
func (c *TwoPhaseCommit) Commit(ctx context.Context, txid TxID) error {
	c.mu.Lock()
	e, ok := c.txs[txid]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownTx, txid)
	}
	if e.state != StatePrepared {
		c.mu.Unlock()
		return fmt.Errorf("%w: commit from state=%s", ErrInvalidState, e.state)
	}
	parts := append([]Participant(nil), e.parts...)
	gid := e.gid
	c.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, len(parts))
	for i, p := range parts {
		wg.Add(1)
		go func(idx int, part Participant) {
			defer wg.Done()
			errs[idx] = part.Commit(ctx, gid)
		}(i, p)
	}
	wg.Wait()

	var firstErr error
	for _, err := range errs {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if firstErr != nil {
		e.state = StateInDoubt
		return fmt.Errorf("%w: %v", ErrInDoubt, firstErr)
	}
	e.state = StateCommitted
	return nil
}

// Rollback 은 현재 phase 에 따라 plain ROLLBACK 또는 ROLLBACK PREPARED 를
// 발행한다. 종결 state 호출은 ErrInvalidState.
func (c *TwoPhaseCommit) Rollback(ctx context.Context, txid TxID) error {
	c.mu.Lock()
	e, ok := c.txs[txid]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownTx, txid)
	}
	if e.state == StateCommitted || e.state == StateRolledBack {
		c.mu.Unlock()
		return fmt.Errorf("%w: rollback from state=%s", ErrInvalidState, e.state)
	}
	parts := append([]Participant(nil), e.parts...)
	gid := e.gid
	c.mu.Unlock()

	c.rollbackAll(ctx, parts, gid)

	c.mu.Lock()
	e.state = StateRolledBack
	c.mu.Unlock()
	return nil
}

func (c *TwoPhaseCommit) rollbackAll(ctx context.Context, parts []Participant, gid string) {
	var wg sync.WaitGroup
	for _, p := range parts {
		wg.Add(1)
		go func(part Participant) {
			defer wg.Done()
			_ = part.Rollback(ctx, gid) // best-effort
		}(p)
	}
	wg.Wait()
}

// State 는 디버깅 / 테스트용 phase 조회.
func (c *TwoPhaseCommit) State(txid TxID) (State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.txs[txid]
	if !ok {
		return 0, false
	}
	return e.state, true
}

// GID 는 PG native PREPARE TRANSACTION 에 사용된 global identifier 를 반환.
// 디버깅 / pg_prepared_xacts catalog cross-ref 용도.
func (c *TwoPhaseCommit) GID(txid TxID) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.txs[txid]
	if !ok {
		return "", false
	}
	return e.gid, true
}
