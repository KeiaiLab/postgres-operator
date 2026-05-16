// Package tx 는 postgres-operator 의 분산 트랜잭션 모델을 구현한다.
//
// 결정: 2PC primary + saga deferred (ADR-0015). PG native PREPARE
// TRANSACTION / COMMIT PREPARED 위에서 pg-router 가 코디네이터 역할.
//
// 본 파일은 *skeleton* — 인터페이스 + sentinel error + 각 phase 의
// TODO marker. 실 구현은 후속 sub-task (P-D §D.10.1 / D.2.2 통합 후).
package tx

import (
	"context"
	"errors"
)

// ErrNotImplemented 는 skeleton 단계의 sentinel error. 각 phase 실
// 구현이 채워지면 제거된다.
var ErrNotImplemented = errors.New("tx: not implemented (ADR-0015 skeleton)")

// TxID 는 분산 트랜잭션의 globally unique 식별자. 형식: ULID 또는
// `<coordinator-id>-<monotonic-seq>` (구현 시 결정).
type TxID string

// ShardID 는 참여 shard 의 식별자. RFC-0002 ShardRange 의 spec.id 와
// 동일 의미.
type ShardID string

// Participant 는 2PC 의 한 shard 참여자. router 가 각 shard 에 대해
// 1개의 Participant connection 을 보유.
type Participant interface {
	// Shard 는 이 participant 가 담당하는 shard 식별자.
	Shard() ShardID

	// Prepare 는 해당 shard 에 `PREPARE TRANSACTION '<gid>'` 를 발행.
	// 성공 시 nil. 실패 시 코디네이터는 전체 abort 결정.
	Prepare(ctx context.Context, gid string) error

	// Commit 은 해당 shard 에 `COMMIT PREPARED '<gid>'` 를 발행.
	// 모든 participant Prepare 성공 후에만 호출.
	Commit(ctx context.Context, gid string) error

	// Rollback 은 해당 shard 에 `ROLLBACK PREPARED '<gid>'` 를 발행.
	// 어느 participant 든 Prepare 실패 시 모든 participant 에 대해 호출.
	Rollback(ctx context.Context, gid string) error
}

// Coordinator 는 분산 트랜잭션의 coordinator 인터페이스. pg-router 가
// leader 일 때 1개 인스턴스만 활성 (D.2.2 Lease election 통합).
type Coordinator interface {
	// Begin 은 새 분산 트랜잭션을 시작. 반환된 TxID 는 이후 모든
	// 호출의 핸들.
	Begin(ctx context.Context) (TxID, error)

	// Enlist 는 트랜잭션에 shard participant 를 등록. 첫 statement
	// 가 해당 shard 로 라우팅될 때 호출.
	Enlist(ctx context.Context, txid TxID, p Participant) error

	// Prepare 는 모든 등록된 participant 에 PREPARE TRANSACTION 발행.
	// 1개라도 실패하면 자동 Rollback 후 error 반환.
	Prepare(ctx context.Context, txid TxID) error

	// Commit 은 Prepare 성공 후 모든 participant 에 COMMIT PREPARED.
	// 이 단계의 실패는 in-doubt — Recoverer 가 처리.
	Commit(ctx context.Context, txid TxID) error

	// Rollback 은 모든 participant 에 ROLLBACK PREPARED 또는 plain
	// ROLLBACK (Prepare 이전 단계인지에 따라).
	Rollback(ctx context.Context, txid TxID) error
}

// Recoverer 는 코디네이터 fail-over 시 in-doubt 분산 tx 복구 hook.
// 신규 leader 부팅 시 1회 실행 → tx log replay + 각 shard
// pg_prepared_xacts reconcile → 최종 commit / rollback 결정 후 실행.
//
// ADR-0015 §Consequences "코디네이터 SPOF" trade-off 의 핵심 완화책.
// 실 구현은 D.2.2 Lease election 통합 후 별 sub-task.
type Recoverer interface {
	// Recover 는 leader 부팅 시 1회 호출. 비결정 tx 없으면 nil 반환.
	Recover(ctx context.Context) error
}

// TwoPhaseCommit 은 Coordinator 의 PG-native 2PC 구현 stub.
//
// 실 구현 TODO (각 phase 별):
//   - Begin: TxID 생성 (ULID) + tx log entry append (operator leader
//     etcd lease 위에)
//   - Enlist: participant connection 추가 + tx log update
//   - Prepare: 모든 participant 에 PREPARE TRANSACTION 병렬 발행 →
//     timeout / 부분 실패 시 전체 Rollback
//   - Commit: 모든 participant 에 COMMIT PREPARED 병렬 발행 →
//     실패한 participant 는 in-doubt 표시, Recoverer 가 처리
//   - Rollback: phase 에 따라 ROLLBACK PREPARED 또는 plain ROLLBACK
type TwoPhaseCommit struct {
	// TODO(D.10.2): tx log backend (etcd client) 주입
	// TODO(D.2.2): Lease election 통합 — leader 가 아니면 모든 method
	//              는 ErrNotLeader 반환
}

// NewTwoPhaseCommit 은 skeleton instance 를 반환. 실 의존성 주입은
// 후속 sub-task 에서 추가.
func NewTwoPhaseCommit() *TwoPhaseCommit {
	return &TwoPhaseCommit{}
}

// Begin — skeleton. TODO: TxID 생성 + tx log append.
func (c *TwoPhaseCommit) Begin(_ context.Context) (TxID, error) {
	return "", ErrNotImplemented
}

// Enlist — skeleton. TODO: participant 등록 + tx log update.
func (c *TwoPhaseCommit) Enlist(_ context.Context, _ TxID, _ Participant) error {
	return ErrNotImplemented
}

// Prepare — skeleton. TODO: 모든 participant 에 PREPARE TRANSACTION
// 병렬 발행 + 부분 실패 시 자동 Rollback.
func (c *TwoPhaseCommit) Prepare(_ context.Context, _ TxID) error {
	return ErrNotImplemented
}

// Commit — skeleton. TODO: 모든 participant 에 COMMIT PREPARED 병렬
// 발행 + 실패는 in-doubt 표시.
func (c *TwoPhaseCommit) Commit(_ context.Context, _ TxID) error {
	return ErrNotImplemented
}

// Rollback — skeleton. TODO: phase 에 따라 ROLLBACK PREPARED 또는
// plain ROLLBACK.
func (c *TwoPhaseCommit) Rollback(_ context.Context, _ TxID) error {
	return ErrNotImplemented
}
