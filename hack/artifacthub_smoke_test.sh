#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/postgres-operator-artifacthub-smoke-test.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

stubbin="$tmpdir/bin"
mkdir -p "$stubbin"

cat >"$stubbin/helm" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
	"repo add"|"repo update")
		exit 0
		;;
	"search repo")
		printf 'NAME                               CHART VERSION      APP VERSION\n'
		printf 'keiailab-postgres-operator/postgres-operator 0.3.0-alpha.16 0.3.0-alpha.16\n'
		exit 0
		;;
	"show chart")
		printf 'version: %s\n' "${EXPECTED_CHART_VERSION}"
		printf 'appVersion: "%s"\n' "${EXPECTED_APP_VERSION}"
		exit 0
		;;
esac
echo "unexpected helm call: $*" >&2
exit 99
SH
chmod +x "$stubbin/helm"

cat >"$stubbin/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

out=""
args=()
while [[ $# -gt 0 ]]; do
	case "$1" in
		-o)
			out="$2"
			shift 2
			;;
		-X|-H|-d)
			shift 2
			;;
		-f|-s|-S|-L|-fsSL)
			shift
			;;
		*)
			args+=("$1")
			shift
			;;
	esac
done

last_index=$((${#args[@]} - 1))
url="${args[$last_index]}"
if [[ -z "$out" ]]; then
	out="/dev/stdout"
fi

case "$url" in
	*/index.yaml)
		printf 'apiVersion: v1\nentries:\n  postgres-operator: []\n' >"$out"
		;;
	*/artifacthub-repo.yml)
		printf 'repositoryID: test-id\n' >"$out"
		;;
	*/repositories/search*)
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "registered" ]]; then
			printf '[{"repository_id":"repo-id","name":"keiailab-postgres-operator","url":"oci://ghcr.io/keiailab/charts/postgres-operator","last_tracking_errors":null}]' >"$out"
		elif [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "tracking_error" ]]; then
			printf '[{"repository_id":"repo-id","name":"keiailab-postgres-operator","url":"oci://ghcr.io/keiailab/charts/postgres-operator","last_tracking_errors":"historical icon fetch failed"}]' >"$out"
		elif [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "package_fallback" ]]; then
			printf '[]' >"$out"
		elif [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" ]]; then
			count_file="${ARTIFACTHUB_TEST_DELAY_FILE:?}"
			count="$(cat "$count_file" 2>/dev/null || printf '0')"
			count="$((count + 1))"
			printf '%s' "$count" >"$count_file"
			if [[ "$count" -ge 2 ]]; then
				printf '[{"repository_id":"repo-id","name":"keiailab-postgres-operator","url":"oci://ghcr.io/keiailab/charts/postgres-operator","last_tracking_errors":null}]' >"$out"
			else
				printf '[]' >"$out"
			fi
		else
			printf '[]' >"$out"
		fi
		;;
	*/repositories/org/keiailab/keiailab-postgres-operator)
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" ]]; then
			printf '{}' >"$out"
		else
			exit 22
		fi
		;;
	*/packages/helm/keiailab-postgres-operator/postgres-operator)
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" ]]; then
			count="$(cat "${ARTIFACTHUB_TEST_DELAY_FILE:?}" 2>/dev/null || printf '0')"
			if [[ "$count" -lt 2 ]]; then
				exit 22
			fi
		fi
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "registered" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "tracking_error" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "package_fallback" ]]; then
			printf '{"name":"postgres-operator","repository":{"repository_id":"repo-id","name":"keiailab-postgres-operator","url":"oci://ghcr.io/keiailab/charts/postgres-operator"}}' >"$out"
		else
			exit 22
		fi
		;;
	*/packages/helm/keiailab-postgres-operator/postgres-operator/*)
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" ]]; then
			count="$(cat "${ARTIFACTHUB_TEST_DELAY_FILE:?}" 2>/dev/null || printf '0')"
			if [[ "$count" -lt 2 ]]; then
				exit 22
			fi
		fi
		if [[ "${ARTIFACTHUB_TEST_CASE:-missing}" == "registered" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "delayed" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "tracking_error" || "${ARTIFACTHUB_TEST_CASE:-missing}" == "package_fallback" ]]; then
			printf '{"name":"postgres-operator","version":"%s","app_version":"%s","signed":true}' "${EXPECTED_CHART_VERSION}" "${EXPECTED_APP_VERSION}" >"$out"
		else
			exit 22
		fi
		;;
	*)
		echo "unexpected curl url: $url" >&2
		exit 99
		;;
esac
SH
chmod +x "$stubbin/curl"

export PATH="$stubbin:$PATH"
export CURL_BIN="$stubbin/curl"
export HELM_BIN="$stubbin/helm"
export ARTIFACTHUB_API_URL="https://artifacthub.test/api/v1"
export ARTIFACTHUB_ORG="keiailab"
export ARTIFACTHUB_REPOSITORY_NAME="keiailab-postgres-operator"
export ARTIFACTHUB_PACKAGE_NAME="postgres-operator"
export HELM_OCI_REPO="oci://ghcr.io/keiailab/charts"
export EXPECTED_CHART_VERSION="0.4.0-beta.6"
export EXPECTED_APP_VERSION="0.4.0-beta.1"
export HELM_REPO_URL="https://keiailab.github.io/postgres-operator"

if ARTIFACTHUB_TEST_CASE=missing bash "$repo_root/hack/artifacthub_smoke.sh" >"$tmpdir/missing.out" 2>&1; then
	echo "expected missing repository case to fail" >&2
	exit 1
fi
grep -q "Artifact Hub repository is not registered" "$tmpdir/missing.out"

export ARTIFACTHUB_API_KEY_ID="key-id"
export ARTIFACTHUB_API_KEY_SECRET="key-secret"
export ARTIFACTHUB_SMOKE_ATTEMPTS="2"
export ARTIFACTHUB_SMOKE_SLEEP_SECONDS="0"
export ARTIFACTHUB_TEST_DELAY_FILE="$tmpdir/delayed.count"
ARTIFACTHUB_TEST_CASE=delayed bash "$repo_root/hack/artifacthub_smoke.sh" >"$tmpdir/delayed.out" 2>&1
grep -q "Artifact Hub repository is not visible yet (1/2)" "$tmpdir/delayed.out"
grep -q "Artifact Hub package OK" "$tmpdir/delayed.out"
unset ARTIFACTHUB_API_KEY_ID ARTIFACTHUB_API_KEY_SECRET ARTIFACTHUB_SMOKE_ATTEMPTS ARTIFACTHUB_SMOKE_SLEEP_SECONDS ARTIFACTHUB_TEST_DELAY_FILE

ARTIFACTHUB_TEST_CASE=registered bash "$repo_root/hack/artifacthub_smoke.sh" >"$tmpdir/registered.out" 2>&1
grep -q "Artifact Hub package OK" "$tmpdir/registered.out"

ARTIFACTHUB_TEST_CASE=tracking_error bash "$repo_root/hack/artifacthub_smoke.sh" >"$tmpdir/tracking-error.out" 2>&1
grep -q "Artifact Hub repository has tracking errors" "$tmpdir/tracking-error.out"
grep -q "Artifact Hub target version OK" "$tmpdir/tracking-error.out"

ARTIFACTHUB_TEST_CASE=package_fallback bash "$repo_root/hack/artifacthub_smoke.sh" >"$tmpdir/package-fallback.out" 2>&1
grep -q "Artifact Hub repository found via package API fallback" "$tmpdir/package-fallback.out"
grep -q "Artifact Hub target version OK" "$tmpdir/package-fallback.out"

echo "artifacthub smoke shell test PASS"
