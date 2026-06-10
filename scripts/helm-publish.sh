#!/usr/bin/env bash
#
# postgres-operator helm-publish 스크립트.
#
# 사용:
#   bash scripts/helm-publish.sh
#
# 동작:
#   1. helm package chart → /tmp 임시 디렉터리.
#   2. gh-pages branch worktree clone (없으면 orphan 생성).
#   3. .tgz copy + artifacthub-repo.yml copy.
#   4. helm repo index (--merge index.yaml 이 있으면).
#   5. commit + push.
#
# 사전조건:
#   - helm CLI 설치.
#   - git remote 'origin' 설정.
#   - HELM_REPO_URL 환경변수 (기본: https://keiailab.github.io/postgres-operator).
#   - HELM_SIGN=1 시 HELM_PRIVATE_KEY_FILE 또는 HELM_KEYRING 에 signing secret key 필요.
#
# CLAUDE.md §2 (RFC 0002) — GHA helm-publish workflow 대체 (로컬 4계층).
# 기존 Makefile `helm-publish` target 의 본문을 scripts/ 로 추출 — 외부 호출자
# (수동 release / 신규 자동화) 가 Makefile 의존 없이 직접 실행 가능.

set -euo pipefail

HELM_CHART="${HELM_CHART:-charts/postgres-operator}"
HELM_REPO_URL="${HELM_REPO_URL:-https://keiailab.github.io/postgres-operator}"
RELEASE_TMP="${RELEASE_TMP:-/tmp/postgres-operator-release}"
GHPAGES_TMP="${GHPAGES_TMP:-/tmp/postgres-operator-gh-pages}"
HELM_SIGN="${HELM_SIGN:-0}"
HELM_GPG_FINGERPRINT="${HELM_GPG_FINGERPRINT:-89A409476828CB992338C378651E51AF520BCB78}"
HELM_GPG_KEY="${HELM_GPG_KEY:-${HELM_GPG_FINGERPRINT}}"
HELM_KEYRING="${HELM_KEYRING:-${HOME}/.gnupg/secring.gpg}"
HELM_VERIFY_KEYRING="${HELM_VERIFY_KEYRING:-}"
HELM_PRIVATE_KEY_FILE="${HELM_PRIVATE_KEY_FILE:-${HOME}/Downloads/keiailab-helm-signing-private.asc}"
HELM_PASSPHRASE_FILE="${HELM_PASSPHRASE_FILE:-${HOME}/Downloads/keiailab-helm-signing-passphrase.txt}"
SIGNING_GNUPGHOME=""

command -v helm >/dev/null 2>&1 || { echo "ERROR: helm CLI 미설치" >&2; exit 1; }

cleanup() {
  if [[ -n "${SIGNING_GNUPGHOME}" && -d "${SIGNING_GNUPGHOME}" ]]; then
    rm -rf "${SIGNING_GNUPGHOME}"
  fi
}
trap cleanup EXIT

