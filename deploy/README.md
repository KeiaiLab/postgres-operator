# deploy/ — GitOps deployment directory

This directory is the manifest entry point used by ArgoCD (or any
equivalent GitOps tool) to drive one-way git → cluster sync. It is
**a separate path from `config/`** — single-shot pushes such as
`make deploy` use `config/default`.

Per ADR-0006, this directory follows the GitOps deploy-overlay pattern.

## Layout

```
deploy/
├── overlays/prod/                 # ArgoCD application path: the operator itself
│   ├── kustomization.yaml         # config/{crd,rbac,manager} → target namespace
│   └── delete-namespace.yaml      # remove the auto-generated Namespace
└── postgres-cluster.yaml          # ArgoCD application path: workload (CR instance)
```

## Cluster prerequisites

- [ ] Target namespace pre-created.
- [ ] StorageClass available (default: `ceph-rbd` or cluster default).
- [ ] (optional) Admin credentials Secret — required when the operator does not auto-create it; inject via ExternalSecret. The RFC-0001 v2 schema supports internal bootstrap.
- [ ] Prometheus Operator (required when `monitoring.serviceMonitor.enabled=true`).
- [ ] PrometheusRule CRD available (required when `monitoring.prometheusRule.enabled=true`).

## Applying (manual verification)

```bash
# 1) render check
kustomize build deploy/overlays/prod | head
kustomize build deploy/overlays/prod | grep -c "kind: Namespace"   # 0

# 2) apply the operator
kustomize build deploy/overlays/prod | kubectl apply -f -
kubectl -n <namespace> rollout status deploy/controller-manager

# 3) apply the workload
kubectl apply -f deploy/postgres-cluster.yaml
kubectl -n <namespace> get postgrescluster postgres-cluster \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

## Change procedure

Any change to this directory must be preceded by an ADR
(`docs/kb/adr/`). Always render-verify with
`kustomize build deploy/overlays/prod`.
