<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/logo/symbol.png" alt="keiailab" width="96"/>
  </a>
</p>

# postgres-operator

> **Keiailab data operator family** — vanilla PostgreSQL 18+ on Kubernetes with HA, backup, pooling, and declarative database workflows.

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-0EA5A8.svg" alt="License: MIT"/></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white" alt="Go 1.26+"/></a>
  <a href="https://www.postgresql.org/"><img src="https://img.shields.io/badge/PostgreSQL-18%2B-336791?logo=postgresql&logoColor=white" alt="PostgreSQL 18+"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.26%2B-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes 1.26+"/></a>
  <a href="https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator"><img src="https://img.shields.io/badge/GHCR-postgres--operator-0EA5A8?logo=github&logoColor=white" alt="GHCR image"/></a>
  <a href="https://github.com/keiailab/postgres-operator/discussions"><img src="https://img.shields.io/github/discussions/keiailab/postgres-operator?label=discussions&logo=github" alt="GitHub Discussions"/></a>
</p>

A Kubernetes operator for running vanilla PostgreSQL 18+, written in Go with Kubebuilder. It manages the full lifecycle of a PostgreSQL cluster — provisioning, high availability, backups, connection pooling, and declarative databases/roles — through plain Kubernetes resources.

The operator runs **unmodified upstream PostgreSQL** — no forked engine, no embedded fork of another operator. Your applications keep using standard PostgreSQL extensions, types, and the libpq/JDBC/asyncpg drivers they already use.

## Features

- **Declarative clusters** — define a `PostgresCluster` and the operator provisions the StatefulSet, Services, ConfigMaps, and PVCs for a primary plus optional replicas.
- **High availability** — replica clusters, automatic primary failure detection, standby promotion, and Lease-based leader election (`internal/controller/failover/`).
- **Backups** — `BackupJob` (one-shot backup/restore via pgBackRest) and `ScheduledBackup` (cron-driven backups, 6-field schedule).
- **Connection pooling** — `Pooler` runs a PgBouncer layer in front of a cluster, with transaction/session pool modes and optional cert-manager TLS.
- **Declarative databases & roles** — `PostgresDatabase` and `PostgresUser` manage databases, schemas, extensions, FDWs, roles, memberships, and passwords against the ready primary.
- **Image catalogs** — `ImageCatalog` / `ClusterImageCatalog` pin the PostgreSQL runtime image per major version, namespace- or cluster-scoped.
- **Observability** — the Helm chart ships a Prometheus `ServiceMonitor`, a `PrometheusRule` with built-in alerts, and Grafana dashboards.
- **Secure by default** — restricted Pod Security Context, deny-by-default `NetworkPolicy`, and TLS via cert-manager.

The chart installs **8 CRDs**:

| CRD | Purpose |
|---|---|
| `PostgresCluster` | Primary + replica topology, the core resource |
| `BackupJob` | One-shot backup or restore (pgBackRest) |
| `ScheduledBackup` | Cron-driven `BackupJob` generation |
| `Pooler` | PgBouncer connection pool layer |
| `PostgresDatabase` | Declarative database / schema / extension / FDW |
| `PostgresUser` | Declarative role / membership / password |
| `ImageCatalog` | Namespace-scoped PostgreSQL image catalog |
| `ClusterImageCatalog` | Cluster-wide PostgreSQL image catalog |

## Status

Current release: **v0.4.0-beta.1**. The operator manages single-cluster PostgreSQL (primary + replicas) with HA, backups, pooling, and monitoring. It is **beta** — verify your own backup/restore procedure before trusting it with production data. See [Roadmap](#roadmap) for what is not yet built.

## Installation

Requires Kubernetes 1.26+. The Prometheus integration additionally requires [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator); TLS features require [cert-manager](https://cert-manager.io/).

```bash
# Install the operator and its CRDs via the bundled Helm chart
helm install postgres-operator ./charts/postgres-operator

# Or enable monitoring at install time
helm install postgres-operator ./charts/postgres-operator \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.prometheusRule.enabled=true \
  --set metrics.grafanaDashboards.enabled=true
```

The operator image is published to `ghcr.io/keiailab/postgres-operator`.

## Usage

Create a single-node cluster:

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quickstart
  namespace: default
spec:
  postgresVersion: "18"
  shards:
    initialCount: 1
    replicas: 1          # 1 primary + 1 replica; use 0 for a single primary
    storage:
      size: 10Gi
```

```bash
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
kubectl wait postgrescluster/quickstart --for=condition=Ready --timeout=5m
```

Add a database and a role declaratively:

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresUser
metadata:
  name: app-user
spec:
  cluster:
    name: quickstart
  name: app
  login: true
  connectionLimit: 25
---
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresDatabase
metadata:
  name: app-db
spec:
  cluster:
    name: quickstart
  name: appdb
  owner: app
  extensions:
    - name: pgcrypto
```

More examples — pooler, scheduled backup, replica cluster, image catalogs — are in [`config/samples/`](config/samples/), and the full operations guide is in [`docs/operator-guide/`](docs/operator-guide/).

### Uninstall

```bash
# Delete CR instances first — finalizers otherwise block CRD removal
kubectl delete postgrescluster,pooler,scheduledbackup --all -A

helm uninstall postgres-operator
```

Helm keeps CRDs on uninstall by design; remove them manually with `kubectl delete crd ...postgres.keiailab.io` if you want a full teardown.

## Roadmap

Beyond single-cluster operations, the long-term goal is a horizontally sharded, distributed-SQL layer on top of vanilla PostgreSQL. The `ShardRange` and `ShardSplitJob` CRD types are defined, but **no controllers exist for them yet** — sharding and the `pg-router` query layer are design-only at this stage.

Planned, roughly in order:

- HA hardening — PITR drill, chaos failover testing
- `ShardRange` CRD controller + `pg-router` (manual multi-shard routing)
- Scatter-gather queries + read-replica autoscaling
- `ShardSplitJob` — online shard splitting
- Automatic split/rebalance triggered by load
- Cross-shard distributed transactions and JOINs

Detailed phase plan, sub-tasks, and SLOs: [`docs/ROADMAP.md`](docs/ROADMAP.md). Architecture and design decisions: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), [`docs/sharding/SHARDING.md`](docs/sharding/SHARDING.md), and the [ADR index](docs/kb/adr/INDEX.md).

## Contributing

The canonical repository is on [GitLab](https://keiailab.synology.me/gitlab/keiailab/oss/postgres-operator), mirrored to [GitHub](https://github.com/keiailab/postgres-operator).

```bash
make lint test validate   # lint + unit tests + manifest validation
make test-e2e             # kind-based end-to-end tests
```

Checks run locally via lefthook (pre-commit / pre-push) and GitLab CI; there is no GitHub Actions gate. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the workflow and [`docs/SECURITY.md`](docs/SECURITY.md) for reporting vulnerabilities privately.

## License

[MIT](LICENSE) © keiailab
