# Deployment Guide — keiailab/postgres-operator (alpha)

> Kubernetes deployment guide for 0.3.0-alpha. RFC 0001 v2 schema.
> Acknowledge the alpha-stage limits (e2e not fully verified, no secret
> rotation yet) before adopting in production.

## Prerequisites

- Kubernetes 1.28+ (depends on CEL XValidation).
- A StorageClass with dynamic PVC provisioning.
- Access to a container registry — `ghcr.io/keiailab/*` (or your own
  mirror).

## 1. Images

### Operator (manager) image

```
ghcr.io/keiailab/postgres-operator:<version>
```

Build:

```fish
make docker-build IMG=ghcr.io/keiailab/postgres-operator:dev
make docker-push IMG=ghcr.io/keiailab/postgres-operator:dev
```

### PG runtime image (instance manager + postgres)

```
ghcr.io/keiailab/pg:18  (PG_MAJOR=18)
ghcr.io/keiailab/pg:17  (optional)
```

Build:

```fish
make docker-build-pg PG_MAJOR=18 PG_IMG=ghcr.io/keiailab/pg:18
make docker-push-pg PG_IMG=ghcr.io/keiailab/pg:18
```

This image is defined by `Dockerfile.pg` — a 2-stage build (golang:1.25-bookworm
to build `cmd/instance`, plus postgres:18-bookworm base + pgBackRest + a
UID/GID 70 user). The `ENTRYPOINT` is `/usr/local/bin/instance` (the
instance manager runs as PID 1 and forks postgres as a child). A
`BackupJob.spec.executionMode=job` runner can override the command and
invoke the `pgbackrest` binary in the same image directly.

## 2. Installing the CRDs + operator

### Helm

```fish
helm install postgres-operator charts/postgres-operator \
    --namespace postgres-operator-system --create-namespace \
    --set image.repository=ghcr.io/keiailab/postgres-operator \
    --set image.tag=dev
```

### kustomize (dev / test)

```fish
make build-installer
kubectl apply -f dist/install.yaml
```

## 3. Deploying a PostgresCluster instance

### Quickstart (single shard, dev)

```fish
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
```

Expected outcome:

- ConfigMap (postgresql.conf + pg_hba.conf), Headless Service, and one
  StatefulSet.
- ServiceAccount + Role + RoleBinding (the instance Pod gets
  `leases` + `PVC fence patch` permissions).
- Pod boot flow:
  1. `initdb` init container — initializes PGDATA on a fresh PVC.
  2. Instance manager starts as PID 1.
  3. Forks the postgres child.
  4. K8s lease-based election converges → primary elected →
     `pg_promote()`.
  5. `/readyz` returns 200 → Pod Ready.

```fish
# Status
kubectl get postgrescluster quickstart -o yaml | yq '.status'
kubectl get sts,svc,pod -l app.kubernetes.io/instance=quickstart
```

### Connecting

Inside the Pod via the Unix socket (peer auth, dev only):

```fish
kubectl exec quickstart-shard-0-0 -c postgres -- \
    psql -h /var/run/postgresql -U postgres -c 'SELECT version()'
```

From another Pod in the cluster (scram-sha-256):

```
psql "host=quickstart-shard-0-headless.default.svc.cluster.local user=postgres dbname=postgres"
```

(In alpha, passwords are injected via a separate Secret — to be wired up
later.)

## 4. Production topology (example)

`config/samples/postgres_v1alpha1_postgrescluster_prod.yaml` — multi-shard
+ router, replicas=2 (3-way HA), monitoring enabled, custom
StorageClass.

Before applying in production verify:

- The StorageClass is fast SSD-backed (alpha recommendation: 1 GB+ /
  min IOPS).
- replicas ≥ 1 (HA — recommended by RFC 0001 §3).
- For RPO=0, set `spec.postgresql.synchronous`. `number` must be
  ≤ `shards.replicas`.
- monitoring.serviceMonitor + the Prometheus operator are pre-installed.
- backup.enabled — only meaningful after the F04 follow-up PR.

### Synchronous-replication example

The operator exposes a structured synchronous-replication surface. The
user does not write the PostgreSQL GUC `synchronous_standby_names`
directly; the operator generates it from the shard Pod names and the
`primary_conninfo application_name`.

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quicksync
spec:
  postgresVersion: "18"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 2
    storage: {size: 10Gi}
  postgresql:
    synchronous:
      method: any
      number: 1
      dataDurability: required
