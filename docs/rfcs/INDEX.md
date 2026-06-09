# RFC Index — postgres-operator

This page lists the active Request-for-Comments drafts that describe the
design of major sub-systems. Each RFC is a self-contained spec for a
specific scope; ADRs (`docs/kb/adr/`) record the decisions that adopt or
reject those proposals.

## Active RFCs

| RFC | Title | Scope |
|---|---|---|
| [0001](0001-postgrescluster-crd-v2.md) | `PostgresCluster` CRD v2 | Shard-aware topology, status conditions, webhook validation |
| [0002](0002-shardrange-crd.md) | `ShardRange` CRD | Sharding metadata source-of-truth (replaces external sharding-node catalog) |
| [0003](0003-shardsplitjob-7step.md) | `ShardSplitJob` 7-step workflow | Online resharding lifecycle |
| [0004](0004-pg-router-architecture.md) | `pg-router` architecture | Stateless query router, vindex evaluation, scatter-gather |
| [0005](0005-distributed-transactions.md) | Distributed transactions | Self-built 2PC + saga coordinator |
| [0006](0006-higher-level-database-5r.md) | Higher-level Database 5R | Declarative `PostgresDatabase` / `PostgresUser` / `Pooler` / `ScheduledBackup` / `ImageCatalog` |
| [0007](0007-ha-election-and-fencing.md) | HA election and fencing | K8s `Lease` based primary election + STONITH |

## Status legend

- **Draft** — design in progress, may change.
- **Accepted** — design frozen, implementation underway. Linked ADR records adoption.
- **Implemented** — code lands in `internal/` and `api/v1alpha1/`. Tracking via Gate progress (see [`ROADMAP.md`](../ROADMAP.md)).
- **Superseded** — replaced by a later RFC; superseded RFCs are deleted, not archived (this index lists only active drafts).

## Reading order

For a top-down read of the architecture, start with [`ROADMAP.md`](../ROADMAP.md) and [`ARCHITECTURE.md`](../ARCHITECTURE.md), then drill down via RFC links above for each sub-system.

---

<p align="center">
  © 2026 keiailab · <a href="../../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
