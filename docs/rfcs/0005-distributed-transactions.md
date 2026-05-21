# RFC-0005: Distributed transactions — 2PC + saga model

- Status: Draft
- Date: 2026-05-02
- Authors: @phil
- Target: Phase P6 (~v0.9.0)
- Supersedes: none (new)

## §1 Summary

Define the transaction model of the self-built distributed SQL. **Single-shard transactions** are forwarded as plain PG `BEGIN/COMMIT`, with zero overhead. **Distributed transactions** are executed as **2PC** with the router as the coordinator, on top of PG's built-in `PREPARE TRANSACTION`. To survive a coordinator outage, the *operator leader pod* owns an etcd lease + transaction log for recovery. When explicitly declared, a **saga** model (user-defined compensation hooks) is also supported. Isolation levels — for single-shard {RC, RR, SER} are all GA / for distributed only {RC + 2PC} is GA in P6 — distributed SERIALIZABLE is considered for v2.0+.

## §2 Motivation

### §2.1 Problem

Atomicity in a distributed DB is in the **C of CAP**. 2PC is a known-good algorithm, but with these implementation pitfalls:

- **Coordinator SPOF**: if the coordinator goes down, PREPARED txns become locked indefinitely → backend pg_xact pressure.
- **Participant failure**: response timeout after PREPARE → blocking or unsafe abort.
- **Recovery**: a new coordinator must know which txns are prepared.
- **Deadlock**: distributed lock graphs are invisible → no distributed deadlock detection.
- **Isolation levels**: distributed SERIALIZABLE requires the distributed version of SSI (Serializable Snapshot Isolation). Very complex.

Most OLTP workloads are sufficient with **READ COMMITTED + single shard**. Even allowing distributed txns only for *rare* cross-shard operations (e.g., money transfer, multi-tenant data migration) covers 99% of cases.

### §2.2 User scenarios

**Scenario 1: single-shard txn (90% case)**
```sql
BEGIN;
  UPDATE users SET balance = balance - 100 WHERE id = 42;
  INSERT INTO transactions (...) VALUES (...);
COMMIT;
```
All rows belong to the same shard (by `tenant_id`) → the router forwards BEGIN/COMMIT directly. Same semantics as a PG single-node transaction. Zero overhead.

**Scenario 2: distributed 2PC (5% case)**
```sql
BEGIN;
  UPDATE accounts SET balance = balance - 100 WHERE id = 1;   -- shard-A
  UPDATE accounts SET balance = balance + 100 WHERE id = 999; -- shard-B
COMMIT;
```
The router detects two participating shards → 2PC. PREPARE both → log → COMMIT PREPARED both.

**Scenario 3: saga (5% case, explicit declaration)**
```sql
-- user-defined saga functions
CALL begin_saga('order_fulfillment');
CALL saga_step('reserve_inventory', 'release_inventory', $$...$$);
CALL saga_step('charge_card', 'refund_card', $$...$$);
CALL saga_step('ship_order', 'cancel_shipment', $$...$$);
CALL commit_saga();
```
On failure, the router runs the registered compensations in reverse order. Non-atomic (eventual consistency), but supports long-running flows.

### §2.3 Non-goals

- Distributed SERIALIZABLE — outside P6. Separate RFC for v2.0+.
- Cross-cluster transactions — permanently unsupported.
- User-defined isolation levels — only the PG standard set.

## §3 Design / Specification

### §3.1 Transaction classification decision

Router's classification algorithm:

```
on BEGIN:
  state = TxState{ shards: {}, mode: Pending }

on each statement:
  plan = planner.Plan(stmt)
  if plan.Type == SingleShard:
    state.shards.add(plan.Shard)
  elif plan.Type == Scatter:
    state.shards.addAll(plan.Shards)
  else:
    state.shards.addAll(allShards)   # broad cross-shard

on COMMIT:
  if len(state.shards) == 1:
    forward COMMIT to that shard       # single-shard path
  elif len(state.shards) > 1:
    execute 2PC with state.shards      # distributed path
  else:
    no-op (empty tx)
```

Even if the first statement is single-shard, a second statement on a different shard auto-escalates to 2PC. The application is not aware of this.

### §3.2 2PC protocol

```
[Phase 1: PREPARE]
  router → shard-A: PREPARE TRANSACTION 'tx-<uuid>-A'
  router → shard-B: PREPARE TRANSACTION 'tx-<uuid>-B'
  (parallel, default timeout 5s)

  [all OK]
    Operator leader writes the commit log to etcd:
      key:   /dtxn/<cluster>/tx-<uuid>
      value: { state: Prepared, shards: [A, B], at: <time> }
      lease: 1h (auto-expires on cleanup)

  [any fail/timeout]
    router → all shards: ROLLBACK PREPARED 'tx-<uuid>-X'
    Response to client: ROLLBACK (40001 retry-able)

[Phase 2: COMMIT]
  router → shard-A: COMMIT PREPARED 'tx-<uuid>-A'
  router → shard-B: COMMIT PREPARED 'tx-<uuid>-B'
  (parallel)

  Operator leader updates etcd:
      state: Committed, completedAt: <time>

  Response to client: COMMIT
```

