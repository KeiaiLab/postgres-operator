# community-operators channel onboarding guide

This document describes how to register the keiailab/postgres-operator
OLM bundle as a pull request against the
[k8s-operatorhub/community-operators](https://github.com/k8s-operatorhub/community-operators)
repository. The intent is to have the bundle automatically synced to
OperatorHub.io and the
[Artifact Hub OLM channel](https://artifacthub.io/packages/olm/community-operators/postgresql).

## Prerequisites

| Item | Status |
|---|---|
| OLM bundle generation (operator-sdk) | ✅ `make bundle VERSION=0.3.0-alpha.18` |
| bundle validate (default suite) | ✅ `operator-sdk bundle validate ./bundle` |
| bundle validate (operatorframework suite) | ✅ Run automatically by the `make validate` gate |
| CSV `alm-examples` aligned with 7 owned CRDs | ✅ T26 (2026-05-12) |
| CSV `customresourcedefinitions.owned` descriptions for 8 CRDs | ✅ T26 (2026-05-12) |
| CHANGELOG `[0.3.0-alpha.N]` entry | ✅ kept in sync at release-tag time |
| Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag | ✅ Drift asserted by `make validate` |
| LICENSE / SECURITY / SUPPORT / CONTRIBUTING / CODE_OF_CONDUCT | ✅ Standards-compliant |

## Bundle image build + push

The bundle image is built from `bundle.Dockerfile` for a single `linux/amd64`
platform (multi-arch is forbidden per RFC-0002 §2).

```bash
make bundle VERSION=0.3.0-alpha.18
make bundle-build VERSION=0.3.0-alpha.18

docker push ghcr.io/keiailab/postgres-operator-bundle:0.3.0-alpha.18
```

`bundle-build` runs `docker buildx build --platform linux/amd64 -f
bundle.Dockerfile` to produce a single-arch image. Push permission is
maintainer-only.

## community-operators PR procedure

Copy this repo's `bundle/` directory byte-for-byte into
`operators/postgres-operator/<version>/` of `k8s-operatorhub/community-operators`.

```bash
# 1. Fork and clone
gh repo fork k8s-operatorhub/community-operators --clone --remote
cd community-operators

# 2. Create the new version directory (the directory must match the
#    bundle's package name; `postgres-operator` is taken by zalando, so
#    we register under `keiailab-postgres-operator`).
mkdir -p operators/keiailab-postgres-operator/0.3.0-alpha.18
cp -r /path/to/postgres-operator/bundle/* \
      operators/keiailab-postgres-operator/0.3.0-alpha.18/

# 3. ci.yaml (community-operators metadata)
cat <<'YAML' > operators/keiailab-postgres-operator/ci.yaml
---
updateGraph: replaces-mode
reviewers:
  - eightynine01
YAML

# 4. PR body — copy the CHANGELOG[0.3.0-alpha.N] section from this repo
gh pr create \
  --repo k8s-operatorhub/community-operators \
  --base main \
  --title "operator keiailab-postgres-operator (0.3.0-alpha.18)" \
  --body-file /path/to/postgres-operator/.github/PULL_REQUEST_TEMPLATE.md
```

The community-operators CI automatically verifies:

- `operator-sdk bundle validate` (default and operatorframework suites).
- Bundle image buildability.
- Channel graph (`replaces` / `skips` / `skipRange`) consistency.
- Policy compliance (LICENSE / `category` / `displayName` / `description`).

After the PR merges it usually takes 1–2 hours for the version to appear
on the Artifact Hub OLM channel and OperatorHub.io.

## Upgrade graph maintenance

The bundle metadata already carries
`operators.operatorframework.io.bundle.channels.v1=alpha` and
`default-channel=alpha` in `metadata.annotations`. From the next release
on, declare `spec.replaces` (or `spec.skips`) in the CSV so OLM can track
the upgrade path 0.3.0-alpha.18 → 0.3.0-alpha.19.

```yaml
# config/manifests/bases/postgres-operator.clusterserviceversion.yaml
spec:
  replaces: postgres-operator.v0.3.0-alpha.18
```

## Verification

1–2 hours after the PR merges:

```bash
# Artifact Hub OLM channel — check that the version is visible
curl -s "https://artifacthub.io/api/v1/packages/olm/community-operators/postgres-operator" \
  | jq '{available_versions: .available_versions[].version}'

# OperatorHub.io — verify the UI listing (in a browser)
open "https://operatorhub.io/operator/postgres-operator"
```

## Regression containment

To prevent someone from submitting a community-operators PR with a
divergent OLM bundle, the local `make validate` enforces:

- `bundle/manifests/postgres.keiailab.io_*.yaml` count ≥ 8 (T26).
- `operator-sdk bundle validate ./bundle` returns 0 errors (T26).
- `operator-sdk bundle validate --select-optional suite=operatorframework`
  returns 0 errors (T26).
- Chart `appVersion` ↔ kustomize `newTag` ↔ dist image-tag are in sync
  (T26).

Only after these four gates pass do we proceed with the release-tag →
bundle-build → PR flow.

## Related

- [ADR-0013](../kb/adr/_archive/v0.x/0013-operatorhub-bundle.md) — OperatorHub bundle scaffold.
- [RFC-0002](../rfcs/_archive/v0.x/) — GitHub Actions ban + local 4-layer gate.
- [CHANGELOG.md](../../CHANGELOG.md) — change summary per alpha cut.
- [release-process.md](../releases/release-process.md) — release-tag procedure.
