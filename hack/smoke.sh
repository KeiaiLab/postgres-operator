#!/usr/bin/env bash
# hack/smoke.sh — kind 환경에서 quickstart sample 을 적용하고 postgres Pod 가
# Ready 가 될 때까지 검증하는 스모크 스크립트.
#
# 의존:
#   - kind, kubectl, helm, docker
#   - GHCR 또는 로컬 빌드의 operator + pg 이미지
#
# 사용:
#   ./hack/smoke.sh [--keep]  # --keep 이면 종료 후에도 kind cluster 유지
#   PG_MAJOR=17 POSTGRES_VERSION=17 SHARD_REPLICAS=1 ./hack/smoke.sh
#   SMOKE_FAILOVER=1 SHARD_REPLICAS=1 ./hack/smoke.sh
#   SMOKE_HIBERNATION=1 ./hack/smoke.sh
#   SMOKE_POOLER=1 ./hack/smoke.sh
#
# 향후 시나리오 (TASKS T27 ⑤+, 미구현):
#   (없음 — T27 ①~④ 모두 완료)
#
# 흐름:
#   1. kind cluster 생성 (이미 있으면 재사용)
#   2. operator + PG image 로컬 빌드 후 kind 에 load
#   3. CRD + operator 설치 (kustomize 또는 helm)
#   4. quickstart sample apply
#   5. Pod Ready 대기 (5분 timeout)
#   6. psql round-trip 검증 (`kubectl exec ... -- psql -c 'SELECT 1'`)
#   7. SMOKE_HIBERNATION=1 이면 cnpg.io/hibernation=on/off + PVC data 보존 검증
#   8. SMOKE_POOLER=1 이면 Pooler Service 경유 psql round-trip 검증
#   9. SMOKE_DATABASE=1 이면 PostgresDatabase CR → status.applied + pg_database 검증
#   10. SMOKE_USER=1 이면 PostgresUser CR → status.applied + pg_roles 검증
#   11. SMOKE_SCHEDULEDBACKUP=1 이면 ScheduledBackup CR → immediate BackupJob 생성 검증
#   12. SMOKE_IMAGECATALOG=1 이면 ImageCatalog/ClusterImageCatalog CR schema + lookup 검증
#   13. replicas>=1 이면 streaming standby 를 pg_stat_replication 으로 확인
#   14. SMOKE_FAILOVER=1 이면 primary Pod 삭제 후 standby promote RTO 측정
#   15. cleanup (--keep 미지정 시 cluster 삭제)

set -euo pipefail

KEEP=0
if [[ "${1:-}" == "--keep" ]]; then
    KEEP=1
fi

CLUSTER_NAME="${CLUSTER_NAME:-postgres-operator-smoke}"
NS="${NS:-default}"
CR_NAME="${CR_NAME:-quickstart}"
POSTGRES_VERSION="${POSTGRES_VERSION:-${PG_MAJOR:-18}}"
PG_MAJOR="${PG_MAJOR:-$POSTGRES_VERSION}"
PG_IMG="${PG_IMG:-ghcr.io/keiailab/pg:${PG_MAJOR}}"
PGBOUNCER_IMG="${PGBOUNCER_IMG:-ghcr.io/cloudnative-pg/pgbouncer:1.24.1}"
SHARD_REPLICAS="${SHARD_REPLICAS:-${POSTGRES_REPLICAS:-${REPLICAS:-0}}}"
DESIRED_MEMBERS=$(( SHARD_REPLICAS + 1 ))
# install.yaml 이 config/manager/kustomization.yaml 의 newTag 를 사용하고, 그 값은
# charts/postgres-operator/Chart.yaml 의 appVersion 과 동기화돼 있다 (Makefile §3 IMAGE_TAG).
# smoke.sh 가 다른 태그 (예: ":smoke") 로 빌드/로드하면 kubelet 이 install.yaml 의 태그를
# pull 하려다 실패한다. drift 방지를 위해 단일 출처에서 태그 도출.
OPERATOR_TAG="${OPERATOR_TAG:-$(awk '/^appVersion:/ { gsub(/"/, "", $2); print $2; exit }' charts/postgres-operator/Chart.yaml)}"
OPERATOR_IMG="${OPERATOR_IMG:-ghcr.io/keiailab/postgres-operator:${OPERATOR_TAG}}"

log() { printf '\n[smoke] %s\n' "$*" >&2; }

format_utc_ts() {
    local epoch="$1"
    if date -u -r "$epoch" +%FT%TZ >/dev/null 2>&1; then
        date -u -r "$epoch" +%FT%TZ
    else
        date -u -d "@$epoch" +%FT%TZ
    fi
}

cleanup() {
    if [[ "$KEEP" == "0" ]]; then
        log "Deleting kind cluster $CLUSTER_NAME"
        kind delete cluster --name "$CLUSTER_NAME" >/dev/null 2>&1 || true
    else
        log "Cluster $CLUSTER_NAME 유지 (--keep). 수동 삭제: kind delete cluster --name $CLUSTER_NAME"
    fi
}

load_image_into_kind() {
    local image="$1"
    local node=""
    local loaded=0

    if kind load docker-image "$image" --name "$CLUSTER_NAME"; then
        return 0
    fi

    log "kind load 실패: $image — 단일 플랫폼 ctr import 로 재시도"
    while IFS= read -r node; do
        [[ -n "$node" ]] || continue
        docker save "$image" | docker exec --privileged -i "$node" \
            ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs -
        loaded=1
    done < <(kind get nodes --name "$CLUSTER_NAME")

    if [[ "$loaded" != "1" ]]; then
        log "ERROR: kind node 를 찾지 못해 image load 실패: $image"
        return 1
    fi
}