```

- `method=any` uses PostgreSQL's `ANY N (...)` quorum form.
- `method=first` uses `FIRST N (...)` priority form.
- `dataDurability=required` blocks commits whenever the requested number
  of standbys is unavailable.
- `dataDurability=preferred` lowers the quorum to the current ready
  replica count, and when no replicas are Ready temporarily disables
  synchronous replication to preserve write availability.
- Configuration changes are propagated to the StatefulSet Pod template
  annotation as a ConfigMap hash, which triggers a shard Pod rolling
  reconcile.

### ImageCatalog-driven runtime image selection

The `spec.imageCatalogRef` shape lets you reference an
`ImageCatalog` (namespace-scoped) or a `ClusterImageCatalog`
(cluster-scoped). When a catalog entry changes, the referencing
`PostgresCluster`'s StatefulSet Pod-template image and its
`postgres.keiailab.io/postgres-image-catalog-sha256` annotation change
together, which triggers a Kubernetes rollout.

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: ImageCatalog
metadata:
  name: postgresql
  namespace: default
spec:
  images:
    - major: 18
      image: ghcr.io/keiailab/pg:18
---
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quickcatalog
  namespace: default
spec:
  imageCatalogRef:
    apiGroup: postgresql.cnpg.io
    kind: ImageCatalog
    name: postgresql
    major: 18
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 1
    storage: {size: 10Gi}
```

Compatibility / safety rules:

- `apiGroup` accepts empty, `postgres.keiailab.io`, or `postgresql.cnpg.io` (the latter is retained for ecosystem compatibility).
- `imageCatalogRef.major` is the single source of truth for image / bin
  directory selection, in place of `postgresVersion`. If both are
  present they must match.
- If the catalog or the major entry cannot be located, the operator does
  **not** fall back to a default image. Instead it fails with
  `status.phase=Degraded`, `Ready=False`, `Reason=ImageCatalogRejected`.

### Standalone replica cluster

The operator exposes the `externalClusters` + `bootstrap.pg_basebackup.source` +
`replica.enabled/source` surface for declaring a standalone replica
cluster. In this mode the ordinal-0 Pod does **not** run `initdb`; it
runs `pg_basebackup` from the external source and writes
`standby.signal` and `primary_conninfo`. The instance manager runs with
`POSTGRES_REPLICA_CLUSTER=standalone`, using a persistent-follower
election so local promotion never occurs.

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: quickreplica
  namespace: default
spec:
  postgresVersion: "18"
  externalClusters:
    - name: primary-eu
      connectionParameters:
        host: primary-eu-rw.data.svc
        port: "5432"
        user: streaming_replica
        dbname: postgres
        sslmode: verify-full
      password:
        name: primary-eu-replication-password
        key: password
      sslKey:
        name: primary-eu-replication
        key: tls.key
      sslCert:
        name: primary-eu-replication
        key: tls.crt
      sslRootCert:
        name: primary-eu-ca
        key: ca.crt
  bootstrap:
    pg_basebackup:
      source: primary-eu
  replica:
    enabled: true
    source: primary-eu
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 0
    storage: {size: 10Gi}
```

Fail-closed rules:

- If `replica.enabled=true`, both `replica.source` and
  `bootstrap.pg_basebackup.source` are required and must be equal.
- The source name must exist in `externalClusters[].name`.
- If `connectionParameters.host` is missing the Pod is not created;
  instead `status.phase=Degraded`, `Ready=False`,
  `Reason=ReplicaClusterRejected`.
- If `password`, `sslKey`, `sslCert`, or `sslRootCert` is set, both
  `name` and `key` must be supplied. Any omission fails closed as
  `ReplicaClusterRejected`.

Current scope:

- The streaming `pg_basebackup` + continuous-recovery path and the
  local-promotion lockout are verified by envtest / unit tests.
- The password Secret is injected via the `PRIMARY_PASSWORD` Secret env,
  converted into `/tmp/primary.pgpass`. The TLS client key / cert / root
  cert come from a projected Secret mounted in an init container and
  copied to `/tmp/primary-client.*`, then wired into
  `primary_conninfo`'s `passfile` / `sslkey` / `sslcert` /
  `sslrootcert`.
- WAL-archive / object-store hybrid, distributed-topology demotion /
  promotion token, and live cross-cluster drill are follow-up.

### Declarative hibernation

Hibernation is opted in via the `cnpg.io/hibernation` annotation (kept
for ecosystem-tool compatibility). Hibernation preserves the shard
StatefulSet's and PVC template's ownership while scaling the database
Pod count to zero. PVCs are not deleted, so the cluster can be
rehydrated later.

```fish
kubectl annotate postgrescluster quickstart --overwrite cnpg.io/hibernation=on
kubectl get postgrescluster quickstart -o \
  'jsonpath={.status.conditions[?(@.type=="cnpg.io/hibernation")]}'

