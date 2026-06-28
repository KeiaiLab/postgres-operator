# Reshard Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining correctness risks around query routing, reshard-copy topology fidelity, online reshard abort cleanup, and ADR-0029 target promotion.

**Architecture:** Work proceeds in small code batches, but verification is grouped into explicit checkpoints to avoid repeatedly starting Docker/WSL/VM resources. The first batch fixes low-blast-radius router and reshard-copy correctness gaps. The second batch implements abort cleanup. The third batch advances ADR-0029 promotion only after shard identity selectors are audited.

**Tech Stack:** Go 1.26, controller-runtime, Kubernetes envtest, PostgreSQL logical replication, pg-router wire protocol code, ShardRange/ShardSplitJob CRDs.

---

## Execution Policy

- Keep Docker Desktop, WSL, and local VMs stopped during development unless a verification checkpoint explicitly requires them.
- Do not run tests after every small edit. Run grouped verification only after a coherent batch is complete.
- Prefer native `go test` on Windows/host first. Start Docker/kind only for the live e2e checkpoint.
- If a generated API/CRD file is affected, run `make manifests generate` and `make sync-crds` at the checkpoint, not after every individual edit.
- Commit after each verified batch.

## Batch 0: Low-Blast-Radius Correctness Hardening

**Files:**
- Modify: `internal/router/query_router.go`
- Modify: `cmd/pg-router/persession.go`
- Modify: `cmd/pg-router/scattermode.go`
- Modify: `internal/controller/shardsplitjob_copy.go`
- Modify: `cmd/reshard-copy-poc/main.go`
- Add or modify tests after the batch: `internal/router/query_router_test.go`, `cmd/pg-router/querymode_test.go`, `cmd/pg-router/scattermode_test.go`, `cmd/reshard-copy-poc/main_test.go`, `internal/controller/shardsplitjob_writeblock_test.go`

- [x] Stop reference-table writes from being routed to one arbitrary shard.
  - In `QueryRouter.Route`, only allow `ReferenceOnly(query)` fast path for read-only queries.
  - In `cmd/pg-router/persession.go`, only scatter keyless read-only simple queries. Return a SQL error for keyless writes.

- [x] Ensure all scatter error paths send `ReadyForQuery`.
  - Replace direct `writePgError(...)` returns in `scatterQuery` failures with an error + `ReadyForQuery('I')` helper.

- [x] Preserve ShardRange vindex type in reshard-copy jobs.
  - Extend `keyspaceVindex` to return `Vindex.Type`.
  - Pass `PGROUTER_VINDEX_TYPE` into reshard-copy Jobs.
  - Update `reshardSpec` to construct hash/range/consistent-hash specs according to env, defaulting to hash for standalone use.

- [x] Add focused tests for the above.
  - `UPDATE countries ...` must not go to `AnyShard`.
  - keyless `UPDATE t SET ...` in query mode must not scatter.
  - scatter transport/routing errors must include `ReadyForQuery`.
  - `PGROUTER_VINDEX_TYPE=range` must build a range spec rather than hash.
  - controller Job env must include `PGROUTER_VINDEX_TYPE`.

Status as of 2026-06-28: code and focused tests are written. `go`/`gofmt` are not available on the Windows host after Docker/WSL shutdown, so the checkpoint command has not been run yet. `git diff --check` passes.

**Checkpoint Verification:**

Run once after Batch 0 code and tests are complete:

```powershell
go test -count=1 ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./internal/controller
```

Expected: all listed packages pass without starting Docker/WSL.

## Batch 1: Online Reshard Abort Cleanup

**Files:**
- Modify: `internal/controller/shardsplitjob_controller.go`
- Modify: `internal/controller/shardsplitjob_copy.go`
- Modify or add tests: `internal/controller/shardsplitjob_writeblock_test.go`
- Optional helper: `internal/controller/shardsplitjob_abort.go`

