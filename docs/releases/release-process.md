# Local release process

Per ADR 0009 this repository does not use GitHub Actions. Release
verification and publishing run as local Makefile targets.

## Success criteria

Postgres Operator release.

User scenario:

1. The maintainer runs preflight with `VERSION=vX.Y.Z`.
2. The maintainer reviews lint, unit/envtest, audit, manifest, Helm, and
   install-bundle verification results.
3. The maintainer runs release with the same `VERSION`.
4. The maintainer confirms that the GHCR image, the Git tag, the GitHub
   Release, and the Helm repo index have been published.

Expected outcomes:

- `make gate` passes.
- `Chart.yaml`'s `version` and `appVersion` match `VERSION` with the
  leading `v` stripped.
- `CHANGELOG.md` has a matching version entry.
- The Helm `--include-crds` render and `dist/install.yaml` contain
  **8 CRDs** (`postgresclusters`, `backupjobs`, `scheduledbackups`,
  `poolers`, `postgresdatabases`, `postgresusers`, `imagecatalogs`,
  `clusterimagecatalogs`).
- `bundle/manifests/postgres.keiailab.io_*.yaml` count ≥ 8 and
  `operator-sdk bundle validate --select-optional suite=operatorframework`
  is clean.
- `kube-linter lint dist/install.yaml` and the helm template both return
  0 lint errors.
- Chart `appVersion` ↔ kustomize `newTag` ↔ dist image tag are all
  aligned.
- `.github/workflows/` is empty (ADR-0009 forbidden permanently).
- The worktree is clean before release.

## Step → verify

1. Regenerate artifacts.
   - Run: `make manifests generate build-installer`.
   - Verify: `git diff -- charts/postgres-operator/crds dist/install.yaml config/crd/bases`.

2. Local verification.
   - Run: `make gate`.
   - Verify: lint, test, audit, and validate all pass.

3. Push-less release verification.
   - Run: `make release-preflight VERSION=v0.1.1-alpha`.
   - Verify: the Helm package is created under
     `/tmp/postgres-operator-release`, cleaned up, and the worktree-clean
     check passes.

4. Actual release.
   - Run: `make release VERSION=v0.1.1-alpha`.
   - Verify: GHCR image push, Git tag push, GitHub Release creation, and
     the `gh-pages` Helm index refresh all succeed.

5. OLM bundle regeneration + community-operators PR (optional, after the
   alpha tag).
   - Run: `make bundle VERSION=0.4.0-beta.N` and
     `make bundle-build VERSION=0.4.0-beta.N`.
   - Verify: `operator-sdk bundle validate ./bundle --select-optional
     suite=operatorframework` is clean and
     `docker push ghcr.io/keiailab/postgres-operator-bundle:0.4.0-beta.N`
     succeeds.
   - Procedure detail:
     [docs/operator-guide/community-operators-onboarding.md](../operator-guide/community-operators-onboarding.md).

6. Artifact Hub registration / search verification.
   - Prerequisite: register a Helm repository in the Artifact Hub control
     panel under the name `keiailab-postgres-operator`.
   - Repository URL: `https://keiailab.github.io/postgres-operator`.
   - Package URL: `https://artifacthub.io/packages/helm/keiailab-postgres-operator/postgres-operator`.
   - API registration: `ARTIFACTHUB_API_KEY_ID=... ARTIFACTHUB_API_KEY_SECRET=... make artifacthub-register`.
   - Verify: `make artifacthub-smoke`.
   - Failure interpretation: if the Helm-repository reachability step
     passes but only the Artifact Hub package-registration step returns
     404, the chart package is fine — the Artifact Hub side is either not
     registered yet or still pending. `charts/artifacthub-repo.yml`'s
     `repositoryID` must equal the ID shown on the Artifact Hub repository
     card for the Verified-publisher badge to appear.
   - Current-state check: if `make artifacthub-smoke` fails with
     `Artifact Hub repository is not registered`, the URL
     `https://keiailab.github.io/postgres-operator` is missing from
     `https://artifacthub.io/api/v1/repositories/search?org=keiailab&kind=0`.
     The Artifact Hub tracker re-processes about every 30 minutes, so wait
     after registering or publishing a new chart version, then re-verify.

## Manual verification commands

```bash
go test $(go list ./... | grep -v /test/e2e)
make lint-config && make lint
make validate
helm lint --strict charts/postgres-operator
helm template --include-crds gate charts/postgres-operator
helm package charts/postgres-operator -d /tmp/postgres-operator-release
make artifacthub-smoke
kubectl create --dry-run=client --validate=false -f dist/install.yaml
rm -rf /tmp/postgres-operator-release
```

## L3 e2e

The kind-based e2e run is an explicit manual gate. It runs on a dedicated
kind cluster only — never on a real dev/prod cluster.

```bash
make test-e2e PILLAR=p1
```

The test cluster is named `postgres-operator-test-e2e` and is deleted when
the target finishes.