wait_for_deployment_available() {
    local namespace="$1"
    local deployment="$2"
    local timeout_seconds="$3"
    local end=$(( $(date +%s) + timeout_seconds ))

    while [[ $(date +%s) -lt $end ]]; do
        if kubectl -n "$namespace" get deployment "$deployment" >/dev/null 2>&1; then
            kubectl -n "$namespace" wait --for=condition=Available deployment "$deployment" --timeout="${timeout_seconds}s"
            return
        fi
        sleep 2
    done

    log "ERROR: Deployment 가 timeout 내 생성되지 않음: $namespace/$deployment"
    return 1
}

wait_for_pooler_paused() {
    local namespace="$1"
    local pooler="$2"
    local desired="$3"
    local timeout_seconds="$4"
    local current=""
    local end=$(( $(date +%s) + timeout_seconds ))

    while [[ $(date +%s) -lt $end ]]; do
        current=$(kubectl -n "$namespace" get pooler "$pooler" -o jsonpath='{.status.paused}' 2>/dev/null || true)
        if [[ "$desired" == "true" && "$current" == "true" ]]; then
            return 0
        fi
        if [[ "$desired" == "false" && "$current" != "true" ]]; then
            return 0
        fi
        sleep 2
    done

    log "ERROR: Pooler $namespace/$pooler paused status=$current, want $desired"
    return 1
}

wait_for_sts_replicas() {
    local namespace="$1"
    local statefulset="$2"
    local desired="$3"
    local timeout_seconds="$4"
    local current=""
    local end=$(( $(date +%s) + timeout_seconds ))

    while [[ $(date +%s) -lt $end ]]; do
        current=$(kubectl -n "$namespace" get sts "$statefulset" -o jsonpath='{.spec.replicas}' 2>/dev/null || true)
        if [[ "${current:-0}" == "$desired" ]]; then
            return 0
        fi
        sleep 2
    done

    log "ERROR: StatefulSet $namespace/$statefulset replicas=$current, want $desired"
    return 1
}

wait_for_hibernation_condition() {
    local namespace="$1"
    local cluster="$2"
    local desired="$3"
    local timeout_seconds="$4"
    local current=""
    local end=$(( $(date +%s) + timeout_seconds ))

    while [[ $(date +%s) -lt $end ]]; do
        current=$(kubectl -n "$namespace" get postgrescluster "$cluster" \
            -o 'jsonpath={.status.conditions[?(@.type=="cnpg.io/hibernation")].status}' 2>/dev/null || true)
        if [[ "$current" == "$desired" ]]; then
            return 0
        fi
        sleep 2
    done

    log "ERROR: PostgresCluster $namespace/$cluster hibernation condition=$current, want $desired"
    return 1
}

wait_for_pooler_config_hash_change() {
    local namespace="$1"
    local pooler="$2"
    local previous_hash="$3"
    local timeout_seconds="$4"
    local current_hash=""
    local pod_hashes=""
    local hash=""
    local all_pods_reloaded=0
    local seen_pod_hash=0
    local end=$(( $(date +%s) + timeout_seconds ))

    while [[ $(date +%s) -lt $end ]]; do
        current_hash=$(kubectl -n "$namespace" get pooler "$pooler" -o jsonpath='{.status.configHash}' 2>/dev/null || true)
        pod_hashes=$(kubectl -n "$namespace" get pods -l "postgres.keiailab.io/pooler=$pooler" \
            -o jsonpath='{range .items[*]}{.metadata.annotations.postgres\.keiailab\.io/pgbouncer-config-sha256}{"\n"}{end}' 2>/dev/null || true)
        all_pods_reloaded=1
        seen_pod_hash=0
        while IFS= read -r hash; do
            [[ -n "$hash" ]] || continue
            seen_pod_hash=1
            if [[ "$hash" != "$current_hash" ]]; then
                all_pods_reloaded=0
                break
            fi
        done <<<"$pod_hashes"
        if [[ -n "$current_hash" && "$current_hash" != "$previous_hash" && "$seen_pod_hash" == "1" && "$all_pods_reloaded" == "1" ]]; then
            return 0
        fi
        sleep 2
    done

    log "ERROR: Pooler $namespace/$pooler configHash=$current_hash podHashes=${pod_hashes//$'\n'/,} previous=$previous_hash"
    return 1
}

pooler_pod_names() {
    local namespace="$1"
    local pooler="$2"
    kubectl -n "$namespace" get pods -l "postgres.keiailab.io/pooler=$pooler" \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort
}

if [[ "${SMOKE_SOURCE_ONLY:-0}" == "1" ]]; then
    return 0 2>/dev/null || exit 0
fi

trap cleanup EXIT

# 1. kind cluster
if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    log "Creating kind cluster $CLUSTER_NAME"
    kind create cluster --name "$CLUSTER_NAME"
else
    log "Reusing existing kind cluster $CLUSTER_NAME"
fi
kubectl cluster-info --context "kind-${CLUSTER_NAME}"

