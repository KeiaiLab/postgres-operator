# Pooler Monitoring Guide

> Per CloudNativePG 1.29: the Pooler PgBouncer exporter is exposed on
> each Pooler Pod's `metrics` port 9127 and emits `cnpg_pgbouncer_`
> prefixed metrics. In a Prometheus Operator environment the
> recommended pattern is for the user to manage the PodMonitor directly.

## User scenario

The user enables the PgBouncer exporter sidecar by setting
`Pooler.spec.pgbouncer.exporter` and scrapes the Pooler Pods with a
Prometheus Operator `PodMonitor`.

Expected outcome:

- The Pooler Pod template and Service receive these labels:
  - `postgres.keiailab.io/cluster`
  - `postgres.keiailab.io/pooler`
  - `postgres.keiailab.io/pooler-type`
- The exporter sidecar exposes the `metrics` container port.
- The Pooler Service exposes the `metrics` Service port.
- The operator-manager `/metrics` exposes `postgres_operator_pooler_phase`.
- The exporter uses the CNPG-compatible metric prefix `cnpg_pgbouncer_`;
  alert rules key off `cnpg_pgbouncer_last_collection_error`,
  `cnpg_pgbouncer_pools_cl_waiting`, and `cnpg_pgbouncer_pools_maxwait`.
- The Helm PrometheusRule detects Pooler failure as well as PgBouncer
  exporter collection failure, client waiting, and excess client max-wait
  time.
- The Helm Grafana dashboard ConfigMap renders the Pooler dashboard with
  exporter-collection-error, waiting-clients, max-wait, active
  client/server connection, and client-slot panels.
- The PgBouncer container runs readiness / liveness / startup probes on
  the `pgbouncer` TCP port.
- The exporter sidecar runs readiness / liveness probes on the
  `metrics` port's `/metrics` path.
- `pgbouncer.parameters` is validated against an allowlist aligned with
  the CNPG 1.29 Pooler PgBouncer option surface.
- Operator-owned keys (`listen_addr`, `listen_port`, `auth_file`,
  `pool_mode`) cannot be overridden directly in the CR.
- `ignore_startup_parameters` always includes `extra_float_digits,options`
  in addition to whatever the user specifies.
- When `pg_hba` is set the operator generates a `pg_hba.conf` ConfigMap
  key + mount and owns PgBouncer's `auth_type=hba` /
  `auth_hba_file=/etc/pgbouncer/config/pg_hba.conf`.
- A Pooler with `instances > 1` automatically receives zone / hostname
  topology spread and a `minAvailable=instances-1` PDB.
- The Pooler Deployment rolling-update defaults converge to
  `maxUnavailable=0`, `maxSurge=1`, `minReadySeconds=5`.
- Setting `deploymentStrategy` explicitly overrides the PgBouncer
  Deployment replacement strategy.
- Setting `serviceAccountName` runs the Pooler Pods under an existing
  ServiceAccount, which is useful for cloud IAM or other external
  authenticators.
- Setting `serverTLSSecret`, `serverCASecret`, `clientTLSSecret`, or
  `clientCASecret` causes the operator to render the PgBouncer TLS file
  configuration and Secret volume mount; missing Secrets or missing
  required keys fail closed as `InvalidSpec`.
- A `type: ro` Pooler renders all ready replicas into PgBouncer's host
  list, and when there are multiple hosts defaults to
  `server_round_robin=1` and `server_login_retry=2`.
- Pooler status records `instances`, `readyReplicas`, `backendTargets`,
  and `configHash`, allowing auditing of the current routing target and
  the deployed PgBouncer config.
- `spec.paused: true` sends `SIGUSR1` to every ready PgBouncer Pod,
  PAUSEing new client traffic; setting it back to `false` sends
  `SIGUSR2` to RESUME. Application can be confirmed via
  `status.paused` and the
  `postgres.keiailab.io/pgbouncer-paused` annotation on each Pod.
