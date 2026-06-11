# PG 17 + Citus 13.x 베이스 이미지
# 빌드: docker buildx build \
#   -t ghcr.io/keiailab/pg:17-citus13-<rev> -f build/images/pg/17/Dockerfile .

ARG PG_MAJOR=17
ARG CITUS_VERSION=13.0

FROM postgres:${PG_MAJOR}-bookworm

ARG PG_MAJOR
ARG CITUS_VERSION

LABEL org.opencontainers.image.source="https://github.com/keiailab/postgres-operator"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.title="postgres-operator pg17+citus13"

ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg lsb-release \
        postgresql-${PG_MAJOR}-pgaudit \
    ; \
    curl -fsSL https://install.citusdata.com/community/deb.sh | bash; \
    apt-get install -y --no-install-recommends \
        postgresql-${PG_MAJOR}-citus-${CITUS_VERSION} \
    ; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

USER postgres
