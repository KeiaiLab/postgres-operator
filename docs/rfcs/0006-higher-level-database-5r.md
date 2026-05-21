# RFC 0006: A higher-level Postgres Operator — 5R refactoring blueprint

- **Status**: Proposed → Accepted (R1 + R2 Implemented 2026-05-03)
- **Authors**: phil
- **Date**: 2026-05-03
- **Refs**: RFC 0001 (CRD v2), RFC 0003 (election + fencing), RFC 0005 (sharding plugin), ADR 0002 (PID 1 model)

## §1 Context — why do we need a "higher level"

An early self-validation pass on ours 0.3.0-alpha (kind v0.31, same node) revealed two truths:

1. **We are smaller on resource footprint**: Pod RSS, manager image, and LoC are substantially smaller than typical full-feature operators. But this is the surface of *decisively missing functionality*.
2. **Vaporware portion of "alpha-deployable"**: the same measurement on our side exposed 3 production bugs (RBAC escalation / forced plugin auto-register / Promote race + already-primary). This is a class that *only surfaces in a real K8s environment*, which unit + envtest could not catch.

Looking at both truths simultaneously, the conclusion is: if we compete by simple *feature catch-up*, we lose the differentiation that *small is a virtue*, and we just become an *incomplete clone of a feature-rich operator*. **Differentiation = identify what existing OSS PG operators structurally cannot do**, and fill that with code volume *in our LoC shape*.

This RFC is that blueprint — defining capabilities that 5 atomic refactors (R1~R5) *unlock* stage by stage.

## §2 5R blueprint

### R1 — Plugin Registry per-cluster + spec.extensions opt-in (Implemented 2026-05-03 commit f7db838)

**Status**: ✅ Implemented.

**Problem**: self-validation bug 2 — all 6 extensions forced on every cluster → vanilla PG image FATAL.

**Solution**:
- `Registry.EnabledExtensions(names)` — explicit opt-in filter.
- `PostgresClusterSpec.Extensions []string` — the user decides per cluster.
- Webhook blocks unregistered names at the admission stage.
- The Register call in `cmd/main.go` = *catalog registration* (not activation). spec.extensions = *activation*.

**Unlock**: per-tenant differentiation (one operator runs different extension sets across multiple clusters).

### R2 — InstanceStatus Feedback Channel (Implemented 2026-05-03)

**Status**: ✅ Implemented.

**Problem**: `PostgresCluster.status.shards[]` is a reconcile-time approximation. Endpoint is always the ord-0 Pod, LagBytes=0 (placeholder), Ready=ReadyReplicas (unrelated to election).

**Solution**:
- New `internal/instance/statusapi` package — `Status{Role, Ready, Endpoint, LagBytes, LastUpdate}` data model.
- Every 5s the instance manager patches its own Pod annotation `postgres.keiailab.io/instance-status`.
- Controller `aggregateShardStatus` — list via Pod label selector → parse annotations → compose primary/replicas.
- Detect split-brain (≥2 primary annotations) + handle stale heartbeats (>30s).

**Unlock**:
- Real-time topology view — the actual leader Pod becomes status.Primary.
- Failover RTO is measurable (annotation timestamp).
- Decision signal for the future R3 (standby.signal aware boot).

### R3 — standby.signal-aware Boot + auto failover (Proposed)

**Status**: 🚧 Proposed (next cycle).

**Problem**: today the instance manager always *assumes primary entry* at boot. When replicas≥1, a Pod with ord!=0 becomes primary (the election agrees on one leader but the supervise side acts as primary on all).

**Solution design**:
1. **Init container decides standby.signal** — if ord==0 (or first boot), the init container runs `pg_basebackup --pgdata $PGDATA --host=primary-endpoint` to replicate data, then `touch $PGDATA/standby.signal`. ord==0 (first cluster) does initdb.
2. **Instance manager honors standby.signal** — at boot, check for PGDATA/standby.signal → standby mode (do not call pg_promote).
3. **On Promote, OnStartedLeading removes standby.signal** — `os.Remove(PGDATA/standby.signal)` then `pg_promote()`.
4. **OnStoppedLeading re-creates standby.signal + sup.Stop fast** — after fencing its own PVC, re-create standby.signal and instance exit. K8s restarts the Pod → instance boots in standby mode.

**Unlock**:
- Auto failover (when replicas≥1) — primary kill → election picks a new leader → the old primary Pod rejoins as a new standby.
- Auto-bootstrap of streaming replication.
- Completes the F03 active side.

**Open**:
- pg_basebackup's primary endpoint discovery — R2's status feedback can tell us the *current primary* (have the init container read controller status?). Or the controller injects it as an init container env.
- Replication slot management (primary creates a slot per standby; the standby uses it). RFC 0003 Appendix B.
- secret-backed replication user authentication (currently trust → scram-sha-256).

