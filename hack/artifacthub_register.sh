#!/usr/bin/env bash
set -euo pipefail

artifacthub_api_url="${ARTIFACTHUB_API_URL:-https://artifacthub.io/api/v1}"
artifacthub_org="${ARTIFACTHUB_ORG:-keiailab}"
artifacthub_repository_name="${ARTIFACTHUB_REPOSITORY_NAME:-keiailab}"
artifacthub_package_name="${ARTIFACTHUB_PACKAGE_NAME:-postgres-operator}"
helm_oci_repo="${HELM_OCI_REPO:-oci://ghcr.io/keiailab/charts}"
helm_repo_url="${HELM_REPO_URL:-https://keiailab.github.io/charts}"

curl_bin="${CURL_BIN:-curl}"
jq_bin="${JQ_BIN:-jq}"

api_key_id="${ARTIFACTHUB_API_KEY_ID:-}"
api_key_secret="${ARTIFACTHUB_API_KEY_SECRET:-}"

if [[ -z "$api_key_id" || -z "$api_key_secret" ]]; then
	echo "ERROR: ARTIFACTHUB_API_KEY_ID and ARTIFACTHUB_API_KEY_SECRET are required." >&2
	exit 1
fi

if ! command -v "$jq_bin" >/dev/null 2>&1; then
	echo "ERROR: required tool not found: $jq_bin" >&2
	exit 1
fi

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/postgres-operator-artifacthub-register.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

json_body="$("$jq_bin" -n \
	--arg name "$artifacthub_repository_name" \
	--arg displayName "Keiailab Helm Catalog" \
	--arg url "${helm_repo_url%/}" \
	'{kind: 0, name: $name, display_name: $displayName, url: $url}')"

echo "=== Artifact Hub repository add ==="
"$curl_bin" -fsSL \
	-X POST "${artifacthub_api_url%/}/repositories/org/${artifacthub_org}" \
	-H "Content-Type: application/json" \
	-H "X-API-KEY-ID: ${api_key_id}" \
	-H "X-API-KEY-SECRET: ${api_key_secret}" \
	-d "$json_body" \
	-o "$tmpdir/created.json"

echo "Artifact Hub repository registration request accepted: ${artifacthub_repository_name}"
echo "Verify with: make artifacthub-smoke"