**uuid naming convention**: `tx-<cluster>-<routerPodName>-<uuidv4>`. The shard-specific suffix `-<shardName>` is appended to prevent collisions with PG's prepared transaction names.

### §3.3 Transaction log (etcd)

The operator leader pod writes an append-only log to etcd:

```
key prefix: /dtxn/<cluster>/

key:   /dtxn/foo/tx-<uuid>
value: protobuf-encoded TxRecord {
  uuid: "..."
  router_pod: "foo-router-7d8b9c-x4k2p"
  shards: ["shard-A", "shard-B"]
  state: STATE_PREPARED | STATE_COMMITTED | STATE_ABORTED
  prepared_at: <ts>
  committed_at: <ts>
  decision: COMMIT | ABORT
}
lease: 1h (renew after Committed/Aborted; short lease for Prepared to prevent leak)
```

The operator leader uses a K8s lease (`coordination.k8s.io/v1`) — election uses the frozen implementation of RFC 0003 (election interface).

### §3.4 Recovery (router crash)

```go
// new router pod startup
func (r *Router) recover(ctx context.Context) error {
    // 1. query PREPARED txns for our podName from etcd
    records, err := r.etcd.GetPreparedByRouter(ctx, r.podName)
    // 2. decide per record
    for _, rec := range records {
        if rec.Decision == COMMIT {
            r.commitPrepared(ctx, rec)   // resume commit phase
        } else if rec.Decision == ABORT {
            r.rollbackPrepared(ctx, rec)
        } else {
            // Crash during Phase 1 — safely send ROLLBACK PREPARED to all shards
            r.rollbackPrepared(ctx, rec)
        }
    }
    return nil
}
```

If a router never returns (Pod deleted), the operator leader's *garbage collector* ROLLBACKs the PREPARED txns after the 1h lease expires. It cross-checks PG's `pg_prepared_xacts` against the etcd log.

### §3.5 saga model

Explicit declaration. The router recognizes it via a PG extension or magic SQL function (decided at P6 implementation):

Option A — annotation-based (preferred):
```sql
/*+ saga(name=order_fulfillment) */ BEGIN;
  /*+ saga_step(forward=$$INSERT INTO orders ...$$,
                compensate=$$DELETE FROM orders WHERE id=...$$) */
  ...
COMMIT;
```

Option B — `CALL` function based (PG 11+):
```sql
CALL pgr.saga_begin('order_fulfillment');
CALL pgr.saga_step('reserve', $$forward sql$$, $$compensate sql$$);
CALL pgr.saga_commit();
```

**Execution semantics**:
- Forward steps *commit sequentially* (each step is an ordinary single- or distributed-txn).
- A failed step runs the registered compensations in *reverse* order.
- Compensation idempotency itself is the *application's responsibility*.
- saga state is recorded at `/saga/<cluster>/<saga_id>` in etcd — recoverable.

**Isolation**: saga gives up the I of ACID (eventual consistency). Other transactions may see rows in between cross-steps. The application must compensate with idempotency tokens / status columns.

### §3.6 Isolation level matrix

| Transaction type | READ COMMITTED | REPEATABLE READ | SERIALIZABLE |
|---|---|---|---|
| single shard | ✓ (plain PG) | ✓ (plain PG) | ✓ (plain PG) |
| distributed 2PC | ✓ (P6 GA) | ⚠ (best-effort, anomalies possible) | ✗ (v2.0+) |
| saga | — (no atomicity, no isolation) | — | — |

Anomaly of distributed RR: snapshots on one shard may not align with the 2PC commit time. *Non-monotonic reads* can occur. Application guidance: use only RC for distributed transactions.

Reason for not providing distributed SERIALIZABLE: distributed SSI requires *predicate tracking + distributed abort*. Huge implementation cost + the copyleft/BUSL avoidance perspective excludes it from P6.

### §3.7 Deadlock detection

Cycles in the distributed lock graph are *currently unhandled*. Per-shard `deadlock_timeout` in PG (default 1s) detects only single-shard deadlocks. Distributed deadlocks:

- *Eventually* resolved by each shard's `lock_timeout` (default 0 → recommend setting 30s).
- On occurrence, handled by application-side retries.

In the long term (P7+), consider introducing a distributed wait-for graph.

### §3.8 Metrics / observability

```
postgresql_dtxn_total{cluster, type=single|distributed|saga, status=commit|abort}  counter
postgresql_dtxn_prepare_duration_seconds{cluster}    histogram
postgresql_dtxn_commit_duration_seconds{cluster}     histogram
postgresql_dtxn_in_flight{cluster, state}             gauge
postgresql_saga_step_duration_seconds{cluster, saga_name, step}  histogram
postgresql_saga_compensation_total{cluster, saga_name}            counter
```

OpenTelemetry: 1 distributed txn = 1 root span + 1 child span per shard + commit/prepare events.

