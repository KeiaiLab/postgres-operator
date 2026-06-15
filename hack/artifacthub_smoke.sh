#!/usr/bin/env bash
set -euo pipefail

artifacthub_api_url="${ARTIFACTHUB_API_URL:-https://artifacthub.io/api/v1}"
artifacthub_org="${ARTIFACTHUB_ORG:-keiailab}"
artifacthub_package_name="${ARTIFACTHUB_PACKAGE_NAME:-postgres-operator}"
artifacthub_repository_name="${ARTIFACTHUB_REPOSITORY_NAME:-keiailab-postgres-operator}"
artifacthub_repository_url="${EXPECTED_ARTIFACTHUB_REPOSITORY_URL:-${ARTIFACTHUB_REPOSITORY_URL:-oci://ghcr.io/keiailab/charts/postgres-operator}}"
helm_repo_url="${HELM_REPO_URL:-https://keiailab.github.io/postgres-operator}"
artifacthub_api_key_id="${AH_API_KEY_ID:-${ARTIFACTHUB_API_KEY_ID:-}}"
artifacthub_api_key_secret="${AH_API_KEY_SECRET:-${ARTIFACTHUB_API_KEY_SECRET:-}}"

curl_bin="${CURL_BIN:-curl}"
helm_bin="${HELM_BIN:-helm}"
jq_bin="${JQ_BIN:-jq}"
smoke_attempts="${ARTIFACTHUB_SMOKE_ATTEMPTS:-1}"
smoke_sleep_seconds="${ARTIFACTHUB_SMOKE_SLEEP_SECONDS:-30}"

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

chart_yaml="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/charts/${artifacthub_package_name}/Chart.yaml"
expected_chart_version="${EXPECTED_CHART_VERSION:-${TAG:-}}"
expected_chart_version="${expected_chart_version##refs/tags/}"
expected_chart_version="${expected_chart_version#v}"
if [[ -z "$expected_chart_version" && -f "$chart_yaml" ]]; then
	expected_chart_version="$(awk -F': *' '/^version:/ {gsub(/"/, "", $2); print $2; exit}' "$chart_yaml")"
fi

expected_app_version="${EXPECTED_APP_VERSION:-${APP_VERSION:-}}"
if [[ -z "$expected_app_version" && -f "$chart_yaml" ]]; then
	expected_app_version="$(awk -F': *' '/^appVersion:/ {gsub(/"/, "", $2); print $2; exit}' "$chart_yaml")"
fi

if [[ -z "$expected_chart_version" ]]; then
	echo "ERROR: expected chart version is unknown. Set EXPECTED_CHART_VERSION or TAG." >&2
	exit 1
fi
if [[ -z "$expected_app_version" ]]; then
	echo "ERROR: expected appVersion is unknown. Set EXPECTED_APP_VERSION or keep Chart.yaml available." >&2
	exit 1
fi

echo "=== Expected release contract ==="
echo "Chart version: ${expected_chart_version}"
echo "App version:   ${expected_app_version}"
echo "Artifact Hub repository URL: ${artifacthub_repository_url%/}"
echo "Smoke attempts: ${smoke_attempts} (sleep ${smoke_sleep_seconds}s)"

echo "=== Legacy Helm repository reachability (warning-only) ==="
if "$curl_bin" -fsSL "${helm_repo_url%/}/index.yaml" -o "$tmpdir/index.yaml" 2>/dev/null; then
	echo "Legacy Helm repository reachable: ${helm_repo_url%/}"
else
	echo "::warning::legacy Helm repository is unreachable; Artifact Hub tracks OCI, so this is not a gate."
fi
if command -v "$helm_bin" >/dev/null 2>&1; then
	"$helm_bin" repo add "$artifacthub_repository_name" "$helm_repo_url" >/dev/null 2>&1 || true
	"$helm_bin" repo update "$artifacthub_repository_name" >/dev/null
	if "$helm_bin" search repo "${artifacthub_repository_name}/${artifacthub_package_name}" --versions --devel \
		| grep -q "${artifacthub_repository_name}/${artifacthub_package_name}"; then
		echo "Legacy Helm index package visible: ${artifacthub_repository_name}/${artifacthub_package_name}"
	else
		echo "::warning::legacy Helm index package not visible; Artifact Hub tracks OCI, so this is not a gate."
	fi
else
	echo "WARN: helm not found; local Helm index search skipped" >&2
fi

require_tool "$curl_bin"
require_tool "$jq_bin"

echo "=== Artifact Hub repository registration ==="
org_query="$(urlencode "$artifacthub_org")"
fetch_json "${artifacthub_api_url%/}/repositories/search?org=${org_query}&kind=0&limit=60" "$tmpdir/repositories.json"

