# ADR-0003: External-dependency license policy (AGPL / BUSL / CSL / SSPL permanently forbidden)

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

This operator is distributed under Apache 2.0, and the user has marked
*license cleanliness* as a top-priority value (decision on 2026-05-02).
During 0.2.0-alpha we explored ADR-0010, isolating Citus (AGPLv3) into a
separate plugin chart. Isolation alone does not fully remove
supply-chain / legal-review / customer-compliance burden (especially for
SaaS-hosting customers), so that direction was retired. At the same
time, *source-available* license families — CockroachDB (BUSL/CSL),
MongoDB drivers (SSPL), Elastic (SSPL) — also violate the OSS
definition and impose redistribution / cloud-use restrictions, so they
are rejected. This ADR locks those decisions into a permanent policy
and enforces them with an automated gate.

## Decision

External OSS dependencies are adopted only when **both** of the
following hold: **(allowed license) ∩ (API stability)**. All other
dependencies are *permanently forbidden*.

Key parameters:

- **License allow-list**: BSD-2-Clause, BSD-3-Clause, Apache-2.0, MIT,
  PostgreSQL License (PGL), ISC, MPL-2.0 (file-level copyleft only).
- **License deny-list**: AGPL (all versions), BUSL (Business Source
  License), CSL (Cockroach Community License), SSPL (Server Side Public
  License), any license with the Commons Clause attached, Elastic
  License (all versions), Confluent Community License.
- **API-stability requirement**: the upstream project must declare
  ≥ v1.0.0 stability or publish a *documented deprecation policy* (at
  least one-minor-version prior notice).
- **Source-borrowing policy**: papers / blogs / documentation from
  forbidden-license projects may be read and referenced, but *no source
  is copied, translated, or ported*. Even README snapshots of AGPL
  projects must not be included in this repo.
- **Concrete allowed examples**:
  - `pg_query_go` (PostgreSQL License) — SQL parser.
  - `pgBackRest` (BSD-2-Clause) — backup wrapper.
  - `controller-runtime` (Apache-2.0) — operator skeleton.
  - `KEDA` (Apache-2.0) — autoscaler trigger.
  - `cert-manager` (Apache-2.0) — TLS.
  - `prometheus-operator` (Apache-2.0) — monitoring.
- **Concrete rejected examples**:
  - Citus (AGPLv3) — license violation.
  - CockroachDB (BUSL → CSL) — license violation.
  - MongoDB drivers (SSPL) — license violation.
  - Patroni (MIT) — the license itself is compatible, but the DCS model
    conflicts; rejected at the API surface (this is an
    *architecture-compatibility* matter, not a license one — needs a
    separate ADR).
- **Automated gates**:
  - `scripts/check-license-policy.sh` parses `go list -m -json all` and
    exits 1 when any license falls outside the allow-list.
  - Enforced as a lefthook L2 pre-push hook.
  - PR bodies must include `check-license-policy: PASS` evidence in the
    "Local gate" block.
- **Exception flow**: every new dependency must declare its license,
  upstream URL, and reason for adoption in the PR body. PRs adding a
  non-allow-list license are blocked (no override; circumventing this
  ADR is forbidden).

## Consequences

Positive:

- Zero license incidents — supply-chain audits and customer legal
  reviews pass without remediation.
- SaaS hosters can embed this operator without extra license obligations
  (the operator stays Apache 2.0).
- The Artifact Hub `artifacthub.io/license` annotation stays simple.
- Contributor onboarding terminates the "can I add this dependency"
  question with a single ADR.

Negative:

- We cannot directly use Citus's 8-year distributed-SQL assets (vindex,
  scatter-gather, online resharding) → §3 self-built path costs ~6
  years (P0–P7 phase roadmap).
- CockroachDB's proven distributed-transaction patterns (transactional
  KV, parallel commits) cannot be code-borrowed → learning by reading
  papers / docs only.
- When a strong tool (e.g. Elastic search integration) is needed, there
  may not be an allow-list alternative.

Trade-offs:

- We trade ~6 years of self-build for *license-clean + API-stability*
  value. A single maintainer can mitigate the load by recruiting OSS
  contributors from P2 onward.
- Cases like Patroni — *license compatible but architecturally
  incompatible* — need a separate justification ADR. This ADR covers
  only the *license dimension*.

## Alternatives Considered

| Option | Why rejected |
|---|---|
| (a) Keep the AGPL-isolation plugin-chart (the original ADR-0010 direction) | User explicitly rejected (2026-05-02). Isolation is partial and customer legal review remains. |
| (b) Dual-license the operator (Apache + AGPL) | Even with a dual-licensed operator, the dependency-license problem stays. Unrelated to this ADR's issue. |
| (c) Allow some source-available licenses (e.g. BUSL with a fair-use clause) | Violates the OSS definition; cloud restrictions exclude SaaS users. |
| (d) Case-by-case judgement (no policy) | A single maintainer would do legal review per dependency; ADR explosion. |
| (e) Allow GPL-2.0 / GPL-3.0 | No network-use clause, but file-level copyleft can propagate to operator core modules. For clarity we cap at MPL-2.0 (file-level). |
| (f) Document the policy without automation | Within a year a violating dependency would slip through. A lefthook hook + PR block is mandatory. |

## References

- ADR-0001 (self-built distributed SQL — a direct consequence of this policy).
- ADR-0002 (single Helm chart — why an isolation chart is no longer needed).
- Prior ADR-0010 (Citus AGPL isolation, now archived) — superseded by this ADR.
- `standards/enforcement.md §1.5` — security scan + audit log.
- `standards/ci.md §3` — pre-push gate catalog (license check added).
- SPDX License List — license-identifier standard.
- OSI Open Source Definition — the basis for our license classification.
