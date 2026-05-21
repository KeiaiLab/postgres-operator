# deploy/ — GitOps deployment directory

This directory is the manifest entry point used by ArgoCD (or any
equivalent GitOps tool) to drive one-way git → cluster sync. It is
**a separate path from `config/`** — single-shot pushes such as
`make deploy` use `config/default`.

Per ADR-0006 the structure here is aligned with mongodb-operator /
valkey-operator.

## Layout

```
deploy/
├── overlays/prod/                 # ArgoCD application path: the operator itself (envName=prod, ns=data)
│   ├── kustomization.yaml         # config/{crd,rbac,manager} → namespace=data
│   └── delete-namespace.yaml      # remove the auto-generated Namespace
└── postgres-cluster.yaml          # ArgoCD application path: workload (CR instance, ns=data)
```

Operational model: per the production cluster ns-consolidation policy
(2026-05-06 cycle: all five charts share the `data` namespace), the
operator and the CRs live in the *same `data` namespace*. envName
separation (`overlays/prod`) is preserved only as an environment
identifier.

## Current operational status (2026-05-08)

The reason `keiailab/postgres-operator` was not deployed earlier was
that the platform-data ApplicationSet
(`platform/data/application.yaml`) did not list the operator path in
its `directories` block. The current production GitOps entry point
is the `postgres-operator/` Helm wrapper chart inside
platform-data.

As verified live on 2026-05-08, ArgoCD Application
`platform-data-postgres-operator` is `Synced/Healthy`
(revision `cc662773f1a286d6b11a768af151db0ccd47b63f`), and the
`platform-data-postgres-operator-controller-manager` Deployment in
the `data` namespace is running `1/1`. The live image is
`ghcr.io/keiailab/postgres-operator:0.3.0-alpha.4`
(`sha256:394ec5eb4aa09d316d957a3c751bb7283f21bfa71f19a9d2871ccbc7ec974f2f`)
and `PostgresCluster/postgres` is `Ready=True`.

This directory remains as the **alternate Kustomize deployment entry
point** (RFC-0004 §3). The production environment uses the
`platform/data/postgres-operator` path as its primary source of truth.

⚠️ **Scope boundary** — the status above means Day-0 alpha-deployable
single-shard deployment is complete. HA replicas, backup/restore
drills, PITR, and long-running soak are still pending, so this is
*not* marked as 0.4.0 single-shard production-ready or GA.

## Cluster prerequisites

- [x] `data` namespace pre-created (Active as of the 2026-05-06 cycle).
- [x] StorageClass `ceph-rbd` (default) available — verified on the production cluster.
- [ ] (optional) `pg-admin-creds` Secret in the `data` namespace — required when postgres-operator does not auto-create it; inject via ExternalSecret. The RFC 0001 v2 schema is capable of internal bootstrap.
- [ ] Prometheus Operator (required when `monitoring.serviceMonitor.enabled=true`).
- [ ] PrometheusRule CRD available (required when `monitoring.prometheusRule.enabled=true`).

## Applying (manual verification)

```fish
# 1) render check
kustomize build deploy/overlays/prod | head
kustomize build deploy/overlays/prod | grep -c "kind: Namespace"   # 0

# 2) apply the operator
kustomize build deploy/overlays/prod | kubectl apply -f -
kubectl -n data rollout status deploy/controller-manager

# 3) apply the workload
kubectl apply -f deploy/postgres-cluster.yaml
kubectl -n data get postgrescluster postgres-cluster \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

## Change procedure

Any change to this directory must be preceded by an ADR
(`docs/kb/adr/`). Always render-verify with
`kustomize build deploy/overlays/prod`.
