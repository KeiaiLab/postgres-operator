#!/usr/bin/env bash
#
# postgres-operator 수동 release 스크립트.
#
# 사용:
#   bash scripts/release.sh v0.1.0 [<registry>]
#
# 동작:
#   1. tag 형식 검증 (vMAJOR.MINOR.PATCH).
#   2. working tree clean 검증.
#   3. 로컬 게이트 (lint + test + audit + validate) — `make gate` 호출.
#   4. version 정합 검증 (Chart.yaml version / appVersion / CHANGELOG.md).
#   5. helm package preflight.
#   6. linux/amd64 단일 아키 이미지 빌드 + push (docker buildx default builder, RFC 0002 §2).
#   7. install.yaml 생성 (dist/install.yaml).
#   8. tag + push.
#   9. gh release create — chart .tgz + dist/install.yaml + (옵션) SBOM 첨부.
#  10. helm-publish (gh-pages).
#
# 사전조건:
#   - git remote 'origin' 설정.
#   - docker buildx 활성화 (기본 빌더).
#   - 컨테이너 레지스트리 로그인 (docker login ghcr.io).
#   - gh CLI 인증 (gh auth status).
#   - (선택) git-cliff: brew install git-cliff (CHANGELOG 자동 생성).
#   - (선택) syft: brew install syft (SBOM 생성).
#
# CLAUDE.md §2 (RFC 0002 — GHA 영구 금지) 준수. 본 스크립트는 *수동* 실행.
# 기존 Makefile `release` target 을 scripts/ 로 추출 — 외부 호출자가 Makefile
# 의존 없이 release pipeline 을 직접 호출 가능. Makefile target 은 호환 유지.

set -euo pipefail

usage() {
  echo "Usage: $0 <version> [registry]"
  echo "  version: vMAJOR.MINOR.PATCH (e.g. v0.1.0)"
  echo "  registry: image registry prefix (default: ghcr.io/keiailab)"
  exit 1
}

[[ $# -lt 1 ]] && usage

VERSION="$1"
REGISTRY="${2:-ghcr.io/keiailab}"
IMAGE_REPOSITORY="${REGISTRY}/postgres-operator"
HELM_CHART="${HELM_CHART:-charts/postgres-operator}"
RELEASE_TMP="${RELEASE_TMP:-/tmp/postgres-operator-release}"

# 1. tag 형식 검증.
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
  echo "ERROR: version must be vMAJOR.MINOR.PATCH[-prerelease] (got: $VERSION)" >&2
  exit 1
fi
TARGET_VER="${VERSION#v}"

# 2. working tree clean.
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "ERROR: working tree dirty. Commit or stash first." >&2
  git status --short
  exit 1
fi

# 3. 로컬 게이트 (lint + test + audit + validate).
echo "==> 로컬 게이트 (make gate)"
make gate

# 4. version 정합 검증.
echo "==> version 정합 검증"
CHART_VER="$(awk '/^version:/ { print $2; exit }' "${HELM_CHART}/Chart.yaml")"
APP_VER="$(awk '/^appVersion:/ { gsub(/"/, "", $2); print $2; exit }' "${HELM_CHART}/Chart.yaml")"
if [[ "$CHART_VER" != "$TARGET_VER" ]]; then
  echo "ERROR: Chart.yaml version=$CHART_VER, 기대=$TARGET_VER" >&2
  exit 1
fi
if [[ "$APP_VER" != "$TARGET_VER" ]]; then
  echo "ERROR: Chart.yaml appVersion=$APP_VER, 기대=$TARGET_VER" >&2
  exit 1
fi
if ! grep -q "\[${TARGET_VER}\]" CHANGELOG.md; then
  echo "ERROR: CHANGELOG.md 에 [$TARGET_VER] 항목 부재" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null; then
  echo "ERROR: tag $VERSION 이미 존재" >&2
  exit 1
fi

# 5. helm package preflight.
echo "==> helm package preflight"
rm -rf "${RELEASE_TMP}"
mkdir -p "${RELEASE_TMP}"
helm package "${HELM_CHART}" -d "${RELEASE_TMP}"
test -f "${RELEASE_TMP}/postgres-operator-${TARGET_VER}.tgz"

# 6. 이미지 빌드 + push (linux/amd64, default builder).
echo "==> 이미지 빌드 + push: ${IMAGE_REPOSITORY}:${VERSION} / :${TARGET_VER}"
docker --context=default buildx build --platform linux/amd64 \
  -t "${IMAGE_REPOSITORY}:${VERSION}" \
  -t "${IMAGE_REPOSITORY}:${TARGET_VER}" \
  --push .

# 7. install.yaml 생성.
echo "==> install.yaml 생성 (dist/install.yaml)"
make build-installer IMG="${IMAGE_REPOSITORY}:${VERSION}"
test -f dist/install.yaml

# 8. tag + push.
echo "==> tag + push: $VERSION"
git tag -a "$VERSION" -m "$VERSION"
git push origin "$VERSION"

# 9. gh release.
echo "==> gh release create"
PREFLAG=""
case "$VERSION" in
  *alpha*|*beta*|*rc*) PREFLAG="--prerelease" ;;
esac

NOTES_FLAG="--notes \"Release ${VERSION}. 변경 내역은 CHANGELOG.md 참조.\""
if command -v git-cliff >/dev/null 2>&1; then
  if git-cliff --strip all --tag "$VERSION" --unreleased > "/tmp/release-notes-${VERSION}.md" 2>/dev/null; then
    NOTES_FLAG="--notes-file /tmp/release-notes-${VERSION}.md"
  fi
fi

SBOM_ASSET=""
if command -v syft >/dev/null 2>&1; then
  echo "==> syft SBOM 생성"
  if syft scan "${IMAGE_REPOSITORY}:${VERSION}" -o spdx-json -q > "/tmp/postgres-operator-${VERSION}.spdx.json" 2>/dev/null; then
    SBOM_ASSET="/tmp/postgres-operator-${VERSION}.spdx.json"
  fi
fi

eval gh release create "$VERSION" -R keiailab/postgres-operator $PREFLAG \
  --title "$VERSION" \
  $NOTES_FLAG \
  "${RELEASE_TMP}/postgres-operator-${TARGET_VER}.tgz" \
  dist/install.yaml \
  $SBOM_ASSET

rm -rf "${RELEASE_TMP}"

# 10. helm-publish (gh-pages).
echo "==> helm-publish (gh-pages)"
bash scripts/helm-publish.sh

echo
echo "==================================================="
echo "릴리스 완료: $VERSION"
echo "==================================================="
echo "  이미지: ${IMAGE_REPOSITORY}:${VERSION}"
echo "  chart:  ${HELM_CHART} (${TARGET_VER})"
echo "  helm:   https://keiailab.github.io/postgres-operator"
echo "  GH release: https://github.com/keiailab/postgres-operator/releases/tag/${VERSION}"
