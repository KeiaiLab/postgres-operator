#!/usr/bin/env bash
set -euo pipefail

artifacthub_api_url="${ARTIFACTHUB_API_URL:-https://artifacthub.io/api/v1}"
artifacthub_org="${ARTIFACTHUB_ORG:-keiailab}"
artifacthub_package_name="${ARTIFACTHUB_PACKAGE_NAME:-postgres-operator}"
artifacthub_repository_name="${ARTIFACTHUB_REPOSITORY_NAME:-keiailab-postgres-operator}"
helm_repo_url="${HELM_REPO_URL:-https://keiailab.github.io/postgres-operator}"

curl_bin="${CURL_BIN:-curl}"
helm_bin="${HELM_BIN:-helm}"
jq_bin="${JQ_BIN:-jq}"

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/postgres-operator-artifacthub.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

normalize_url() {
	local url="$1"
	url="${url%/}"
	printf '%s\n' "$url"
}

require_tool() {
	local tool="$1"
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "ERROR: required tool not found: $tool" >&2
		exit 1
	fi
}

urlencode() {
	local value="$1"
	VALUE="$value" python3 - <<'PY'
import os
import urllib.parse

print(urllib.parse.quote(os.environ["VALUE"], safe=""))
PY
}

fetch_json() {
	local url="$1"
	local out="$2"
	"$curl_bin" -fsSL "$url" -o "$out"
}

echo "=== Helm repository reachability ==="
"$curl_bin" -fsSL "${helm_repo_url%/}/index.yaml" -o "$tmpdir/index.yaml"
"$curl_bin" -fsSL "${helm_repo_url%/}/artifacthub-repo.yml" -o "$tmpdir/artifacthub-repo.yml"
grep -q '^repositoryID:' "$tmpdir/artifacthub-repo.yml"

echo "Helm repository OK: ${helm_repo_url%/}"

if command -v "$helm_bin" >/dev/null 2>&1; then
	"$helm_bin" repo add "$artifacthub_repository_name" "$helm_repo_url" >/dev/null 2>&1 || true
	"$helm_bin" repo update "$artifacthub_repository_name" >/dev/null
	"$helm_bin" search repo "${artifacthub_repository_name}/${artifacthub_package_name}" --versions --devel \
		| grep -q "${artifacthub_repository_name}/${artifacthub_package_name}"
	echo "Helm index package OK: ${artifacthub_repository_name}/${artifacthub_package_name}"
else
	echo "WARN: helm not found; local Helm index search skipped" >&2
fi

require_tool "$jq_bin"

echo "=== Artifact Hub repository registration ==="
org_query="$(urlencode "$artifacthub_org")"
fetch_json "${artifacthub_api_url%/}/repositories/search?org=${org_query}&kind=0&limit=60" "$tmpdir/repositories.json"

normalized_helm_url="$(normalize_url "$helm_repo_url")"
repo_filter='
	.[]?
	| select((.url // "" | sub("/$"; "")) == $url or .name == $name)
'
repo_json="$("$jq_bin" -e -c --arg url "$normalized_helm_url" --arg name "$artifacthub_repository_name" "$repo_filter" "$tmpdir/repositories.json" 2>/dev/null || true)"

if [[ -z "$repo_json" ]]; then
	echo "ERROR: Artifact Hub repository is not registered." >&2
	echo "  org: ${artifacthub_org}" >&2
	echo "  expected name: ${artifacthub_repository_name}" >&2
	echo "  expected url: ${normalized_helm_url}" >&2
	echo "  fix: make artifacthub-register ARTIFACTHUB_API_KEY_ID=... ARTIFACTHUB_API_KEY_SECRET=..." >&2
	exit 2
fi

repo_id="$("$jq_bin" -r '.repository_id' <<<"$repo_json")"
tracking_errors="$("$jq_bin" -r '.last_tracking_errors // empty' <<<"$repo_json")"
echo "Artifact Hub repository OK: ${repo_id}"

if [[ -n "$tracking_errors" ]]; then
	echo "ERROR: Artifact Hub repository tracking errors:" >&2
	echo "$tracking_errors" >&2
	exit 3
fi

echo "=== Artifact Hub package registration ==="
package_url="${artifacthub_api_url%/}/packages/helm/${artifacthub_repository_name}/${artifacthub_package_name}"
if ! "$curl_bin" -fsSL "$package_url" -o "$tmpdir/package.json"; then
	echo "ERROR: Artifact Hub repository exists but package is not indexed yet." >&2
	echo "  package API: $package_url" >&2
	echo "  retry after Artifact Hub tracker runs, or push a new chart version to force reprocessing." >&2
	exit 4
fi

"$jq_bin" -e --arg name "$artifacthub_package_name" '.name == $name' "$tmpdir/package.json" >/dev/null
echo "Artifact Hub package OK: https://artifacthub.io/packages/helm/${artifacthub_repository_name}/${artifacthub_package_name}"

echo "=== Provenance (.prov) 도달성 ==="
# VERSION: Chart.yaml에서 추출 (TAG 환경변수가 없을 때 fallback)
VERSION="${TAG:-}"
if [[ -z "$VERSION" ]]; then
	chart_yaml="$(dirname "$0")/../charts/${artifacthub_package_name}/Chart.yaml"
	if [[ -f "$chart_yaml" ]]; then
		VERSION="$(grep '^version:' "$chart_yaml" | awk '{print $2}' | tr -d '"')"
	fi
fi

verify_provenance() {
	local prov="${helm_repo_url%/}/${artifacthub_package_name}-${VERSION}.tgz.prov"
	echo "→ provenance 확인: ${prov}"
	if "$curl_bin" -fsSL -o "$tmpdir/chart.tgz.prov" "${prov}" 2>/dev/null; then
		echo "✓ .prov 도달 가능 (Signed badge 전제 충족)"
	else
		echo "::warning::.prov 부재 — Signed badge 미달성(로컬 helm-publish.sh --sign 필요). warn-only, 통과."
	fi
}

if [[ -n "$VERSION" ]]; then
	verify_provenance
else
	echo "WARN: VERSION 미확인 — .prov 검증 건너뜀" >&2
fi
