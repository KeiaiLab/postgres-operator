# ADR-0006: Introducing the GitOps deploy overlay

- Date: 2026-05-06 (revised 2026-05-08: reflect live GitOps deployment status)
- Status: Accepted
- Authors: @eightynine01

## Context

This operator was bootstrapped from Operator SDK / kubebuilder and carries a `config/{crd,rbac,manager,default,...}` kustomize tree. `config/default` forces the namespace to `postgres-operator-system` and the namePrefix to `postgres-operator-`. This is appropriate for *one-shot cluster pushes* such as `make deploy`, but in a GitOps scenario (ArgoCD does git → cluster one-way sync) it has the following problems:

1. The ArgoCD Application's `destination.namespace` is operated as `prod`, but `config/default` says `namespace: postgres-operator-system` — they disagree → drift becomes permanent.
2. ArgoCD repeatedly tries to create the Namespace resource (`postgres-operator-system`) that `config/default` auto-generates → conflicts with the prod cluster's *pre-created prod ns* policy.
3. The `deploy/overlays/prod/` entry point was missing from this repo.

### Current operating state (2026-05-06 inventory, direct kubectl queries)

```
$ kubectl config current-context
keiailab
$ kubectl get ns data prod db
data    Active   4h55m
Error from server (NotFound): namespaces "prod" not found
Error from server (NotFound): namespaces "db" not found
$ kubectl get storageclass
ceph-rbd (default)   rook-ceph.rbd.csi.ceph.com   Retain   Immediate   12d
ceph-fs              rook-ceph.cephfs.csi.ceph.com   Retain   Immediate   11d
cold-rbd             rook-ceph.rbd.csi.ceph.com   Retain   Immediate   9d
$ kubectl get application -n argocd -l argocd.argoproj.io/sync-wave=1
platform-data-postgres   OutOfSync   Degraded
platform-data-<service-A>    OutOfSync   Healthy
platform-data-<service-B>     OutOfSync   Degraded
```

<!-- live-verified: 2026-05-06 -->

Derived decisions:

- **Apply ns unification policy**: per the 2026-05-06 user-explicit cycle, unify all data-tier charts into the single `data` ns. The `deploy/overlays/prod/kustomization.yaml` of this ADR also aligns with `namespace: data` (envName=prod is kept only as an identifier).
- **StorageClass alignment**: `ceph-block` is absent. The default of the production cluster is `ceph-rbd`. The `storageClass` in CR samples is also changed to `ceph-rbd`.
- **postgresql deployment status update (2026-05-08)**: at the initial 2026-05-06 inventory, postgresql was not in the ApplicationSet path, but the platform-data Helm wrapper (`platform/data/postgres-operator`) was subsequently adopted as the production source of truth. Currently `platform-data-postgres-operator` is `Synced/Healthy`, the controller Deployment is `1/1`, and `PostgresCluster/postgres` is `Ready=True`. This `deploy/` is retained as an alternative direct-apply entry point.
- **Other data-tier operators**: operated via the platform-data umbrella chart. This `deploy/` is an *alternative/standby entry point*. Applying both simultaneously will cause a helm release conflict.

## Decision

Introduce a GitOps overlay layer following the established kustomize overlay pattern.

```
deploy/
├── overlays/prod/
│   ├── kustomization.yaml      # bundles config/{crd,rbac,manager} into the prod ns
│   └── delete-namespace.yaml   # removes the auto-generated Namespace via strategic-merge
└── <workload>.yaml             # CR instance (db ns, separate ArgoCD application)
```

- `namespace: data` in `kustomization.yaml` applies to all namespaced resources.
- `patches.target.name` uses the *raw name before namePrefix is applied* (`system`) — the overlay imports `config/manager` directly, not `config/default`.
- CR instances use the `data` namespace and are synced via a separate ArgoCD application or Helm wrapper (operator and workload lifecycles are separated).

## Consequences

Positive:
- The ArgoCD application source candidate is made explicit as `deploy/overlays/prod` — either the platform-data umbrella chart absorbs this path as a dependency, or this path can be registered *directly* as the ApplicationSet generator path.
- `config/default` is preserved for *local development*, so the `make deploy` workflow does not regress.
- Follows the established kustomize overlay pattern, reducing cognitive load.

Negative:
- Depends on the raw name `system` in `config/manager/manager.yaml`. If the kubebuilder scaffold changes in the future, the patch target must be updated.
- This repo chooses to leave the kubebuilder scaffold as-is (prioritizing regeneration safety) rather than manually changing the deployment name to the full name.

## Alternatives Considered

1. **Use `config/default` directly as the ArgoCD source** — hard to forcibly change the namespace, and the auto-generated Namespace resource issue remains. Rejected.
2. **Manually change the Namespace name in `config/manager/manager.yaml` to the full name** — requires a patch on every regeneration. Degraded operator-sdk regenerate compatibility. Rejected.
3. **Use a Helm chart (`charts/`) as the GitOps source** — the platform-data umbrella chart already follows this pattern (absorbing the operator chart as a dependency). postgres-operator could do the same. This ADR is about introducing a *separate entry point*, not negating the helm path. The two entry points (helm wrapper / kustomize overlay) must yield the same cluster state — a parity invariant must be introduced in the future. Covered in a follow-up ADR.