### R4 — Multi-controller Split (Proposed)

**Status**: 🚧 Proposed (after R3).

**Problem**: a single `PostgresClusterReconciler` handles every sub-resource (RBAC + ConfigMap + STS + Service + Status aggregation + Router Deployment) in one reconcile. It will get heavier when Backup is added. F04 (BackupController) and F03-active (FailoverController) need separate lifecycles.

**Solution design**:
- `PostgresClusterReconciler` — handles only topology (RBAC, ConfigMap, Service, STS, Router) + status aggregation.
- `ShardController` (new) — lifecycle of a single shard (STS bootstrap, replication slot, standby join).
- `BackupController` (F04) — focused on `BackupJob` CR + evaluates `PostgresCluster.spec.backup` cron schedules.
- `FailoverController` (F03-active) — watches Pod annotations → triggers demote when primary is stale.

Each controller reconciles independently + has its own goroutine + its own watcher.

**Unlock**:
- Independent fail isolation — a backup reconcile failure does not block cluster reconcile.
- Code-review unit split — one PR changes one controller.
- Test isolation — envtest scenarios shrink.

**Open**:
- Inter-controller communication — use only the Status subresource? Or Pod annotation events?
- Watcher duplication — multiple controllers watching the same Pod adds K8s API load.

### R5 — Native Distributed SQL Active (Proposed — our unique differentiation)

**Status**: 🚧 Proposed (long term — P2+, can be split into a separate RFC).

**Problem**: `shardingMode: native` only has a schema + the RFC 0005 plugin SDK. Actual distributed SQL (cross-shard query, distributed catalog, range-based shard key, ShardSplitJob) is absent. Existing OSS PostgreSQL operators support only single-shard primary + replicas — *no multi-shard PostgreSQL operator currently exists in the Apache-2.0 OSS space*. Ours is a self-built layer on top of vanilla PG.

**Solution design** (RFC 0005 ShardingPlugin Active side):

#### R5a — Distributed Catalog
- New CRD `ShardCatalog` — per-cluster shard key → shard ordinal mapping.
- The catalog is what the router uses for query routing. It holds ranges per shard (e.g., `user_id 0~999 → shard-0`).
- Catalog changes (split, merge, rebalance) are atomic via ShardSplitJob (RFC 0003).

#### R5b — Router Active Logic
- The current router Deployment is a placeholder (PG image as-is). New cmd/router binary.
- router = libpq protocol parser + query rewriter. Extracts the client query's shard key → catalog lookup → forwards to the shard's backend.
- Single-shard query: zero hop (router decides the shard and forwards directly).
- Multi-shard query: router fans out + scatter-gathers (aggregate queries).
- Cross-shard transaction: 2PC (XA-like) — RFC 0005 Phase 3.

#### R5c — Auto-split active side
- When `AutoSplit.Triggers` (e.g., sizeThresholdGB) are satisfied, the controller creates a ShardSplitJob.
- ShardSplitJob runs the RFC 0003 7-step (initdb, pg_basebackup, range copy, catalog update, primary cutover, drain, cleanup).
- When requireApproval=true, proceeds only after the operator annotates approval.

**Unlock**:
- *The only OSS multi-shard PostgreSQL operator* (vanilla-PG based).
- Horizontal scale of TB-class datasets.
- Even a single cluster can be multi-region (each shard in a different region).

**Open**:
- SQL parsing for the query rewriter — own implementation vs. a wrapper based on pgproto3.
- Safety of prepared transactions for cross-shard 2PC — postgres prepared_transactions setting + recovery scenarios.
- Catalog consistency — using a separate DCS like etcd vs. embedded in PostgresCluster.status.

## §3 Priority + dependencies

```
R1 (extensions opt-in) ──┐
                         │
R2 (status feedback)  ───┼──→ R3 (standby boot)  ───→  R4 (multi-controller)
                         │                        ↘
                         └──────────────────────→  R5 (native sharding active)
```

- R1 + R2 = finished in the current commit chain (df7a0ca + f7db838 + follow-ups). The last missing piece of *production-grade single-shard*.
- R3 = the last piece of production HA (auto failover with replicas≥1).
- R4 = a natural consequence after R3.
- R5 = differentiation — done in a separate multi-cycle effort (the P2 body).

## §4 Phase definitions

