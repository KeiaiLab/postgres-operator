---
title: "keiailab/postgres-operator"
description: "Apache-2.0 PostgreSQL Kubernetes Operator — independent new implementation with no embedded external backend"
---

This operator is an independent new implementation that builds a *self-built distributed SQL layer* on top of vanilla PostgreSQL 18+ in a K8s native fashion (ADR-0001 keystone). No external PostgreSQL operator runtime is embedded into the product or repackaged as a wrapper. External backend dependencies (AGPL/BUSL/CSL/SSPL) are permanently forbidden (ADR-0003).

If you are curious about the *why* behind the design decisions, read [ADR-0001](kb/adr/0001-self-built-distributed-sql.md) first. For deployment, see the [deployment guide](operator-guide/deployment.md).

## Key features

- **Declarative PostgresCluster**: the operator creates the StatefulSet, Service, instance RBAC, and network policy.
- **K8s lease-based HA** ([RFC-0007](rfcs/0007-ha-election-and-fencing.md)): no third-party HA agent. Uses the K8s API as the DCS.
- **Self-managed ShardRange metadata** ([RFC-0002](rfcs/0002-shardrange-crd.md)): the K8s CRD is the source of truth — no external KV layer or third-party distributed-node catalog required.
- **Stateless QueryRouter** ([RFC-0004](rfcs/0004-pg-router-architecture.md)): horizontal scaling via HPA, PgBouncer integration, lossless Pod restart targeted.
- **Distributed transactions** ([RFC-0005](rfcs/0005-distributed-transactions.md)): self-built 2PC + saga — independent of backend extensions.

## Documentation structure

- [Architecture](ARCHITECTURE.md) — System design overview
- [ADRs](kb/adr/INDEX.md) — Architecture Decision Records
- [RFCs](rfcs/INDEX.md) — Per-phase design RFCs
- [Runbooks](runbooks/INDEX.md) — Operational procedures
- [Deployment](operator-guide/deployment.md) — Installation and deployment
- [Upgrading](UPGRADING.md) — Version migration guide
- [Gap Analysis](gap-analysis/GAP-ANALYSIS.md) — CNPG feature comparison and migration roadmap

## License

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
