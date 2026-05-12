#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/postgres-operator-artifacthub-register-test.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

if ARTIFACTHUB_API_KEY_ID="" ARTIFACTHUB_API_KEY_SECRET="" \
	bash "$repo_root/hack/artifacthub_register.sh" >"$tmpdir/missing.out" 2>&1; then
	echo "expected missing Artifact Hub API credentials to fail" >&2
	exit 1
fi
grep -q "ARTIFACTHUB_API_KEY_ID and ARTIFACTHUB_API_KEY_SECRET are required" "$tmpdir/missing.out"

stubcurl="$tmpdir/curl"
cat >"$stubcurl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

out=""
while [[ $# -gt 0 ]]; do
	printf '%s\n' "$1" >>"$ARTIFACTHUB_TEST_ARGS"
	case "$1" in
		-o)
			out="$2"
			printf '%s\n' "$2" >>"$ARTIFACTHUB_TEST_ARGS"
			shift 2
			;;
		-d)
			printf '%s' "$2" >"$ARTIFACTHUB_TEST_BODY"
			shift 2
			;;
		*)
			shift
			;;
	esac
done

if [[ -z "$out" ]]; then
	out="/dev/stdout"
fi
printf '{"repository_id":"repo-id"}' >"$out"
SH
chmod +x "$stubcurl"

export ARTIFACTHUB_API_URL="https://artifacthub.test/api/v1"
export ARTIFACTHUB_ORG="keiailab"
export ARTIFACTHUB_REPOSITORY_NAME="keiailab-postgres-operator"
export HELM_REPO_URL="https://keiailab.github.io/postgres-operator"
export ARTIFACTHUB_API_KEY_ID="key-id"
export ARTIFACTHUB_API_KEY_SECRET="key-secret"
export ARTIFACTHUB_TEST_ARGS="$tmpdir/curl.args"
export ARTIFACTHUB_TEST_BODY="$tmpdir/body.json"

CURL_BIN="$stubcurl" bash "$repo_root/hack/artifacthub_register.sh" >"$tmpdir/register.out" 2>&1

grep -q -- "-X" "$ARTIFACTHUB_TEST_ARGS"
grep -q -- "POST" "$ARTIFACTHUB_TEST_ARGS"
grep -q -- "https://artifacthub.test/api/v1/repositories/org/keiailab" "$ARTIFACTHUB_TEST_ARGS"
grep -q -- "X-API-KEY-ID: key-id" "$ARTIFACTHUB_TEST_ARGS"
grep -q -- "X-API-KEY-SECRET: key-secret" "$ARTIFACTHUB_TEST_ARGS"
jq -e \
	'.kind == 0
		and .name == "keiailab-postgres-operator"
		and .display_name == "Postgres Operator (Keiailab)"
		and .url == "https://keiailab.github.io/postgres-operator"' \
	"$ARTIFACTHUB_TEST_BODY" >/dev/null
grep -q "registration request accepted" "$tmpdir/register.out"

echo "artifacthub register shell test PASS"