| Phase | Version | R1 | R2 | R3 | R4 | R5 | Capability summary |
|---|---|---|---|---|---|---|---|
| **alpha** (current) | 0.3.0 | ✅ | ✅ | ❌ | ❌ | schema only | single-shard primary only |
| **beta** | 0.4.0 | ✅ | ✅ | ✅ | ❌ | schema only | single-shard HA |
| **GA-single** | 1.0.0 | ✅ | ✅ | ✅ | ✅ | schema only | single-shard HA + lighter footprint |
| **GA-distributed** | 2.0.0 | ✅ | ✅ | ✅ | ✅ | ✅ | multi-shard distributed SQL |

Acceptance moment of this RFC (2026-05-03): **enter alpha — R1/R2 complete**. The next cycle is R3 (beta).

## §5 Failure Modes (scenarios that break each R's hypothesis)

### R1 failure possibilities
- User leaves `extensions=[]` → vanilla as-is (intended default). Not a failure.
- User specifies an extension not in the image → admission webhook blocks. Not a failure.
- *Image catalog problem*: the webhook validates plugin registration but does not validate the image's .so contents → user responsibility. **R1 Phase 2: per-extension image catalog + auto-image-selection**.

### R2 failure possibilities
- Instance manager's patch fails (RBAC absent, API load) → stale annotation → controller detects stale heartbeat + forces Ready=false. Failure = graceful degraded mode.
- Annotation race (controller reading while instance patches) → strategic merge patch is atomic. Not a failure.
- *Two simultaneous primary annotations*: detect split-brain + log warning + keep the first candidate. **This is the verification responsibility of R5 + RFC 0003 fence; R2 itself only reports gracefully**.

### R3 failure possibilities
- pg_basebackup failure (network / permission) → init container retries → Pod CrashLoopBackOff. Controller exposes via status.condition. Manual user intervention needed.
- Old primary not fenced (clock skew, lease expiry race) → both try to promote. *PVC label fence (RFC 0003 Appendix A) fail-fasts — a primary inspects its own PVC's fenced=true label and rejects*. Correct behavior.
- standby.signal absent race (right after Pod restart, instance boots first → enters primary mode) → the init container *must run first*. K8s guarantees init container ordering.

### R4 failure possibilities
- Multiple controllers writing to the same status.subresource → race. **Single-writer principle**: each controller updates *only its own area's conditions* (e.g., BackupController = backup-only conditions).
- Watcher event flood — broad RBAC + frequent Pod changes can cause reconcile loops to runaway. Filter with controller-runtime predicates + workqueue rate limit.

### R5 failure possibilities
- Limits of the query rewriter's SQL parsing — not all PG queries can be parsed safely. *fallback to broadcast* — when in doubt, fan out to all shards (slower but correct).
- Cross-shard 2PC — coordinator failure leaves prepared transactions behind. A *2PC recovery daemon* is needed (separate RFC 0007).
- Catalog consistency — catalog state consistency across operator restart. The K8s status subresource is the single truth, and etcd's RAFT guarantees consistency.

## §6 Open Questions (decide after acceptance)

1. **R3 replication user authentication** — currently trust (alpha). At what point should secret-backed scram-sha-256 be enforced? (Proposal: 0.4.0 beta).
2. **R5 router image** — cmd/router as a separate binary (Go) vs. a PgBouncer fork. (Proposal: Go native — freedom of a protocol-level rewriter).
3. **R5 catalog storage** — inside PostgresCluster status vs. a separate ShardCatalog CRD. (Proposal: separate CRD — aligns with RFC 0005's ShardSplitJob).
4. **R4 controller binary split** — multiple controllers inside a single manager binary (current controller-runtime pattern) vs. separate deployments. (Proposal: single binary — avoid operational overhead).
5. **alpha → beta backwards compat** — spec/status schema changes with R3. (Proposal: in-place v1alpha1 update + no CRD storage migration — alpha stage means 0 external users).

## §7 Measurable success criteria

| Phase | Metric | Target |
|---|---|---|
| alpha (R1+R2) | Self-validation re-run passes alpha-deployable (smoke.sh) | Pod Ready < 60s |
| beta (R3) | RTO until new primary after killing primary in a replicas=2 cluster | < 30s |
| GA-single (R4) | Number of times cluster reconcile is blocked while a Backup CR is applied | 0 (independent reconcile) |
| GA-distributed (R5) | Cross-shard query accuracy on a shardCount=4 cluster | 100% match (single-shard reference) |

## §8 The essence of this RFC's cycle 5R

What self-validation taught us: *few features is not lightweight — unverified features are vaporware*. 5R is verified by *each R having its own unit test + envtest + self-validation remeasurement*. 1 R = 1 atomic commit + 1 deployable cycle. The RFC draws the *big picture* but each cycle proceeds in *small, verifiable* units.

The philosophy: a small LoC base *competes* with much larger operators because **every single line is verified**.