- Changing `spec.pgbouncer.parameters` is applied in-place: once the
  ConfigMap projection's `config.sha256` reaches the new hash on each
  ready Pod, the operator sends `SIGHUP`. The Deployment generation and
  Pod names stay put; the per-Pod annotation
  `postgres.keiailab.io/pgbouncer-config-sha256` records the applied
  hash.

## Pooler example

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: Pooler
metadata:
  name: quickstart-rw
  namespace: default
spec:
  cluster:
    name: quickstart
  instances: 3
  paused: false
  type: rw
  serviceAccountName: quickstart-pooler
  deploymentStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  pgbouncer:
    image: ghcr.io/cloudnative-pg/pgbouncer:1.24.1
    poolMode: transaction
    authSecretRef:
      name: quickstart-pooler-auth
    serverTLSSecret:
      name: quickstart-pooler-server-tls
    serverCASecret:
      name: quickstart-pooler-server-ca
    clientTLSSecret:
      name: quickstart-pooler-client-tls
    clientCASecret:
      name: quickstart-pooler-client-ca
    exporter:
      image: example.com/pgbouncer-exporter:0.8
      port: 9127
    parameters:
      max_client_conn: "1000"
      default_pool_size: "20"
    pg_hba:
      - hostssl all app 10.0.0.0/8 scram-sha-256
      - hostnossl all all 0.0.0.0/0 reject
```

## PodMonitor example

`config/samples/postgres_v1alpha1_pooler_podmonitor.yaml`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: quickstart-rw-pooler
  namespace: default
spec:
  namespaceSelector:
    matchNames:
      - default
  selector:
    matchLabels:
      postgres.keiailab.io/cluster: quickstart
      postgres.keiailab.io/pooler: quickstart-rw
      postgres.keiailab.io/pooler-type: rw
  podMetricsEndpoints:
    - port: metrics
      path: /metrics
      interval: 30s
      scrapeTimeout: 10s
```

## Verification

```fish
kubectl get pod -l postgres.keiailab.io/pooler=quickstart-rw \
    -o jsonpath='{.items[0].metadata.labels}'

kubectl get svc quickstart-rw-pooler \
    -o jsonpath='{.spec.ports[?(@.name=="metrics")].port}'

kubectl get deploy quickstart-rw-pooler \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="pgbouncer")].readinessProbe.tcpSocket.port}'

kubectl get deploy quickstart-rw-pooler \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="pgbouncer-exporter")].readinessProbe.httpGet.path}'

kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'ignore_startup_parameters'

kubectl get pdb quickstart-rw-pooler-pdb \
    -o jsonpath='{.spec.minAvailable}'

kubectl get deploy quickstart-rw-pooler \
    -o jsonpath='{.spec.strategy.rollingUpdate.maxUnavailable}'

kubectl get deploy quickstart-rw-pooler \
    -o jsonpath='{.spec.template.spec.serviceAccountName}'

kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'server_tls_sslmode = verify-ca'

kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'auth_hba_file = /etc/pgbouncer/config/pg_hba.conf'

kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pg_hba\.conf}'

kubectl get deploy quickstart-rw-pooler \
    -o jsonpath='{.spec.template.spec.volumes[?(@.name=="pgbouncer-tls-server")].secret.secretName}'

kubectl get configmap quickstart-ro-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'server_round_robin = 1'

kubectl get configmap quickstart-ro-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'server_login_retry = 2'

kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'unix_socket_dir ='

kubectl get pooler quickstart-ro \
    -o jsonpath='{.status.backendTargets}'

kubectl get pooler quickstart-ro \
    -o jsonpath='{.status.configHash}'

kubectl patch pooler quickstart-rw --type=merge -p '{"spec":{"paused":true}}'
kubectl get pooler quickstart-rw \
    -o jsonpath='{.status.paused}'
kubectl logs -l postgres.keiailab.io/pooler=quickstart-rw -c pgbouncer --tail=80 | grep 'got SIGUSR1'

kubectl patch pooler quickstart-rw --type=merge -p '{"spec":{"paused":false}}'
kubectl logs -l postgres.keiailab.io/pooler=quickstart-rw -c pgbouncer --tail=80 | grep 'got SIGUSR2'

old_hash=$(kubectl get pooler quickstart-rw -o jsonpath='{.status.configHash}')
kubectl patch pooler quickstart-rw --type=merge \
    -p '{"spec":{"pgbouncer":{"parameters":{"max_client_conn":"120","default_pool_size":"12"}}}}'
new_hash=$(kubectl get pooler quickstart-rw -o jsonpath='{.status.configHash}')
test "$old_hash" != "$new_hash"
kubectl get pods -l postgres.keiailab.io/pooler=quickstart-rw \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.annotations.postgres\.keiailab\.io/pgbouncer-config-sha256}{"\n"}{end}'
kubectl logs -l postgres.keiailab.io/pooler=quickstart-rw -c pgbouncer --since=3m | grep -E 'SIGHUP|reload'
kubectl get configmap quickstart-rw-pooler-config \
    -o jsonpath='{.data.pgbouncer\.ini}' | grep 'default_pool_size = 12'

kubectl apply -f config/samples/postgres_v1alpha1_pooler_podmonitor.yaml

helm template monitor charts/postgres-operator \
    --set metrics.serviceMonitor.enabled=true \
    --set metrics.prometheusRule.enabled=true \
    --set metrics.grafanaDashboards.enabled=true \
    | rg 'PostgresPoolerExporterCollectionFailed|PostgresPoolerClientWaiting|PostgresPoolerClientMaxWaitHigh|cnpg_pgbouncer_last_collection_error|cnpg_pgbouncer_pools_cl_waiting|cnpg_pgbouncer_pools_maxwait'

helm template monitor charts/postgres-operator \
    --set metrics.grafanaDashboards.enabled=true \
    | rg 'postgres-operator-pooler.json|cnpg_pgbouncer_pools_sv_active|cnpg_pgbouncer_lists_used_clients'
```