# 2. images — local build + kind load
log "Building operator image $OPERATOR_IMG"
docker build -t "$OPERATOR_IMG" .
log "Building PG image $PG_IMG (PG_MAJOR=$PG_MAJOR)"
docker build -f Dockerfile.pg --build-arg PG_MAJOR="$PG_MAJOR" -t "$PG_IMG" .
if [[ "${SMOKE_POOLER:-0}" == "1" ]]; then
    log "Pulling PgBouncer image $PGBOUNCER_IMG"
    docker pull "$PGBOUNCER_IMG"
fi
log "Loading images into kind"
load_image_into_kind "$OPERATOR_IMG"
load_image_into_kind "$PG_IMG"
if [[ "${SMOKE_POOLER:-0}" == "1" ]]; then
    load_image_into_kind "$PGBOUNCER_IMG"
fi

# 3. CRD + operator 설치 (kustomize 결과 dist/install.yaml 사용)
log "Generating dist/install.yaml + applying"
make build-installer >/dev/null
# operator image override — local kind 에서는 IfNotPresent 로 로딩 이미지 사용.
kubectl apply --server-side -f dist/install.yaml

# operator Pod Ready 대기
log "Waiting for operator manager Pod"
kubectl -n postgres-operator-system wait --for=condition=Available deployment \
    -l control-plane=controller-manager --timeout=180s

# 4. quickstart CR
log "Applying quickstart sample (namespace=$NS postgresVersion=$POSTGRES_VERSION shardReplicas=$SHARD_REPLICAS)"
if ! kubectl get namespace "$NS" >/dev/null 2>&1; then
    kubectl create namespace "$NS"
fi
kubectl apply -f - <<EOF
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: ${CR_NAME}
  namespace: ${NS}
spec:
  postgresVersion: "${POSTGRES_VERSION}"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: ${SHARD_REPLICAS}
    storage:
      size: 10Gi
EOF

# 5. Pod Ready 대기 (5분 timeout — initdb + 첫 부팅 여유)
STS_NAME="${CR_NAME}-shard-0"
log "Waiting for StatefulSet $STS_NAME to have ReadyReplicas >= 1"
end=$(( $(date +%s) + 300 ))
while [[ $(date +%s) -lt $end ]]; do
    ready=$(kubectl -n "$NS" get sts "$STS_NAME" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
    if [[ "${ready:-0}" -ge 1 ]]; then
        break
    fi
    sleep 5
done
if [[ "${ready:-0}" -lt 1 ]]; then
    log "ERROR: StatefulSet did not become Ready in 5 minutes"
    kubectl -n "$NS" describe sts "$STS_NAME" || true
    kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME}" -o wide || true
    kubectl -n "$NS" logs "${STS_NAME}-0" -c postgres --tail=200 || true
    exit 1
fi

# 6. psql round-trip
POD="${STS_NAME}-0"
log "Running psql round-trip in $POD"
out=""
end=$(( $(date +%s) + 120 ))
while [[ $(date +%s) -lt $end ]]; do
    out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT 1' 2>&1 || true)
    if [[ "$out" == "1" ]]; then
        break
    fi
    sleep 2
done
if [[ "$out" != "1" ]]; then
    log "ERROR: psql round-trip failed: $out"
    exit 1
fi

log "SUCCESS — quickstart cluster Ready, psql SELECT 1 = 1"
log "Cluster status:"
kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | tail -40

# 7. Declarative hibernation smoke (선택 실행)
if [[ "${SMOKE_HIBERNATION:-0}" == "1" ]]; then
    log "[7/15] Declarative hibernation smoke (cnpg.io/hibernation=on/off)"
    HIBERNATION_MARKER="smoke-$(date +%s)"
    kubectl -n "$NS" exec "$POD" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres \
        -v ON_ERROR_STOP=1 \
        -c 'CREATE TABLE IF NOT EXISTS smoke_hibernation(marker text primary key, created_at timestamptz default now())' \
        -c "INSERT INTO smoke_hibernation(marker) VALUES ('${HIBERNATION_MARKER}') ON CONFLICT DO NOTHING"

    kubectl -n "$NS" annotate postgrescluster "$CR_NAME" --overwrite cnpg.io/hibernation=on
    wait_for_sts_replicas "$NS" "$STS_NAME" 0 120
    wait_for_hibernation_condition "$NS" "$CR_NAME" True 120
    kubectl -n "$NS" get pvc "data-${STS_NAME}-0" >/dev/null
    if [[ -n "$(kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME},app.kubernetes.io/component=shard" -o name 2>/dev/null || true)" ]]; then
        log "ERROR: shard Pods still exist after hibernation"
        kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME},app.kubernetes.io/component=shard" -o wide || true
        exit 1
    fi
    log "  PASS: hibernated with StatefulSet replicas=0 and PVC retained"

    kubectl -n "$NS" annotate postgrescluster "$CR_NAME" --overwrite cnpg.io/hibernation=off
    wait_for_sts_replicas "$NS" "$STS_NAME" "$DESIRED_MEMBERS" 120
    wait_for_hibernation_condition "$NS" "$CR_NAME" False 120

    log "  waiting for StatefulSet $STS_NAME to become Ready after rehydration"
    end=$(( $(date +%s) + 300 ))
    while [[ $(date +%s) -lt $end ]]; do
        ready=$(kubectl -n "$NS" get sts "$STS_NAME" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
        if [[ "${ready:-0}" -ge 1 ]]; then
            break
        fi
        sleep 5
    done
    if [[ "${ready:-0}" -lt 1 ]]; then
        log "ERROR: StatefulSet did not become Ready after rehydration"
        kubectl -n "$NS" describe sts "$STS_NAME" || true
        kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME}" -o wide || true
        exit 1
    fi

    hibernation_out=""
    end=$(( $(date +%s) + 120 ))
    while [[ $(date +%s) -lt $end ]]; do
        hibernation_out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
            psql -h /var/run/postgresql -U postgres -d postgres -At \
            -c "SELECT count(*) FROM smoke_hibernation WHERE marker='${HIBERNATION_MARKER}'" 2>&1 || true)
        if [[ "$hibernation_out" == "1" ]]; then
            break
        fi
        sleep 2
    done
    if [[ "$hibernation_out" != "1" ]]; then
        log "ERROR: hibernation data preservation check failed: $hibernation_out"
        kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | tail -80 || true
        exit 1
    fi
    log "  PASS: rehydrated and preserved PVC data marker"
