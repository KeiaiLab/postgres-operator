#!/usr/bin/env bash
#
# scripts/sbom-attach.sh — D.11.5 SBOM + cosign signing pipeline (ROADMAP G6 L195).
#
# postgres-operator + pg (PostgreSQL runtime) 이미지에 대해:
#   1. SPDX-JSON SBOM 생성 (syft)
#   2. SBOM attestation attach (cosign attest)
#   3. 이미지 자체 sign (cosign sign)
#   4. provenance verify (cosign verify-attestation + cosign verify)
#
# 의존성:
#   - syft (https://github.com/anchore/syft) ≥ 1.0
#   - cosign (https://github.com/sigstore/cosign) ≥ 2.0
#   - 인증: COSIGN_KEY (private key 경로) + COSIGN_PASSWORD env 또는 keyless OIDC
#
# 호출 예:
#   IMAGE_OPERATOR=ghcr.io/keiailab/postgres-operator:v0.3.0-alpha.18 \
#   IMAGE_PG=ghcr.io/keiailab/pg:18 \
#   ./scripts/sbom-attach.sh
#
# release tag push 시점 (manual or local) — RFC-0002 정합으로 GH Actions 사용 안 함.

set -euo pipefail

# --- 입력 -----------------------------------------------------------------------------

IMAGE_OPERATOR="${IMAGE_OPERATOR:?IMAGE_OPERATOR env required (e.g. ghcr.io/keiailab/postgres-operator:vX.Y.Z)}"
IMAGE_PG="${IMAGE_PG:-}"
SBOM_DIR="${SBOM_DIR:-./dist/sbom}"
COSIGN_KEY="${COSIGN_KEY:-}"
SKIP_VERIFY="${SKIP_VERIFY:-0}"

# --- 사전 확인 -------------------------------------------------------------------------

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "❌ missing dependency: $1" >&2; exit 1; }
}

require syft
require cosign

mkdir -p "$SBOM_DIR"

# --- SBOM 생성 함수 -------------------------------------------------------------------

generate_sbom() {
  local image="$1"
  local name
  name=$(basename "${image%%:*}")
  local out="$SBOM_DIR/${name}.spdx.json"
  echo "📦 SBOM 생성: $image → $out"
  syft "$image" -o spdx-json="$out"
  if [[ ! -s "$out" ]]; then
    echo "❌ SBOM 생성 실패 — empty file" >&2
    exit 1
  fi
  echo "$out"
}

# --- SBOM attest + image sign ---------------------------------------------------------

attest_and_sign() {
  local image="$1"
  local sbom="$2"

  local sign_args=()
  local attest_args=("--predicate" "$sbom" "--type" "spdxjson")
  if [[ -n "$COSIGN_KEY" ]]; then
    sign_args+=("--key" "$COSIGN_KEY")
    attest_args+=("--key" "$COSIGN_KEY")
  else
    echo "⚠️  COSIGN_KEY 미설정 — keyless OIDC 사용 가정 (CI/local OIDC token 필요)"
  fi

  echo "🔏 이미지 sign: $image"
  cosign sign --yes "${sign_args[@]}" "$image"

  echo "📜 SBOM attest: $image"
  cosign attest --yes "${attest_args[@]}" "$image"
}

# --- verify --------------------------------------------------------------------------

verify_signature() {
  local image="$1"
  if [[ "$SKIP_VERIFY" == "1" ]]; then
    echo "⏭️  verify skip (SKIP_VERIFY=1)"
    return 0
  fi
  echo "🔍 verify signature: $image"
  if [[ -n "$COSIGN_KEY" ]]; then
    cosign verify --key "${COSIGN_KEY}.pub" "$image" >/dev/null
    cosign verify-attestation --key "${COSIGN_KEY}.pub" --type spdxjson "$image" >/dev/null
  else
    # keyless — cert-identity / cert-oidc-issuer 강제 권장
    cosign verify "$image" >/dev/null
    cosign verify-attestation --type spdxjson "$image" >/dev/null
  fi
  echo "✅ $image verify PASS"
}

# --- 메인 ----------------------------------------------------------------------------

main() {
  echo "===== D.11.5 SBOM + cosign pipeline 시작 ====="
  echo "  operator image: $IMAGE_OPERATOR"
  [[ -n "$IMAGE_PG" ]] && echo "  pg image:       $IMAGE_PG"
  echo "  SBOM dir:       $SBOM_DIR"
  echo

  local sbom
  sbom=$(generate_sbom "$IMAGE_OPERATOR")
  attest_and_sign "$IMAGE_OPERATOR" "$sbom"
  verify_signature "$IMAGE_OPERATOR"

  if [[ -n "$IMAGE_PG" ]]; then
    sbom=$(generate_sbom "$IMAGE_PG")
    attest_and_sign "$IMAGE_PG" "$sbom"
    verify_signature "$IMAGE_PG"
  fi

  echo
  echo "===== D.11.5 PASS — SBOM + cosign attach + verify 완료 ====="
}

main "$@"
