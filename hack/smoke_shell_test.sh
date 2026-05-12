#!/usr/bin/env bash
# hack/smoke.sh 의 kind image load fallback 을 실제 Docker 없이 검증한다.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

CALLS_FILE="$TMP_DIR/calls"
export SMOKE_TEST_CALLS="$CALLS_FILE"

cat >"$TMP_DIR/kind" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'kind %s\n' "$*" >>"$SMOKE_TEST_CALLS"
case "$*" in
    "load docker-image ghcr.io/test/image:tag --name unit")
        exit 1
        ;;
    "get nodes --name unit")
        printf 'unit-control-plane\n'
        ;;
    "delete cluster --name unit")
        ;;
    *)
        printf 'unexpected kind call: %s\n' "$*" >&2
        exit 64
        ;;
esac
STUB

cat >"$TMP_DIR/docker" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >>"$SMOKE_TEST_CALLS"
case "$*" in
    "save ghcr.io/test/image:tag")
        printf 'fake-image-tar'
        ;;
    "exec --privileged -i unit-control-plane ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs -")
        cat >/dev/null
        ;;
    *)
        printf 'unexpected docker call: %s\n' "$*" >&2
        exit 64
        ;;
esac
STUB

cat >"$TMP_DIR/kubectl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl %s\n' "$*" >>"$SMOKE_TEST_CALLS"
case "$*" in
    "-n default get sts demo-shard-0 -o jsonpath={.spec.replicas}")
        printf '0'
        ;;
    "-n default get postgrescluster demo -o jsonpath={.status.conditions[?(@.type==\"cnpg.io/hibernation\")].status}")
        printf 'True'
        ;;
    "-n default get pooler demo-rw -o jsonpath={.status.configHash}")
        printf 'new-hash'
        ;;
    "-n default get pods -l postgres.keiailab.io/pooler=demo-rw -o jsonpath={range .items[*]}{.metadata.annotations.postgres\\.keiailab\\.io/pgbouncer-config-sha256}{\"\\n\"}{end}")
        printf 'new-hash\nnew-hash\n'
        ;;
    *)
        printf 'unexpected kubectl call: %s\n' "$*" >&2
        exit 64
        ;;
esac
STUB

chmod +x "$TMP_DIR/kind" "$TMP_DIR/docker" "$TMP_DIR/kubectl"

(
    export PATH="$TMP_DIR:$PATH"
    export SMOKE_SOURCE_ONLY=1
    source "$ROOT_DIR/hack/smoke.sh"

    declare -F load_image_into_kind >/dev/null
    declare -F wait_for_sts_replicas >/dev/null
    declare -F wait_for_hibernation_condition >/dev/null
    declare -F wait_for_pooler_config_hash_change >/dev/null

    CLUSTER_NAME=unit load_image_into_kind ghcr.io/test/image:tag
    wait_for_sts_replicas default demo-shard-0 0 1
    wait_for_hibernation_condition default demo True 1
    wait_for_pooler_config_hash_change default demo-rw old-hash 1

    grep -F 'kind load docker-image ghcr.io/test/image:tag --name unit' "$CALLS_FILE" >/dev/null
    grep -F 'docker exec --privileged -i unit-control-plane ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs -' "$CALLS_FILE" >/dev/null
    grep -F 'kubectl -n default get sts demo-shard-0 -o jsonpath={.spec.replicas}' "$CALLS_FILE" >/dev/null
    grep -F 'kubectl -n default get postgrescluster demo -o jsonpath={.status.conditions[?(@.type=="cnpg.io/hibernation")].status}' "$CALLS_FILE" >/dev/null
    grep -F 'kubectl -n default get pooler demo-rw -o jsonpath={.status.configHash}' "$CALLS_FILE" >/dev/null
    grep -F 'kubectl -n default get pods -l postgres.keiailab.io/pooler=demo-rw -o jsonpath={range .items[*]}{.metadata.annotations.postgres\.keiailab\.io/pgbouncer-config-sha256}{"\n"}{end}' "$CALLS_FILE" >/dev/null
    if grep -F 'get deployment demo-rw-pooler -o jsonpath={.spec.template.metadata.annotations' "$CALLS_FILE" >/dev/null; then
        printf 'in-place reload 검증에서 Deployment template hash 를 기다리면 안 됩니다.\n' >&2
        exit 1
    fi
    if grep -F -- '--all-platforms' "$CALLS_FILE" >/dev/null; then
        printf 'fallback import 에 --all-platforms 가 포함되면 안 됩니다.\n' >&2
        exit 1
    fi
)

printf 'smoke shell test PASS\n'