else
    log "[7/15] skip hibernation smoke — SMOKE_HIBERNATION=${SMOKE_HIBERNATION:-unset} (set SMOKE_HIBERNATION=1 to enable)"
fi

# 8. Pooler Service psql smoke (선택 실행)
if [[ "${SMOKE_POOLER:-0}" == "1" ]]; then
    POOLER_NAME="${CR_NAME}-rw"
    POOLER_AUTH_SECRET="${POOLER_NAME}-auth"
    POOLER_PASSWORD="${POOLER_PASSWORD:-pooler-smoke-password}"
    escaped_pooler_password="${POOLER_PASSWORD//\'/\'\'}"
    escaped_userlist_password="${POOLER_PASSWORD//\"/\"\"}"

    log "[8/15] Pooler Service psql smoke (Pooler=$POOLER_NAME)"
    kubectl -n "$NS" exec "$POD" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres \
        -v ON_ERROR_STOP=1 -c "ALTER USER postgres PASSWORD '${escaped_pooler_password}'"
    kubectl -n "$NS" create secret generic "$POOLER_AUTH_SECRET" \
        --from-literal=userlist.txt="\"postgres\" \"${escaped_userlist_password}\"" \
        --dry-run=client -o yaml | kubectl apply -f -
    kubectl apply -f - <<EOF
apiVersion: postgres.keiailab.io/v1alpha1
kind: Pooler
metadata:
  name: ${POOLER_NAME}
  namespace: ${NS}
spec:
  cluster:
    name: ${CR_NAME}
  instances: 2
  type: rw
  pgbouncer:
    image: ${PGBOUNCER_IMG}
    poolMode: transaction
    authSecretRef:
      name: ${POOLER_AUTH_SECRET}
    parameters:
      max_client_conn: "100"
      default_pool_size: "10"
