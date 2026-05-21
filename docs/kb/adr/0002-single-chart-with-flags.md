# ADR-0002: Single Helm chart + component flags policy

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

The Helm packaging strategy is redesigned on top of the previous ADR 0007 (Helm chart P1) and RFC 0002 (no GitHub Actions) flows. In the earlier 0.2.0-alpha stage, splitting an auxiliary chart to isolate an AGPLv3 third-party PostgreSQL sharding extension was considered, but with the 2026-05-02 user decision to adopt a self-built distributed SQL layer, the AGPL dependency itself was removed (see ADR-0001). Therefore the motivation for *a separate auxiliary chart for license isolation* has disappeared. At the same time, the cost for a single maintainer to keep multiple charts in lockstep over a 6-year timeline is realistically unsustainable. Components such as router / resharder / rebalancer / KEDA glue / backup / monitoring need to be *optionally enabled*, but this can be sufficiently modeled with `values.yaml` flags instead of chart separation.

## Decision

Package all operator components in a single `charts/postgres-operator/` chart, and toggle optional components via boolean flags in `values.yaml`.

Key parameters:

- Chart count: **1** (`postgres-operator`).
- Chart versioning policy: SemVer + appVersion alignment (operator image and chart version are in lockstep).
- Component toggle location: top-level keys in `values.yaml` (`router.enabled`, `resharder.enabled`, `rebalancer.enabled`, `autoscale.keda.enabled`, `backup.enabled`, `monitoring.serviceMonitor.enabled`, `monitoring.prometheusRule.enabled`, `security.networkPolicies.enabled`).
- Schema validation: write `values.schema.json` in *strict top-level* mode (`additionalProperties: false`) so typos / unsupported keys are immediately rejected at install/upgrade time.
- Conditional rendering: every `templates/<component>.yaml` is guarded with `{{- if .Values.<component>.enabled }} ... {{- end }}`.
- The umbrella sample chart is not included in this chart and is split into a separate repo (`postgres-operator-samples`) to isolate the operational chart from demo-only dependencies.
- Helm compatibility: 3.18+ required, 4.0 readiness secured (no Wasm plugin usage, SSA-by-default compatibility verified).
- ArtifactHub: apply for verified-publisher status with `artifacthub-repo.yml` + signed `.prov`.

## Consequences

Positive:

- Operational simplification — users deploy the full stack with one `helm install` and pick components by adjusting flags only.
- The number of charts a single maintainer must keep in lockstep is fixed at 1, reducing release burden.
- ArtifactHub listing, search, and signing flows are simplified around a single package.
- No Helm dependency graph → dependency conflicts at install/upgrade time are avoided.

Negative:

- The `values.yaml` schema may become bloated. In particular, keys accumulate during P2 (router) ~ P6 (distributed transactions).
- Users must download the entire chart even when they only want some components (template files are rendered lazily so there is no runtime cost, but chart size grows).
- Independent release cycles per component are not possible — to patch only the router, the entire chart must have its version bumped.

Trade-offs:

- Mandatory strict mode on `values.schema.json` mitigates bloat. If schema updates are omitted from any PR, `helm lint --strict` blocks it.
- When adding a component, justify the new flag and default value via an ADR (a change in a user-visible default is a minor bump).
- Chart size growth is judged less valuable than operational simplification in a single-maintainer environment.

## Alternatives Considered

| Alternative | Reason for rejection |
|---|---|
| (a) 3-chart split (`postgres-operator-lib` + `postgres-operator` + `postgres-operator-sample`) | The cost for a single maintainer to keep 3 charts in lockstep is excessive. The abstraction value of a library chart only pays off when there are multiple consumers, but this project has a single consumer (itself). |
| (b) Single chart + external sample repo + library chart | Library chart portion rejected. Sample repo split is *partially adopted*. The operator chart is self-contained and does not depend on an external library. |
| (c) operator-sdk / OLM bundle only (no Helm support) | Excludes direct K8s users (non-OpenShift users). ArtifactHub's Helm package channel becomes unusable as well. |
| (d) Kustomize overlay only | Version management, releases, and dependency expression are weaker than Helm. Users already expect the Helm ecosystem. |
| (e) One chart + per-component sub-charts (`charts/charts/router/`) | Values propagation rules for sub-charts are complex and conflict with strict `values.schema.json` validation. Excessive for single-component toggles that can be sufficiently expressed by flags. |

## References

- ADR-0001 (decision for self-built distributed SQL — the direct cause of the AGPL third-party extension dependency removal)
- Previous ADR 0007 (Helm chart P1, archived) — this ADR redefines and supersedes it
- RFC 0002 (no GitHub Actions, archived) — local gate unification policy delegates helm lint to pre-push as well
- standards/linting.md — `helm lint --strict` is enforced in the L2 pre-push hook
- standards/ci.md — 4-tier gates, helm template + kustomize build are L2/L3 stages
- standards/adr.md — formal standard for this document