2026-05-12 kind run:

```fish
CLUSTER_NAME=postgres-operator-smoke-pooler-0512 \
    CR_NAME=quickstartpooler \
    SMOKE_POOLER=1 \
    ./hack/smoke.sh --keep
```

Observed:

- `psql SELECT 1 = 1` against the quickstart Postgres.
- Pooler Deployment `2/2` ready.
- Pooler status `phase: Ready`, `readyReplicas: 2`, `backendTargets` reflected.
- `SELECT 1 = 1` through the Pooler Service.
- After patching `spec.paused=true`, new Pooler clients block until the
  5-second timeout.
- PgBouncer logs show `got SIGUSR1, pausing all activity` and
  `got SIGUSR2, continuing from PAUSE`.
- After patching `spec.paused=false`, `SELECT 1 = 1` works through the
  Pooler Service again.
- Patching `spec.pgbouncer.parameters.default_pool_size=12` /
  `max_client_conn=120` changes `status.configHash` while leaving Pod
  names and Deployment generation untouched; the Pod hash annotation is
  updated, PgBouncer logs show the `SIGHUP` reload, the ConfigMap
  reflects the new value, and `SELECT 1 = 1` works through the Pooler
  Service.
- PgBouncer config `unix_socket_dir = ` disables the Unix socket.
- Helm PrometheusRule render contains
  `PostgresPoolerExporterCollectionFailed`,
  `PostgresPoolerClientWaiting`,
  `PostgresPoolerClientMaxWaitHigh`, plus
  `cnpg_pgbouncer_last_collection_error`,
  `cnpg_pgbouncer_pools_cl_waiting`,
  `cnpg_pgbouncer_pools_maxwait`.
- Helm Grafana dashboard render contains
  `postgres-operator-pooler.json`,
  `cnpg_pgbouncer_pools_sv_active`,
  `cnpg_pgbouncer_lists_used_clients`.

Remaining:

- Live Prometheus Operator scrape and Grafana dashboard import.
- Built-in auth user / TLS auto-issuance reconciliation (T27 ⑤ done,
  T27 ⑥ done; TLS auto-issuance is T29).
