# PG 16 + Citus 13.x 베이스 이미지
# 빌드: docker buildx build \
#   -t ghcr.io/keiailab/pg:16-citus13-<rev> -f build/images/pg/16/Dockerfile .
#
# 멀티아키 금지 (전역 규칙). --platform 미지정 시 linux/amd64 단일.

ARG PG_MAJOR=16
ARG CITUS_VERSION=13.0

FROM postgres:16-bookworm@sha256:efedf3595f1d6f415c08568ba171029bf54052e754cc9f030e3f2412b21f3d67

ARG PG_MAJOR
ARG CITUS_VERSION

LABEL org.opencontainers.image.source="https://github.com/keiailab/postgres-operator"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.title="postgres-operator pg16+citus13"

ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg lsb-release \
        postgresql-${PG_MAJOR}-pgaudit \
    ; \
    # Citus apt repo
    curl -fsSL https://repos.citusdata.com/community/gpgkey \
        | gpg --dearmor -o /usr/share/keyrings/citusdata-community.gpg; \
    echo "deb [signed-by=/usr/share/keyrings/citusdata-community.gpg] https://repos.citusdata.com/community/debian/ $(. /etc/os-release; echo "$VERSION_CODENAME") main" \
        > /etc/apt/sources.list.d/citusdata_community.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        postgresql-${PG_MAJOR}-citus-${CITUS_VERSION} \
    ; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# Citus는 shared_preload_libraries 등록이 필요. instance manager가 postgresql.conf에 주입.
USER postgres
