# Deployment Guide — keiailab/postgres-operator (alpha)

> 0.3.0-alpha 시점의 K8s 배포 가이드. RFC 0001 v2 schema 기준.
> production 사용은 alpha 단계 한계 (e2e 미검증, secret rotation 부재) 인지 후 진행.

## 사전 요구

- K8s 1.28+ (CEL XValidation 의존)
- StorageClass — PVC 동적 프로비저닝 가능해야 함
- Container registry 접근 — `ghcr.io/keiailab/*` (또는 사용자 자체 mirror)

## 1. 이미지

### 운영자 (manager) 이미지

```
ghcr.io/keiailab/postgres-operator:<version>
```

빌드:

```fish
make docker-build IMG=ghcr.io/keiailab/postgres-operator:dev
make docker-push IMG=ghcr.io/keiailab/postgres-operator:dev
```

### PG runtime 이미지 (instance manager + postgres)

```
ghcr.io/keiailab/pg:18  (PG_MAJOR=18)
ghcr.io/keiailab/pg:17  (옵션)
```

빌드:

```fish
make docker-build-pg PG_MAJOR=18 PG_IMG=ghcr.io/keiailab/pg:18
make docker-push-pg PG_IMG=ghcr.io/keiailab/pg:18
```

본 이미지는 `Dockerfile.pg` 정의 — 2-stage build (golang:1.25-bookworm 으로
`cmd/instance` 빌드 + postgres:18-bookworm base + UID/GID 70 user). ENTRYPOINT 는
`/usr/local/bin/instance` (instance manager 가 PID 1 으로 동작, postgres child 를 fork).

## 2. CRD + operator 설치

### Helm

```fish
helm install postgres-operator charts/postgresql-operator \
    --namespace postgres-operator-system --create-namespace \
    --set image.repository=ghcr.io/keiailab/postgres-operator \
    --set image.tag=dev
```

### kustomize (개발/테스트)

```fish
make build-installer
kubectl apply -f dist/install.yaml
```

## 3. PostgresCluster 인스턴스 배포

### Quickstart (single shard, dev)

```fish
kubectl apply -f config/samples/postgres_v1alpha1_postgrescluster_dev.yaml
```

기대 결과:

- ConfigMap (postgresql.conf + pg_hba.conf), Headless Service, StatefulSet 1 개
- ServiceAccount + Role + RoleBinding (instance Pod 가 leases + PVC fence patch 권한)
- Pod 부팅 흐름:
  1. initdb init container — fresh PVC 면 PGDATA 초기화
  2. instance manager 가 PID 1 으로 시작
  3. postgres child fork
  4. K8s lease 기반 election 수렴 → primary 선출 → `pg_promote()`
  5. `/readyz` 200 → Pod Ready

```fish
# 상태 확인
kubectl get postgrescluster quickstart -o yaml | yq '.status'
kubectl get sts,svc,pod -l app.kubernetes.io/instance=quickstart
```

### 접속

Pod 안 Unix socket 으로 (peer auth, dev):

```fish
kubectl exec quickstart-shard-0-0 -c postgres -- \
    psql -h /var/run/postgresql -U postgres -c 'SELECT version()'
```

Pod 외부 (cluster 내부 다른 Pod 에서, scram-sha-256):

```
psql "host=quickstart-shard-0-headless.default.svc.cluster.local user=postgres dbname=postgres"
```

(password 는 alpha 단계에서 secret 으로 별도 주입 — 후속).

## 4. Production 토폴로지 (예시)

`config/samples/postgres_v1alpha1_postgrescluster_prod.yaml` — multi-shard +
router, replicas=2 (3-way HA), monitoring 활성, custom storageClass.

production 적용 전 확인:

- StorageClass 가 fast SSD-backed (alpha 권장 1GB+/min IOPS)
- replicas≥1 (HA — RFC 0001 §3 권장)
- monitoring.serviceMonitor + Prometheus operator 사전 설치
- backup.enabled — F04 후속 PR 후에만 의미

## 5. 스모크 검증 (kind)

```fish
./hack/smoke.sh           # cleanup 후 종료
./hack/smoke.sh --keep    # cluster 유지 (디버깅)
```

본 스크립트는:

1. kind cluster `postgres-operator-smoke` 생성
2. operator + PG 이미지 로컬 빌드 + kind load
3. dist/install.yaml apply
4. quickstart sample apply
5. StatefulSet ReadyReplicas≥1 대기 (5분 timeout)
6. `psql -c 'SELECT 1'` round-trip 검증

## 6. alpha 단계 한계 (production 도입 전 인지 사항)

- **secret 미통합** — postgres user password 가 alpha 에서는 trust/peer auth 의존.
  scram-sha-256 host auth 는 ConfigMap 으로만 활성. K8s Secret + dynamic
  password rotation 은 후속 cycle (F04 backup 과 같이).
- **failover 미검증** — primary kill → new primary 선출까지의 RTO/RPO 는 RFC 0003
  문서값이지 e2e 검증 결과 아님. F05 e2e 시나리오에서 chaos-mesh 검증.
- **standby 재구성 미구현** — primary demote → standby 재진입의 standby.signal /
  primary_conninfo 자동 구성은 F03 후속.
- **단일 shard 만 GA** — shardingMode=native + multi-shard + router 는 P2 진입 후
  의미. 본 alpha 는 shardingMode=none (single shard) 만 보장.

## 7. 트러블슈팅

| 증상 | 원인 / 조치 |
|---|---|
| Pod 가 ImagePullBackOff | `ghcr.io/keiailab/pg:18` 가 cluster registry 에서 미발견. `make docker-build-pg` + `kind load docker-image` 또는 사설 mirror push. |
| Pod CrashLoopBackOff (initdb) | PVC ownership 문제. SecurityContext FSGroup=70 이 적용되지 않은 StorageClass — PVC 동적 프로비저닝 시 fsGroup 전파 가능 검사. |
| /readyz 503 "starting election" | 정상 부트스트랩 phase. 30~60s 안 해소되지 않으면 leases RBAC 부재. `kubectl get role,rolebinding -l app.kubernetes.io/instance=<cluster>`. |
| /readyz 503 "postgres not ready" | postgres child 가 LocalDSN 으로 응답 안함. instance Pod 안 `kubectl exec ... -c postgres -- ls /var/run/postgresql` — Unix socket 부재 시 postgresql.conf 의 unix_socket_directories 확인. |
| Reconcile 무한 requeue | controller log 확인. webhook CEL XValidation 거부 가능 — `kubectl get postgrescluster <name> -o yaml` events. |

## 8. 참조

- ADR 0002 — instance manager PID 1 모델
- ADR 0006 — dataplane SecurityContext
- RFC 0001 — PostgresCluster CRD v2 schema
- RFC 0003 — election + fencing 인터페이스
- HANDOFF.md — 진행 중 작업 + 다음 cycle 진입점
