#!/usr/bin/env bash
# scripts/check-version-sync.sh
#
# 5-way Version SSOT 정합 검사 — CITATION.cff + Chart.yaml(version+appVersion) +
# kustomization.yaml + dist/install.yaml.
#
# 사용자 글로벌 standards/enforcement.md §3.1.4 (`version-ssot-drift` rule) +
# standards/checklist.md PR row "Python/Go package version bump 시 N-way SSOT
# 정합 검증" 정합. lefthook pre-push `version-sync` hook 가 본 script 호출.
#
# Makefile validate target (L227-231) 가 3-way drift (Chart + kustomize + dist)
# 검사. 본 script 는 CITATION.cff 추가 + 모두 통합 검사 (academic citation
# 정합 회복) — 라이브 evidence: 2026-05-20 RCA 발견 CITATION.cff drift
# `v0.3.0-alpha.15` ↔ Chart `0.3.0-alpha.18` 3 release 뒤짐.
#
# 우회: VERSION_SYNC_SKIP=1 환경변수 (의도적 mid-release skip 시).

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ "${VERSION_SYNC_SKIP:-0}" = "1" ]; then
  echo "[skip] VERSION_SYNC_SKIP=1 — version SSOT 정합 검사 우회"
  exit 0
fi

# SSOT 추출 — 각 파일에서 version 추출 (sed/awk minimal, bash 3.x+ 호환).
CITATION_VER=$(grep -E '^version:' CITATION.cff | sed -E 's/^version:[[:space:]]+v?//' | sed -E 's/[[:space:]]+$//')
CHART_VER=$(grep -E '^version:' charts/postgres-operator/Chart.yaml | head -1 | awk '{print $2}')
CHART_APPVER=$(grep -E '^appVersion:' charts/postgres-operator/Chart.yaml | sed -E 's/^appVersion:[[:space:]]+//' | sed -E 's/"//g' | sed -E 's/[[:space:]]+$//')
KUST_TAG=$(grep -E '^[[:space:]]+newTag:' config/manager/kustomization.yaml | awk '{print $2}')
DIST_TAG=$(grep -m 1 -E 'image:[[:space:]]+ghcr.io/keiailab/postgres-operator:' dist/install.yaml | sed -E 's/.*://' | sed -E 's/[[:space:]]+$//')

# 5-way drift 검사 — Chart.yaml version 기준.
TARGET="$CHART_VER"
DRIFT=0
DRIFT_REPORT=""

check() {
  local label="$1"
  local val="$2"
  if [ "$val" != "$TARGET" ]; then
    DRIFT=1
    DRIFT_REPORT="${DRIFT_REPORT}  - ${label}: ${val} (≠ ${TARGET})\n"
  fi
}

check "CITATION.cff version (v-prefix 제거 후 비교)" "$CITATION_VER"
check "Chart.yaml appVersion" "$CHART_APPVER"
check "config/manager/kustomization.yaml newTag" "$KUST_TAG"
check "dist/install.yaml image tag" "$DIST_TAG"

if [ "$DRIFT" -ne 0 ]; then
  echo "❌ version SSOT drift 발견 — Chart.yaml version=${TARGET} 기준 부정합:"
  printf "%b" "$DRIFT_REPORT"
  echo ""
  echo "fix 방법:"
  echo "  1. CITATION.cff version: v${TARGET}"
  echo "  2. Chart.yaml appVersion: \"${TARGET}\""
  echo "  3. config/manager/kustomization.yaml newTag: ${TARGET}"
  echo "  4. dist/install.yaml image: ghcr.io/keiailab/postgres-operator:${TARGET} (make validate 가 자동 갱신)"
  echo ""
  echo "우회: VERSION_SYNC_SKIP=1 git push (의도적 mid-release skip)"
  exit 1
fi

echo "✅ 5-way version SSOT 정합: ${TARGET}"
echo "  CITATION.cff version=v${CITATION_VER}"
echo "  Chart.yaml version=${CHART_VER}"
echo "  Chart.yaml appVersion=${CHART_APPVER}"
echo "  kustomization.yaml newTag=${KUST_TAG}"
echo "  dist/install.yaml image=${DIST_TAG}"
