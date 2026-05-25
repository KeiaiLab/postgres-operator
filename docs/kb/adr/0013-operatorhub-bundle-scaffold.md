# ADR-0013: OperatorHub.io bundle scaffold (PR-B9 cross-cut)

- Date: 2026-05-10
- Status: Accepted
- Authors: @eightynine01

## Context

The standard OperatorHub scaffold pattern (operator-sdk 1.42 + kustomize) was
established as the technical prerequisite for OperatorHub.io registration.
postgres-operator adopts this pattern to gain external OperatorHub
discoverability via the same bundle scaffolding.

## Decision

Standard OperatorHub scaffold pattern:
1. `config/manifests/bases/postgres-operator.clusterserviceversion.yaml` —
   2 CRDs owned (PostgresCluster, BackupJob), metadata (description / keywords
   / maintainers / provider / maturity=alpha / minKubeVersion=1.26.0).
2. `config/manifests/kustomization.yaml` — CSV + crd + rbac + manager + samples.
   webhook is excluded due to the absence of a kustomization.yaml
   (`config/webhook/manifests.yaml` is a single file) — OLM handles webhook
   deployment automatically.
3. Makefile `bundle` / `bundle-build` targets (operator-sdk 1.42 + kustomize).
4. alm-examples — 2 samples (dev + prod) inline JSON.

Updating the image tag in `config/manager/kustomization.yaml` is *omitted* in
this PR — the postgres release pipeline already handles `kustomize edit set image`
at image-push time.

## Consequences

Positive:
- Standard OperatorHub scaffold pattern adopted consistently.
- `make bundle VERSION=...` is reproducible — entry point for release-
  automation follow-ups.
- 2 CRDs are explicitly listed in `customresourcedefinitions.owned` —
  OLM catalog is accurate.

Negative:
- alm-examples absent for BackupJob (sample file missing) — operator-sdk
  warning. Add a BackupJob sample in the follow-up PR-B9.2.1.
- `containerImage` is `0.3.0-alpha.15` (alpha) — at the community-operators
  PR time, a decision to split into a stable channel is required.

## Alternatives Considered

1. **A custom non-standard pattern**: rejected. Following the established
   operator-sdk scaffold pattern maximizes reproducibility.
2. **Include webhook**: rejected. Adding a kustomization.yaml under
   config/webhook is a separate task (impacts kubebuilder regenerate). OLM
   can handle webhook deployment automatically.

## References

- operator-sdk 1.42: <https://sdk.operatorframework.io/docs/olm-integration/>.
- Follow-up: PR-B9.2.1 add BackupJob sample, PR-B9.3 submit community-operators PR.
