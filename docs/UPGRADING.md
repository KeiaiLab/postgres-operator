<p align="center">
  <b>English</b> |
  <a href="UPGRADING.ko.md">한국어</a> |
  <a href="UPGRADING.ja.md">日本語</a> |
  <a href="UPGRADING.zh.md">中文</a>
</p>

# Upgrading postgres-operator

This document covers the migration steps required when upgrading postgres-operator
across minor or major versions. Helm users can apply all changes simply by upgrading
the chart, but static manifest (`kubectl apply -f`) users must manually patch
some items such as RBAC.

## 0. Version Policy (semver)

| Change Type | semver bump | Example |
|---|---|---|
| New controller / CR / API | minor (v1.X -> v1.X+1) | Add PostgresPooler |
| Breaking API signature change | major (v1.X -> v2.0) | Change PostgresCluster.spec.storage struct |
| Bug fix / dependency bump | patch (v1.X.Y -> v1.X.Y+1) | controller-runtime 0.19->0.20 |
| keiailab-commons dependency bump | minor (commons v0.X -> v0.X+1) | Adopt pkg/reconcile |

## 1. v0.1.x -> v0.2.x

### Helm Users

```bash
helm repo update
helm upgrade postgres-operator <repo>/postgres-operator \
  --namespace postgres-operator-system \
  --version 0.2.x
```

The chart synchronizes RBAC, CRD, and Deployment automatically. No additional steps required.

### Static Manifest Users -- RBAC Migration

Check differences in the `dist/install.yaml` output of `make build-installer`:

```bash
kubectl diff -f dist/install.yaml
kubectl apply -f dist/install.yaml
```

New permissions on the existing ClusterRole (none at present -- this minor has no RBAC changes):

| API group | Resource | Reason | Added In |
|---|---|---|---|
| (none) | -- | -- | -- |

## 2. v0.2.x -> v0.3.x (planned)

### Adopt keiailab-commons v0.9.0 (Sprint 1 + S5)

```bash
# After bumping the keiailab-commons dependency in go.mod
go mod tidy
```

- **New imports**: `github.com/keiailab/keiailab-commons/pkg/pvc`, `pkg/topology` (Sprint 1)
- **Additional imports planned**: `pkg/reconcile`, `pkg/resources` (S5 follow-up)
- **Duplicate code removal**: Local helpers in `internal/controller/` are replaced by keiailab-commons helpers. Behavior is identical.

Migration impact:
- Reconcile behavior is unchanged (refactor only, no external behavior changes)
- No CRD spec changes (v1alpha2 conversion is a separate cycle)
- No Helm chart impact

## 3. v0.3.x -> v1.0.0 (planned -- production-grade milestone)

When CLAUDE.md section 7 *production-grade* criteria (P0+P1+P2+OP+C all pass) are met.

- Promote all CR API stability to `Stable` (v1)
- No breaking changes (v0.x -> v1.0 is a *naming-only* change)
- Production-grade quality verified via `make audit-quality`

## 4. GHA Dual-Track Policy (ADR-0019)

This repo is an *exception* to RFC-0002 (GitHub Actions permanent ban) -- as a public
OSS operator it requires external trust gates, so it maintains 14 GHA workflows
alongside the local 4-layer gates (lefthook) in a dual-track setup (ADR-0019).

GHA workflow updates during upgrades are handled automatically via
`dependabot/github_actions/*` PRs. Adding new files to `.github/workflows/`
via *human PRs* requires a *separate ADR* plus user approval.

## 5. General Migration Checklist

Before upgrading:
- [ ] CRD changes (`api/v1alpha1/` ObjectMeta compatible with v1alpha2)
- [ ] `make verify` (lint + test + build + audit) passes
- [ ] Existing e2e suite passes (`make integration-test`)
- [ ] Dependabot dependency bump PRs are integrated

After upgrading:
- [ ] Update Helm chart `dependencies:` (keiailab-commons library chart)
- [ ] Verify each CR spec compatibility (especially storage, resources)
- [ ] Verify reconcile results (`kubectl get postgrescluster -A`)
- [ ] Operational metrics (`Reconcile{Total,Latency,Errors}`) are normal

## 6. Breaking Change Notification Policy

- **Deprecation**: Mark with `// Deprecated:` comment in a new minor, remove after 2 minors
- **Breaking**: Major version bump + dedicated section in this UPGRADING.md + ADR
- **No post-hoc notification**: All breaking changes require *at least 1 minor* of prior deprecation

## References

- ADR index: `docs/kb/adr/INDEX.md`

---

<p align="center">
  (c) 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
