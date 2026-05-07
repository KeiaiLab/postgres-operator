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
#
# 흐름:
#   1. kind cluster 생성 (이미 있으면 재사용)
#   2. operator + PG image 로컬 빌드 후 kind 에 load
#   3. CRD + operator 설치 (kustomize 또는 helm)
#   4. quickstart sample apply
#   5. Pod Ready 대기 (5분 timeout)
#   6. psql round-trip 검증 (`kubectl exec ... -- psql -c 'SELECT 1'`)
#   7. replicas>=1 이면 streaming standby 를 pg_stat_replication 으로 확인
#   8. SMOKE_FAILOVER=1 이면 primary Pod 삭제 후 standby promote RTO 측정
#   9. cleanup (--keep 미지정 시 cluster 삭제)

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
SHARD_REPLICAS="${SHARD_REPLICAS:-${POSTGRES_REPLICAS:-${REPLICAS:-0}}}"
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
log "Loading images into kind"
kind load docker-image "$OPERATOR_IMG" --name "$CLUSTER_NAME"
kind load docker-image "$PG_IMG" --name "$CLUSTER_NAME"

# 3. CRD + operator 설치 (kustomize 결과 dist/install.yaml 사용)
log "Generating dist/install.yaml + applying"
make build-installer >/dev/null
# operator image override — local kind 에서는 IfNotPresent 로 로딩 이미지 사용.
kubectl apply -f dist/install.yaml

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
out=$(kubectl -n "$NS" exec "$POD" -c postgres -- psql -h /var/run/postgresql -U postgres -d postgres -At -c 'SELECT 1' 2>&1 || true)
if [[ "$out" != "1" ]]; then
    log "ERROR: psql round-trip failed: $out"
    exit 1
fi

log "SUCCESS — quickstart cluster Ready, psql SELECT 1 = 1"
log "Cluster status:"
kubectl -n "$NS" get postgrescluster "$CR_NAME" -o yaml | tail -40

# 7. WAL lag 측정 (F02 100% 게이트, ADR-0056 Phase A1)
#    standby 가 *진짜로 replay* 하는지 + 부하 대비 lag 측정.
#    REPLICAS=1 일 때 standby 부재 → 측정 skip.
log "[7/8] WAL replication lag measurement"
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

# 8. promote / demote RTO 측정 (F02 100% 게이트 추가, ADR-0056 Phase A2-A4 prerequisite)
#    primary kill → standby 가 새 primary 로 promote 되는 시간. RTO 목표 < 30s.
#    SMOKE_FAILOVER=1 환경변수 설정 시에만 실행 (default skip — 데이터 plane 변경 영향).
if [[ "${REPLICAS:-1}" -ge 2 ]] && [[ "${SMOKE_FAILOVER:-0}" == "1" ]]; then
    log "[8/8] Failover RTO measurement (SMOKE_FAILOVER=1)"
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
    log "[8/8] skip failover RTO — SMOKE_FAILOVER=${SMOKE_FAILOVER:-unset} (set SMOKE_FAILOVER=1 to enable)"
fi
