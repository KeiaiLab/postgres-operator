# Postgres Operator Helm Chart

<p align="center">
  <img src="https://raw.githubusercontent.com/KeiaiLab/postgres-operator/main/docs/logo/symbol.png" alt="Keiailab Postgres Operator symbol" width="96"/>
</p>

The `postgres-operator` chart deploys the operator manager, RBAC,
CRDs, and NetworkPolicies for `keiailab/postgres-operator`.

## Design assets

| Asset | URL | Usage |
|---|---|---|
| Centered service symbol | https://raw.githubusercontent.com/KeiaiLab/postgres-operator/main/docs/logo/symbol.png | Artifact Hub package icon and screenshot |
| Light wordmark | https://raw.githubusercontent.com/KeiaiLab/postgres-operator/main/docs/logo/light.png | Light backgrounds and docs cards |
| Dark wordmark | https://raw.githubusercontent.com/KeiaiLab/postgres-operator/main/docs/logo/dark.png | Dark backgrounds and social cards |
| Branding guide | https://github.com/KeiaiLab/postgres-operator/blob/main/docs/BRANDING.md | GitHub visual usage rules |

## Prerequisites

- Kubernetes 1.26+
- Helm 3.8+
- `kubectl` configured against the target cluster

## Install

```bash
helm install postgres-operator ./charts/postgres-operator \
  --namespace postgres-operator-system \
  --create-namespace
```

CRDs ship under the `crds/` directory and are applied by Helm by
default. In environments that manage the CRD lifecycle separately,
use the standard Helm option:

```bash
helm install postgres-operator ./charts/postgres-operator \
  --namespace postgres-operator-system \
  --create-namespace \
  --skip-crds
```

## Key values

| Value | Description | Default |
|---|---|---|
| `image.repository` | operator image repository | `ghcr.io/keiailab/postgres-operator` |
| `image.tag` | operator image tag. Falls back to chart `appVersion` when empty | `""` |
| `replicas` | manager replica count | `1` |
| `rbac.create` | whether to create the ClusterRole / ClusterRoleBinding | `true` |
| `networkPolicies.enabled` | whether to create the data-plane NetworkPolicy | `true` |
| `metrics.enabled` | whether to expose the manager metrics port | `true` |
| `metrics.service.enabled` | whether to create the metrics ClusterIP Service | `true` |
| `metrics.serviceMonitor.enabled` | whether to create a Prometheus Operator ServiceMonitor | `false` |
| `metrics.prometheusRule.enabled` | whether to create PrometheusRule alerting rules | `false` |
| `metrics.grafanaDashboards.enabled` | whether to create the Grafana dashboard ConfigMap | `false` |
| `metrics.grafanaDashboards.labelName` | dashboard sidecar discovery label key | `grafana_dashboard` |
| `metrics.grafanaDashboards.labelValue` | dashboard sidecar discovery label value | `"1"` |
| `metrics.prometheusRule.thresholds.poolerClientWaiting` | PgBouncer waiting-client alert threshold | `0` |
| `metrics.prometheusRule.thresholds.poolerClientMaxWaitSeconds` | PgBouncer max client-wait-time alert threshold | `30` |

## User-perspective verification

[Feature] Helm chart install

User scenario:
1. The user installs the chart into the target namespace.
2. The user confirms that the CRDs are registered.
3. The user confirms that the operator Deployment and RBAC are created.
4. The user applies the dev sample `PostgresCluster`.

Expected outcome:
- The CRDs `postgresclusters.postgres.keiailab.io`, `imagecatalogs.postgres.keiailab.io`, `clusterimagecatalogs.postgres.keiailab.io`, `backupjobs.postgres.keiailab.io`, `scheduledbackups.postgres.keiailab.io`, `poolers.postgres.keiailab.io`, `postgresdatabases.postgres.keiailab.io`, `postgresusers.postgres.keiailab.io` are listed.
- The manager Deployment is created.
- RBAC and NetworkPolicies render.
- The metrics Service renders by default; the ServiceMonitor and PrometheusRule can be enabled via values.
- The PrometheusRule detects BackupJob failures, Pooler failures, replica WAL lag growth, PgBouncer exporter collection failures, and growth in pool client waiting / max wait time.
- The Grafana dashboard ConfigMap contains the cluster-overview and Pooler dashboard JSON and can be auto-imported by the kube-prometheus-stack sidecar label.
- Applying the sample CR does not produce a schema error.
- `ImageCatalog` / `ClusterImageCatalog` are used to select runtime images from `PostgresCluster.spec.imageCatalogRef`. When a catalog entry changes, the StatefulSet init / main container image and the image-catalog-hash annotation change together, surfacing the rollout drift.
- When using `BackupJob.spec.executionMode=job`, also specify
  `spec.jobTemplate` containing the runner image and command. The
  operator only injects ownerReference, labels, and the standard env
  into that `batch/v1.Job`.
- A `Pooler` must specify the PgBouncer image and an auth Secret
  holding `userlist.txt`; the operator creates the ConfigMap,
  Deployment, and Service. When `spec.pgbouncer.exporter` is set, an
  exporter sidecar and the `metrics` Service port are also created.
- `PostgresDatabase` runs `psql` inside the `postgres` container of
  the ready primary Pod to apply database, tablespace, schema,
  extension, FDW, and foreign-server declarations and records
  `status.applied`. Database / schema privilege grant / revoke are
  also applied declaratively. CRs with
  `databaseReclaimPolicy=delete` drop the actual database on
  deletion before removing the finalizer.
- `PostgresUser` runs `psql` inside the `postgres` container of the
  ready primary Pod to apply role flags, `inRoles`,
  `connectionLimit`, `validUntil`, the password Secret or
  `disablePassword`, and records `status.applied`. When the
  referenced Secret changes, the `PostgresUser` is reconciled again
  and the successfully applied Secret revision is recorded in
  `status.passwordSecretResourceVersion`.
- `PostgresCluster.status.managedRolesStatus` aggregates the
  `PostgresUser`s referencing this cluster by `byStatus`,
  `cannotReconcile`, and `passwordStatus`.

```bash
helm lint --strict ./charts/postgres-operator
helm template --include-crds gate ./charts/postgres-operator
helm template monitor ./charts/postgres-operator \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
kubectl get crd postgresclusters.postgres.keiailab.io backupjobs.postgres.keiailab.io scheduledbackups.postgres.keiailab.io poolers.postgres.keiailab.io postgresdatabases.postgres.keiailab.io postgresusers.postgres.keiailab.io
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresuser.yaml
kubectl apply -f config/samples/postgres_v1alpha1_postgresdatabase.yaml
```

## Uninstall

```bash
helm uninstall postgres-operator -n postgres-operator-system
```

Helm does not delete CRDs on uninstall. To remove the CRDs as well,
delete them explicitly:

```bash
kubectl delete crd postgresclusters.postgres.keiailab.io
kubectl delete crd backupjobs.postgres.keiailab.io
kubectl delete crd scheduledbackups.postgres.keiailab.io
kubectl delete crd poolers.postgres.keiailab.io
kubectl delete crd postgresdatabases.postgres.keiailab.io
kubectl delete crd postgresusers.postgres.keiailab.io
```