prepare_signing_key() {
  command -v gpg >/dev/null 2>&1 || { echo "ERROR: HELM_SIGN=1 이지만 gpg CLI 미설치" >&2; exit 1; }

  if [[ -f "${HELM_PRIVATE_KEY_FILE}" ]]; then
    SIGNING_GNUPGHOME="$(mktemp -d)"
    chmod 700 "${SIGNING_GNUPGHOME}"
    gpg --batch --homedir "${SIGNING_GNUPGHOME}" --import "${HELM_PRIVATE_KEY_FILE}" >/dev/null

    local key_listing found_fingerprint signing_key_uid
    key_listing="$(
      { gpg --batch --homedir "${SIGNING_GNUPGHOME}" --with-colons --list-secret-keys --fingerprint "${HELM_GPG_FINGERPRINT}" 2>/dev/null || true; }
    )"
    found_fingerprint="$(awk -F: '$1 == "fpr" { print $10; exit }' <<< "${key_listing}")"
    signing_key_uid="$(awk -F: '$1 == "uid" { print $10; exit }' <<< "${key_listing}")"
    if [[ "${found_fingerprint}" != "${HELM_GPG_FINGERPRINT}" ]]; then
      echo "ERROR: signing key fingerprint mismatch: expected=${HELM_GPG_FINGERPRINT} actual=${found_fingerprint:-missing}" >&2
      exit 1
    fi
    if [[ -z "${signing_key_uid}" ]]; then
      echo "ERROR: signing key UID missing for fingerprint ${HELM_GPG_FINGERPRINT}" >&2
      exit 1
    fi

    HELM_KEYRING="${SIGNING_GNUPGHOME}/secring.gpg"
    HELM_VERIFY_KEYRING="${SIGNING_GNUPGHOME}/pubring.gpg"
    gpg --batch --homedir "${SIGNING_GNUPGHOME}" --yes --export-secret-keys "${HELM_GPG_FINGERPRINT}" > "${HELM_KEYRING}"
    gpg --batch --homedir "${SIGNING_GNUPGHOME}" --yes --export "${HELM_GPG_FINGERPRINT}" > "${HELM_VERIFY_KEYRING}"
    chmod 600 "${HELM_KEYRING}" "${HELM_VERIFY_KEYRING}"
    if [[ "${HELM_GPG_KEY}" == "${HELM_GPG_FINGERPRINT}" ]]; then
      HELM_GPG_KEY="${signing_key_uid}"
    fi
    return
  fi

  local key_listing found_fingerprint signing_key_uid
  key_listing="$(
    { gpg --batch --with-colons --list-secret-keys --fingerprint "${HELM_GPG_FINGERPRINT}" 2>/dev/null || true; }
  )"
  found_fingerprint="$(awk -F: '$1 == "fpr" { print $10; exit }' <<< "${key_listing}")"
  signing_key_uid="$(awk -F: '$1 == "uid" { print $10; exit }' <<< "${key_listing}")"
  if [[ "${found_fingerprint}" != "${HELM_GPG_FINGERPRINT}" ]]; then
    echo "ERROR: HELM_SIGN=1 requires ${HELM_PRIVATE_KEY_FILE} or local secret key ${HELM_GPG_FINGERPRINT}" >&2
    exit 1
  fi
  if [[ -z "${signing_key_uid}" ]]; then
    echo "ERROR: signing key UID missing for fingerprint ${HELM_GPG_FINGERPRINT}" >&2
    exit 1
  fi
  if [[ ! -s "${HELM_KEYRING}" ]]; then
    echo "ERROR: local secret key exists but Helm keyring is missing: ${HELM_KEYRING}" >&2
    echo "       Set HELM_PRIVATE_KEY_FILE or HELM_KEYRING explicitly." >&2
    exit 1
  fi
  if [[ "${HELM_GPG_KEY}" == "${HELM_GPG_FINGERPRINT}" ]]; then
    HELM_GPG_KEY="${signing_key_uid}"
  fi
  HELM_VERIFY_KEYRING="${HELM_VERIFY_KEYRING:-${HOME}/.gnupg/pubring.gpg}"
}

echo "==> helm package"
rm -rf "${RELEASE_TMP}" "${GHPAGES_TMP}"
mkdir -p "${RELEASE_TMP}"
if [[ "${HELM_SIGN}" == "1" ]]; then
  prepare_signing_key
  echo "INFO: chart 서명 활성 (PGP key ${HELM_GPG_KEY})"
  sign_args=(--sign --key "${HELM_GPG_KEY}" --keyring "${HELM_KEYRING}")
  if [[ -f "${HELM_PASSPHRASE_FILE}" ]]; then
    sign_args+=(--passphrase-file "${HELM_PASSPHRASE_FILE}")
  fi
  helm package "${sign_args[@]}" "${HELM_CHART}" -d "${RELEASE_TMP}"
  chart_archive="$(find "${RELEASE_TMP}" -maxdepth 1 -name '*.tgz' | head -n 1)"
  helm verify "${chart_archive}" --keyring "${HELM_VERIFY_KEYRING}"
else
  helm package "${HELM_CHART}" -d "${RELEASE_TMP}"
fi

echo "==> gh-pages worktree"
if git ls-remote --exit-code --heads origin gh-pages >/dev/null 2>&1; then
  git clone --branch gh-pages --single-branch "$(git remote get-url origin)" "${GHPAGES_TMP}"
else
  git clone "$(git remote get-url origin)" "${GHPAGES_TMP}"
  (cd "${GHPAGES_TMP}" && git checkout --orphan gh-pages && git rm -rf . >/dev/null 2>&1 || true)
fi

echo "==> helm repo index"
cp "${RELEASE_TMP}"/postgres-operator-*.tgz "${GHPAGES_TMP}/"
cp "${RELEASE_TMP}"/postgres-operator-*.tgz.prov "${GHPAGES_TMP}/" 2>/dev/null || true
cp "$(pwd)/charts/artifacthub-repo.yml" "${GHPAGES_TMP}/" 2>/dev/null || true

if [[ -f "${GHPAGES_TMP}/index.yaml" ]]; then
  (cd "${GHPAGES_TMP}" && helm repo index . --merge index.yaml --url "${HELM_REPO_URL}")
else
  (cd "${GHPAGES_TMP}" && helm repo index . --url "${HELM_REPO_URL}")
fi

echo "==> commit + push"
CHART_VERSION="$(awk '/^version:/ { print $2; exit }' "$(pwd)/${HELM_CHART}/Chart.yaml")"
(
  cd "${GHPAGES_TMP}"
  git add -A
  if git diff --cached --quiet; then
    echo "INFO: gh-pages 변경 없음 — push skip"
  else
    git commit -m "chore(helm): publish ${CHART_VERSION}"
    git push origin gh-pages
  fi
)

rm -rf "${RELEASE_TMP}" "${GHPAGES_TMP}"
echo "Helm chart 게시 완료 (version=${CHART_VERSION})"
