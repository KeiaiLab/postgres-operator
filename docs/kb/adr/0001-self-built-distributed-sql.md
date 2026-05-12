# ADR-0001: Build a self-built distributed-SQL layer on PostgreSQL

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil
- Supersedes: `_archive/v0.x/0001-stateless-query-router-on-citus.md`, `_archive/v0.x/0010-license-and-sharding-strategy.md`

## Context

The project evolved through 0.2.0-alpha as a Kubernetes operator that
runs PostgreSQL. At ADR-0010 (2026-05-01) it was on its way to a *dual
backend* model: *Citus extension under AGPL isolation + vanilla PG18 as
default*. On 2026-05-02, after a comparative analysis of similar
systems (Citus / YugabyteDB / CockroachDB / Vitess / CloudNativePG), we
confirmed that no product on the market simultaneously satisfies all of
the following:

1. **100% PostgreSQL wire / SQL compatibility** — distribution can be
   adopted without changing application code.
2. **License-clean** (Apache-2.0 / BSD / MIT / PG License only) — no
   obligations when exposed as commercial SaaS.
3. **K8s-native integration** — CRDs + reconcilers + KEDA-driven auto
   sharding.
4. **Auto sharding** (write-side scale-out) — closes the Citus gap of
   manual-split-only.

At decision time (2026-05-02), the user (eightynine01@gmail.com) chose
**C** out of four options — A: Citus packaging; B: a pragmatic
integration (pgcat + Citus rebalancer delegate + KEDA auto-split + CNPG
HA embed); C: a full self-built distributed-SQL — and additionally
specified **remove every external backend dependency** and **single
chart + flags**. On 2026-05-07 we narrowed the principle further: we
may reference external system designs, but we cannot embed those
systems; we must **build as a new service**. This is an ambitious
6+-year decision; *long-term differentiation comes not from invention
alone but from invention + license-clean + K8s-native achieved
simultaneously* — a redefinition of the product identity.

## Decision

We will build a *self-built distributed-SQL layer* on top of
PostgreSQL. We permanently exclude code from Citus / CloudNativePG /
Patroni / CockroachDB from our dependency graph. Phrases such as
"PGO-class" / "Citus-class" describe quality bars and problem domains
in comparison; they do not mean embedding those products' controllers,
CRDs, extensions, or runtimes.

Key parameters:

- **Operator core license**: Apache-2.0 (unchanged).
- **External OSS dependency policy**: see ADR-0003 (license policy).
  AGPL / BUSL / CSL / SSPL are permanently forbidden. Only BSD / Apache
  / MIT / PG License with v1+ stability commitment are allowed.
- **In-house components** (details in `docs/architecture/`):
  - `pg-router` — PostgreSQL wire-protocol parser, vindex evaluation,
    scatter-gather, distributed-transaction coordinator.
  - `vindex` module — hash / range / consistent-hash / lookup.
  - `ShardRange` CRD — source of truth for keyspace + key range + shard
    placement.
  - `ShardSplitJob` CRD + resharder controller — 7-step online
    resharding workflow.
  - `Rebalancer` controller — homegrown shard-placement rebalancer.
  - Distributed-transaction coordinator — 2PC (using PG `PREPARE
    TRANSACTION`) + saga.
  - HA — based on the instance manager (uses the RFC 0003 P2-T1 frozen
    interface; no Patroni).
- **Reusable assets**: pgBackRest (BSD-2), pg_query_go (PG License),
  controller-runtime (Apache-2.0), KEDA (Apache-2.0). All meet ADR-0003.
- **Removed backend dependencies**: Citus extension, Citus metadata
  (`pg_dist_*`), CloudNativePG `Cluster` CR, Patroni DCS, CockroachDB
  range-KV layer. Zero lines of code; only documents / papers /
  operational idioms may be referenced.
- **Clean-room re-implementation**: we may read external systems'
  public designs and reuse their problem decomposition, but
  implementation is rewritten as new types, controllers, instance
  managers, and routers in this repo.
- **Helm packaging** (ADR-0002): one chart + component flags (router /
  resharder / rebalancer / keda / backup / monitoring).
- **CRD lifecycle** (ADR-0004): owned by the operator manager
  (server-side apply); the Helm `crds/` directory is retired.
- **Version channels** (ADR-0005): alpha / beta / stable. CRD
  apiVersion v1alpha1 → v1beta1 → v1.
- **Phase roadmap** (`docs/roadmap.md`): P0 (redesign reset, 0.3.0) →
  P1 (single-shard production-ready, 0.4.0) → P2 (multi-shard manual,
  0.5.0) → P3 (vindex extension + read autoscale, 0.6.0) → P4 (online
  split, 0.7.0) → P5 (auto split + rebalance, 0.8.0) → P6 (distributed
  transactions, 0.9.0) → P7 (stabilization + Artifact Hub verified,
  1.0.0). Estimate: ~64 months (5.3 years) at one engineer at 50%
  capacity.