- [x] Add a cleanup path for `Failed` and `Aborted` online resharding jobs.
  - Drop target subscription.
  - Drop source publication.
  - Ensure write-block is released or status explicitly records that manual intervention is required.

- [x] Keep target StatefulSet/PVC by default.
  - Do not delete target data automatically on abort; it is needed for RCA and manual recovery.

- [x] Record cleanup failures in status.
  - Prefer a condition or failure reason that tells the operator which pub/sub/slot artifact remains.

Status as of 2026-06-28: `cdc-abort` Job mode and terminal `Failed`/`Aborted` cleanup reconciliation are implemented. Controller tests now cover cdc-setup failure, cdc-finalize failure, abort cleanup idempotency, and `AbortCleanup=False` status on cleanup failure. The checkpoint command has not been run yet because the host Go toolchain is unavailable and Docker/WSL remain intentionally stopped.

**Checkpoint Verification:**

Run once after Batch 1:

```powershell
go test -count=1 ./internal/controller
```

Expected: controller tests cover cdc-setup failure, cdc-finalize failure, and abort cleanup idempotency.

## Batch 2: ADR-0029 P-A.2 Selector Audit And Promotion Prep

**Files:**
- Audit/modify: `internal/controller/aggregate_status.go`
- Audit/modify: `internal/controller/metrics.go` or metrics-related builders if present
- Audit/modify: failover controller files under `internal/controller` and `internal/instance`
- Modify: `docs/kb/adr/0029-reshard-target-promotion-identity-transition.md`
- Modify: `docs/WORK_HANDOFF.ko.md`

- [x] Inventory every ordinal shard selector.
  - Search for `postgres.keiailab.io/shard`, `ShardStatefulSetName`, `shard-`, and ordinal parsing.
  - Classify each use as either identity, resource naming, compatibility, or legacy selector.

- [x] Generalize status aggregation to `shard-id`.
  - Keep backward-compatible fallback for existing ordinal shards.
  - Do not change StatefulSet selectors in-place.

- [x] Prepare Promote phase design notes before coding P-B.
  - Define exact fence/adopt/status/decommission order.
  - Define idempotency markers.
  - Define live chaos drill requirements.

Status as of 2026-06-28: selector audit is documented in ADR-0029 P-A.2. `aggregateShardStatus` now lists by cluster-level labels and filters pods by legacy `postgres.keiailab.io/shard=<ord>` OR additive `postgres.keiailab.io/shard-id=shard-<ord>`, without changing StatefulSet or Service selectors. Metrics/failover remain status consumers. Named target shard rows such as `shard-id=t1` are intentionally left for Promote P-B/P-C.

**Checkpoint Verification:**

Run once after Batch 2:

```powershell
go test -count=1 ./internal/controller
```

Expected: existing ordinal clusters still aggregate status, and target shards with `shard-id` can be observed without selector mutation.

## Batch 2.5: Reshard Target Service Endpoint

**Files:**
- Modify: `internal/controller/builders.go`
- Modify: `internal/controller/builders_test.go`
- Modify: `cmd/instance/main.go`
- Modify: `cmd/instance/main_test.go`

- [x] Pass the actual StatefulSet `serviceName` to the instance manager as `POSTGRES_SERVICE_NAME`.
  - Ordinal shards receive `<cluster>-shard-<ordinal>-headless`.
  - Reshard targets receive `<cluster>-rsd-<target>-headless`.

- [x] Build status endpoints from the provided service name.
  - Keep the legacy ordinal fallback when the env var is absent so already-running Pods remain compatible during upgrades.
  - Prevent reshard target Pods from reporting `*.shard-0-headless` endpoints before Promote P-B/P-C.

**Checkpoint Verification:**

```powershell
go test -count=1 ./cmd/instance
go test -count=1 ./internal/controller -run TestBuildTargetShardStatefulSet_Isolation
go test -count=1 ./cmd/instance ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./internal/controller
```

