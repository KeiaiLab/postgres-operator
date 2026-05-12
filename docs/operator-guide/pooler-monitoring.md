# Pooler Monitoring Guide

> CNPG 1.29 기준: Pooler PgBouncer exporter 는 각 Pooler Pod 의 `metrics` port
> 9127 에서 노출되며, `cnpg_pgbouncer_` prefix metric 을 제공한다.
> Prometheus Operator 환경에서는 PodMonitor 를 사용자가 직접 관리하는 패턴이 권장된다.

## 사용자 시나리오

사용자는 `Pooler.spec.pgbouncer.exporter` 를 설정해 PgBouncer exporter sidecar 를
활성화하고, Prometheus Operator 의 `PodMonitor` 로 해당 Pooler Pod 를 scrape 한다.

기대 결과:

- Pooler Pod template 과 Service 에 다음 라벨이 붙는다.
  - `postgres.keiailab.io/cluster`
  - `postgres.keiailab.io/pooler`
  - `postgres.keiailab.io/pooler-type`
- exporter sidecar 는 `metrics` container port 를 노출한다.
- Pooler Service 는 `metrics` Service port 를 노출한다.
- operator manager `/metrics` 는 `postgres_operator_pooler_phase` 를 노출한다.
- exporter 는 CNPG 호환 metric prefix 인 `cnpg_pgbouncer_` 를 사용해야 하며,
  알림 규칙은 `cnpg_pgbouncer_last_collection_error`,
  `cnpg_pgbouncer_pools_cl_waiting`, `cnpg_pgbouncer_pools_maxwait` 를 기준으로 한다.
- Helm PrometheusRule 은 Pooler 실패와 별도로 PgBouncer exporter collection 실패,
  client 대기 발생, client 최대 대기시간 초과를 감지한다.
- Helm Grafana dashboard ConfigMap 은 Pooler dashboard 에 exporter collection error,
  waiting clients, max wait, active client/server connection, client slot 패널을 렌더한다.
- PgBouncer 컨테이너는 `pgbouncer` TCP port 로 readiness/liveness/startup probe 를 수행한다.
- exporter sidecar 는 `metrics` port 의 `/metrics` 로 readiness/liveness probe 를 수행한다.
- `pgbouncer.parameters` 는 CNPG 1.29 Pooler 문서의 PgBouncer option 표면에 맞춘 allowlist 로 검증된다.
- operator-owned key 인 `listen_addr`, `listen_port`, `auth_file`, `pool_mode` 는 CR 에서 직접 override 할 수 없다.
- `ignore_startup_parameters` 는 사용자가 값을 지정해도 `extra_float_digits,options` 를 항상 포함한다.
- `pg_hba` 를 지정하면 `pg_hba.conf` ConfigMap key 와 mount 를 생성하고, PgBouncer `auth_type=hba`, `auth_hba_file=/etc/pgbouncer/config/pg_hba.conf` 를 operator 가 소유한다.
- `instances > 1` 인 Pooler 는 zone/hostname topology spread 와 `minAvailable=instances-1` PDB 를 자동 생성한다.
- Pooler Deployment rolling update 는 `maxUnavailable=0`, `maxSurge=1`, `minReadySeconds=5` 로 수렴한다.
- `deploymentStrategy` 를 지정하면 PgBouncer Deployment 교체 전략을 명시적으로 override 할 수 있다.
- `serviceAccountName` 을 지정하면 cloud IAM 등 외부 인증에 맞춘 기존 ServiceAccount 로 Pooler Pod 를 실행할 수 있다.
- `serverTLSSecret`, `serverCASecret`, `clientTLSSecret`, `clientCASecret` 을 지정하면 PgBouncer TLS 파일 설정과 Secret volume mount 를 operator 가 생성하고, Secret 부재/필수 키 누락은 `InvalidSpec` 으로 fail-closed 한다.
- `type: ro` Pooler 는 ready replica 전체를 PgBouncer host list 로 렌더하고 복수 host 일 때 `server_round_robin=1`, `server_login_retry=2` 를 기본 적용한다.
- Pooler status 는 `instances`, `readyReplicas`, `backendTargets`, `configHash` 를 기록해 현재 라우팅 대상과 배포된 PgBouncer config 를 audit 할 수 있다.
- `spec.paused: true` 는 준비된 PgBouncer Pod 에 `SIGUSR1` 을 보내 신규 client 처리를 PAUSE 하고, `false` 로 되돌리면 `SIGUSR2` 로 RESUME 한다. 적용 여부는 `status.paused` 와 각 Pod 의 `postgres.keiailab.io/pgbouncer-paused` annotation 으로 확인한다.
- `spec.pgbouncer.parameters` 변경은 ConfigMap projection 의 `config.sha256` 가 새 hash 로 보이는 ready Pod 에 `SIGHUP` 을 보내 in-place reload 로 반영한다. Deployment generation 과 Pod 이름은 유지되고, 각 Pod annotation `postgres.keiailab.io/pgbouncer-config-sha256` 로 적용 hash 를 audit 한다.

## Pooler 예시

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

## PodMonitor 예시

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

## 검증

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

2026-05-12 kind 실측:

```fish
CLUSTER_NAME=postgres-operator-smoke-pooler-0512 \
    CR_NAME=quickstartpooler \
    SMOKE_POOLER=1 \
    ./hack/smoke.sh --keep
```

확인 결과:

- quickstart Postgres `psql SELECT 1 = 1`
- Pooler Deployment `2/2` ready
- Pooler status `phase: Ready`, `readyReplicas: 2`, `backendTargets` 반영
- Pooler Service 경유 `SELECT 1 = 1`
- `spec.paused=true` 패치 후 신규 Pooler client 가 5s timeout 으로 차단됨
- PgBouncer 로그에서 `got SIGUSR1, pausing all activity` 와 `got SIGUSR2, continuing from PAUSE` 확인
- `spec.paused=false` 패치 후 Pooler Service 경유 `SELECT 1 = 1` 재확인
- `spec.pgbouncer.parameters.default_pool_size=12`, `max_client_conn=120` 패치 후 `status.configHash` 변경, Pod 이름/Deployment generation 유지, Pod hash annotation 반영, PgBouncer `SIGHUP` reload 로그, ConfigMap 반영, Pooler Service 경유 `SELECT 1 = 1` 재확인
- PgBouncer config `unix_socket_dir = ` 로 Unix socket 비활성화
- Helm PrometheusRule 렌더링에서 `PostgresPoolerExporterCollectionFailed`,
  `PostgresPoolerClientWaiting`, `PostgresPoolerClientMaxWaitHigh` 와
  `cnpg_pgbouncer_last_collection_error`, `cnpg_pgbouncer_pools_cl_waiting`,
  `cnpg_pgbouncer_pools_maxwait` 확인
- Helm Grafana dashboard 렌더링에서 `postgres-operator-pooler.json`,
  `cnpg_pgbouncer_pools_sv_active`, `cnpg_pgbouncer_lists_used_clients` 확인

남은 범위:

- Prometheus Operator live scrape 와 Grafana dashboard import 검증.
- built-in auth user/TLS 자동 생성 reconciliation.