- **Production guarantee**: each phase ends with a *deployable stable
  version*. From P1 onward single-shard production use is supported.

## Consequences

**Positives**:

- *Zero license incidents, permanently* — AGPL contagion, BUSL
  commercial restrictions, and SSPL FUD are all avoided. SaaS exposure
  is free.
- *100% PostgreSQL compatibility* — overcomes the limits of forks
  (YugabyteDB) and wire-only solutions (Cockroach at ~40%). Every
  PG 18+ extension / type / function remains available.
- *K8s-native metadata integration* — the `ShardRange` CRD is the
  source of truth and etcd becomes the distributed-metadata store. A
  separate KV layer (Cockroach Range, Citus `pg_dist_node`) is not
  needed.
- *Clear differentiation* — "K8s-native + license-clean + auto sharding
  for vanilla PG" is not currently on the market.
- *Learning value* — internalizing the distributed-SQL knowledge of
  Citus (8 years) and Vitess (10 years) by re-implementing it directly.

**Negatives / costs**:

- *6+-year timeline* — ~64 months at one engineer at 50% capacity;
  realistically 6+ years is possible. Mid-way abandonment risk is
  mitigated by each phase shipping a production-deployable artifact
  (P1 single-shard usable from the start).
- *Re-invention cost* — implementing Citus's proven rebalancer / shard
  placement / DDL propagation ourselves. Initial bug density will be
  high.
- *Unit tests + chaos tests* — distributed-system correctness
  validation (Jepsen-style) must be built in house.
- *PG wire-protocol drift* — router compatibility work whenever PG 19 /
  20 ships.
- *Legacy code removal* — drop `internal/citus/` and
  `internal/plugin/extension/citus/`. About ~3 K LoC lost (tests
  included).
- *Invalidates the legacy "PGO-class + first-class Citus" messaging* —
  README, roadmap, and tutorials must be fully rewritten.

**Trade-offs**:

- *Invention vs integration* — this decision sides with invention. Even
  if a single-maintainer burns out, the policy is not to revert to
  embedding an external backend. We may shrink scope, but the
  shrink direction is "harden the single-shard operator quality" or
  "narrow the router scope", not "switch to a Citus / CNPG / PGO
  wrapper".
- *Time to market* — option A (Citus packaging, 12 months) could have
  shipped v1.0 in 2027. Under this decision v1.0 slides to
  2031–2032, raising the risk that a *competing solution closes the
  same gap in the interim*.

## Alternatives Considered

| Option | Why rejected |
|---|---|
| **A. K8s-native Citus + CNPG packaging** (12 months, keeps the ADR-0010 direction) | AGPL dependency + Citus has no auto-split + differentiation is limited to *integration*. The user explicitly rejected this (2026-05-02). |
| **B. pgcat + Citus rebalancer delegate + KEDA auto-split + CNPG HA** (24 months, devil's-advocate recommendation) | pgcat is PG-compatible but only at the query-parser level + no scatter-gather. CNPG API drift is a long-term risk. The user explicitly rejected this. |
| **D. CloudNativePG fork + sharding patch** | Forking severs the upstream stream → self-defeating. CNPG's ~1.5 K commits / year of investment would be lost. |
| **E. Embed CockroachDB** | BUSL / CSL forbid commercial use; PG SQL feature parity is ~40%. Violates the ADR-0003 license policy. |
| **F. YugabyteDB fork** | YSQL is a PG11 fork — some extensions are unsupported and the result is a different product. |
| **G. Apache ShardingSphere-Proxy** | JVM operational burden + DDL / extension partial restrictions + no K8s operator. |

## References

- User decision log: `/Users/phil/.claude/plans/eager-wobbling-torvalds.md` §1, §3.
- Comparison analysis: `/Users/phil/.claude/plans/eager-wobbling-torvalds-agent-a335628aa15778167.md`.
- ADR-0002: single-chart + flags Helm policy (packaging aspect of this decision).
- ADR-0003: license policy — permanently forbidden AGPL / BUSL / CSL / SSPL (dependency aspect of this decision).
- ADR-0004: the CRD lifecycle is owned by the operator manager.
- ADR-0005: alpha → beta → stable channels + CRD apiVersion evolution.
- RFC-0001 through RFC-0005: the 5 core component RFCs (CRD v2 / `ShardRange` / `ShardSplitJob` / `pg-router` / distributed transactions).
- Retired decision: `_archive/v0.x/0010-license-and-sharding-strategy.md` (Citus AGPL isolation + vanilla-PG-default model).
- Org-wide references: `~/Documents/ai-dev/standards/principles.md` §1 (Think Before Coding), §2 (Simplicity First — *conflict* — this decision puts *long-term license-clean + differentiation* ahead of simplicity); `standards/adr.md` (ADR authoring rules).