EOF
    wait_for_deployment_available "$NS" "${POOLER_NAME}-pooler" 180 || {
        log "ERROR: Pooler Deployment did not become Available"
        kubectl -n "$NS" describe deploy "${POOLER_NAME}-pooler" || true
        kubectl -n "$NS" get pods -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -o wide || true
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    }
    pooler_out=""
    pooler_error=""
    end=$(( $(date +%s) + 120 ))
    while [[ $(date +%s) -lt $end ]]; do
        if pooler_out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
            env PGPASSWORD="$POOLER_PASSWORD" psql -h "${POOLER_NAME}-pooler" -p 5432 -U postgres -d postgres -At -c 'SELECT 1' 2>&1); then
            pooler_error=""
            if [[ "$pooler_out" == "1" ]]; then
                break
            fi
        else
            pooler_error="$pooler_out"
        fi
        sleep 3
    done
    if [[ "$pooler_out" != "1" ]]; then
        log "ERROR: Pooler psql round-trip failed: ${pooler_error:-$pooler_out}"
        kubectl -n "$NS" get pooler "$POOLER_NAME" -o yaml || true
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    fi
    log "  PASS: Pooler Service SELECT 1 = 1"

    log "  Applying Pooler PAUSE"
    kubectl -n "$NS" patch pooler "$POOLER_NAME" --type=merge -p '{"spec":{"paused":true}}'
    wait_for_pooler_paused "$NS" "$POOLER_NAME" true 120
    paused_out=""
    set +e
    paused_out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        timeout 5 env PGPASSWORD="$POOLER_PASSWORD" psql -h "${POOLER_NAME}-pooler" -p 5432 -U postgres -d postgres -At -c 'SELECT 1' 2>&1)
    paused_status=$?
    set -e
    if [[ "$paused_status" -ne 124 ]]; then
        log "ERROR: Pooler PAUSE did not block a new client as expected (exit=$paused_status): $paused_out"
        kubectl -n "$NS" get pooler "$POOLER_NAME" -o yaml || true
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    fi
    log "  PASS: Pooler PAUSE blocks new client until timeout"

    log "  Applying Pooler RESUME"
    kubectl -n "$NS" patch pooler "$POOLER_NAME" --type=merge -p '{"spec":{"paused":false}}'
    wait_for_pooler_paused "$NS" "$POOLER_NAME" false 120
    pooler_resume_out=""
    if ! pooler_resume_out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        env PGPASSWORD="$POOLER_PASSWORD" psql -h "${POOLER_NAME}-pooler" -p 5432 -U postgres -d postgres -At -c 'SELECT 1' 2>&1); then
        log "ERROR: Pooler RESUME psql round-trip failed: $pooler_resume_out"
        kubectl -n "$NS" get pooler "$POOLER_NAME" -o yaml || true
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    fi
    if [[ "$pooler_resume_out" != "1" ]]; then
        log "ERROR: Pooler RESUME returned unexpected psql output: $pooler_resume_out"
        exit 1
    fi
    log "  PASS: Pooler RESUME SELECT 1 = 1"

    log "  Applying Pooler config parameter patch"
    pooler_previous_hash=$(kubectl -n "$NS" get pooler "$POOLER_NAME" -o jsonpath='{.status.configHash}')
    pooler_previous_generation=$(kubectl -n "$NS" get deployment "${POOLER_NAME}-pooler" -o jsonpath='{.metadata.generation}')
    pooler_previous_pods=$(pooler_pod_names "$NS" "$POOLER_NAME")
    kubectl -n "$NS" patch pooler "$POOLER_NAME" --type=merge \
        -p '{"spec":{"pgbouncer":{"parameters":{"max_client_conn":"120","default_pool_size":"12"}}}}'
    wait_for_pooler_config_hash_change "$NS" "$POOLER_NAME" "$pooler_previous_hash" 180
    pooler_current_generation=$(kubectl -n "$NS" get deployment "${POOLER_NAME}-pooler" -o jsonpath='{.metadata.generation}')
    if [[ "$pooler_current_generation" != "$pooler_previous_generation" ]]; then
        log "ERROR: Pooler config patch triggered Deployment rollout generation $pooler_previous_generation -> $pooler_current_generation"
        kubectl -n "$NS" get deployment "${POOLER_NAME}-pooler" -o yaml || true
        exit 1
    fi
    pooler_current_pods=$(pooler_pod_names "$NS" "$POOLER_NAME")
    if [[ "$pooler_current_pods" != "$pooler_previous_pods" ]]; then
        log "ERROR: Pooler config patch replaced Pods instead of in-place reload"
        printf 'before:\n%s\nafter:\n%s\n' "$pooler_previous_pods" "$pooler_current_pods" >&2
        exit 1
    fi
    pooler_config=$(kubectl -n "$NS" get cm "${POOLER_NAME}-pooler-config" -o jsonpath='{.data.pgbouncer\.ini}')
    if ! grep -F 'default_pool_size = 12' <<<"$pooler_config" >/dev/null; then
        log "ERROR: Pooler ConfigMap missing updated default_pool_size:"
        printf '%s\n' "$pooler_config" >&2
        exit 1
    fi
    if ! grep -F 'max_client_conn = 120' <<<"$pooler_config" >/dev/null; then
        log "ERROR: Pooler ConfigMap missing updated max_client_conn:"
        printf '%s\n' "$pooler_config" >&2
        exit 1
    fi
    pooler_config_out=""
    if ! pooler_config_out=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        env PGPASSWORD="$POOLER_PASSWORD" psql -h "${POOLER_NAME}-pooler" -p 5432 -U postgres -d postgres -At -c 'SELECT 1' 2>&1); then
        log "ERROR: Pooler config reload psql round-trip failed: $pooler_config_out"
        kubectl -n "$NS" get pooler "$POOLER_NAME" -o yaml || true
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    fi
    if [[ "$pooler_config_out" != "1" ]]; then
        log "ERROR: Pooler config reload returned unexpected psql output: $pooler_config_out"
        exit 1
    fi
    if ! kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --since=3m | grep -E 'SIGHUP|reload' >/dev/null; then
        log "ERROR: Pooler config reload did not leave SIGHUP/reload evidence in PgBouncer logs"
        kubectl -n "$NS" logs -l "postgres.keiailab.io/pooler=${POOLER_NAME}" -c pgbouncer --tail=200 || true
        exit 1
    fi
    log "  PASS: Pooler config hash changed, in-place reload completed, Pods unchanged, SELECT 1 = 1"
else
    log "[8/15] skip Pooler Service psql smoke — SMOKE_POOLER=${SMOKE_POOLER:-unset} (set SMOKE_POOLER=1 to enable)"
fi

# 9. PostgresDatabase declarative smoke (T22 / T27 — psql reconcile 검증)
#    PostgresDatabase CR 적용 → status.applied=true → pg_database 존재 확인.
#    databaseReclaimPolicy=delete 로 CR 삭제 시 DROP DATABASE 자동 처리도 검증.
if [[ "${SMOKE_DATABASE:-0}" == "1" ]]; then
    log "[9/15] PostgresDatabase declarative smoke (psql reconcile)"
    DB_TS="$(date +%s)"
    DB_NAME="smoke-db-${DB_TS}"        # K8s metadata.name 은 RFC 1123 subdomain.
    PG_DB_NAME="smoke_db_${DB_TS}"      # PostgreSQL identifier 는 underscore 허용.
    cat <<DBSPEC | kubectl -n "$NS" apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresDatabase
metadata:
  name: ${DB_NAME}
  namespace: ${NS}
spec:
  cluster:
    name: ${CR_NAME}
  name: ${PG_DB_NAME}
  ensure: present
  databaseReclaimPolicy: delete