Status as of 2026-06-28: all three checkpoint commands pass on Windows Go 1.26.4. Docker Desktop and WSL remain stopped.

## Batch 2.6: Active Named Target Status

**Files:**
- Modify: `internal/controller/aggregate_status.go`
- Modify: `internal/controller/aggregate_status_test.go`
- Modify: `internal/controller/postgrescluster_controller.go`
- Modify: `internal/controller/postgrescluster_controller_test.go`

- [x] Add `PostgresCluster.status.shards[]` rows for active non-ordinal shard names found in `ShardRange.spec.ranges`.
  - Ordinal names such as `shard-0` remain handled by the existing ordinal loop.
  - Non-ordinal targets such as `t1` are appended with `name=t1` and `ordinal=-1`.

- [x] Aggregate target Pods by `postgres.keiailab.io/reshard-target=<id>` or adopted `postgres.keiailab.io/shard-id=<id>`.
  - Reuse the same annotation, stale heartbeat, fenced PVC, rogue-primary, and PodReady logic as ordinal shards.
  - Mark overall shard readiness false when an active named target has no Ready primary.

**Checkpoint Verification:**

```powershell
go test -count=1 ./internal/controller -run TestAggregateNamedShardStatus_UsesReshardTargetLabel
go test -count=1 ./internal/controller --ginkgo.focus="adds active named reshard targets"
go test -count=1 ./internal/controller
go test -count=1 ./cmd/instance ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./internal/controller
```

Status as of 2026-06-28: all checkpoint commands pass on Windows Go 1.26.4. This is a P-B status/backend-resolution slice only. Source decommission, target replica scale-up/HA, and named shard spec-model migration remain open.

## Batch 3: Native Router Concurrent-Write E2E Design

**Files:**
- Modify: `docs/WORK_HANDOFF.ko.md`
- Modify: `docs/sharding/ROUTER-GAP-ANALYSIS.ko.md`
- Add or modify live e2e test files only after the scenario is finalized.

- [x] Document the native-router concurrent-write scenario.
  - All client writes go through `PGROUTER_MODE=query`.
  - Online `ShardSplitJob` runs while writes continue.
  - Write-block must reject writes with `ReadyForQuery`.
  - RoutingUpdate must move post-cutover writes to the target shard.
  - Final validation must check row count, checksum, key ownership, source cleanup, target indexes, and constraints.

- [x] Include PK-less target verification.
  - Validate UPDATE/DELETE logical replication behavior when target starts without PK and receives schema/indexes later.

**Checkpoint Verification:**

This batch is design-first. Live verification should be run only when Docker/kind resources are intentionally restarted.

Status as of 2026-06-28: the scenario is documented in `docs/sharding/ROUTER-GAP-ANALYSIS.ko.md`. It covers native router writes, online CDC, write-block `ReadyForQuery`, target routing update, checksum/key ownership validation, schema/index/constraint validation, PK-less target behavior, and abort cleanup. No live e2e has been run because Docker/kind remain intentionally stopped.

## Final Verification Gate

Run after all selected batches are complete:

```powershell
go test -count=1 ./cmd/instance ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./internal/controller
make test-integration
```

Live gate, only when resources are intentionally available:

```powershell
kind create cluster --name pgop-dev
# Build/load operator and reshard-copy images, deploy operator, then run offline and online ShardSplitJob e2e.
```

---

## Self-Review

- Spec coverage: resource conservation, development-first workflow, router correctness, reshard-copy vindex fidelity, abort cleanup, ADR-0029 promotion prep, and native concurrent-write e2e are covered.
- Placeholder scan: no task is left as an undefined placeholder; each batch names files, behavior, and verification.
- Type consistency: `PGROUTER_VINDEX_TYPE`, `Vindex.Type`, `shard-id`, `WriteBlocked`, `ReferenceOnly`, and `ReadyForQuery` names match the current codebase vocabulary.