normalized_artifacthub_repository_url="$(normalize_url "$artifacthub_repository_url")"
repo_filter='
	.[]?
	| select(.name == $name and ((.url // "" | sub("/$"; "")) == $url))
'
repo_json="$("$jq_bin" -e -c --arg url "$normalized_artifacthub_repository_url" --arg name "$artifacthub_repository_name" "$repo_filter" "$tmpdir/repositories.json" 2>/dev/null || true)"

if [[ -z "$repo_json" ]]; then
	echo "ERROR: Artifact Hub repository is not registered." >&2
	echo "  org: ${artifacthub_org}" >&2
	echo "  expected name: ${artifacthub_repository_name}" >&2
	echo "  expected url: ${normalized_artifacthub_repository_url}" >&2
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

if [[ -n "$artifacthub_api_key_id" && -n "$artifacthub_api_key_secret" ]]; then
	repo_display_name="$("$jq_bin" -r '.display_name // "Keiailab Postgres Operator"' <<<"$repo_json")"
	repo_update_body="$("$jq_bin" -n \
		--arg name "$artifacthub_repository_name" \
		--arg display_name "$repo_display_name" \
		--arg url "$normalized_artifacthub_repository_url" \
		'{kind: 0, name: $name, display_name: $display_name, url: $url}')"
	if "$curl_bin" -fsSL \
		-X PUT "${artifacthub_api_url%/}/repositories/org/${artifacthub_org}/${artifacthub_repository_name}" \
		-H "Content-Type: application/json" \
		-H "X-API-KEY-ID: ${artifacthub_api_key_id}" \
		-H "X-API-KEY-SECRET: ${artifacthub_api_key_secret}" \
		-d "$repo_update_body" \
		-o /dev/null; then
		echo "Artifact Hub repository update accepted (tracker nudge)"
	else
		echo "::warning::Artifact Hub repository update nudge failed; continuing with passive tracker retry."
	fi
fi

echo "=== GHCR OCI chart availability ==="
if command -v "$helm_bin" >/dev/null 2>&1; then
	oci_chart_ready=false
	for attempt in $(seq 1 "$smoke_attempts"); do
		if "$helm_bin" show chart "$normalized_artifacthub_repository_url" --version "$expected_chart_version" >"$tmpdir/oci-chart.yaml" 2>"$tmpdir/oci-chart.err"; then
			oci_chart_ready=true
			break
		fi
		if [[ "$attempt" -lt "$smoke_attempts" ]]; then
			echo "GHCR OCI chart not visible yet (${attempt}/${smoke_attempts}); waiting ${smoke_sleep_seconds}s..."
			sleep "$smoke_sleep_seconds"
		fi
	done
	if [[ "$oci_chart_ready" != "true" ]]; then
		cat "$tmpdir/oci-chart.err" >&2 || true
		echo "ERROR: GHCR OCI chart is not published yet." >&2
		echo "  chart: ${normalized_artifacthub_repository_url}" >&2
		echo "  version: ${expected_chart_version}" >&2
		exit 4
	fi
	oci_chart_version="$(awk -F': *' '/^version:/ {gsub(/"/, "", $2); print $2; exit}' "$tmpdir/oci-chart.yaml")"
	oci_app_version="$(awk -F': *' '/^appVersion:/ {gsub(/"/, "", $2); print $2; exit}' "$tmpdir/oci-chart.yaml")"
	if [[ "$oci_chart_version" != "$expected_chart_version" || "$oci_app_version" != "$expected_app_version" ]]; then
		echo "ERROR: GHCR OCI chart metadata mismatch." >&2
		echo "  expected chart/app: ${expected_chart_version}/${expected_app_version}" >&2
		echo "  actual chart/app:   ${oci_chart_version}/${oci_app_version}" >&2
		exit 5
	fi
	echo "GHCR OCI chart OK: ${normalized_artifacthub_repository_url}:${expected_chart_version}"
else
	echo "ERROR: helm not found; cannot verify GHCR OCI chart." >&2
	exit 5
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

echo "=== Artifact Hub target version 정합성 ==="
package_version_url="${package_url}/${expected_chart_version}"
artifacthub_package_ready=false
package_metadata_filter='
	(.name // $name) == $name
	and .version == $version
	and .app_version == $app_version
	and .signed == true
'
for attempt in $(seq 1 "$smoke_attempts"); do
	if "$curl_bin" -fsSL "$package_version_url" -o "$tmpdir/package-version.json" 2>"$tmpdir/package-version.err"; then
		if "$jq_bin" -e \
			--arg name "$artifacthub_package_name" \
			--arg version "$expected_chart_version" \
			--arg app_version "$expected_app_version" \
			"$package_metadata_filter" \
			"$tmpdir/package-version.json" >/dev/null; then
			artifacthub_package_ready=true
			break
		fi
		if [[ "$attempt" -lt "$smoke_attempts" ]]; then
			echo "Artifact Hub target metadata not ready yet (${attempt}/${smoke_attempts}); waiting ${smoke_sleep_seconds}s..."
			"$jq_bin" '{version, app_version, signed, repository: .repository.url}' "$tmpdir/package-version.json"
			sleep "$smoke_sleep_seconds"
		fi
	else
		if [[ "$attempt" -lt "$smoke_attempts" ]]; then
			echo "Artifact Hub target version not indexed yet (${attempt}/${smoke_attempts}); waiting ${smoke_sleep_seconds}s..."
			sleep "$smoke_sleep_seconds"
		fi
	fi
done
if [[ "$artifacthub_package_ready" != "true" ]]; then
	if [[ -s "$tmpdir/package-version.json" ]]; then
		echo "ERROR: Artifact Hub package metadata did not reach the expected state." >&2
		"$jq_bin" '{name, version, app_version, signed, repository: .repository.url}' "$tmpdir/package-version.json" >&2
		echo "  expected chart/app/signed: ${expected_chart_version}/${expected_app_version}/true" >&2
		exit 5
	fi
	cat "$tmpdir/package-version.err" >&2 || true
	echo "ERROR: Artifact Hub repository exists but target chart version is not indexed yet." >&2
	echo "  package API: $package_version_url" >&2
	echo "  retry after Artifact Hub tracker runs, or push a new chart version to force reprocessing." >&2
	exit 5
fi
echo "Artifact Hub target version OK: ${expected_chart_version}"