### §3.9 Client-side guidance

What the application needs to know:

1. **Distributed txns are retry-able** (40001 SQLSTATE). Recommend exponential backoff + jitter.
2. **Distributed deadlocks are resolved by lock_timeout** — the application must not assume hangs longer than 30s.
3. **saga requires explicit declaration** — no auto-inference. Business-logic responsibility.
4. **Isolation level**: recommend `SET TRANSACTION ISOLATION LEVEL READ COMMITTED` (for distributed txns).

## §4 Drawbacks / Trade-offs

- **The blocking nature of 2PC**: if the coordinator dies after prepare, prepared txns on shards hold locks → impact lasts until the 1h lease expires. Mitigation: shorter lease (5min) + leader alerts.
- **etcd load**: 2 etcd writes per distributed txn (prepare + commit/abort). At 1k distributed txns/sec, this is ~40% of etcd's write QPS limit (3k~5k). 10k/sec is unfit → recommend a single-shard design.
- **saga is non-ACID**: large user responsibility. Misuse can corrupt data. Mitigation: strengthen docs + e2e examples.
- **No distributed SERIALIZABLE**: some apps need more than SI. Explicitly state that this operator's target workload is OLTP CRUD-centric.

## §5 Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| **Spanner-style TrueTime + MVCC** | Requires clock sync (atomic clock); unfit for K8s |
| **Adopt a BUSL-licensed distributed SSI pattern** | BUSL license risk; zero-code-reuse policy |
| **Single shard only (reject distributed txns)** | Usability ↓; cannot support standard use cases like money transfer |
| **HLC + distributed commit time** | Complexity ↑; exceeds P6 scope. v2.0 candidate |
| **External dtm library (e.g., dtm-labs/dtm)** | Apache 2.0 compatible, but adds a Go service to operate; misaligned with K8s-native policy |

## §6 Open Questions

1. saga declaration syntax — annotation-based vs. CALL-based — need to pick one. Decide after a P6 implementation spike.
2. Lease length for PREPARED txns — 5min vs. 1h trade-off (too short risks false aborts, too long extends lock retention).
3. Algorithm for distributed deadlock detection if introduced in P7+ — Chandy-Misra-Haas? Edge chasing? Separate RFC.
4. Nested saga support — sagas inside sagas? The first version supports only *flat*, nested is for v2.0+.

## §7 Implementation Plan

### P6 (~v0.9.0)

#### P6-T1: single-shard txn (actually works since P2; this RFC just makes the semantics explicit)
- [ ] e2e test for the single-shard path (jepsen-style consistency).

#### P6-T2: 2PC basics
- [ ] `internal/dtxn/coordinator.go` — 2PC state machine.
- [ ] `internal/dtxn/log/etcd.go` — etcd transaction log.
- [ ] Integrate with the router's BEGIN/COMMIT path.
- [ ] e2e: 2-shard transfer, coordinator kill (router pod delete) → atomicity after recovery.

#### P6-T3: recovery
- [ ] Router startup recovery (handle PREPARED txns).
- [ ] Garbage collector on the operator leader (clean up prepared txns past lease).
- [ ] e2e: chaos-mesh scenario killing router/leader simultaneously.

#### P6-T4: saga
- [ ] saga DSL decision (annotation vs. CALL).
- [ ] `internal/dtxn/saga/executor.go` — sequential forward, reverse compensate.
- [ ] e2e: 3-step saga, fail on step 2 → exactly one compensation on step 1.

### Verification commands

```bash
go test ./internal/dtxn/...                          # unit (state machine)
go test ./internal/dtxn/log/etcd/...                 # etcd integration (testcontainers)
make test-e2e PILLAR=p6 -- --focus="2PC"
make test-e2e PILLAR=p6 -- --focus="saga"
make test-jepsen PILLAR=p6                           # consistency (Linearizability for single, RC for distributed)
make test-chaos PILLAR=p6 -- --kill=router
make test-chaos PILLAR=p6 -- --kill=operator-leader
```

Success criteria:
- Single shard: linearizable (same semantics as PG single-node).
- Distributed 2PC: atomicity guaranteed (0 inconsistencies out of 100 chaos test runs).
- saga: 100% compensation execution + idempotency guarantee on partial forward failure.

## §8 References

- Plan: `~/.claude/plans/eager-wobbling-torvalds.md` §3.4
- PostgreSQL Two-Phase Commit: https://www.postgresql.org/docs/18/sql-prepare-transaction.html
- 2PC original: Gray, Jim. *Notes on Database Operating Systems* (1978)
- Saga: Garcia-Molina & Salem, *Sagas* (SIGMOD 1987)
- Spanner (for reference only): https://research.google/pubs/pub39966/
- etcd lease: https://etcd.io/docs/v3.5/learning/api/#lease-api
- RFC 0003: ShardSplitJob (cutover is also a kind of distributed atomic operation)
- RFC 0004: pg-router (coordinator location)
- ADR 0003: License policy (no AGPL/BUSL)