DBSPEC

    db_applied=""
    end=$(( $(date +%s) + 120 ))
    while [[ $(date +%s) -lt $end ]]; do
        db_applied=$(kubectl -n "$NS" get postgresdatabase "$DB_NAME" -o jsonpath='{.status.applied}' 2>/dev/null || echo "")
        if [[ "$db_applied" == "true" ]]; then
            break
        fi
        sleep 3
    done
    if [[ "$db_applied" != "true" ]]; then
        log "ERROR: PostgresDatabase status.applied != true (got=${db_applied})"
        kubectl -n "$NS" get postgresdatabase "$DB_NAME" -o yaml | tail -40 || true
        exit 1
    fi

    db_exists=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        psql -h /var/run/postgresql -U postgres -d postgres -At \
        -c "SELECT count(*) FROM pg_database WHERE datname='${PG_DB_NAME}'" 2>&1 || echo "")
    if [[ "$db_exists" != "1" ]]; then
        log "ERROR: PostgresDatabase reconciler did not CREATE DATABASE: pg_database count=${db_exists}"
        exit 1
    fi
    log "  PASS: PostgresDatabase CR applied → status.applied=true, pg_database 존재 검증"

    kubectl -n "$NS" delete postgresdatabase "$DB_NAME" --wait=true --timeout=60s >/dev/null
    db_dropped=""
    end=$(( $(date +%s) + 60 ))
    while [[ $(date +%s) -lt $end ]]; do
        db_dropped=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
            psql -h /var/run/postgresql -U postgres -d postgres -At \
            -c "SELECT count(*) FROM pg_database WHERE datname='${PG_DB_NAME}'" 2>/dev/null || echo "")
        if [[ "$db_dropped" == "0" ]]; then
            break
        fi
        sleep 2
    done
    if [[ "$db_dropped" != "0" ]]; then
        log "ERROR: PostgresDatabase reclaim=delete did not DROP DATABASE: pg_database count=${db_dropped}"
        exit 1
    fi
    log "  PASS: PostgresDatabase reclaim=delete finalizer DROP DATABASE 검증"
else
    log "[9/15] skip PostgresDatabase smoke — SMOKE_DATABASE=${SMOKE_DATABASE:-unset} (set SMOKE_DATABASE=1 to enable)"
fi

# 10. PostgresUser declarative smoke (T22 / T27 — role/membership psql reconcile 검증)
#    PostgresUser CR 적용 → status.applied=true → pg_roles 조회 → CR 삭제 → role 제거 검증.
if [[ "${SMOKE_USER:-0}" == "1" ]]; then
    log "[10/15] PostgresUser declarative smoke (psql reconcile)"
    USER_TS="$(date +%s)"
    USER_NAME="smoke-user-${USER_TS}"
    PG_USER_NAME="smoke_user_${USER_TS}"
    cat <<USERSPEC | kubectl -n "$NS" apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresUser
metadata:
  name: ${USER_NAME}
  namespace: ${NS}
spec:
  cluster:
    name: ${CR_NAME}
  name: ${PG_USER_NAME}
  ensure: present
  login: true
  createdb: false
  createrole: false
  replication: false
  bypassrls: false
  inherit: true
  connectionLimit: 10
  disablePassword: true
USERSPEC

    user_applied=""
    end=$(( $(date +%s) + 120 ))
    while [[ $(date +%s) -lt $end ]]; do
        user_applied=$(kubectl -n "$NS" get postgresuser "$USER_NAME" -o jsonpath='{.status.applied}' 2>/dev/null || echo "")
        if [[ "$user_applied" == "true" ]]; then
            break
        fi
        sleep 3
    done
    if [[ "$user_applied" != "true" ]]; then
        log "ERROR: PostgresUser status.applied != true (got=${user_applied})"
        kubectl -n "$NS" get postgresuser "$USER_NAME" -o yaml | tail -40 || true
        exit 1
    fi

    role_exists=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
        psql -h /var/run/postgresql -U postgres -d postgres -At \
        -c "SELECT count(*) FROM pg_roles WHERE rolname='${PG_USER_NAME}' AND rolcanlogin=true AND rolconnlimit=10" 2>&1 || echo "")
    if [[ "$role_exists" != "1" ]]; then
        log "ERROR: PostgresUser reconciler did not CREATE ROLE: pg_roles count=${role_exists}"
        exit 1
    fi
    log "  PASS: PostgresUser CR applied → status.applied=true, pg_roles 존재 검증 (login=true, connlimit=10)"

    kubectl -n "$NS" delete postgresuser "$USER_NAME" --wait=true --timeout=60s >/dev/null
    role_dropped=""
    end=$(( $(date +%s) + 60 ))
    while [[ $(date +%s) -lt $end ]]; do
        role_dropped=$(kubectl -n "$NS" exec "$POD" -c postgres -- \
            psql -h /var/run/postgresql -U postgres -d postgres -At \
            -c "SELECT count(*) FROM pg_roles WHERE rolname='${PG_USER_NAME}'" 2>/dev/null || echo "")
        if [[ "$role_dropped" == "0" ]]; then
            break
        fi
        sleep 2
    done
    if [[ "$role_dropped" != "0" ]]; then
        log "ERROR: PostgresUser CR deletion did not DROP ROLE: pg_roles count=${role_dropped}"
        exit 1
    fi
    log "  PASS: PostgresUser CR 삭제 → DROP ROLE 검증"
else
    log "[10/15] skip PostgresUser smoke — SMOKE_USER=${SMOKE_USER:-unset} (set SMOKE_USER=1 to enable)"
fi

# 11. ScheduledBackup declarative smoke (T27 ③ — cron → BackupJob 생성 검증)
#     immediate=true 로 첫 reconcile 에서 BackupJob 1 건이 생성됨을 확인한다.
#     실제 pgBackRest 실행은 외부 repo 환경이 없으면 Failed 로 끝나도 OK —
#     본 시나리오는 ScheduledBackup → BackupJob CR 생성 경로만 검증한다.
if [[ "${SMOKE_SCHEDULEDBACKUP:-0}" == "1" ]]; then
    log "[11/15] ScheduledBackup declarative smoke (cron → BackupJob 생성 검증)"
    SB_NAME="smoke-sb-$(date +%s)"
    cat <<SBSPEC | kubectl -n "$NS" apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: ScheduledBackup
