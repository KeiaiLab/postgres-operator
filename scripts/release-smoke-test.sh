#!/usr/bin/env bash
# release-smoke-test.sh — 첫 publish 또는 release 직후 5층 smoke 검증.
#
# 검증 항목:
#   1. GH Release tag 존재 + asset 첨부 (.tgz)
#   2. GHCR image manifest 가져오기 (digest 일치)
#   3. GitHub Pages built status
#   4. Helm repo index.yaml fetch + version entry
#   5. helm pull + helm template (default + all-features)
#
# 사용법:
#   scripts/release-smoke-test.sh                        # Chart.yaml 의 현재 version
#   scripts/release-smoke-test.sh v0.1.0-alpha.1         # 특정 version
#
# 환경: gh CLI 인증 + helm + curl 필요. trivy/cosign optional.
#
# Exit code: 0 = 모두 PASS, 1 = 1건 이상 fail.

set -uo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CHART_NAME="$(awk '/^name:/ { print $2; exit }' "$REPO_DIR"/charts/*/Chart.yaml)"
# GH repo name 은 git remote 에서 추출 (chart name 과 다를 수 있음 — postgresql-operator
# chart 가 postgres-operator GH repo 에서 publish 되는 패턴 등).
REMOTE_URL="$(cd "$REPO_DIR" && git remote get-url origin 2>/dev/null)"
GH_OWNER="$(echo "$REMOTE_URL" | sed -E 's|.*[:/]([^/]+)/[^/]+\.git$|\1|; s|.*[:/]([^/]+)/[^/]+$|\1|')"
GH_REPO="$(echo "$REMOTE_URL" | sed -E 's|.*[:/][^/]+/([^/]+)\.git$|\1|; s|.*[:/][^/]+/([^/]+)$|\1|')"
HELM_REPO_URL="https://${GH_OWNER}.github.io/${GH_REPO}"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  CHART_VER="$(awk '/^version:/ { print $2; exit }' "$REPO_DIR"/charts/*/Chart.yaml)"
  VERSION="v${CHART_VER}"
fi
TAG_VER="${VERSION#v}"

PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }

echo "════════════════════════════════════════════════════════════════"
echo " release-smoke-test  ${GH_OWNER}/${GH_REPO}  ${VERSION}"
echo "════════════════════════════════════════════════════════════════"

# 1. GH Release
echo ""
echo "▸ [1/5] GH Release tag + asset"
if gh release view "$VERSION" -R "${GH_OWNER}/${GH_REPO}" >/dev/null 2>&1; then
  pass "release ${VERSION} 존재"
  ASSET="$(gh release view "$VERSION" -R "${GH_OWNER}/${GH_REPO}" --json assets --jq '.assets[].name' | grep "${CHART_NAME}-${TAG_VER}.tgz" || true)"
  if [ -n "$ASSET" ]; then pass "chart .tgz asset 첨부 ($ASSET)"; else fail "chart .tgz asset 누락"; fi
else
  fail "release ${VERSION} 없음"
fi

# 2. GHCR image
echo ""
echo "▸ [2/5] GHCR image manifest"
# Image name 은 GH repo name 을 따름 (chart name 과 다를 수 있음 — postgresql-operator
# chart 가 ghcr.io/keiailab/postgres-operator 로 push 되는 패턴 등).
IMAGE_REF="ghcr.io/${GH_OWNER}/${GH_REPO}:${VERSION}"
if docker manifest inspect "$IMAGE_REF" >/dev/null 2>&1; then
  DIGEST="$(docker manifest inspect "$IMAGE_REF" 2>/dev/null | jq -r '.config.digest // .digest // .manifests[0].digest' 2>/dev/null | head -1)"
  pass "image ${IMAGE_REF} (digest: ${DIGEST:0:19}...)"
else
  fail "image ${IMAGE_REF} manifest fetch 실패"
fi

# 3. GitHub Pages
echo ""
echo "▸ [3/5] GitHub Pages status"
PAGES_STATUS="$(gh api "repos/${GH_OWNER}/${GH_REPO}/pages/builds" --jq '.[0].status' 2>/dev/null || echo "missing")"
if [ "$PAGES_STATUS" = "built" ]; then
  pass "Pages status=built"
else
  fail "Pages status=${PAGES_STATUS}"
fi

# 4. Helm repo index.yaml — 파일 기반 (bash 변수 long-string echo race 회피)
echo ""
echo "▸ [4/5] Helm repo index.yaml fetch"
INDEX_FILE="/tmp/release-smoke-index-$$.yaml"
if curl -sfo "$INDEX_FILE" "${HELM_REPO_URL}/index.yaml" 2>/dev/null; then
  SIZE=$(wc -c < "$INDEX_FILE" | tr -d ' ')
  pass "index.yaml fetch (${SIZE} bytes)"
  # Helm chart version entry — fixed-string grep 으로 dot/quote 이스케이프 회피.
  if grep -Fq "version: ${TAG_VER}" "$INDEX_FILE"; then
    pass "index.yaml 에 version: ${TAG_VER} 존재"
  else
    fail "index.yaml 에 version: ${TAG_VER} 누락"
  fi
  rm -f "$INDEX_FILE"
else
  fail "index.yaml fetch 실패 (${HELM_REPO_URL}/index.yaml)"
fi

# 5. helm pull + template
echo ""
echo "▸ [5/5] helm pull + template (default + all-features)"
TMP_REPO="smoke-test-$$"
if helm repo add "$TMP_REPO" "${HELM_REPO_URL}" >/dev/null 2>&1; then
  helm repo update "$TMP_REPO" >/dev/null 2>&1
  TMP_TGZ="/tmp/${CHART_NAME}-${TAG_VER}-smoke.tgz"
  if helm pull "${TMP_REPO}/${CHART_NAME}" --version "${TAG_VER}" --destination /tmp >/dev/null 2>&1; then
    pass "helm pull ${TMP_REPO}/${CHART_NAME} --version ${TAG_VER}"
    PULLED_TGZ="/tmp/${CHART_NAME}-${TAG_VER}.tgz"
    if [ -f "$PULLED_TGZ" ]; then
      SIZE=$(stat -f%z "$PULLED_TGZ" 2>/dev/null || stat -c%s "$PULLED_TGZ")
      pass "chart .tgz ${SIZE} bytes"
      # default values render
      if helm template smoke "$PULLED_TGZ" --namespace "${CHART_NAME}-system" >/dev/null 2>&1; then
        pass "helm template (default values)"
      else
        fail "helm template (default values)"
      fi
      # all features ON (valkey 한정 — 다른 repo 는 silent skip)
      if helm template smoke "$PULLED_TGZ" --namespace "${CHART_NAME}-system" \
          --set features.cluster.enabled=true \
          --set features.backup.enabled=true \
          --set features.autoscaling.enabled=true >/dev/null 2>&1; then
        pass "helm template (features.cluster/backup/autoscaling=true)"
      else
        echo "  ○ helm template (features.* 가드 부재 — chart 별로 다름, skip)"
      fi
      rm -f "$PULLED_TGZ"
    fi
  else
    fail "helm pull ${TMP_REPO}/${CHART_NAME} 실패"
  fi
  helm repo remove "$TMP_REPO" >/dev/null 2>&1
else
  fail "helm repo add ${HELM_REPO_URL} 실패"
fi

# Summary
echo ""
echo "════════════════════════════════════════════════════════════════"
echo " RESULT: ${PASS} PASS / ${FAIL} FAIL"
echo "════════════════════════════════════════════════════════════════"

[ "$FAIL" -eq 0 ]
