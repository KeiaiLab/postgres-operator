#!/usr/bin/env bash
# T29 stage 3 — cert-manager kind drill for Pooler autoTLS.
#
# This script runs *after* hack/smoke.sh has stood up a kind cluster
# (with --keep), installs cert-manager into that cluster, creates a
# self-signed Issuer, applies the Pooler autoTLS sample, and verifies
# that the operator emits a cert-manager Certificate CR and that the
# Pooler Deployment mounts the issued Secret.
#
# Prerequisites:
#   - kind cluster `postgres-operator-smoke` exists (from `./hack/smoke.sh --keep`).
#   - The operator manager is already deployed and the dev PostgresCluster
#     is Ready (`hack/smoke.sh` ran the quickstart sample).
#
# Usage:
#   ./hack/smoke-cert-manager.sh
#   ./hack/smoke-cert-manager.sh --keep     # do not uninstall cert-manager at the end
#
# Idempotent: safe to re-run.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-postgres-operator-smoke}"
NS="${NS:-default}"
CR_NAME="${CR_NAME:-quickpg18}"
KEEP=0
if [[ "${1:-}" == "--keep" ]]; then
    KEEP=1
fi

CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.17.4}"

log() { echo "[cert-manager-smoke] $*"; }

# kubectl context check.
if ! kubectl config current-context | grep -q "kind-${CLUSTER_NAME}"; then
    log "ERROR: kubectl context does not point to kind-${CLUSTER_NAME}"
    log "Run \`kubectl config use-context kind-${CLUSTER_NAME}\` first"
    exit 1
fi

log "[1/6] Installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply --server-side -f \
    "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

log "[2/6] Waiting for cert-manager webhooks"
kubectl -n cert-manager rollout status deployment/cert-manager --timeout=180s
kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=180s
kubectl -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=180s

log "[3/6] Creating self-signed Issuer in namespace=${NS}"
cat <<ISSUER | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ca-issuer
  namespace: ${NS}
spec:
  selfSigned: {}
ISSUER

log "[4/6] Applying Pooler autoTLS sample"
# The sample assumes cluster.name=quickstart. Patch it to use ${CR_NAME}
# inline so we don't depend on smoke.sh's CR name.
cat <<POOLER | kubectl -n "$NS" apply -f -
apiVersion: postgres.keiailab.io/v1alpha1
kind: Pooler
metadata:
  name: smoke-rw-autotls
  namespace: ${NS}
spec:
  cluster:
    name: ${CR_NAME}
  instances: 1
  type: rw
  pgbouncer:
    image: ghcr.io/cloudnative-pg/pgbouncer:1.24.1
    poolMode: session
    autoTLS:
      issuerRef:
        name: ca-issuer
        kind: Issuer
      clientEnabled: true
      serverEnabled: false
POOLER

log "[5/6] Waiting for the operator to emit the Certificate CR"
cert_observed=""
end=$(( $(date +%s) + 120 ))
while [[ $(date +%s) -lt $end ]]; do
    cert_observed=$(kubectl -n "$NS" get certificate \
        smoke-rw-autotls-client-tls -o jsonpath='{.metadata.name}' 2>/dev/null || echo "")
    if [[ -n "$cert_observed" ]]; then
        break
    fi
    sleep 3
done
if [[ -z "$cert_observed" ]]; then
    log "ERROR: Certificate CR smoke-rw-autotls-client-tls was not created within 120s"
    kubectl -n "$NS" describe pooler smoke-rw-autotls | tail -40 || true
    exit 1
fi
log "  Certificate CR observed: ${cert_observed}"

log "  Waiting for cert-manager to mark Certificate Ready=True"
cert_ready=""
end=$(( $(date +%s) + 120 ))
while [[ $(date +%s) -lt $end ]]; do
    cert_ready=$(kubectl -n "$NS" get certificate smoke-rw-autotls-client-tls \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    if [[ "$cert_ready" == "True" ]]; then
        break
    fi
    sleep 3
done
if [[ "$cert_ready" != "True" ]]; then
    log "ERROR: Certificate.status.Ready != True (got=${cert_ready})"
    kubectl -n "$NS" describe certificate smoke-rw-autotls-client-tls | tail -30 || true
    exit 1
fi
log "  PASS: Certificate Ready=True"

log "  Waiting for issued Secret to exist"
secret_present=""
end=$(( $(date +%s) + 60 ))
while [[ $(date +%s) -lt $end ]]; do
    if kubectl -n "$NS" get secret smoke-rw-autotls-client-tls >/dev/null 2>&1; then
        secret_present="yes"
        break
    fi
    sleep 2
done
if [[ "$secret_present" != "yes" ]]; then
    log "ERROR: Secret smoke-rw-autotls-client-tls was never created"
    exit 1
fi
log "  PASS: Secret smoke-rw-autotls-client-tls is present"

log "[6/6] Verifying Pooler Deployment mounts the issued Secret"
mount_ok=""
end=$(( $(date +%s) + 60 ))
while [[ $(date +%s) -lt $end ]]; do
    mount_ok=$(kubectl -n "$NS" get deployment smoke-rw-autotls \
        -o jsonpath='{.spec.template.spec.volumes[?(@.secret.secretName=="smoke-rw-autotls-client-tls")].name}' 2>/dev/null || echo "")
    if [[ -n "$mount_ok" ]]; then
        break
    fi
    sleep 3
done
if [[ -z "$mount_ok" ]]; then
    log "ERROR: Pooler Deployment is not mounting the issued Secret"
    kubectl -n "$NS" get deployment smoke-rw-autotls -o yaml | tail -40 || true
    exit 1
fi
log "  PASS: Deployment volume \`${mount_ok}\` references smoke-rw-autotls-client-tls"

log "All cert-manager autoTLS drill steps passed."

if [[ "$KEEP" == "0" ]]; then
    log "Cleaning up — kubectl delete pooler smoke-rw-autotls + Issuer + cert-manager"
    kubectl -n "$NS" delete pooler smoke-rw-autotls --wait=true --timeout=60s || true
    kubectl -n "$NS" delete issuer ca-issuer || true
    kubectl delete -f \
        "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" || true
else
    log "cert-manager + Pooler smoke-rw-autotls retained (--keep)."
fi