metadata:
  name: ${SB_NAME}
  namespace: ${NS}
spec:
  schedule: "0 0 0 1 1 *"
  cluster:
    name: ${CR_NAME}
  tool: pgbackrest
  repo: repo1
  type: full
  immediate: true
  suspend: false
  concurrencyPolicy: Forbid
  backupOwnerReference: self
SBSPEC

    sb_last=""
    end=$(( $(date +%s) + 120 ))
    while [[ $(date +%s) -lt $end ]]; do
        sb_last=$(kubectl -n "$NS" get scheduledbackup "$SB_NAME" -o jsonpath='{.status.lastBackupJobName}' 2>/dev/null || echo "")
        if [[ -n "$sb_last" ]]; then
            break
        fi
        sleep 3
    done
    if [[ -z "$sb_last" ]]; then
        log "ERROR: ScheduledBackup status.lastBackupJobName 가 생성되지 않음"
        kubectl -n "$NS" get scheduledbackup "$SB_NAME" -o yaml | tail -40 || true
        exit 1
    fi

    if ! kubectl -n "$NS" get backupjob "$sb_last" >/dev/null 2>&1; then
        log "ERROR: ScheduledBackup 가 보고한 BackupJob/${sb_last} 가 실제로는 존재하지 않음"
        exit 1
    fi
    log "  PASS: ScheduledBackup immediate=true → BackupJob/${sb_last} 생성 검증"

    kubectl -n "$NS" delete scheduledbackup "$SB_NAME" --wait=true --timeout=30s >/dev/null
    log "  PASS: ScheduledBackup CR 삭제 — backupOwnerReference=self 가 BackupJob 의 ownerReference 정리를 위임"
else
    log "[11/15] skip ScheduledBackup smoke — SMOKE_SCHEDULEDBACKUP=${SMOKE_SCHEDULEDBACKUP:-unset} (set SMOKE_SCHEDULEDBACKUP=1 to enable)"
fi

# 12. ImageCatalog / ClusterImageCatalog declarative smoke (T27 ④ — schema + lookup 검증)
#     ImageCatalog (namespace-scoped) 와 ClusterImageCatalog (cluster-scoped) 양 CR
#     이 schema valid 하게 apply 되는지 + kubectl get 으로 회수 가능한지 확인한다.
#     실제 PostgresCluster.spec.imageCatalogRef 회로 검증은 별도 e2e 시나리오.
if [[ "${SMOKE_IMAGECATALOG:-0}" == "1" ]]; then
    log "[12/15] ImageCatalog/ClusterImageCatalog schema + lookup smoke"
    IC_NAME="smoke-ic-$(date +%s)"
    CIC_NAME="smoke-cic-$(date +%s)"

    cat <<ICSPEC | kubectl -n "$NS" apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: ImageCatalog
metadata:
  name: ${IC_NAME}
  namespace: ${NS}
spec:
  images:
    - major: 18
      image: ghcr.io/keiailab/pg:18
ICSPEC

    cat <<CICSPEC | kubectl apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: ClusterImageCatalog
metadata:
  name: ${CIC_NAME}
spec:
  images:
    - major: 17
      image: ghcr.io/keiailab/pg:17
    - major: 18
      image: ghcr.io/keiailab/pg:18
CICSPEC

    ic_count=$(kubectl -n "$NS" get imagecatalog "$IC_NAME" -o jsonpath='{.spec.images[*].major}' 2>&1 | wc -w | tr -d ' ')
    if [[ "$ic_count" -lt 1 ]]; then
        log "ERROR: ImageCatalog/${IC_NAME} apply did not surface images[*].major"
        kubectl -n "$NS" get imagecatalog "$IC_NAME" -o yaml | tail -20 || true
        exit 1
    fi

    cic_count=$(kubectl get clusterimagecatalog "$CIC_NAME" -o jsonpath='{.spec.images[*].major}' 2>&1 | wc -w | tr -d ' ')
    if [[ "$cic_count" -lt 2 ]]; then
        log "ERROR: ClusterImageCatalog/${CIC_NAME} apply did not surface 2 major versions (got ${cic_count})"
        kubectl get clusterimagecatalog "$CIC_NAME" -o yaml | tail -20 || true
        exit 1
    fi

    log "  PASS: ImageCatalog (1 major) + ClusterImageCatalog (2 majors) schema valid + lookup 검증"

    kubectl -n "$NS" delete imagecatalog "$IC_NAME" --wait=true --timeout=30s >/dev/null
    kubectl delete clusterimagecatalog "$CIC_NAME" --wait=true --timeout=30s >/dev/null
    log "  PASS: ImageCatalog / ClusterImageCatalog cleanup 완료"
else
    log "[12/15] skip ImageCatalog smoke — SMOKE_IMAGECATALOG=${SMOKE_IMAGECATALOG:-unset} (set SMOKE_IMAGECATALOG=1 to enable)"
fi