# Rehydrate
kubectl annotate postgrescluster quickstart --overwrite cnpg.io/hibernation=off
# or remove the annotation
kubectl annotate postgrescluster quickstart cnpg.io/hibernation-
```

Expected state during hibernation:

- shard StatefulSet `spec.replicas=0`.
- `status.phase=Hibernated`.
- `status.conditions[type=cnpg.io/hibernation].status=True`.
- `Ready=False`, `Progressing=False`.
- If a native router is on, its Deployment is also `replicas=0`.

## 5. Smoke verification (kind)

```fish
./hack/smoke.sh           # tears down on exit
./hack/smoke.sh --keep    # keep the cluster (debugging)
PG_MAJOR=17 POSTGRES_VERSION=17 CR_NAME=quickstart17 ./hack/smoke.sh
PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18 ./hack/smoke.sh
PG_MAJOR=17 POSTGRES_VERSION=17 CR_NAME=quickstart17ha SHARD_REPLICAS=1 ./hack/smoke.sh
PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18ha SHARD_REPLICAS=1 ./hack/smoke.sh
SMOKE_POOLER=1 CR_NAME=quickstartpooler ./hack/smoke.sh
SMOKE_HIBERNATION=1 CR_NAME=quickstarthibernate ./hack/smoke.sh
PG_MAJOR=18 POSTGRES_VERSION=18 CR_NAME=quickstart18fo SHARD_REPLICAS=1 SMOKE_FAILOVER=1 ./hack/smoke.sh
```

The script:

1. Creates the kind cluster `postgres-operator-smoke`.
2. Builds the operator + PG images locally and loads them into kind
   (`SMOKE_POOLER=1` also loads the PgBouncer image).
3. Applies `dist/install.yaml` server-side.
4. Applies the quickstart sample.
5. Waits up to 5 minutes for `StatefulSet.ReadyReplicas ≥ 1`.
6. Verifies a `psql -c 'SELECT 1'` round-trip.
7. With `SMOKE_HIBERNATION=1`: exercises the hibernation annotation
   `cnpg.io/hibernation=on/off`, StatefulSet `replicas=0`, PVC
   preservation, and a marker-row `SELECT` on rehydration.
8. With `SMOKE_POOLER=1`: creates the Pooler CR + the PgBouncer auth
   Secret, runs `psql SELECT 1` through the Pooler Service, blocks new
   clients when `spec.paused=true`, reconnects after `spec.paused=false`,
   patches `pgbouncer.parameters` and confirms a configHash change with
   an in-place `SIGHUP` reload (no Pod replacement) and a successful
   re-connection.
9. With `SHARD_REPLICAS≥1`: observes the streaming standby in
   `pg_stat_replication`.
10. With `SMOKE_FAILOVER=1`: deletes the primary Pod and measures the
    standby-promotion RTO.

## 6. Pooler monitoring

Enabling the PgBouncer exporter sidecar adds stable selector labels to
the Pooler Pods / Service. In a Prometheus Operator environment, manage
the `PodMonitor` directly (no auto-generation).

```fish
kubectl apply -f config/samples/postgres_v1alpha1_pooler_podmonitor.yaml
```

See `docs/operator-guide/pooler-monitoring.md` for the full example.

`PG_MAJOR` selects the base major of the runtime image to build;
`POSTGRES_VERSION` is wired into `PostgresCluster.spec.postgresVersion`.
The 0.3.0-alpha smoke matrix is PG17 + PG18. `SHARD_REPLICAS` is mapped
1:1 to `spec.shards.replicas`.

## 7. Alpha-stage caveats (read before adopting in production)

- **Secret integration is incomplete** — in alpha, the postgres user
  password relies on trust / peer auth. scram-sha-256 host auth is
  enabled only by ConfigMap. Kubernetes Secret + dynamic password
  rotation is a follow-up cycle (alongside F04 backup).
- **HA verification is bounded** — on 2026-05-07 the PG18
  `SHARD_REPLICAS=1 SMOKE_FAILOVER=1` smoke confirmed that primary-Pod
  deletion → standby promotion takes RTO 21 s (< 30 s), the CR status
  primary converges, and the restarted old primary rejoins as standby.
  chaos-mesh kill / network partition, multi-node failure, and full
  pgBackRest-integrated production HA are F05 follow-up.
- **Standby reconstruction is bounded** — the restarted old primary's
  marker is created when the existing PGDATA and current-primary-endpoint
  comparison indicates so, and the instance manager runs `pg_rewind`
  with the same `PRIMARY_ENDPOINT` and writes `standby.signal` /
  `primary_conninfo`. The first-boot and rejoin standbys use the Pod
  name as `application_name`, so synchronous replication's standby
  names align. On `pg_rewind` failure we fall back to a fresh
  `pg_basebackup`; if that also fails the original data dir is restored.
  The failure cause is surfaced in
  `PostgresCluster.status.shards[].replicas[].reason/message`. Live
  divergent-WAL rewind drill and external fencing / STONITH-class
  verification are F03 / F05 follow-up.
- **Synchronous replication has no live verification yet** — the
  `postgresql.synchronous` CRD / schema, the required / preferred config
  rendering, the ConfigMap-hash rolling reconcile, and the standby
  `application_name` wiring are all unit-test-pinned. A real commit
  latency / RPO=0 kind drill is F05 follow-up.
- **Hibernation lacks live measurement** — the hibernation annotation
  `cnpg.io/hibernation=on/off`, StatefulSet scale-to-zero / restore,
  PVC-template preservation, and the condition / phase surface are
  envtest-verified, and a `SMOKE_HIBERNATION=1` kind drill path was
  added. Actual PVC data-preservation rehydration `SELECT` round-trips
  are F05 follow-up.
- **Only single-shard is GA** — `shardingMode=native` + multi-shard +
  router become meaningful after P2. This alpha guarantees only
  `shardingMode=none` (single shard).

## 8. Troubleshooting

| Symptom | Cause / remedy |
|---|---|
| Pod stuck in ImagePullBackOff | `ghcr.io/keiailab/pg:18` not present in the cluster registry. Run `make docker-build-pg` + `kind load docker-image`, or push to a private mirror. |
| PgBouncer image kind-load fails with an OCI-index digest error | `hack/smoke.sh` falls back to a single-platform `ctr images import` when `kind load docker-image` fails. The upstream PgBouncer image attestation manifest can trigger this on Docker Desktop arm64. |
| CRD apply fails with `metadata.annotations: Too long` | `dist/install.yaml` exceeds the client-side apply size limit. Use `kubectl apply --server-side -f dist/install.yaml` instead. |
| PgBouncer Pod CrashLoops on a read-only-rootfs with `/tmp/.s.PGSQL.5432` | The operator should render `unix_socket_dir = ` to disable the Unix socket. If the ConfigMap lacks that line, rebuild with the latest operator image. |
| A single-member quickstart CrashLoops after PVC label `postgres.keiailab.io/fenced=true` | Legacy alpha-image bug in single-member election-stop handling. The latest PG runtime image skips the PVC fence / fast demote when `POSTGRES_MEMBER_COUNT=1` and leadership stops. |
| Pod CrashLoopBackOff during initdb | PVC ownership issue. The StorageClass may not propagate `fsGroup`. Confirm that the SecurityContext `FSGroup=70` is applied. |
| `/readyz` 503 with "starting election" | Normal bootstrap phase. If it persists for 30–60 s, leases RBAC is missing. Inspect `kubectl get role,rolebinding -l app.kubernetes.io/instance=<cluster>`. |
| `/readyz` 503 with "postgres not ready" | The postgres child does not answer on the local DSN. Inside the Pod run `kubectl exec ... -c postgres -- ls /var/run/postgresql` — if the Unix socket is missing, double-check `unix_socket_directories` in `postgresql.conf`. |
| Reconcile loops endlessly | Check the controller log. The webhook CEL XValidation may be rejecting the CR — `kubectl get postgrescluster <name> -o yaml`'s events. |

## 9. References

- ADR 0002 — instance-manager PID 1 model.
- ADR 0006 — dataplane SecurityContext.
- RFC 0001 — PostgresCluster CRD v2 schema.
- RFC 0003 — election + fencing interface.
- docs/internal/HANDOFF.md — work-in-progress and the next-cycle entry point.
