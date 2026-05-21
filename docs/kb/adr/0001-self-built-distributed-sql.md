# ADR-0001: Build a self-built distributed-SQL layer on PostgreSQL

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil
- Supersedes: prior v0.x decisions (history preserved in git)

## Context

The project evolved through 0.2.0-alpha as a Kubernetes operator that
runs PostgreSQL. Earlier, a *dual backend* model was on the table:
isolating a third-party PostgreSQL sharding extension under AGPL +
vanilla PG18 as default. On 2026-05-02, after a comparative analysis of
the broader PostgreSQL / distributed-SQL ecosystem, we confirmed that no
product on the market simultaneously satisfies all of the following:

1. **100% PostgreSQL wire / SQL compatibility** — distribution can be
   adopted without changing application code.
2. **License-clean** (Apache-2.0 / BSD / MIT / PG License only) — no
   obligations when exposed as commercial SaaS.
3. **K8s-native integration** — CRDs + reconcilers + KEDA-driven auto
   sharding.
4. **Auto sharding** (write-side scale-out) — closes the manual-split
   gap left by existing extensions.

At decision time (2026-05-02), the user chose **C** out of four options
— A: package an external sharding extension; B: a pragmatic integration
of multiple third-party components; C: a full self-built distributed-SQL
— and additionally specified **remove every external backend
dependency** and **single chart + flags**. On 2026-05-07 we narrowed the
principle further: we may reference external system designs at the
problem-decomposition level, but we cannot embed those systems; we must
**build as a new service**. This is an ambitious 6+-year decision;
*long-term differentiation comes not from invention alone but from
invention + license-clean + K8s-native achieved simultaneously* — a
redefinition of the product identity.

## Decision

We will build a *self-built distributed-SQL layer* on top of
PostgreSQL. We permanently exclude code, runtimes, controllers, CRDs,
and extensions of external PostgreSQL operators and distributed-SQL
backends from our dependency graph.

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
  - HA — based on the in-house instance manager (uses the RFC 0003
    P2-T1 frozen interface; no external HA agent runtime).
- **Reusable assets**: pgBackRest (BSD-2), pg_query_go (PG License),
  controller-runtime (Apache-2.0), KEDA (Apache-2.0). All meet ADR-0003.
- **Removed backend dependencies**: any external PostgreSQL sharding
  extension, any external PostgreSQL operator CR, any external HA agent
  DCS, and any external distributed-database range-KV layer. Zero lines
  of code; only general design literature may inform problem
  decomposition.
- **Clean-room re-implementation**: implementation is rewritten as new
  types, controllers, instance managers, and routers in this repo.
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
- *100% PostgreSQL compatibility* — overcomes the limits of forks and
  wire-only solutions. Every PG 18+ extension / type / function remains
  available.
- *K8s-native metadata integration* — the `ShardRange` CRD is the
  source of truth and etcd becomes the distributed-metadata store. No
  separate KV layer is needed.
- *Clear differentiation* — "K8s-native + license-clean + auto sharding
  for vanilla PG" is not currently on the market.

**Negatives / costs**:

- *6+-year timeline* — ~64 months at one engineer at 50% capacity;
  realistically 6+ years is possible. Mid-way abandonment risk is
  mitigated by each phase shipping a production-deployable artifact
  (P1 single-shard usable from the start).
- *Re-invention cost* — implementing a proven rebalancer / shard
  placement / DDL propagation ourselves. Initial bug density will be
  high.
- *Unit tests + chaos tests* — distributed-system correctness
  validation (Jepsen-style) must be built in house.
- *PG wire-protocol drift* — router compatibility work whenever PG 19 /
  20 ships.
- *Legacy code removal* — drop the previous third-party-extension
  integration packages. Roughly a few thousand LoC removed (tests
  included).
- *Messaging refresh* — README, roadmap, and tutorials must be
  rewritten to reflect the self-built positioning.

**Trade-offs**:

- *Invention vs integration* — this decision sides with invention. Even
  if a single-maintainer burns out, the policy is not to revert to
  embedding an external backend. We may shrink scope, but the
  shrink direction is "harden the single-shard operator quality" or
  "narrow the router scope", not "switch to an external operator
  wrapper".
- *Time to market* — packaging an external sharding extension could
  have shipped v1.0 sooner. Under this decision v1.0 slides several
  years out, raising the risk that a *competing solution closes the
  same gap in the interim*.

## Alternatives Considered

| Option | Why rejected |
|---|---|
| **A. Package an external PostgreSQL sharding extension + an upstream PG operator** | AGPL dependency + manual-split-only on the extension side + differentiation reduced to *integration*. User explicitly rejected (2026-05-02). |
| **B. Pragmatic multi-component integration** (external pooler + external rebalancer delegate + KEDA auto-split + external HA) | Component-level PG-compatibility only + API drift across upstream components is a long-term risk. User explicitly rejected. |
| **D. Fork an upstream PostgreSQL operator + sharding patch** | Forking severs the upstream stream → self-defeating. We would lose the upstream's continuous investment. |
| **E. Embed an external distributed SQL database** | BUSL / CSL forbid commercial use; PG SQL feature parity is partial. Violates the ADR-0003 license policy. |
| **F. Fork an alternative PG-compatible distributed database** | PG-compatibility forks typically lag in extensions and result in a different product. |
| **G. JVM-based proxy projects** | JVM operational burden + DDL / extension partial restrictions + no first-party K8s operator. |

## References

- ADR-0002: single-chart + flags Helm policy (packaging aspect of this decision).
- ADR-0003: license policy — permanently forbidden AGPL / BUSL / CSL / SSPL (dependency aspect of this decision).
- ADR-0004: the CRD lifecycle is owned by the operator manager.
- ADR-0005: alpha → beta → stable channels + CRD apiVersion evolution.
- RFC-0001 through RFC-0005: the 5 core component RFCs (CRD v2 / `ShardRange` / `ShardSplitJob` / `pg-router` / distributed transactions).
- Retired prior v0.x decisions (history preserved in git).
- Org-wide references: `~/Documents/ai-dev/standards/principles.md` §1 (Think Before Coding), §2 (Simplicity First — *conflict* — this decision puts *long-term license-clean + differentiation* ahead of simplicity); `standards/adr.md` (ADR authoring rules).