# 13. WAL lag 측정 (F02 100% 게이트, ADR-0056 Phase A1)
#    standby 가 *진짜로 replay* 하는지 + 부하 대비 lag 측정.
#    REPLICAS=1 일 때 standby 부재 → 측정 skip.
log "[13/15] WAL replication lag measurement"
REPLICAS=$(kubectl -n "$NS" get sts "$STS_NAME" -o jsonpath='{.spec.replicas}')
if [[ "${REPLICAS:-1}" -ge 2 ]]; then
    # primary 에서 pgbench init + 부하 (10 client × 100 txn)
    log "  pgbench init + 부하 (10 client × 100 txn)"
    kubectl -n "$NS" exec "$POD" -c postgres -- bash -c \
        "pgbench -h /var/run/postgresql -U postgres -i -s 1 postgres 2>&1 | tail -3" || true
    kubectl -n "$NS" exec "$POD" -c postgres -- bash -c \
        "pgbench -h /var/run/postgresql -U postgres -c 10 -t 100 postgres 2>&1 | tail -2" || true
    # primary 의 pg_stat_replication 으로 standby 의 replay_lag 조회
    log "  pg_stat_replication.replay_lag (target: < 1s)"
    wal_lag=""
    wal_error=""
    end=$(( $(date +%s) + 60 ))
    while [[ $(date +%s) -lt $end ]]; do
        if wal_lag=$(kubectl -n "$NS" exec "$POD" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At \
            -c "SELECT application_name, state, write_lag, flush_lag, replay_lag FROM pg_stat_replication WHERE state = 'streaming';" 2>&1); then
            wal_error=""
            if [[ -n "${wal_lag//$'\n'/}" ]]; then
                break
            fi
        else
            wal_error="$wal_lag"
        fi
        sleep 2
    done
    if [[ -n "$wal_lag" ]]; then
        printf '%s\n' "$wal_lag" >&2
    fi
    if [[ -z "${wal_lag//$'\n'/}" ]]; then
        log "ERROR: streaming standby was not observed in pg_stat_replication within 60s"
        [[ -n "$wal_error" ]] && log "last psql error: $wal_error"
        kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME}" -o wide || true
        exit 1
    fi
else
    log "  skip — REPLICAS=$REPLICAS (standby 부재)"
fi

# 10. promote / demote RTO 측정 (F02 100% 게이트 추가, ADR-0056 Phase A2-A4 prerequisite)
#    primary kill → standby 가 새 primary 로 promote 되는 시간. RTO 목표 < 30s.
#    SMOKE_FAILOVER=1 환경변수 설정 시에만 실행 (default skip — 데이터 plane 변경 영향).
if [[ "${REPLICAS:-1}" -ge 2 ]] && [[ "${SMOKE_FAILOVER:-0}" == "1" ]]; then
    log "[14/15] Failover RTO measurement (SMOKE_FAILOVER=1)"
    KILL_TS=$(date +%s)
    kubectl -n "$NS" delete pod "$POD" --wait=false || true
    log "  primary killed at $(format_utc_ts "$KILL_TS") — waiting for new primary"
    # 다른 pod (-1) 에서 새 primary 도달 대기 (max 60s)
    end=$(( KILL_TS + 60 ))
    failover_done=0
    while [[ $(date +%s) -lt $end ]]; do
        new_primary=$(kubectl -n "$NS" exec "${STS_NAME}-1" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_is_in_recovery();' 2>/dev/null || echo "")
        if [[ "$new_primary" == "f" ]]; then
            RECOVER_TS=$(date +%s)
            RTO=$(( RECOVER_TS - KILL_TS ))
            log "  RTO = ${RTO}s (target < 30s)"
            if [[ "$RTO" -le 30 ]]; then
                log "  PASS: RTO < 30s"
                failover_done=1
            else
                log "ERROR: RTO > 30s"
                exit 1
            fi
            break
        fi
        sleep 2
    done
    if [[ "$failover_done" != "1" ]]; then
        log "ERROR: standby did not promote within 60s"
        kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | tail -60 || true
        kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${CR_NAME}" -o wide || true
        kubectl -n "$NS" logs "${STS_NAME}-1" -c postgres --tail=200 || true
        exit 1
    fi
    log "  waiting for CR status primary=${STS_NAME}-1"
    end=$(( $(date +%s) + 60 ))
    status_primary=""
    while [[ $(date +%s) -lt $end ]]; do
        status_primary=$(kubectl -n "$NS" get postgrescluster "$CR_NAME" -o jsonpath='{.status.shards[0].primary.pod}' 2>/dev/null || echo "")
        if [[ "$status_primary" == "${STS_NAME}-1" ]]; then
            break
        fi
        sleep 2
    done
    if [[ "$status_primary" != "${STS_NAME}-1" ]]; then
        log "ERROR: CR status primary=$status_primary, want ${STS_NAME}-1"
        kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | tail -80 || true
        exit 1
    fi
    old_primary_recovery=$(kubectl -n "$NS" exec "${STS_NAME}-0" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT pg_is_in_recovery();' 2>/dev/null || echo "")
    if [[ "$old_primary_recovery" != "t" ]]; then
        log "ERROR: restarted old primary recovery=$old_primary_recovery, want t"
        kubectl -n "$NS" logs "${STS_NAME}-0" -c postgres --tail=200 || true
        exit 1
    fi
    log "  PASS: CR status reflects ${STS_NAME}-1 and restarted old primary is standby"
else
    log "[14/15] skip failover RTO — SMOKE_FAILOVER=${SMOKE_FAILOVER:-unset} (set SMOKE_FAILOVER=1 to enable)"
fi
