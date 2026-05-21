---
title: "keiailab/postgres-operator"
description: "Apache-2.0 PostgreSQL Kubernetes Operator — independent new implementation with no embedded external backend"
---

This operator is an independent new implementation that builds a *self-built distributed SQL layer* on top of vanilla PostgreSQL 18+ in a K8s native fashion (ADR 0001 keystone). No external PostgreSQL operator runtime is embedded into the product or repackaged as a wrapper. External backend dependencies (AGPL/BUSL/CSL/SSPL) are permanently forbidden (ADR 0003).

If you want to bring up a cluster in 5 minutes, head to the [Quickstart](/tutorials/quickstart). If you are curious about the *why* behind the design decisions, read [ADR 0001](/adr/0001-self-built-distributed-sql) first.

## Key features

- **Declarative PostgresCluster**: the operator creates the StatefulSet, Service, instance RBAC, and network policy.
- **K8s lease-based HA roadmap** (RFC 0003): no third-party HA agent. Uses the K8s API as the DCS.
- **Self-managed ShardRange metadata roadmap** (RFC 0002): the K8s CRD is the source of truth — no external KV layer or third-party distributed-node catalog required.
- **Stateless QueryRouter roadmap** (RFC 0004): horizontal scaling via HPA, PgBouncer integration, lossless Pod restart targeted.
- **Distributed transactions roadmap** (RFC 0005): self-built 2PC + saga — independent of backend extensions.

## Current verification state

- `0.3.0-alpha.4` image/chart/SBOM publish and release-smoke 12 PASS / 0 FAIL complete.
- argos `platform-data-postgres-operator` ArgoCD Application confirmed `Synced/Healthy`, controller Deployment `1/1` live.
- argos `data` namespace confirms `PostgresCluster/argos-postgres` single-shard `Ready=True`.
- After switching `ghcr.io/keiailab/pg:18` to public pull, restart confirmed without pull secret.
- HA replica, backup/restore drill, and long-running soak remain as GA conditions.

## Documentation structure

- [`/architecture/`](/architecture/) — System design overview
- [`/adr/`](/adr/) — Architecture Decision Records (0001~0005)
- [`/rfcs/`](/rfcs/) — Per-phase design RFCs (0001~0005)
- [`/api-reference/`](/api-reference/) — CRD specs
- [`/runbooks/`](/runbooks/) — Operational procedures
- [`/tutorials/`](/tutorials/) — Getting started

## License

[Apache 2.0](https://github.com/keiailab/postgres-operator/blob/main/LICENSE) © 2026 keiailab.
