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
#   - HELM_SIGN=1 시 PGP key (HELM_GPG_KEY) keyring import 필요.
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
HELM_GPG_KEY="${HELM_GPG_KEY:-89A409476828CB992338C378651E51AF520BCB78}"
HELM_KEYRING="${HELM_KEYRING:-${HOME}/.gnupg/secring.gpg}"

command -v helm >/dev/null 2>&1 || { echo "ERROR: helm CLI 미설치" >&2; exit 1; }

echo "==> helm package"
rm -rf "${RELEASE_TMP}" "${GHPAGES_TMP}"
mkdir -p "${RELEASE_TMP}"
if [[ "${HELM_SIGN}" == "1" ]]; then
  echo "INFO: chart 서명 활성 (PGP key ${HELM_GPG_KEY})"
  helm package --sign --key "${HELM_GPG_KEY}" --keyring "${HELM_KEYRING}" "${HELM_CHART}" -d "${RELEASE_TMP}"
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
